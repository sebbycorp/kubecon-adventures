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
	"net/http"
	"os"
	"os/signal"
	"path"
	"strings"
	"syscall"
	"time"

	"github.com/sebbycorp/kubecon-adventures/arcade/games/defense"
	"github.com/sebbycorp/kubecon-adventures/arcade/games/duckhunt"
	"github.com/sebbycorp/kubecon-adventures/arcade/games/escape"
	"github.com/sebbycorp/kubecon-adventures/arcade/games/flow"
	"github.com/sebbycorp/kubecon-adventures/arcade/games/fork"
	"github.com/sebbycorp/kubecon-adventures/arcade/games/heist"
	"github.com/sebbycorp/kubecon-adventures/arcade/internal/kagent"
	"github.com/sebbycorp/kubecon-adventures/arcade/internal/platform"
)

//go:embed web
var webFS embed.FS

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	kagentURL := flag.String("kagent-url", envOr("KAGENT_URL", "http://kagent-controller.kagent:8083"), "kagent controller URL")
	namespace := flag.String("namespace", envOr("AGENT_NAMESPACE", "kagent"), "namespace of the games' Agents")
	idle := flag.Int("idle", 20, "Duck Hunt: seconds before the Hunter shoots an idle duck")
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
	esc := escape.New(p, agentClient("escapee"))
	frk := fork.New(p, agentClient("forky"))
	flw := flow.New(p, agentClient("flowbot"))
	gta := heist.New(p, heist.NewKagentOps(agentClient("heister"), agentClient("sidekick"), envOr("HEIST_EXTRACT", "resume-first")),
		strings.Split(envOr("HEIST_NODES", "kagent-worker,kagent-worker2"), ","))
	cd := defense.New(p, defense.NewKagentOps(agentClient(defense.AgentName)))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go p.RunSubstratePoller(ctx)
	go p.RunAteletScraper(ctx, &platform.AteletScraper{Target: *atelet})
	duck.Run(ctx)
	esc.Run(ctx)
	frk.Run(ctx)
	flw.Run(ctx)
	gta.Run(ctx)
	cd.Run(ctx)

	web := func(dir string) fs.FS {
		sub, err := fs.Sub(webFS, path.Join("web", dir))
		if err != nil {
			panic(err)
		}
		return sub
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { http.ServeFileFS(w, r, web(""), "index.html") })
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(web("shared"))))
	mux.Handle("GET /admin", http.RedirectHandler("/admin/duck-hunt", http.StatusFound))
	arcadeAPI(mux, p, duck, esc, frk, flw, gta, cd)
	duck.Routes(mux, web("duck-hunt"))
	esc.Routes(mux, web("sandbox-escape"))
	frk.Routes(mux, web("fork-frenzy"))
	flw.Routes(mux, web("agent-flow"))
	gta.Routes(mux, web("substrate-siege"))
	cd.Routes(mux, web("cluster-defense"))

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
	// Substrate Siege deletes its booked checkpoints and Cluster Defense its round's
	// sessions on the way out (bounded).
	deadline := time.After(20 * time.Second)
	for _, stopped := range []<-chan struct{}{gta.Stopped(), cd.Stopped()} {
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
func arcadeAPI(mux *http.ServeMux, p *platform.Platform, duck *duckhunt.Game, esc *escape.Game, frk *fork.Game, flw *flow.Game, gta *heist.Game, cd *defense.Game) {
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

	// Flat numbers for dashboards (Homepage customapi widgets).
	mux.HandleFunc("GET /api/stats", func(w http.ResponseWriter, r *http.Request) {
		s := p.Substrate()
		t := p.Timings.Snapshot(nil)
		out := map[string]any{
			"pools": len(s.Pools), "workers": s.Workers, "agents": len(s.Agents),
			"actors": s.Actors, "running": s.Running, "suspended": s.Suspended,
			"players": p.Players.Count(), "snapshotP50Ms": t["snapshot"].P50, "restoreP50Ms": t["restore"].P50,
		}
		for _, pool := range s.Pools {
			out["pool_"+pool.Name] = fmt.Sprintf("%d/%d workers", pool.Workers, pool.Replicas)
		}
		for k, v := range duck.Stats() {
			out["duckHunt_"+k] = v
		}
		for k, v := range esc.Stats() {
			out["sandboxEscape_"+k] = v
		}
		for k, v := range frk.Stats() {
			out["forkFrenzy_"+k] = v
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
		platform.WriteJSON(w, out)
	})
	// Old Duck Hunt stats URL, kept for existing dashboards.
	mux.HandleFunc("GET /duck-hunt/api/stats", func(w http.ResponseWriter, r *http.Request) { platform.WriteJSON(w, duck.Stats()) })
}
