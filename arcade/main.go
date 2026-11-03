// The KubeCon agent arcade: every game's characters are kagent agents running as
// Agent Substrate actors. One backend serves all games, the arcade front page and
// the shared Substrate stats.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"math"
	"net/http"
	"os"
	"os/signal"
	"path"
	"strings"
	"syscall"
	"time"

	"github.com/sebbycorp/kubecon-adventures/arcade/games/defense"
	"github.com/sebbycorp/kubecon-adventures/arcade/games/duckhunt"
	"github.com/sebbycorp/kubecon-adventures/arcade/games/flappy"
	"github.com/sebbycorp/kubecon-adventures/arcade/games/flow"
	"github.com/sebbycorp/kubecon-adventures/arcade/games/gatewaydef"
	"github.com/sebbycorp/kubecon-adventures/arcade/games/heist"
	"github.com/sebbycorp/kubecon-adventures/arcade/games/podpacker"
	"github.com/sebbycorp/kubecon-adventures/arcade/internal/kagent"
	"github.com/sebbycorp/kubecon-adventures/arcade/internal/platform"
)

//go:embed web
var webFS embed.FS

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	kagentURL := flag.String("kagent-url", envOr("KAGENT_URL", "http://kagent-controller.kagent:8083"), "kagent controller URL")
	namespace := flag.String("namespace", envOr("AGENT_NAMESPACE", "kagent"), "namespace of the games' Agents")
	idle := flag.Int("idle", 20, "Duck Hunt: seconds a target may fly before it is put back to sleep (8-30)")
	atelet := flag.String("atelet-metrics", envOr("ATELET_METRICS", "dns:atelet-metrics.ate-system.svc.cluster.local:9090"),
		`atelet Prometheus endpoints: "dns:<host>:<port>", a comma-separated URL list, or "" to disable`)
	flag.Parse()

	agentClient := func(agent string) *kagent.Kagent {
		k, err := kagent.NewKagent(*kagentURL, "arcade", *namespace, agent)
		if err != nil {
			slog.Error("kagent client", "agent", agent, "err", err)
			os.Exit(1)
		}
		return k
	}
	p := platform.New(agentClient(""))
	duck := duckhunt.New(p, agentClient("duck"), *idle)
	flw := flow.New(p, agentClient("flowbot"))
	kagent.Observe(flw.OnOp) // every game's kagent calls show on the Agent Flow screen
	gta := heist.New(p, heist.NewKagentOps(agentClient("heister"), agentClient("sidekick"), agentClient("siegeplayer"), envOr("HEIST_EXTRACT", "resume-first")),
		strings.Split(envOr("HEIST_NODES", "kagent-worker,kagent-worker2"), ","))
	cd := defense.New(p, defense.NewKagentOps(agentClient(defense.AgentName)))
	gw := envOr("GATEDEF_GATEWAY", "http://arcade.agentgateway-system.svc.cluster.local")
	gd := gatewaydef.New(p, &gatewaydef.HTTPProber{GuardURL: gw + "/gatedef/guard/v1/chat/completions", LimitURL: gw + "/gatedef/limit",
		Client: &http.Client{Timeout: 5 * time.Second}}, gatewaydef.NewAgentProber(agentClient("rogue")))
	fl := flappy.New(p, agentClient(flappy.AgentName))
	pp := podpacker.New(p, agentClient(podpacker.AgentName))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go p.RunSubstratePoller(ctx)
	go p.RunAteletScraper(ctx, &platform.AteletScraper{Target: *atelet})
	duck.Run(ctx)
	flw.Run(ctx)
	gta.Run(ctx)
	cd.Run(ctx)
	gd.Run(ctx)
	fl.Run(ctx)
	pp.Run(ctx)
	missions := newExtBoard(missionsBoard)
	go missions.run(ctx)
	tokens := newTokens() // every game's LLM token spend, from agentgateway's metrics
	go tokens.run(ctx)

	web := func(dir string) fs.FS {
		sub, err := fs.Sub(webFS, path.Join("web", dir))
		if err != nil {
			panic(err)
		}
		return sub
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("GET /{$}", servePage(versionedPage(web(""), "index.html")))
	mux.HandleFunc("GET /tv", servePage(versionedPage(web(""), "tv.html"))) // the booth TV
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(web("shared"))))
	mux.Handle("GET /admin", http.RedirectHandler("/admin/duck-hunt", http.StatusFound))
	arcadeAPI(mux, p, duck, flw, gta, cd, gd, fl, pp, missions, tokens)
	duck.Routes(mux, web("duck-hunt"))
	flw.Routes(mux, web("agent-flow"))
	gta.Routes(mux, web("substrate-siege"))
	cd.Routes(mux, web("cluster-defense"))
	gd.Routes(mux, web("gateway-defense"))
	fl.Routes(mux, web("flappy"))
	pp.Routes(mux, web("pod-packer"))

	srv := &http.Server{Addr: *addr, Handler: mux}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	slog.Info("arcade listening", "addr", *addr, "kagent", *kagentURL, "namespace", *namespace)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("server", "err", err)
		os.Exit(1)
	}
	// Substrate Siege deletes its booked checkpoints, Cluster Defense its round's sessions and
	// Flappy Substrate its octopus pool on the way out (bounded).
	deadline := time.After(20 * time.Second)
	for _, stopped := range []<-chan struct{}{gta.Stopped(), cd.Stopped(), fl.Stopped(), pp.Stopped()} {
		select {
		case <-stopped:
		case <-deadline:
		}
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// arcadeAPI serves what all games share: joining once, and arcade-wide stats.
func arcadeAPI(mux *http.ServeMux, p *platform.Platform, duck *duckhunt.Game, flw *flow.Game, gta *heist.Game, cd *defense.Game, gd *gatewaydef.Game, fl *flappy.Game, pp *podpacker.Game, missions topper, tokens *Tokens) {
	mux.HandleFunc("GET /api/tokens", func(w http.ResponseWriter, r *http.Request) { platform.WriteJSON(w, tokens.View()) })
	mux.HandleFunc("POST /api/join", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Nick string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			platform.HTTPError(w, http.StatusBadRequest, "bad request")
			return
		}
		nick, ok := platform.CleanNick(req.Nick)
		if !ok {
			platform.HTTPError(w, http.StatusBadRequest, "pick a nickname: 2-16 letters, numbers, spaces, - or _")
			return
		}
		pl := p.Players.Join(nick)
		http.SetCookie(w, &http.Cookie{Name: platform.PlayerCookie, Value: pl.ID, Path: "/", MaxAge: 7 * 24 * 3600, HttpOnly: true, SameSite: http.SameSiteLaxMode})
		platform.WriteJSON(w, pl)
	})
	mux.HandleFunc("GET /api/me", p.Players.WithPlayer(func(w http.ResponseWriter, r *http.Request, pl platform.Player) {
		platform.WriteJSON(w, pl)
	}))

	// Every game's leaderboard, top 5 each, after the arcade-wide one (path "/", top 10):
	// every player's best score in each of our games, in arcade points (overall.go).
	mux.HandleFunc("GET /api/boards", func(w http.ResponseWriter, r *http.Request) {
		all := arcadeBoards(1000, duck, flw, gta, cd, gd, fl, pp, missions)
		out := []GameBoard{{Game: "All games", Path: "/", Unit: "arcade pts", Rows: overallBoard(10, all)}}
		for _, g := range all {
			g.Rows = g.Rows[:min(5, len(g.Rows))]
			out = append(out, g)
		}
		platform.WriteJSON(w, out)
	})

	// Flat numbers for dashboards (Homepage customapi widgets).
	mux.HandleFunc("GET /api/stats", func(w http.ResponseWriter, r *http.Request) {
		s := p.Substrate()
		t := p.Timings.Snapshot(nil)
		out := map[string]any{
			"pools": len(s.Pools), "workers": s.Workers, "agents": len(s.Agents),
			"actors": s.Actors, "running": s.Running, "suspended": s.Suspended,
			"players": p.Players.Count(), "snapshotP50Ms": t["snapshot"].P50, "restoreP50Ms": t["restore"].P50,
		}
		tv := tokens.View()
		out["tokens"], out["tokensPerMin"], out["tokensUsd"], out["tokensUsdPerMin"] = math.Round(tv.Tokens), math.Round(tv.PerMin), math.Round(tv.USD*100)/100, math.Round(tv.USDMin*1000)/1000
		for _, pool := range s.Pools {
			out["pool_"+pool.Name] = fmt.Sprintf("%d/%d workers", pool.Workers, pool.Replicas)
		}
		for k, v := range duck.Stats() {
			out["duckHunt_"+k] = v
		}
		for k, v := range flw.Stats() {
			out["agentFlow_"+k] = v
		}
		for k, v := range gta.Stats() {
			out["heist_"+k] = v
		}
		for k, v := range cd.Stats() {
			out["clusterDefense_"+k] = v
		}
		for k, v := range gd.Stats() {
			out["gatewayDefense_"+k] = v
		}
		for k, v := range fl.Stats() {
			out["flappy_"+k] = v
		}
		for k, v := range pp.Stats() {
			out["podPacker_"+k] = v
		}
		platform.WriteJSON(w, out)
	})
	// Duck Hunt stats at their own URL, kept for existing dashboards.
	mux.HandleFunc("GET /duck-hunt/api/stats", func(w http.ResponseWriter, r *http.Request) { platform.WriteJSON(w, duck.Stats()) })
}

