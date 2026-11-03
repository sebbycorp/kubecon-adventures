package main

import (
	"testing"

	"github.com/sebbycorp/kubecon-adventures/arcade/internal/platform"
)

func TestOverallBoard(t *testing.T) {
	games := []GameBoard{
		{Game: "Substrate Siege", Path: "/substrate-siege", Rows: []platform.BoardRow{{Nick: "Ada", Color: "blue", Score: 40}, {Nick: "Linus", Color: "red", Score: 10}}},
		{Game: "Duck Hunt", Path: "/duck-hunt", Rows: []platform.BoardRow{{Nick: "ada", Color: "blue", Score: 3000}, {Nick: "Grace", Color: "gold", Score: 12000}}},
		{Game: "Flappy Substrate", Path: "/flappy", Rows: []platform.BoardRow{{Nick: "Linus", Color: "red", Score: 20}, {Nick: "Linus", Color: "red", Score: 5}}},
		{Game: "AI agentgateway missions", Path: missionsURL, External: true, Rows: []platform.BoardRow{{Nick: "Ada", Color: "blue", Score: 99999}}},
	}
	got := overallBoard(10, games)
	// Grace: 12000/6000 = 2000. Ada: 1000 (Siege) + 500 (Duck Hunt, same player despite case),
	// missions ignored. Linus: 250 (Siege) + 500 (Flappy, only his best row counts).
	want := []struct {
		nick  string
		score int
		games string
	}{{"Grace", 2000, "1 game · best: Duck Hunt"}, {"Ada", 1500, "2 games · best: Substrate Siege"}, {"Linus", 750, "2 games · best: Flappy Substrate"}}
	if len(got) != len(want) {
		t.Fatalf("rows = %+v", got)
	}
	for i, w := range want {
		if got[i].Nick != w.nick || got[i].Score != w.score || got[i].Detail != w.games {
			t.Fatalf("row %d = %+v, want %+v", i, got[i], w)
		}
	}
	if top := overallBoard(1, games); len(top) != 1 || top[0].Nick != "Grace" {
		t.Fatalf("top 1 = %+v", top)
	}
	if arcadePoints("/nope", 5) != 0 || arcadePoints("/flappy", -3) != 0 {
		t.Fatal("unknown game or negative score should be 0")
	}
}
