package gatewaydef

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
	"time"

	"github.com/sebbycorp/kubecon-adventures/arcade/internal/platform"
)

// versioned rewrites a page's /gateway-defense/static/<file> links to carry a hash of each
// file, so Cloudflare (which caches .js for hours) serves every deploy at once.
func versioned(web fs.FS, page string) []byte {
	b, err := fs.ReadFile(web, page)
	if err != nil {
		panic(err)
	}
	return staticRE.ReplaceAllFunc(b, func(link []byte) []byte {
		f, err := fs.ReadFile(web, strings.TrimPrefix(string(link), "/gateway-defense/static/"))
		if err != nil {
			return link
		}
		sum := sha256.Sum256(f)
		return fmt.Appendf(nil, "%s?v=%x", link, sum[:4])
	})
}

var staticRE = regexp.MustCompile(`/gateway-defense/static/[\w.-]+`)

// blockTimeout bounds one real call (the Suspend Beam's can take a few seconds).
const blockTimeout = 35 * time.Second

// Routes mounts Gateway Defense under /gateway-defense.
func (g *Game) Routes(mux *http.ServeMux, web fs.FS) {
	page := func(name string) http.HandlerFunc {
		body := versioned(web, name)
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			_, _ = w.Write(body)
		}
	}
	mux.HandleFunc("GET /gateway-defense", page("phone.html"))
	mux.HandleFunc("GET /gateway-defense/screen", page("screen.html"))
	mux.Handle("GET /gateway-defense/static/", http.StripPrefix("/gateway-defense/static/", http.FileServerFS(web)))
	mux.HandleFunc("GET /gateway-defense/api/rules", func(w http.ResponseWriter, r *http.Request) { platform.WriteJSON(w, g.rules) })
	mux.HandleFunc("GET /gateway-defense/api/events", func(w http.ResponseWriter, r *http.Request) {
		done, ok := g.screen(r.Header.Get("cf-connecting-ip") != "")
		if !ok {
			platform.HTTPError(w, http.StatusTooManyRequests, "too many big screens open")
			return
		}
		defer done()
		g.hub.ServeSSE(w, r)
	})
	mux.HandleFunc("POST /gateway-defense/api/start", g.p.Players.WithPlayer(func(w http.ResponseWriter, r *http.Request, p platform.Player) {
		id, err := g.Start(p)
		platform.Result(w, map[string]string{"run": id}, err)
	}))
	mux.HandleFunc("POST /gateway-defense/api/block", g.p.Players.WithPlayer(func(w http.ResponseWriter, r *http.Request, p platform.Player) {
		var req struct{ Run, Tower, Creep string }
		if json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&req) != nil {
			platform.HTTPError(w, http.StatusBadRequest, "bad request")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), blockTimeout)
		defer cancel()
		o, err := g.Block(ctx, p, req.Run, req.Tower, req.Creep)
		platform.Result(w, o, err)
	}))
	mux.HandleFunc("POST /gateway-defense/api/finish", g.p.Players.WithPlayer(func(w http.ResponseWriter, r *http.Request, p platform.Player) {
		var req struct {
			Run string
			Finish
		}
		if json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&req) != nil {
			platform.HTTPError(w, http.StatusBadRequest, "bad request")
			return
		}
		res, err := g.Finish(p, req.Run, req.Finish)
		platform.Result(w, res, err)
	}))
	// The sink behind the gatedef guard and limit routes (manifests/gatedef.yaml): it only
	// counts. The guard route's "LLM" is this handler, so a guard miss never reaches one.
	sink := func(w http.ResponseWriter, r *http.Request) {
		kind := r.PathValue("kind")
		if kind != "guard" && kind != "limit" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 4096))
		if r.Header.Get("cf-connecting-ip") == "" { // only what came through the in-cluster routes
			g.Sink(kind)
		}
		platform.WriteJSON(w, map[string]any{"object": "chat.completion", "model": "sink",
			"choices": []map[string]any{{"index": 0, "finish_reason": "stop", "message": map[string]string{"role": "assistant", "content": "sink: there is no LLM here"}}}})
	}
	mux.HandleFunc("POST /gateway-defense/sink/{kind}", sink)
	mux.HandleFunc("GET /gateway-defense/sink/{kind}", sink)
}
