package defense

import (
	"context"
	"testing"
	"time"

	"github.com/sebbycorp/kubecon-adventures/arcade/internal/platform"
)

// waitFor ticks g until cond holds or 5 s pass.
func waitFor(t *testing.T, g *Game, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		g.tick()
		g.mu.Lock()
		ok := cond()
		g.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestGameSweepsOrphansThenPlays(t *testing.T) {
	f := newFakeOps()
	for range 3 {
		_, _ = f.Spawn(context.Background(), "old")
	}
	g := New(nil, f)
	g.listing = true
	g.sweep(context.Background())
	if f.liveCount() != 0 || g.listing {
		t.Fatalf("orphans left: %d, listing=%v", f.liveCount(), g.listing)
	}
}

// TestGameRoundLifecycle runs a round against fake ops until the cluster fills, and
// checks every session the round made is deleted.
func TestGameRoundLifecycle(t *testing.T) {
	f := newFakeOps()
	g := New(nil, f)
	defer g.r.Close(context.Background())
	g.w.Slots = 3
	g.mu.Lock()
	if !g.online(platform.Player{ID: "p", Nick: "Pat", Color: "blue"}) {
		t.Fatal("join refused")
	}
	if err := g.play(); err != nil {
		t.Fatal(err)
	}
	g.mu.Unlock()
	// Speed the world up: a tick here is 1/15 s, so step the clock along.
	waitFor(t, g, "tasks answered", func() bool {
		g.w.Now += 0.5
		done := 0
		for _, a := range g.w.Agents {
			if a.Done {
				done++
			}
		}
		return done >= 2 || g.w.Round.Phase == Over
	})
	waitFor(t, g, "cluster full", func() bool { g.w.Now += 0.5; return g.w.Round.Phase == Over })
	if g.w.Round.Reason != "full" || g.w.Round.Peak != 3 {
		t.Fatalf("round = %+v", g.w.Round)
	}
	waitFor(t, g, "sessions deleted", func() bool { return f.liveCount() == 0 && g.r.InFlight("delete") == 0 })
	if f.spawned.Load() < 3 {
		t.Fatalf("spawned %d", f.spawned.Load())
	}
	if g.tokensPerMin() == 0 {
		t.Fatal("task tokens not counted")
	}
}

func TestGamePlayerCapAndPause(t *testing.T) {
	g := New(nil, newFakeOps())
	defer g.r.Close(context.Background())
	g.mu.Lock()
	defer g.mu.Unlock()
	for i := range maxPlayers {
		if !g.online(platform.Player{ID: string(rune('A' + i)), Nick: "x"}) {
			t.Fatalf("player %d refused", i)
		}
	}
	if g.online(platform.Player{ID: "late", Nick: "late"}) {
		t.Fatal("player past the cap accepted")
	}
	g.paused = true
	if err := g.play(); err == nil {
		t.Fatal("play while paused")
	}
	g.paused, g.listing = false, true
	if err := g.play(); err != errStartingUp {
		t.Fatalf("play while sweeping = %v", err)
	}
}

func TestPolicyTaskShowsGatewayLine(t *testing.T) {
	g := New(nil, newFakeOps())
	defer g.r.Close(context.Background())
	_ = g.w.Play()
	addAgent(g.w, "a1", Vec{10, 10})
	g.route(Result{Kind: "task", ID: "a1", Err: "policy: rate limited by agentgateway: 429"})
	if a := g.w.Agents["a1"]; !a.Done || a.Say == "" {
		t.Fatalf("agent = %+v", a)
	}
}
