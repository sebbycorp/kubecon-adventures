package heist

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

	"github.com/sebbycorp/kubecon-adventures/arcade/internal/platform"
)

const staticPrefix = "/grand-theft-agent/static/"

// resetTimeout bounds a whole admin reset.
const resetTimeout = 2 * opTimeout

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
	city := g.city.View()
	mux.HandleFunc("GET /grand-theft-agent/api/city", func(w http.ResponseWriter, r *http.Request) {
		platform.WriteJSON(w, city)
	})

	mux.HandleFunc("POST /admin/grand-theft-agent/api/reset", func(w http.ResponseWriter, r *http.Request) {
		// A dropped admin request must not cancel the deletes half way.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), resetTimeout)
		defer cancel()
		n, err := g.Reset(ctx)
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

// RectView is a Rect on the wire.
type RectView struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// CityView is GET /grand-theft-agent/api/city: the map the clients draw. Tiles are rows
// of tile bytes ('.' road, '#' building, 'V' Vault, 'D' Dispatch); districts are by node.
type CityView struct {
	W         int                 `json:"w"`
	H         int                 `json:"h"`
	Tiles     []string            `json:"tiles"`
	Districts map[string]RectView `json:"districts"`
	Vault     RectView            `json:"vault"`
	Dispatch  RectView            `json:"dispatch"`
}

// View is the city as clients see it. The city never changes after NewCity.
func (c *City) View() CityView {
	rv := func(r Rect) RectView { return RectView{r.X, r.Y, r.W, r.H} }
	v := CityView{W: c.W, H: c.H, Districts: map[string]RectView{}, Vault: rv(c.Vault), Dispatch: rv(c.Dispatch)}
	for y := range c.H {
		v.Tiles = append(v.Tiles, string(c.Tiles[y*c.W:(y+1)*c.W]))
	}
	for n, r := range c.Districts {
		v.Districts[n] = rv(r)
	}
	return v
}
