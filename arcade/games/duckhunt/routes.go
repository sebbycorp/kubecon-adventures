package duckhunt

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"sync"
	"time"

	"github.com/sebbycorp/kubecon-adventures/arcade/internal/platform"
)

// maxScreens caps the big-screen and phone event streams at once (the state carries the
// board and the feed, pushed up to 10 times a second).
const maxScreens = 64

// Routes mounts Agent Hunt: pages and API under /duck-hunt, admin under /admin/duck-hunt.
// web holds phone.html, screen.html, admin.html and hunt.js.
func (g *Game) Routes(mux *http.ServeMux, web fs.FS) {
	page := func(name string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeFileFS(w, r, web, name)
		}
	}
	players := g.p.Players
	var mu sync.Mutex
	streams := 0

	mux.HandleFunc("GET /duck-hunt", page("phone.html"))
	mux.HandleFunc("GET /duck-hunt/screen", page("screen.html"))
	mux.Handle("GET /duck-hunt/static/", http.StripPrefix("/duck-hunt/static/", http.FileServerFS(web)))
	mux.HandleFunc("GET /duck-hunt/api/events", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if streams >= maxScreens {
			mu.Unlock()
			platform.HTTPError(w, http.StatusTooManyRequests, "too many screens")
			return
		}
		streams++
		mu.Unlock()
		defer func() { mu.Lock(); streams--; mu.Unlock() }()
		g.hub.ServeSSE(w, r)
	})
	mux.HandleFunc("GET /duck-hunt/api/board", func(w http.ResponseWriter, r *http.Request) { platform.WriteJSON(w, g.Top(10)) })

	body := func(r *http.Request, v any) error { return json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(v) }
	mux.HandleFunc("POST /duck-hunt/api/start", players.WithPlayer(func(w http.ResponseWriter, r *http.Request, p platform.Player) {
		id, err := g.Start(p)
		platform.Result(w, map[string]any{"run": id, "seconds": RunSeconds}, err)
	}))
	mux.HandleFunc("POST /duck-hunt/api/launch", players.WithPlayer(func(w http.ResponseWriter, r *http.Request, p platform.Player) {
		var req struct{ Run string }
		if body(r, &req) != nil {
			platform.HTTPError(w, http.StatusBadRequest, "bad request")
			return
		}
		l, err := g.Launch(r.Context(), p.ID, req.Run)
		if errors.Is(err, ErrBusy) {
			platform.WriteJSON(w, map[string]any{"busy": true, "retryMs": 450})
			return
		}
		platform.Result(w, l, err)
	}))
	mux.HandleFunc("POST /duck-hunt/api/hit", players.WithPlayer(func(w http.ResponseWriter, r *http.Request, p platform.Player) {
		var req struct{ Run, Flight string }
		if body(r, &req) != nil {
			platform.HTTPError(w, http.StatusBadRequest, "bad request")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
		defer cancel()
		h, err := g.Shoot(ctx, p.ID, req.Run, req.Flight)
		platform.Result(w, h, err)
	}))
	mux.HandleFunc("POST /duck-hunt/api/escape", players.WithPlayer(func(w http.ResponseWriter, r *http.Request, p platform.Player) {
		var req struct{ Run, Flight string }
		if body(r, &req) != nil {
			platform.HTTPError(w, http.StatusBadRequest, "bad request")
			return
		}
		platform.Result(w, nil, g.Escape(p.ID, req.Run, req.Flight))
	}))
	mux.HandleFunc("POST /duck-hunt/api/finish", players.WithPlayer(func(w http.ResponseWriter, r *http.Request, p platform.Player) {
		var req struct{ Run string }
		if body(r, &req) != nil {
			platform.HTTPError(w, http.StatusBadRequest, "bad request")
			return
		}
		res, err := g.Finish(p.ID, req.Run)
		platform.Result(w, res, err)
	}))

	// Admin: everything under /admin, which the tunnel blocks publicly.
	mux.HandleFunc("GET /admin/duck-hunt", page("admin.html"))
	mux.HandleFunc("GET /admin/duck-hunt/api/state", func(w http.ResponseWriter, r *http.Request) { platform.WriteJSON(w, g.Snapshot()) })
	mux.HandleFunc("POST /admin/duck-hunt/api/reset", func(w http.ResponseWriter, r *http.Request) {
		g.Reset()
		platform.WriteJSON(w, map[string]bool{"ok": true})
	})
}

// Stats are Agent Hunt's numbers for dashboards (the home page reads players).
func (g *Game) Stats() map[string]any {
	s := g.Snapshot()
	return map[string]any{
		"players": s.Runs, "pool": s.Pool, "awake": s.Awake, "asleep": s.Asleep, "hits": s.Hits,
		"suspendP50Ms": s.Timings["suspend"].P50, "resumeP50Ms": s.Timings["resume"].P50,
		"llmCalls": s.Turns, "tokens": s.Tokens,
	}
}
