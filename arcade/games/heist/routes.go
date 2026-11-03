package heist

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"

	"github.com/sebbycorp/kubecon-adventures/arcade/internal/platform"
)

const staticPrefix = "/substrate-siege/static/"

// resetTimeout bounds a whole admin reset.
const resetTimeout = 2 * opTimeout

// versioned rewrites a page's /substrate-siege/static/<file> links to carry a hash of
// each file as served, so Cloudflare (which caches .js for hours) serves every deploy at
// once.
func versioned(web fs.FS, page string) []byte {
	b, err := fs.ReadFile(web, page)
	if err != nil {
		panic(err)
	}
	return staticRE.ReplaceAllFunc(b, func(link []byte) []byte {
		f, err := served(web, strings.TrimPrefix(string(link), staticPrefix), 0)
		if err != nil {
			return link
		}
		sum := sha256.Sum256(f)
		return fmt.Appendf(nil, "%s?v=%x", link, sum[:4])
	})
}

var (
	staticRE = regexp.MustCompile(`/substrate-siege/static/[\w./-]+`)
	importRE = regexp.MustCompile(`((?:from|import)\s*)"(\./[\w./-]+\.js)"`)
)

// served is a static file as it goes out. A JavaScript module's relative imports
// ("./x.js") get ?v=<hash of what is served for x.js>, so a change anywhere in a module
// graph changes every module above it, up to the page's link. depth guards a cycle.
func served(web fs.FS, name string, depth int) ([]byte, error) {
	b, err := fs.ReadFile(web, name)
	if err != nil || !strings.HasSuffix(name, ".js") || depth > 8 || !importRE.Match(b) {
		return b, err
	}
	dir := path.Dir(name)
	return importRE.ReplaceAllFunc(b, func(m []byte) []byte {
		sub := importRE.FindSubmatch(m)
		dep, err := served(web, path.Join(dir, string(sub[2])), depth+1)
		if err != nil {
			return m
		}
		sum := sha256.Sum256(dep)
		return fmt.Appendf(nil, `%s"%s?v=%x"`, sub[1], sub[2], sum[:4])
	}), nil
}

// staticFiles serves /substrate-siege/static/: modules with relative imports from
// memory (rewritten once at start), everything else from web.
func staticFiles(web fs.FS) http.Handler {
	files := http.StripPrefix(staticPrefix, http.FileServerFS(web))
	mods := map[string][]byte{}
	_ = fs.WalkDir(web, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".js") {
			return nil
		}
		raw, _ := fs.ReadFile(web, p)
		if b, err := served(web, p, 0); err == nil && !bytes.Equal(b, raw) {
			mods[p] = b
		}
		return nil
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if b, ok := mods[strings.TrimPrefix(r.URL.Path, staticPrefix)]; ok {
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			_, _ = w.Write(b)
			return
		}
		files.ServeHTTP(w, r)
	})
}

// Routes mounts Substrate Siege under /substrate-siege and its admin under
// /admin/substrate-siege.
func (g *Game) Routes(mux *http.ServeMux, web fs.FS) {
	page := func(name string) http.HandlerFunc {
		body := versioned(web, name)
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			_, _ = w.Write(body)
		}
	}
	mux.HandleFunc("GET /substrate-siege", page("phone.html"))
	// The game's first name: old QR codes and links still land.
	mux.Handle("GET /grand-theft-agent", http.RedirectHandler("/substrate-siege", http.StatusMovedPermanently))
	mux.Handle("GET /grand-theft-agent/screen", http.RedirectHandler("/substrate-siege/screen", http.StatusMovedPermanently))
	mux.HandleFunc("GET /substrate-siege/screen", page("screen.html"))
	mux.Handle("GET "+staticPrefix, staticFiles(web))
	mux.HandleFunc("GET /substrate-siege/ws", g.serveWS)
	city := g.city.View()
	mux.HandleFunc("GET /substrate-siege/api/city", func(w http.ResponseWriter, r *http.Request) {
		platform.WriteJSON(w, city)
	})

	mux.HandleFunc("POST /admin/substrate-siege/api/reset", func(w http.ResponseWriter, r *http.Request) {
		// A dropped admin request must not cancel the deletes half way.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), resetTimeout)
		defer cancel()
		n, cps, err := g.Reset(ctx)
		platform.Result(w, map[string]int{"deletedSessions": n, "deletedCheckpoints": cps}, err)
	})
	mux.HandleFunc("GET /admin/substrate-siege/api/state", func(w http.ResponseWriter, r *http.Request) {
		platform.WriteJSON(w, g.State())
	})
	mux.HandleFunc("POST /admin/substrate-siege/api/pause", func(w http.ResponseWriter, r *http.Request) {
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

// AdminState is GET /admin/substrate-siege/api/state: what a screen sees, plus the
// pause switch.
type AdminState struct {
	SnapCommon
	Paused bool `json:"paused"`
}

// State is the admin view of the whole game.
func (g *Game) State() AdminState {
	g.mu.Lock()
	defer g.mu.Unlock()
	return AdminState{SnapCommon: g.commonLocked(nil), Paused: g.paused}
}

// RectView is a Rect on the wire.
type RectView struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// CityView is GET /substrate-siege/api/city: the map the clients draw. Tiles are rows
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
