// Command agentclean deletes every kagent session of the named Agents, which deletes
// their Substrate actors (and snapshots), after deleting each session's checkpoints. It
// finds sessions two ways: from Substrate's actors whose template belongs to an Agent
// (this catches sessions of Agents that no longer exist), and from kagent's session
// list per Agent and user. Without -yes it only prints what it would delete.
//
//	go run ./cmd/agentclean -kagent-url http://localhost:18083 [-yes] escapee forky
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/sebbycorp/kubecon-adventures/arcade/internal/kagent"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func main() {
	url := flag.String("kagent-url", "http://localhost:18083", "kagent controller URL")
	ns := flag.String("namespace", "kagent", "namespace of the Agents")
	users := flag.String("users", "arcade,admin@kagent.dev", "kagent user IDs whose sessions to look through (comma-separated)")
	yes := flag.Bool("yes", false, "delete; without it, only print what would be deleted")
	flag.Parse()
	agents := flag.Args()
	if len(agents) == 0 {
		fmt.Fprintln(os.Stderr, "usage: agentclean [-yes] <agent>...")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// One client per user and Agent: kagent scopes sessions by the caller's user ID.
	client := func(user, agent string) *kagent.Kagent {
		k, err := kagent.NewKagent(*url, user, *ns, agent)
		if err != nil {
			fmt.Fprintln(os.Stderr, "kagent:", err)
			os.Exit(1)
		}
		return k
	}
	us := strings.Split(*users, ",")

	sessions := map[string]string{} // session ID -> Agent
	actors, err := client(us[0], "").AllActors(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "list Substrate actors:", err)
		os.Exit(1)
	}
	for key, a := range actors {
		if !slices.Contains(agents, a.Agent) {
			continue
		}
		_, name, _ := strings.Cut(key, "/")
		if id, ok := strings.CutPrefix(name, "session-"); ok {
			sessions[id] = a.Agent
		}
	}
	for _, ag := range agents {
		for _, u := range us {
			ids, err := client(u, ag).ListSessionIDs(ctx)
			if err != nil {
				continue // an Agent that no longer exists, or a user with no sessions
			}
			for _, id := range ids {
				sessions[id] = ag
			}
		}
	}

	ids := make([]string, 0, len(sessions))
	for id := range sessions {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b string) int { return strings.Compare(sessions[a]+a, sessions[b]+b) })
	verb := "would delete"
	if *yes {
		verb = "deleting"
	}
	var cps, failed int
	for _, id := range ids {
		var ckpts []string
		for _, u := range us {
			if got, err := client(u, sessions[id]).ListCheckpointIDs(ctx, id); err == nil && len(got) > 0 {
				ckpts = got
				break
			}
		}
		fmt.Printf("%s session %s (agent %s, %d checkpoints)\n", verb, id, sessions[id], len(ckpts))
		cps += len(ckpts)
		if !*yes {
			continue
		}
		for _, cp := range ckpts {
			if err := firstOK(us, func(u string) error { return client(u, sessions[id]).DeleteCheckpoint(ctx, cp) }); err != nil {
				fmt.Printf("  checkpoint %s: %v\n", cp, status.Code(err))
				failed++
			}
		}
		if err := firstOK(us, func(u string) error { return client(u, sessions[id]).Delete(ctx, id) }); err != nil {
			fmt.Printf("  session %s: %v\n", id, status.Code(err))
			failed++
		}
	}
	fmt.Printf("%d sessions, %d checkpoints for agents %s\n", len(ids), cps, strings.Join(agents, ", "))
	if *yes {
		left := 0
		if after, err := client(us[0], "").AllActors(ctx); err == nil {
			for _, a := range after {
				if slices.Contains(agents, a.Agent) {
					left++
				}
			}
		}
		fmt.Printf("%d failed; %d actors of these agents left in Substrate\n", failed, left)
		if failed > 0 {
			os.Exit(1)
		}
	}
}

// firstOK runs call as each user until one succeeds; NotFound counts as done.
func firstOK(users []string, call func(user string) error) error {
	var err error
	for _, u := range users {
		if err = call(u); err == nil || status.Code(err) == codes.NotFound {
			return nil
		}
	}
	return err
}