// GameBoard is one game's leaderboard on the arcade home page: Unit names its score.
// External games are not the arcade's: Path is their full URL.
type GameBoard struct {
	Game     string              `json:"game"`
	Path     string              `json:"path"`
	Unit     string              `json:"unit"`
	Rows     []platform.BoardRow `json:"rows"`
	External bool                `json:"external,omitempty"`
}

// topper is a game with a leaderboard.
type topper interface {
	Top(n int) []platform.BoardRow
}

// arcadeBoards is every game's top n, in the home page's order.
func arcadeBoards(n int, duck, flw, gta, cd, gd, fl, pp, missions topper) []GameBoard {
	return []GameBoard{
		{"Substrate Siege", "/substrate-siege", "best run", gta.Top(n), false},
		{"Cluster Defense", "/cluster-defense", "best round", cd.Top(n), false},
		{"Gateway Defense", "/gateway-defense", "points", gd.Top(n), false},
		{"Agent Flow", "/agent-flow", "tasks", flw.Top(n), false},
		{"Duck Hunt", "/duck-hunt", "points", duck.Top(n), false},
		{"Flappy Substrate", "/flappy", "points", fl.Top(n), false},
		{"Pod Packer", "/pod-packer", "points", pp.Top(n), false},
		{"AI agentgateway missions", missionsURL, "pts", missions.Top(n), true},
	}
}
