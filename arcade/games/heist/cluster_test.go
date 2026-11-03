//go:build cluster

package heist

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/sebbycorp/kubecon-adventures/arcade/internal/kagent"
)

// TestCheckpointSuspended answers: can a SUSPENDED session be checkpointed?
func TestCheckpointSuspended(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	k, err := kagent.NewKagent(os.Getenv("KAGENT_URL"), "heist-test", "kagent", "heister")
	if err != nil {
		t.Fatal(err)
	}
	sid, err := k.CreateSession(ctx, "heist-checkpoint-test")
	if err != nil {
		t.Fatal(err)
	}
	defer k.Delete(context.Background(), sid)

	start := time.Now()
	reply, err := k.Prompt(ctx, sid, "Run this bash command in /data/workspace: echo loot > /data/workspace/loot.txt")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("prompt 1: %v", time.Since(start))

	start = time.Now()
	// Right after a turn kagent answers FailedPrecondition ("runtime cleanup or lifecycle
	// work is not yet settled") until the turn settles, so retry that.
	for tries := 1; ; tries++ {
		err = k.Suspend(ctx, sid)
		if err == nil {
			t.Logf("suspend: %v (%d tries)", time.Since(start), tries)
			break
		}
		if status.Code(err) != codes.FailedPrecondition || time.Since(start) > 60*time.Second {
			t.Fatal(err)
		}
		time.Sleep(500 * time.Millisecond)
	}

	start = time.Now()
	cp, err := k.Checkpoint(ctx, sid, string(reply.Task.ID))
	if err != nil {
		t.Logf("CHECKPOINT WHILE SUSPENDED: FAILED after %v: %v", time.Since(start), err)
	} else {
		t.Logf("CHECKPOINT WHILE SUSPENDED: WORKED in %v, checkpoint id %s", time.Since(start), cp)
	}

	start = time.Now()
	if err := k.Resume(ctx, sid); err != nil {
		t.Fatal(err)
	}
	t.Logf("resume: %v", time.Since(start))

	// Same "not yet settled" window right after resume: retry.
	for start = time.Now(); ; time.Sleep(500 * time.Millisecond) {
		reply, err = k.Prompt(ctx, sid, "Run this bash command in /data/workspace: cat /data/workspace/loot.txt")
		if err == nil || !strings.Contains(err.Error(), "not yet settled") || time.Since(start) > 60*time.Second {
			break
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	out.WriteString(reply.Text + reply.JSON)
	for _, c := range reply.Tools {
		out.WriteString(c.Output)
	}
	if !strings.Contains(out.String(), "loot") {
		t.Fatalf("workspace file lost after resume: %q", out.String())
	}
	// Delete hits the same window right after a turn.
	for start = time.Now(); ; time.Sleep(500 * time.Millisecond) {
		err = k.Delete(ctx, sid)
		if err == nil || !strings.Contains(err.Error(), "not yet settled") || time.Since(start) > 60*time.Second {
			break
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("delete ok after %v", time.Since(start))
}

// TestKagentOpsLifecycle runs the real adapter through a target's and an ally's life:
// Spawn, 2 Steps, Freeze, Spring, Step, Extract; SpawnAlly, Freeze, Spring, Delete.
func TestKagentOpsLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	url := os.Getenv("KAGENT_URL")
	heister, err := kagent.NewKagent(url, "heist-test", "kagent", "heister")
	if err != nil {
		t.Fatal(err)
	}
	sidekick, err := kagent.NewKagent(url, "heist-test", "kagent", "sidekick")
	if err != nil {
		t.Fatal(err)
	}
	o := NewKagentOps(heister, sidekick, "resume-first")
	var made []string
	defer func() { // every session this test made is gone afterwards, pass or fail
		for _, sid := range made {
			_ = o.Delete(context.Background(), sid)
		}
	}()
	op := func(name string) func() {
		start := time.Now()
		return func() { t.Logf("TIMING %-14s %v", name, time.Since(start).Round(time.Millisecond)) }
	}
	// Each op gets the game's per-op timeout.
	opCtx := func() (context.Context, context.CancelFunc) { return context.WithTimeout(ctx, opTimeout) }

	c, cc := opCtx()
	done := op("spawn")
	sid, node, err := o.Spawn(c, "lifecycle-test")
	cc()
	done()
	if err != nil {
		t.Fatal(err)
	}
	made = append(made, sid)
	t.Logf("target %s on node %q, job %q", sid, node, jobFor(sid))

	step := func(n int) {
		c, cc := opCtx()
		defer cc()
		done := op(fmt.Sprintf("step %d", n))
		r, err := o.Step(c, sid, n, 4)
		done()
		if err != nil {
			t.Fatalf("step %d: %v", n, err)
		}
		t.Logf("step %d: %+v", n, r)
	}
	step(1)
	step(2)

	c, cc = opCtx()
	done = op("freeze")
	ms, err := o.Freeze(c, sid)
	cc()
	done()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("freeze reported %d ms", ms)

	c, cc = opCtx()
	done = op("spring")
	node, say, ms, err := o.Spring(c, sid)
	cc()
	done()
	if err != nil {
		t.Fatal(err)
	}
	if say == "" || say == sprungFallback {
		t.Fatalf("spring: no line from the agent (%q)", say)
	}
	t.Logf("spring: node %q, resume %d ms, say %q", node, ms, say)

	step(3)

	c, cc = opCtx()
	done = op("extract")
	ms, err = o.Extract(c, sid)
	cc()
	done()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("extract reported %d ms", ms)
	ids, err := heister.ListSessionIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if id == sid {
			t.Fatalf("session %s still listed after Extract", sid)
		}
	}

	// The ally: sidekick greets, freezes, is sprung, and is deleted.
	c, cc = opCtx()
	done = op("ally")
	aid, node, say, err := o.SpawnAlly(c, "lifecycle-ally")
	cc()
	done()
	if aid != "" {
		made = append(made, aid)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("ally %s on node %q says %q", aid, node, say)
	if say == "" {
		t.Fatal("ally: empty greeting")
	}
	c, cc = opCtx()
	done = op("ally freeze")
	_, err = o.Freeze(c, aid)
	cc()
	done()
	if err != nil {
		t.Fatal(err)
	}
	c, cc = opCtx()
	done = op("ally spring")
	_, say, _, err = o.Spring(c, aid)
	cc()
	done()
	if err != nil || say == "" || say == allyFallback {
		t.Fatalf("ally spring: no line from the agent (%q) %v", say, err)
	}
	t.Logf("ally sprung: %q", say)
	c, cc = opCtx()
	done = op("ally delete")
	err = o.Delete(c, aid)
	cc()
	done()
	if err != nil {
		t.Fatal(err)
	}
	ids, err = sidekick.ListSessionIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if id == aid {
			t.Fatalf("ally session %s still listed after Delete", aid)
		}
	}
}
