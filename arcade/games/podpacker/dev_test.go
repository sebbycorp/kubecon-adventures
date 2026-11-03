//go:build podpackerdev

package podpacker

// A local dev server for Pod Packer's pages, with kagent faked (resumes and
// suspends take about as long as on the cluster) and nothing else of the arcade running,
// so it never touches the cluster. Run it with:
//
//	PODPACKER_DEV_ADDR=127.0.0.1:8197 go test -tags podpackerdev -run TestDevServer -timeout 0 ./games/podpacker/

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/sebbycorp/kubecon-adventures/arcade/internal/platform"
)

type slowOps struct{ *fakeOps }

func (s slowOps) Resume(ctx context.Context, id string) error {
	time.Sleep(time.Duration(220+rand.IntN(260)) * time.Millisecond)
	return s.fakeOps.Resume(ctx, id)
}
func (s slowOps) Suspend(ctx context.Context, id string) error {
	time.Sleep(time.Duration(60+rand.IntN(140)) * time.Millisecond)
	return s.fakeOps.Suspend(ctx, id)
}

func TestDevServer(t *testing.T) {
	addr := os.Getenv("PODPACKER_DEV_ADDR")
	if addr == "" {
		t.Skip("PODPACKER_DEV_ADDR not set")
	}
	p := platform.New(nil)
	g := New(p, slowOps{newFake(0)})
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
	mux.HandleFunc("GET /api/boards", func(w http.ResponseWriter, r *http.Request) {
		platform.WriteJSON(w, []map[string]any{{"game": "Pod Packer", "path": "/pod-packer", "unit": "points", "rows": g.Top(5)}})
	})
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../web/shared"))))
	mux.Handle("GET /agent-flow/static/", http.StripPrefix("/agent-flow/static/", http.FileServer(http.Dir("../../web/agent-flow"))))
	g.Routes(mux, os.DirFS("../../web/pod-packer"))
	t.Logf("Pod Packer dev server on http://%s/pod-packer", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		t.Fatal(err)
	}
}
