//go:build cluster

package heist

import (
	"context"
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
