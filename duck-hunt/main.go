// Duck Hunt: every duck is a kagent agent session running as an Agent Substrate actor.
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
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

//go:embed web
var webFS embed.FS

const playerCookie = "dh_player"

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	kagentURL := flag.String("kagent-url", envOr("KAGENT_URL", "http://kagent-controller.kagent:8083"), "kagent controller URL")
	namespace := flag.String("namespace", envOr("DUCK_NAMESPACE", "kagent"), "duck Agent namespace")
	agent := flag.String("agent", envOr("DUCK_AGENT", "duck"), "duck Agent name")
	idle := flag.Int("idle", 20, "seconds before the Hunter shoots an idle duck")
	atelet := flag.String("atelet-metrics", envOr("ATELET_METRICS", "dns:atelet-metrics.ate-system.svc.cluster.local:9090"),
		`atelet Prometheus endpoints: "dns:<host>:<port>", a comma-separated URL list, or "" to disable`)
	flag.Parse()

	k, err := NewKagent(*kagentURL, "duck-hunt", *namespace, *agent)
	if err != nil {
		slog.Error("kagent client", "err", err)
		os.Exit(1)
	}
	g := NewGame(k, *idle)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go g.hub.Run(ctx, g.Snapshot)
	go g.RunHunter(ctx)
	go g.RunSubstratePoller(ctx)
	go g.RunActorPoller(ctx)
	go g.RunAteletScraper(ctx, &AteletScraper{Target: *atelet, TemplatePrefix: *agent + "-"})

	srv := &http.Server{Addr: *addr, Handler: routes(g)}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	slog.Info("duck hunt listening", "addr", *addr, "kagent", *kagentURL, "agent", *namespace+"/"+*agent)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("server", "err", err)
		os.Exit(1)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func routes(g *Game) http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(webFS, "web")
	page := func(name string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { http.ServeFileFS(w, r, static, name) }
	}

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.Handle("GET /{$}", http.RedirectHandler("/duck-hunt", http.StatusFound))
	mux.HandleFunc("GET /duck-hunt", page("phone.html"))
	mux.HandleFunc("GET /duck-hunt/screen", page("screen.html"))
	mux.Handle("GET /duck-hunt/static/", http.StripPrefix("/duck-hunt/static/", http.FileServerFS(static)))
	mux.HandleFunc("GET /admin", page("admin.html"))

	// Player API
	mux.HandleFunc("GET /duck-hunt/api/events", g.hub.ServeSSE)
	// Flat numbers for dashboards (Homepage customapi widgets).
	mux.HandleFunc("GET /duck-hunt/api/stats", func(w http.ResponseWriter, r *http.Request) {
		s := g.Snapshot()
		out := map[string]any{
			"pools": len(s.Substrate.Pools), "workers": s.Substrate.Workers, "agents": len(s.Substrate.Agents),
			"actors": s.Substrate.Actors, "running": s.Substrate.Running, "suspended": s.Substrate.Suspended,
			"players": s.Players, "ducks": len(s.Ducks), "llmCalls": s.LLMCalls,
			"tokens": s.Tokens.Input + s.Tokens.Output, "duckSandboxesAwake": s.Awake.Now, "duckWakeUps": s.Awake.WakeUps,
		}
		flying, down := 0, 0
		for _, d := range s.Ducks {
			switch d.State {
			case Flying:
				flying++
			case Shot, Reviving:
				down++
			}
		}
		out["ducksFlying"], out["ducksDown"] = flying, down
		for _, k := range []string{"snapshot", "restore", "hatch"} {
			out[k+"P50Ms"] = s.Timings[k].P50
		}
		for _, p := range s.Substrate.Pools {
			out["pool_"+p.Name] = fmt.Sprintf("%d/%d workers", p.Workers, p.Replicas)
		}
		writeJSON(w, out)
	})
	mux.HandleFunc("POST /duck-hunt/api/join", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Nick string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, http.StatusBadRequest, "bad request")
			return
		}
		nick, ok := cleanNick(req.Nick)
		if !ok {
			httpError(w, http.StatusBadRequest, "pick a nickname: 2-16 letters, numbers, spaces, - or _")
			return
		}
		p := g.AddPlayer(nick)
		http.SetCookie(w, &http.Cookie{Name: playerCookie, Value: p.ID, Path: "/", MaxAge: 7 * 24 * 3600, HttpOnly: true, SameSite: http.SameSiteLaxMode})
		writeJSON(w, p)
	})
	mux.HandleFunc("GET /duck-hunt/api/me", func(w http.ResponseWriter, r *http.Request) {
		p, ok := currentPlayer(g, r)
		if !ok {
			httpError(w, http.StatusUnauthorized, "join first")
			return
		}
		writeJSON(w, p)
	})
	mux.HandleFunc("POST /duck-hunt/api/hatch/{kind}", withPlayer(g, func(w http.ResponseWriter, r *http.Request, p Player) {
		id, err := g.Hatch(p.ID, r.PathValue("kind"))
		result(w, map[string]string{"duckId": id}, err)
	}))
	mux.HandleFunc("POST /duck-hunt/api/ducks/{id}/quack", withPlayer(g, func(w http.ResponseWriter, r *http.Request, p Player) {
		result(w, nil, g.Quack(p.ID, r.PathValue("id")))
	}))
	mux.HandleFunc("POST /duck-hunt/api/ducks/{id}/revive", withPlayer(g, func(w http.ResponseWriter, r *http.Request, p Player) {
		result(w, nil, g.Revive(p.ID, r.PathValue("id")))
	}))

	// Admin API: everything under /admin, so sign-in can be added in front of that prefix later.
	mux.HandleFunc("POST /admin/api/ducks/{id}/shoot", func(w http.ResponseWriter, r *http.Request) {
		result(w, nil, g.Shoot(r.PathValue("id")))
	})
	mux.HandleFunc("POST /admin/api/ducks/{id}/revive", func(w http.ResponseWriter, r *http.Request) {
		result(w, nil, g.Revive("", r.PathValue("id")))
	})
	mux.HandleFunc("POST /admin/api/ducks/{id}/delete", func(w http.ResponseWriter, r *http.Request) {
		result(w, nil, g.Delete(r.PathValue("id")))
	})
	mux.HandleFunc("POST /admin/api/settings", func(w http.ResponseWriter, r *http.Request) {
		var s Settings
		if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
			httpError(w, http.StatusBadRequest, "bad request")
			return
		}
		g.SetSettings(s)
		writeJSON(w, g.Snapshot().Settings)
	})
	mux.HandleFunc("POST /admin/api/reset", func(w http.ResponseWriter, r *http.Request) {
		n, err := g.Reset(r.Context())
		result(w, map[string]int{"deletedSessions": n}, err)
	})
	mux.HandleFunc("GET /admin/api/state", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, g.Snapshot()) })
	return mux
}

