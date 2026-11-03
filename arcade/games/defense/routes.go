package defense

import (
	"bytes"
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

const staticPrefix = "/cluster-defense/static/"

var (
	staticRE = regexp.MustCompile(`/cluster-defense/static/[\w./-]+`)
	importRE = regexp.MustCompile(`((?:from|import)\s*)"(\./[\w./-]+\.js)"`)
)

// versioned rewrites a page's static links to carry a hash of each file as served, so
// Cloudflare (which caches .js for hours) serves every deploy at once.
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

// served is a static file as it goes out: a module's relative imports carry the hash
// of what is served for them, so a change anywhere reaches the page's link.
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

// Routes mounts Cluster Defense under /cluster-defense and its admin under
// /admin/cluster-defense.
func (g *Game) Routes(mux *http.ServeMux, web fs.FS) {
	page := func(name string) http.HandlerFunc {
		body := versioned(web, name)
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			_, _ = w.Write(body)
		}
	}
	mux.HandleFunc("GET /cluster-defense", page("phone.html"))
	mux.HandleFunc("GET /cluster-defense/screen", page("screen.html"))
	mux.Handle("GET "+staticPrefix, staticFiles(web))
	mux.HandleFunc("GET /cluster-defense/ws", g.serveWS)

	mux.HandleFunc("GET /admin/cluster-defense/api/state", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		s := g.commonLocked(nil)
		g.mu.Unlock()
		platform.WriteJSON(w, s)
	})
	mux.HandleFunc("POST /admin/cluster-defense/api/pause", func(w http.ResponseWriter, r *http.Request) {
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
	// End the live round now: every session it made is deleted.
	mux.HandleFunc("POST /admin/cluster-defense/api/end", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		if g.w.Round.Phase == Live {
			g.w.end("ended")
			for _, in := range g.w.Tick(0) {
				g.submit(in)
			}
		}
		g.mu.Unlock()
		platform.WriteJSON(w, map[string]bool{"ok": true})
	})
}
