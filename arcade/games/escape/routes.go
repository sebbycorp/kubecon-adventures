package escape

import (
	"encoding/json"
	"io/fs"
	"net/http"

	"github.com/sebbycorp/kubecon-adventures/arcade/internal/platform"
)

// Routes mounts Sandbox Escape under /sandbox-escape and its admin under /admin/sandbox-escape.
func (g *Game) Routes(mux *http.ServeMux, web fs.FS) {
	page := func(name string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { http.ServeFileFS(w, r, web, name) }
	}
	mux.HandleFunc("GET /sandbox-escape", page("phone.html"))
	mux.HandleFunc("GET /sandbox-escape/screen", page("screen.html"))
	mux.Handle("GET /sandbox-escape/static/", http.StripPrefix("/sandbox-escape/static/", http.FileServerFS(web)))
	mux.HandleFunc("GET /sandbox-escape/api/events", g.hub.ServeSSE)
	mux.HandleFunc("POST /sandbox-escape/api/try", g.p.Players.WithPlayer(func(w http.ResponseWriter, r *http.Request, p platform.Player) {
		var req struct{ Plan, Idea string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			platform.HTTPError(w, http.StatusBadRequest, "bad request")
			return
		}
		platform.Result(w, nil, g.Try(p, req.Plan, req.Idea))
	}))

	mux.HandleFunc("POST /admin/sandbox-escape/api/reset", func(w http.ResponseWriter, r *http.Request) {
		n, err := g.Reset(r.Context())
		platform.Result(w, map[string]int{"deletedSessions": n}, err)
	})
	mux.HandleFunc("GET /admin/sandbox-escape/api/state", func(w http.ResponseWriter, r *http.Request) { platform.WriteJSON(w, g.Snapshot()) })
}