var nickRE = regexp.MustCompile(`^[\p{L}\p{N} _-]{2,16}$`)

// blocked keeps the obvious stuff off the big screen.
var blocked = []string{"fuck", "shit", "cunt", "nigg", "fag", "bitch", "porn", "nazi", "hitler", "rape", "dick", "cock", "pussy"}

func cleanNick(s string) (string, bool) {
	s = strings.Join(strings.Fields(s), " ")
	if !nickRE.MatchString(s) {
		return "", false
	}
	lower := strings.ToLower(strings.NewReplacer("0", "o", "1", "i", "3", "e", "4", "a", "5", "s", "$", "s").Replace(s))
	for _, b := range blocked {
		if strings.Contains(lower, b) {
			return "", false
		}
	}
	return s, true
}

func currentPlayer(g *Game, r *http.Request) (Player, bool) {
	c, err := r.Cookie(playerCookie)
	if err != nil {
		return Player{}, false
	}
	return g.Player(c.Value)
}

func withPlayer(g *Game, h func(http.ResponseWriter, *http.Request, Player)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := currentPlayer(g, r)
		if !ok {
			httpError(w, http.StatusUnauthorized, "join first")
			return
		}
		h(w, r, p)
	}
}

func result(w http.ResponseWriter, v any, err error) {
	switch {
	case errors.Is(err, errNotYours):
		httpError(w, http.StatusForbidden, err.Error())
	case err != nil:
		httpError(w, http.StatusConflict, err.Error())
	case v == nil:
		writeJSON(w, map[string]bool{"ok": true})
	default:
		writeJSON(w, v)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// Hub pushes game snapshots to every connected screen over Server-Sent Events,
// at most 10 times a second.
type Hub struct {
	mu      sync.Mutex
	clients map[chan []byte]struct{}
	poke    chan struct{}
}

func NewHub() *Hub {
	return &Hub{clients: map[chan []byte]struct{}{}, poke: make(chan struct{}, 1)}
}

func (h *Hub) Poke() {
	select {
	case h.poke <- struct{}{}:
	default:
	}
}

func (h *Hub) Run(ctx context.Context, snapshot func() State) {
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	heartbeat := time.NewTicker(5 * time.Second)
	defer heartbeat.Stop()
	dirty := true
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.poke:
			dirty = true
			continue
		case <-heartbeat.C:
			dirty = true
			continue
		case <-t.C:
		}
		if !dirty {
			continue
		}
		dirty = false
		b, _ := json.Marshal(snapshot())
		h.mu.Lock()
		for c := range h.clients {
			select {
			case c <- b:
			default: // slow client; it gets the next one
			}
		}
		h.mu.Unlock()
	}
}

func (h *Hub) ServeSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		httpError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	c := make(chan []byte, 1)
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.clients, c)
		h.mu.Unlock()
	}()
	h.Poke()
	for {
		select {
		case <-r.Context().Done():
			return
		case b := <-c:
			if _, err := w.Write(append(append([]byte("data: "), b...), '\n', '\n')); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
