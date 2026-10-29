package duckhunt

import (
	"encoding/json"
	"io/fs"
	"net/http"

	"github.com/sebbycorp/kubecon-adventures/arcade/internal/platform"
)

// Routes mounts Duck Hunt: pages and API under /duck-hunt, admin under /admin/duck-hunt.
// web holds phone.html, screen.html, admin.html and sky.js.
func (g *Game) Routes(mux *http.ServeMux, web fs.FS) {
	page := func(name string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { http.ServeFileFS(w, r, web, name) }
	}
	players := g.p.Players

	mux.HandleFunc("GET /duck-hunt", page("phone.html"))
	mux.HandleFunc("GET /duck-hunt/screen", page("screen.html"))
	mux.Handle("GET /duck-hunt/static/", http.StripPrefix("/duck-hunt/static/", http.FileServerFS(web)))
	mux.HandleFunc("GET /duck-hunt/api/events", g.hub.ServeSSE)

	mux.HandleFunc("POST /duck-hunt/api/hatch/{kind}", players.WithPlayer(func(w http.ResponseWriter, r *http.Request, p platform.Player) {
		id, err := g.Hatch(p, r.PathValue("kind"))
		platform.Result(w, map[string]string{"duckId": id}, err)
	}))
	mux.HandleFunc("POST /duck-hunt/api/ducks/{id}/quack", players.WithPlayer(func(w http.ResponseWriter, r *http.Request, p platform.Player) {
		platform.Result(w, nil, g.Quack(p.ID, r.PathValue("id")))
	}))
	mux.HandleFunc("POST /duck-hunt/api/ducks/{id}/revive", players.WithPlayer(func(w http.ResponseWriter, r *http.Request, p platform.Player) {
		platform.Result(w, nil, g.Revive(p.ID, r.PathValue("id")))
	}))

	// Admin: everything under /admin, so sign-in can be added in front of that prefix later.
	mux.HandleFunc("GET /admin/duck-hunt", page("admin.html"))
	mux.HandleFunc("POST /admin/duck-hunt/api/ducks/{id}/shoot", func(w http.ResponseWriter, r *http.Request) {
		platform.Result(w, nil, g.Shoot(r.PathValue("id")))
	})
	mux.HandleFunc("POST /admin/duck-hunt/api/ducks/{id}/revive", func(w http.ResponseWriter, r *http.Request) {
		platform.Result(w, nil, g.Revive("", r.PathValue("id")))
	})
	mux.HandleFunc("POST /admin/duck-hunt/api/ducks/{id}/delete", func(w http.ResponseWriter, r *http.Request) {
		platform.Result(w, nil, g.Delete(r.PathValue("id")))
	})
	mux.HandleFunc("POST /admin/duck-hunt/api/settings", func(w http.ResponseWriter, r *http.Request) {
		var s Settings
		if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
			platform.HTTPError(w, http.StatusBadRequest, "bad request")
			return
		}
		g.SetSettings(s)
		platform.WriteJSON(w, g.Snapshot().Settings)
	})
	mux.HandleFunc("POST /admin/duck-hunt/api/reset", func(w http.ResponseWriter, r *http.Request) {
		n, err := g.Reset(r.Context())
		platform.Result(w, map[string]int{"deletedSessions": n}, err)
	})
	mux.HandleFunc("GET /admin/duck-hunt/api/state", func(w http.ResponseWriter, r *http.Request) { platform.WriteJSON(w, g.Snapshot()) })
}

// Stats are Duck Hunt's numbers for dashboards.
func (g *Game) Stats() map[string]any {
	s := g.Snapshot()
	flying, down := 0, 0
	for _, d := range s.Ducks {
		switch d.State {
		case Flying:
			flying++
		case Shot, Reviving:
			down++
		}
	}
	return map[string]any{
		"players": s.Players, "ducks": len(s.Ducks), "ducksFlying": flying, "ducksDown": down,
		"duckSandboxesAwake": s.Awake.Now, "duckWakeUps": s.Awake.WakeUps, "hatchP50Ms": s.Timings["hatch"].P50,
		"llmCalls": s.LLMCalls, "tokens": s.Tokens.Input + s.Tokens.Output,
	}
}
