package main

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// AteletScraper reads Substrate's own checkpoint (snapshot) and restore timings from
// every atelet's Prometheus endpoint, for actors of the duck Agent's templates.
type AteletScraper struct {
	// Target is "dns:<host>:<port>" (every A record of a headless Service) or a
	// comma-separated list of base URLs such as "http://localhost:19091".
	Target         string
	TemplatePrefix string
	last           map[string][2]float64 // "<endpoint> <metric>" -> {sum, count}
}

const (
	checkpointMetric = "ate_actor_checkpoint_duration_seconds"
	restoreMetric    = "ate_actor_restore_duration_seconds"
)

func (s *AteletScraper) endpoints(ctx context.Context) ([]string, error) {
	if hostport, ok := strings.CutPrefix(s.Target, "dns:"); ok {
		host, port, err := net.SplitHostPort(hostport)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupHost(ctx, host)
		if err != nil {
			return nil, err
		}
		var out []string
		for _, ip := range ips {
			out = append(out, "http://"+net.JoinHostPort(ip, port))
		}
		return out, nil
	}
	return strings.Split(s.Target, ","), nil
}

// Scrape returns the new observations since the last call as (average ms, count)
// for checkpoints and restores.
func (s *AteletScraper) Scrape(ctx context.Context) (snapMs float64, snapN int, restMs float64, restN int, err error) {
	if s.last == nil {
		s.last = map[string][2]float64{}
	}
	eps, err := s.endpoints(ctx)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	var dSnap, dRest [2]float64
	for _, ep := range eps {
		totals, err := s.scrapeOne(ctx, ep)
		if err != nil {
			slog.Debug("atelet scrape", "endpoint", ep, "err", err)
			continue
		}
		for metric, v := range totals {
			key := ep + " " + metric
			prev, seen := s.last[key]
			s.last[key] = v
			if !seen || v[1] < prev[1] { // first sight or atelet restarted
				continue
			}
			d := [2]float64{v[0] - prev[0], v[1] - prev[1]}
			if metric == checkpointMetric {
				dSnap[0], dSnap[1] = dSnap[0]+d[0], dSnap[1]+d[1]
			} else {
				dRest[0], dRest[1] = dRest[0]+d[0], dRest[1]+d[1]
			}
		}
	}
	if dSnap[1] > 0 {
		snapMs, snapN = 1000*dSnap[0]/dSnap[1], int(dSnap[1])
	}
	if dRest[1] > 0 {
		restMs, restN = 1000*dRest[0]/dRest[1], int(dRest[1])
	}
	return snapMs, snapN, restMs, restN, nil
}

// scrapeOne sums the phase="total" series of both histograms for our templates.
func (s *AteletScraper) scrapeOne(ctx context.Context, base string) (map[string][2]float64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/metrics", nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %s", resp.Status)
	}
	out := map[string][2]float64{checkpointMetric: {}, restoreMetric: {}}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		for metric := range out {
			var idx int
			switch {
			case strings.HasPrefix(line, metric+"_sum{"):
				idx = 0
			case strings.HasPrefix(line, metric+"_count{"):
				idx = 1
			default:
				continue
			}
			if !strings.Contains(line, `phase="total"`) || !strings.Contains(line, `ate_template_name="`+s.TemplatePrefix) {
				continue
			}
			f := strings.Fields(line)
			v, err := strconv.ParseFloat(f[len(f)-1], 64)
			if err != nil {
				continue
			}
			t := out[metric]
			t[idx] += v
			out[metric] = t
		}
	}
	return out, sc.Err()
}

// RunAteletScraper feeds Substrate's snapshot/restore timings into the game stats.
func (g *Game) RunAteletScraper(ctx context.Context, s *AteletScraper) {
	if s.Target == "" {
		return
	}
	for {
		c, cancel := context.WithTimeout(ctx, 5*time.Second)
		snapMs, snapN, restMs, restN, err := s.Scrape(c)
		cancel()
		if err != nil {
			slog.Warn("atelet scrape", "err", err)
		}
		// Each scrape yields an average over its new observations; record it once per
		// observation (capped) so percentiles stay roughly weighted.
		for i := 0; i < min(snapN, 50); i++ {
			g.record("snapshot", int64(snapMs+0.5))
		}
		for i := 0; i < min(restN, 50); i++ {
			g.record("restore", int64(restMs+0.5))
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}
