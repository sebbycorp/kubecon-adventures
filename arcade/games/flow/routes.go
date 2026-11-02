package flow

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"regexp"
	"strings"

	"github.com/sebbycorp/kubecon-adventures/arcade/internal/platform"
)

// versioned rewrites a page's /agent-flow/static/<file> links to carry a hash of each
// file, so Cloudflare (which caches .js and .svg for hours) serves every deploy at once.
func versioned(web fs.FS, page string) []byte {
	b, err := fs.ReadFile(web, page)
	if err != nil {
		panic(err)
	}
	return staticRE.ReplaceAllFunc(b, func(link []byte) []byte {
		f, err := fs.ReadFile(web, strings.TrimPrefix(string(link), "/agent-flow/static/"))
		if err != nil {
			return link
		}
		sum := sha256.Sum256(f)
		return fmt.Appendf(nil, "%s?v=%x", link, sum[:4])
	})
}

var staticRE = regexp.MustCompile(`/agent-flow/static/[\w.-]+`)

// Routes mounts Agent Flow under /agent-flow and its admin under /admin/agent-flow.
func (g *Game) Routes(mux *http.ServeMux, web fs.FS) {
	page := func(name string) http.HandlerFunc {
		body := versioned(web, name)
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			_, _ = w.Write(body)
		}
	}
	mux.HandleFunc("GET /agent-flow", page("phone.html"))
	mux.HandleFunc("GET /agent-flow/screen", page("screen.html"))
	mux.Handle("GET /agent-flow/static/", http.StripPrefix("/agent-flow/static/", http.FileServerFS(web)))
	mux.HandleFunc("GET /agent-flow/api/events", g.hub.ServeSSE)
	mux.HandleFunc("POST /agent-flow/api/run", g.p.Players.WithPlayer(func(w http.ResponseWriter, r *http.Request, p platform.Player) {
		var req struct{ Task, Ask string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			platform.HTTPError(w, http.StatusBadRequest, "bad request")
			return
		}
		platform.Result(w, nil, g.Start(p, req.Task, req.Ask))
	}))
	mux.HandleFunc("POST /agent-flow/api/sleep", g.p.Players.WithPlayer(func(w http.ResponseWriter, r *http.Request, p platform.Player) {
		platform.Result(w, nil, g.Sleep(p))
	}))
	mux.HandleFunc("POST /agent-flow/api/wake", g.p.Players.WithPlayer(func(w http.ResponseWriter, r *http.Request, p platform.Player) {
		platform.Result(w, nil, g.Wake(p))
	}))
	// The booth webhook agents call. Anyone can call it; only a cleaned name is kept.
	mux.HandleFunc("POST /agent-flow/hook", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Name string }
		_ = json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&req)
		platform.WriteJSON(w, map[string]any{"ok": true, "message": g.HookCalled(req.Name)})
	})

	mux.HandleFunc("POST /admin/agent-flow/api/reset", func(w http.ResponseWriter, r *http.Request) {
		n, err := g.Reset(r.Context())
		platform.Result(w, map[string]int{"deletedSessions": n}, err)
	})
	mux.HandleFunc("GET /admin/agent-flow/api/state", func(w http.ResponseWriter, r *http.Request) { platform.WriteJSON(w, g.Snapshot()) })
}
