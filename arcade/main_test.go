package main

import (
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
	b := arcadeBoards(5, top("duck"), top("flow"), top("siege"), top("defense"), top("gateway"))
	want := []struct{ game, path, nick string }{
		{"Substrate Siege", "/substrate-siege", "siege"}, {"Cluster Defense", "/cluster-defense", "defense"},
		{"Gateway Defense", "/gateway-defense", "gateway"}, {"Agent Flow", "/agent-flow", "flow"}, {"Duck Hunt", "/duck-hunt", "duck"},
	}
	if len(b) != len(want) {
		t.Fatalf("boards = %+v", b)
	}
	for i, w := range want {
		if b[i].Game != w.game || b[i].Path != w.path || b[i].Unit == "" || len(b[i].Rows) != 1 || b[i].Rows[0].Nick != w.nick {
			t.Fatalf("board %d = %+v, want %+v", i, b[i], w)
		}
	}
	for _, n := range asks {
		if n != 5 {
			t.Fatalf("asked for top %d, want 5", n)
		}
	}
}
