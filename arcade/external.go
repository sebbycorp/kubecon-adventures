package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/sebbycorp/kubecon-adventures/arcade/internal/platform"
)

// Missions is "AI agentgateway missions", a game someone else runs that the arcade links
// to. Its public leaderboard is read with a plain GET, nothing is ever sent to it.
const (
	missionsURL   = "https://gd-frontend-413299367082.us-central1.run.app/"
	missionsBoard = missionsURL + "api/leaderboard"
)

// extBoard is an external game's public leaderboard, polled once a minute. Rows are empty
// when it has none or can't be reached; the home page then says the board is in the game.
type extBoard struct {
	url    string
	client *http.Client
	mu     sync.Mutex
	rows   []platform.BoardRow
}

func newExtBoard(url string) *extBoard {
	return &extBoard{url: url, client: &http.Client{Timeout: 5 * time.Second}}
}

// run polls the board every minute until ctx ends.
func (e *extBoard) run(ctx context.Context) {
	for {
		rows, err := e.fetch(ctx)
		if err != nil {
			slog.Debug("external leaderboard", "url", e.url, "err", err)
		}
		e.mu.Lock()
		e.rows = rows
		e.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Minute):
		}
	}
}

// fetch reads the board: {"top": [{"name", "earned" | "storyStars" | "total" | "bestScore"}]}.
func (e *extBoard) fetch(ctx context.Context) ([]platform.BoardRow, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &httpStatusError{resp.StatusCode}
	}
	var body struct {
		Top []struct {
			Name                                 string
			Earned, StoryStars, Total, BestScore *int
		}
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return nil, err
	}
	var rows []platform.BoardRow
	for _, t := range body.Top {
		score := 0
		for _, v := range []*int{t.Earned, t.StoryStars, t.Total, t.BestScore} {
			if v != nil {
				score = *v
				break
			}
		}
		name := []rune(t.Name)
		if nick, ok := platform.CleanNick(string(name[:min(len(name), 16)])); ok {
			rows = append(rows, platform.BoardRow{Nick: nick, Score: score})
		}
		if len(rows) == 10 {
			break
		}
	}
	return rows, nil
}

// Top is the n best of the last poll.
func (e *extBoard) Top(n int) []platform.BoardRow {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]platform.BoardRow{}, e.rows[:min(n, len(e.rows))]...)
}

type httpStatusError struct{ code int }

func (e *httpStatusError) Error() string { return http.StatusText(e.code) }
