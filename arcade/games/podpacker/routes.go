package podpacker

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/sebbycorp/kubecon-adventures/arcade/internal/platform"
)

// staticRE matches a page's links to its own static files.
var staticRE = regexp.MustCompile(`/pod-packer/static/[\w.-]+`)

// versioned is page from web with each /pod-packer/static/<file> link carrying a hash of
// the file, so Cloudflare (which caches .js for hours) serves every deploy at once.
func versioned(web fs.FS, page string) []byte {
	b, err := fs.ReadFile(web, page)
	if err != nil {
		panic(err)
	}
	return staticRE.ReplaceAllFunc(b, func(link []byte) []byte {
		f, err := fs.ReadFile(web, strings.TrimPrefix(string(link), "/pod-packer/static/"))
		if err != nil {
			return link
		}
		sum := sha256.Sum256(f)
		return fmt.Appendf(nil, "%s?v=%x", link, sum[:4])
	})
}

// maxScreens caps the big-screen event streams at once.
const maxScreens = 64

// Routes mounts Pod Packer: pages and API under /pod-packer, admin under /admin/pod-packer.
// web holds phone.html, screen.html and packer.js.
func (g *Game) Routes(mux *http.ServeMux, web fs.FS) {
	page := func(name string) http.HandlerFunc {
		body := versioned(web, name)
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			_, _ = w.Write(body)
		}
	}
	players := g.p.Players
	var mu sync.Mutex
	streams := 0

	mux.HandleFunc("GET /pod-packer", page("phone.html"))
	mux.HandleFunc("GET /pod-packer/screen", page("screen.html"))
	mux.Handle("GET /pod-packer/static/", http.StripPrefix("/pod-packer/static/", http.FileServerFS(web)))
	mux.HandleFunc("GET /pod-packer/api/rules", func(w http.ResponseWriter, r *http.Request) { platform.WriteJSON(w, g.rules) })
	mux.HandleFunc("GET /pod-packer/api/board", func(w http.ResponseWriter, r *http.Request) { platform.WriteJSON(w, g.Top(10)) })
	mux.HandleFunc("GET /pod-packer/api/events", func(w http.ResponseWriter, r *http.Request) {
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

	body := func(r *http.Request, v any) error { return json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(v) }
	mux.HandleFunc("POST /pod-packer/api/start", players.WithPlayer(func(w http.ResponseWriter, r *http.Request, p platform.Player) {
		s, err := g.Start(p)
		platform.Result(w, s, err)
	}))
	mux.HandleFunc("POST /pod-packer/api/clear", players.WithPlayer(func(w http.ResponseWriter, r *http.Request, p platform.Player) {
		var req struct {
			Run         string
			Lines, Node int
		}
		if body(r, &req) != nil {
			platform.HTTPError(w, http.StatusBadRequest, "bad request")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		res, err := g.Clear(ctx, p.ID, req.Run, req.Lines, req.Node)
		platform.Result(w, res, err)
	}))
	mux.HandleFunc("POST /pod-packer/api/finish", players.WithPlayer(func(w http.ResponseWriter, r *http.Request, p platform.Player) {
		var req struct {
			Run                                   string
			Score, Lines, Tetrises, Pieces, Level int
		}
		if body(r, &req) != nil {
			platform.HTTPError(w, http.StatusBadRequest, "bad request")
			return
		}
		res, err := g.Finish(p.ID, req.Run, req.Score, req.Lines, req.Tetrises, req.Pieces)
		platform.Result(w, res, err)
	}))

	// Admin: everything under /admin, which the tunnel blocks publicly.
	mux.HandleFunc("GET /admin/pod-packer/api/state", func(w http.ResponseWriter, r *http.Request) { platform.WriteJSON(w, g.Snapshot()) })
	mux.HandleFunc("POST /admin/pod-packer/api/reset", func(w http.ResponseWriter, r *http.Request) {
		g.Reset()
		platform.WriteJSON(w, map[string]bool{"ok": true})
	})
}

// Stats are Pod Packer's numbers for dashboards (the home page reads players).
func (g *Game) Stats() map[string]any {
	s := g.Snapshot()
	return map[string]any{
		"players": s.Runs, "pool": s.Pool, "awake": s.Awake, "asleep": s.Asleep, "snapshots": s.Snaps, "clears": s.Clears,
		"suspendP50Ms": s.Timings["suspend"].P50, "resumeP50Ms": s.Timings["resume"].P50,
	}
}
