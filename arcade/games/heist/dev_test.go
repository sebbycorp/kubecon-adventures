//go:build siegedev

package heist

// A local dev server for Substrate Siege's pages, with kagent faked (spawns, turns,
// suspends and resumes take about as long as on the cluster) and nothing else of the
// arcade running, so it never touches the cluster. POST /dev/teleport?p=<id>&x=&y=
// moves a player (screenshots of each district). Run it with:
//
//	SIEGE_DEV_ADDR=127.0.0.1:8096 go test -tags siegedev -run TestDevServer -timeout 0 ./games/heist/

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/sebbycorp/kubecon-adventures/arcade/internal/platform"
)

// devOps is liveOps with the cluster's timings.
type devOps struct{ *liveOps }

func nap(lo, hi int) { time.Sleep(time.Duration(lo+rand.IntN(hi-lo)) * time.Millisecond) }

func (o devOps) Spawn(ctx context.Context, name string) (string, string, error) {
	nap(200, 500)
	return o.liveOps.Spawn(ctx, name)
}
func (o devOps) Step(ctx context.Context, id string, step, steps int) (StepResult, error) {
	nap(2000, 4000)
	lines := []string{"Shh... working on it.", "Almost got it, keep watch!", "Easy money!", "Nothing to see here."}
	return StepResult{Say: lines[rand.IntN(len(lines))]}, nil
}
func (o devOps) Freeze(ctx context.Context, id string) (int64, error) {
	ms := 60 + rand.IntN(140)
	time.Sleep(time.Duration(ms) * time.Millisecond)
	return int64(ms), nil
}
func (o devOps) Wake(ctx context.Context, id string) (int64, error) {
	ms := 200 + rand.IntN(250)
	time.Sleep(time.Duration(ms) * time.Millisecond)
	return int64(ms), nil
}
func (o devOps) SpawnPlayer(ctx context.Context, name string) (string, string, string, error) {
	nap(700, 1200)
	return o.liveOps.SpawnPlayer(ctx, name)
}

func TestDevServer(t *testing.T) {
	addr := os.Getenv("SIEGE_DEV_ADDR")
	if addr == "" {
		t.Skip("SIEGE_DEV_ADDR not set")
	}
	p := platform.New(nil)
	g := New(p, devOps{&liveOps{}}, []string{"kagent-worker", "kagent-worker2"})
	g.Run(context.Background())
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/join", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Nick string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		nick, ok := platform.CleanNick(req.Nick)
		if !ok {
			platform.HTTPError(w, http.StatusBadRequest, "bad nick")
			return
		}
		pl := p.Players.Join(nick)
		http.SetCookie(w, &http.Cookie{Name: platform.PlayerCookie, Value: pl.ID, Path: "/"})
		platform.WriteJSON(w, pl)
	})
	mux.HandleFunc("GET /api/me", p.Players.WithPlayer(func(w http.ResponseWriter, r *http.Request, pl platform.Player) { platform.WriteJSON(w, pl) }))
	mux.HandleFunc("POST /dev/teleport", func(w http.ResponseWriter, r *http.Request) {
		x, _ := strconv.ParseFloat(r.URL.Query().Get("x"), 64)
		y, _ := strconv.ParseFloat(r.URL.Query().Get("y"), 64)
		g.mu.Lock()
		if a := g.w.Avatars[r.URL.Query().Get("p")]; a != nil {
			a.Pos = Vec{x, y}
		}
		g.mu.Unlock()
	})
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../web/shared"))))
	mux.Handle("GET /agent-flow/static/", http.StripPrefix("/agent-flow/static/", http.FileServer(http.Dir("../../web/agent-flow"))))
	g.Routes(mux, os.DirFS("../../web/substrate-siege"))
	t.Logf("Substrate Siege dev server on http://%s/substrate-siege", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		t.Fatal(err)
	}
}
