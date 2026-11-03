package duckhunt

import (
	"testing"

	"github.com/sebbycorp/kubecon-adventures/arcade/internal/platform"
)

func TestTop(t *testing.T) {
	g := &Game{scores: map[string]*score{}}
	ada := &Duck{PlayerID: "a", Nick: "Ada", Color: "blue"}
	linus := &Duck{PlayerID: "l", Nick: "Linus", Color: "red"}
	grace := &Duck{PlayerID: "g", Nick: "Grace", Color: "gold"}
	g.scoreLocked(ada).hatches++   // 2
	g.scoreLocked(ada).quacks++    // 3
	g.scoreLocked(linus).revives++ // 3, but more revives: ahead of Ada
	g.scoreLocked(grace)           // 0 points: not on the board
	got := g.Top(5)
	want := []platform.BoardRow{
		{Nick: "Linus", Color: "red", Score: 3, Detail: "0 ducks · 1 revives · 0 quacks"},
		{Nick: "Ada", Color: "blue", Score: 3, Detail: "1 ducks · 0 revives · 1 quacks"},
	}
	if len(got) != len(want) {
		t.Fatalf("top = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if top1 := g.Top(1); len(top1) != 1 || top1[0].Nick != "Linus" {
		t.Fatalf("top 1 = %+v", top1)
	}
}
