package selfie

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"regexp"
	"strings"
	"sync"

	"github.com/sebbycorp/kubecon-adventures/arcade/internal/platform"
)

var staticRE = regexp.MustCompile(`/selfie/static/[\w.-]+`)

// versioned is page from web with each /selfie/static/<file> link carrying a hash of the
// file, so Cloudflare serves every deploy at once.
func versioned(web fs.FS, page string) []byte {
	b, err := fs.ReadFile(web, page)
	if err != nil {
		panic(err)
	}
	return staticRE.ReplaceAllFunc(b, func(link []byte) []byte {
		f, err := fs.ReadFile(web, strings.TrimPrefix(string(link), "/selfie/static/"))
		if err != nil {
			return link
		}
		sum := sha256.Sum256(f)
		return fmt.Appendf(nil, "%s?v=%x", link, sum[:4])
	})
}

const maxScreens = 32

// Routes mounts Substrate Selfie: pages and API under /selfie, admin under /admin/selfie.
func (g *Game) Routes(mux *http.ServeMux, web fs.FS) {
	page := func(name string) http.HandlerFunc {
		body := versioned(web, name)
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			_, _ = w.Write(body)
		}
	}
	var mu sync.Mutex
	streams := 0
	mux.HandleFunc("GET /selfie", page("phone.html"))
	mux.HandleFunc("GET /selfie/screen", page("screen.html"))
	mux.Handle("GET /selfie/static/", http.StripPrefix("/selfie/static/", http.FileServerFS(web)))
	mux.HandleFunc("GET /selfie/api/scenes", func(w http.ResponseWriter, r *http.Request) { platform.WriteJSON(w, Scenes) })
	mux.HandleFunc("GET /selfie/api/events", func(w http.ResponseWriter, r *http.Request) {
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
	// The photo is the raw JPEG body. It is read into memory, captioned, and dropped.
	mux.HandleFunc("POST /selfie/api/captions", g.p.Players.WithPlayer(func(w http.ResponseWriter, r *http.Request, p platform.Player) {
		img, err := io.ReadAll(io.LimitReader(r.Body, maxImage+1))
		if err != nil {
			platform.HTTPError(w, http.StatusBadRequest, "bad upload")
			return
		}
		res, err := g.Make(r.Context(), p, img)
		switch {
		case errors.Is(err, ErrTooBig):
			platform.HTTPError(w, http.StatusRequestEntityTooLarge, err.Error())
		case errors.Is(err, ErrSlowDown), errors.Is(err, ErrBusy):
			platform.HTTPError(w, http.StatusTooManyRequests, err.Error())
		case err != nil:
			platform.HTTPError(w, http.StatusBadRequest, err.Error())
		default:
			platform.WriteJSON(w, res)
		}
	}))
	mux.HandleFunc("GET /admin/selfie/api/state", func(w http.ResponseWriter, r *http.Request) { platform.WriteJSON(w, g.Snapshot()) })
	mux.HandleFunc("POST /admin/selfie/api/reset", func(w http.ResponseWriter, r *http.Request) {
		g.Reset()
		platform.WriteJSON(w, map[string]bool{"ok": true})
	})
}
