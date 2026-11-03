package main

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Token spend, read from agentgateway's own metrics: every game's LLM calls go through one
// route each on the arcade Gateway, and agentgateway counts their input, cached input and
// output tokens. The arcade polls them every 15 s for the TV, the home page and stats.

// tokenRoutes names each LLM route's game.
var tokenRoutes = map[string]string{
	"openai":         "Duck Hunt & Flappy Substrate",
	"openai-heist":   "Substrate Siege",
	"openai-defense": "Cluster Defense",
	"openai-gatedef": "Gateway Defense",
	"openai-flow":    "Agent Flow",
	"openai-selfie":  "Substrate Selfie",
}

// Prices are USD per million tokens (gpt-4.1-mini by default; TOKEN_PRICES="in,cached,out"
// overrides). The cost is an estimate from agentgateway's counts, not a bill.
type prices struct{ In, Cached, Out float64 }

func pricesFromEnv() prices {
	p := prices{In: 0.40, Cached: 0.10, Out: 1.60}
	if v := strings.Split(os.Getenv("TOKEN_PRICES"), ","); len(v) == 3 {
		f := func(s string, d float64) float64 {
			if x, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil && x >= 0 {
				return x
			}
			return d
		}
		p = prices{f(v[0], p.In), f(v[1], p.Cached), f(v[2], p.Out)}
	}
	return p
}

// tokenCount is one route's (or the total's) cumulative counts. In includes the cached
// input, as OpenAI's prompt tokens do.
type tokenCount struct{ In, Cached, Out float64 }

func (c tokenCount) total() float64 { return c.In + c.Out }
func (c tokenCount) usd(p prices) float64 {
	return (math.Max(0, c.In-c.Cached)*p.In + c.Cached*p.Cached + c.Out*p.Out) / 1e6
}

var tokenLineRE = regexp.MustCompile(`^agentgateway_gen_ai_client_token_usage_sum\{(.*)\} ([0-9.eE+-]+)$`)
var labelRE = regexp.MustCompile(`(\w+)="([^"]*)"`)

// parseTokens reads agentgateway's Prometheus text into counts per LLM route (the route's
// name without its namespace).
func parseTokens(r io.Reader) map[string]tokenCount {
	out := map[string]tokenCount{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		m := tokenLineRE.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		v, err := strconv.ParseFloat(m[2], 64)
		if err != nil {
			continue
		}
		var route, kind string
		for _, l := range labelRE.FindAllStringSubmatch(m[1], -1) {
			switch l[1] {
			case "route":
				route = l[2][strings.LastIndex(l[2], "/")+1:]
			case "gen_ai_token_type":
				kind = l[2]
			}
		}
		c := out[route]
		switch kind {
		case "input":
			c.In += v
		case "input_cache_read":
			c.Cached += v
		case "output":
			c.Out += v
		default:
			continue
		}
		out[route] = c
	}
	return out
}

// tokenSample is one poll's totals, kept for a minute to compute the rate.
type tokenSample struct {
	at     time.Time
	routes map[string]tokenCount
}

// Tokens polls agentgateway and keeps the latest counts and a minute of samples.
type Tokens struct {
	url    string
	prices prices
	mu     sync.Mutex
	recent []tokenSample
	err    string
}

func newTokens() *Tokens {
	url := os.Getenv("AGW_METRICS_URL")
	if url == "" {
		url = "http://arcade-gw-metrics.agentgateway-system.svc.cluster.local:15020/metrics"
	}
	return &Tokens{url: url, prices: pricesFromEnv()}
}

func (t *Tokens) run(ctx context.Context) {
	for {
		t.poll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(15 * time.Second):
		}
	}
}

func (t *Tokens) poll(ctx context.Context) {
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(c, http.MethodGet, t.url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.fail(err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.fail(resp.Status)
		return
	}
	t.add(tokenSample{at: time.Now(), routes: parseTokens(resp.Body)})
}

func (t *Tokens) fail(msg string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.err == "" {
		slog.Warn("token metrics", "url", t.url, "err", msg)
	}
	t.err = msg
}

func (t *Tokens) add(s tokenSample) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.err = ""
	t.recent = append(t.recent, s)
	for len(t.recent) > 2 && s.at.Sub(t.recent[0].at) > 75*time.Second {
		t.recent = t.recent[1:]
	}
}

// TokenRoute is one game's spend.
type TokenRoute struct {
	Route  string  `json:"route"`
	Game   string  `json:"game"`
	Tokens float64 `json:"tokens"`
	Cached float64 `json:"cached"`
	Output float64 `json:"output"`
	USD    float64 `json:"usd"`
	PerMin float64 `json:"perMin"`
}

// TokenView is /api/tokens: totals since agentgateway last started, and the last minute's rate.
type TokenView struct {
	Tokens float64      `json:"tokens"`
	USD    float64      `json:"usd"`
	PerMin float64      `json:"perMin"`
	USDMin float64      `json:"usdPerMin"`
	Routes []TokenRoute `json:"routes"`
	Model  string       `json:"model"`
	Prices prices       `json:"pricesPerMillion"`
	Error  string       `json:"error,omitempty"`
}

func (t *Tokens) View() TokenView {
	t.mu.Lock()
	defer t.mu.Unlock()
	v := TokenView{Model: "gpt-4.1-mini", Prices: t.prices, Error: t.err, Routes: []TokenRoute{}}
	if len(t.recent) == 0 {
		return v
	}
	last, first := t.recent[len(t.recent)-1], t.recent[0]
	mins := last.at.Sub(first.at).Minutes()
	for route, c := range last.routes {
		game := tokenRoutes[route]
		if game == "" {
			game = route
		}
		r := TokenRoute{Route: route, Game: game, Tokens: c.total(), Cached: c.Cached, Output: c.Out, USD: c.usd(t.prices)}
		if mins > 0.1 {
			// A counter that went down means agentgateway restarted; count from zero.
			d := c.total() - first.routes[route].total()
			if d < 0 {
				d = c.total()
			}
			r.PerMin = d / mins
			if prev := first.routes[route]; c.total() >= prev.total() {
				v.USDMin += (c.usd(t.prices) - prev.usd(t.prices)) / mins
			}
		}
		v.Tokens += r.Tokens
		v.USD += r.USD
		v.PerMin += r.PerMin
		v.Routes = append(v.Routes, r)
	}
	slices.SortFunc(v.Routes, func(a, b TokenRoute) int { return int(math.Copysign(1, b.Tokens-a.Tokens)) })
	return v
}
