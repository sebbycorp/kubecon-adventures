package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sebbycorp/kubecon-adventures/arcade/internal/platform"
)

type fakeTop struct {
	nick string
	asks *[]int
}

func (f fakeTop) Top(n int) []platform.BoardRow {
	*f.asks = append(*f.asks, n)
	return []platform.BoardRow{{Nick: f.nick, Score: 1}}
}

func TestArcadeBoards(t *testing.T) {
	var asks []int
	top := func(nick string) topper { return fakeTop{nick, &asks} }
	b := arcadeBoards(5, top("duck"), top("flow"), top("siege"), top("defense"), top("gateway"), top("missions"))
	want := []struct{ game, path, nick string }{
		{"Substrate Siege", "/substrate-siege", "siege"}, {"Cluster Defense", "/cluster-defense", "defense"},
		{"Gateway Defense", "/gateway-defense", "gateway"}, {"Agent Flow", "/agent-flow", "flow"}, {"Agent Hunt", "/duck-hunt", "duck"},
		{"AI agentgateway missions", missionsURL, "missions"},
	}
	if len(b) != len(want) {
		t.Fatalf("boards = %+v", b)
	}
	for i, w := range want {
		if b[i].Game != w.game || b[i].Path != w.path || b[i].Unit == "" || len(b[i].Rows) != 1 || b[i].Rows[0].Nick != w.nick {
			t.Fatalf("board %d = %+v, want %+v", i, b[i], w)
		}
	}
	if !b[len(b)-1].External || b[0].External {
		t.Fatal("only the missions board is external")
	}
	for _, n := range asks {
		if n != 5 {
			t.Fatalf("asked for top %d, want 5", n)
		}
	}
}

func TestExtBoard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method %s: the external board must only be read", r.Method)
		}
		_, _ = w.Write([]byte(`{"top":[{"name":"Ada","earned":120},{"name":"Linus Torvalds Jr the Third","storyStars":9},{"name":"<script>","total":3}]}`))
	}))
	defer srv.Close()
	e := newExtBoard(srv.URL)
	rows, err := e.fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0] != (platform.BoardRow{Nick: "Ada", Score: 120}) || rows[1].Nick != "Linus Torvalds J" || rows[1].Score != 9 {
		t.Fatalf("rows = %+v", rows)
	}
	e.rows = rows
	if len(e.Top(1)) != 1 || len(e.Top(5)) != 2 {
		t.Fatal("Top not capped")
	}
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer down.Close()
	if rows, err := newExtBoard(down.URL).fetch(context.Background()); err == nil || rows != nil {
		t.Fatalf("down board: rows=%v err=%v", rows, err)
	}
}
