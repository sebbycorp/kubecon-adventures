package fork

import (
	"encoding/json"
	"io/fs"
	"net/http"

	"github.com/sebbycorp/kubecon-adventures/arcade/internal/platform"
)

// Routes mounts Fork Frenzy under /fork-frenzy and its admin under /admin/fork-frenzy.
func (g *Game) Routes(mux *http.ServeMux, web fs.FS) {
	page := func(name string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { http.ServeFileFS(w, r, web, name) }
	}
	players := g.p.Players
	mux.HandleFunc("GET /fork-frenzy", page("phone.html"))
	mux.HandleFunc("GET /fork-frenzy/screen", page("screen.html"))
	mux.Handle("GET /fork-frenzy/static/", http.StripPrefix("/fork-frenzy/static/", http.FileServerFS(web)))
	mux.HandleFunc("GET /fork-frenzy/api/events", g.hub.ServeSSE)

	mux.HandleFunc("POST /fork-frenzy/api/teach", players.WithPlayer(func(w http.ResponseWriter, r *http.Request, p platform.Player) {
		var req struct{ Creature, Secret string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			platform.HTTPError(w, http.StatusBadRequest, "bad request")
			return
		}
		platform.Result(w, nil, g.Teach(p, req.Creature, req.Secret))
	}))
	for path, act := range map[string]func(platform.Player) error{
		"checkpoint": g.Checkpoint, "fork": g.Fork, "ask": g.AskAll, "restart": g.Restart,
	} {
		mux.HandleFunc("POST /fork-frenzy/api/"+path, players.WithPlayer(func(w http.ResponseWriter, r *http.Request, p platform.Player) {
			platform.Result(w, nil, act(p))
		}))
	}

	mux.HandleFunc("POST /admin/fork-frenzy/api/reset", func(w http.ResponseWriter, r *http.Request) {
		n, err := g.Reset(r.Context())
		platform.Result(w, map[string]int{"deletedSessions": n}, err)
	})
	mux.HandleFunc("GET /admin/fork-frenzy/api/state", func(w http.ResponseWriter, r *http.Request) { platform.WriteJSON(w, g.Snapshot()) })
}
