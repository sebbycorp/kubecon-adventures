package heist

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

const staticPrefix = "/grand-theft-agent/static/"

// versioned rewrites a page's /grand-theft-agent/static/<file> links to carry a hash of
// each file, so Cloudflare (which caches .js for hours) serves every deploy at once.
func versioned(web fs.FS, page string) []byte {
	b, err := fs.ReadFile(web, page)
	if err != nil {
		panic(err)
	}
	return staticRE.ReplaceAllFunc(b, func(link []byte) []byte {
		f, err := fs.ReadFile(web, strings.TrimPrefix(string(link), staticPrefix))
		if err != nil {
			return link
		}
		sum := sha256.Sum256(f)
		return fmt.Appendf(nil, "%s?v=%x", link, sum[:4])
	})
}

var staticRE = regexp.MustCompile(`/grand-theft-agent/static/[\w./-]+`)

// Routes mounts Grand Theft Agent under /grand-theft-agent and its admin under
// /admin/grand-theft-agent.
func (g *Game) Routes(mux *http.ServeMux, web fs.FS) {
	page := func(name string) http.HandlerFunc {
		body := versioned(web, name)
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			_, _ = w.Write(body)
		}
	}
	mux.HandleFunc("GET /grand-theft-agent", page("phone.html"))
	mux.HandleFunc("GET /grand-theft-agent/screen", page("screen.html"))
	mux.Handle("GET "+staticPrefix, http.StripPrefix(staticPrefix, http.FileServerFS(web)))
	mux.HandleFunc("GET /grand-theft-agent/ws", g.serveWS)

	mux.HandleFunc("POST /admin/grand-theft-agent/api/reset", func(w http.ResponseWriter, r *http.Request) {
		n, err := g.Reset(r.Context())
		platform.Result(w, map[string]int{"deletedSessions": n}, err)
	})
	mux.HandleFunc("GET /admin/grand-theft-agent/api/state", func(w http.ResponseWriter, r *http.Request) {
		platform.WriteJSON(w, g.State())
	})
	mux.HandleFunc("POST /admin/grand-theft-agent/api/pause", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Paused *bool `json:"paused"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&req); err != nil || req.Paused == nil {
			platform.HTTPError(w, http.StatusBadRequest, `want {"paused":bool}`)
			return
		}
		g.Pause(*req.Paused)
		platform.WriteJSON(w, map[string]bool{"paused": *req.Paused})
	})
}

// AdminState is GET /admin/grand-theft-agent/api/state: what a screen sees, plus the
// pause switch and the per-player stashes.
type AdminState struct {
	SnapCommon
	Paused bool                  `json:"paused"`
	Stash  map[string][]PartView `json:"stash"`
}

// State is the admin view of the whole game.
func (g *Game) State() AdminState {
	g.mu.Lock()
	defer g.mu.Unlock()
	s := AdminState{SnapCommon: g.commonLocked(nil), Paused: g.paused, Stash: map[string][]PartView{}}
	for id, ps := range g.w.Stash {
		s.Stash[id] = *parts(ps)
	}
	return s
}
