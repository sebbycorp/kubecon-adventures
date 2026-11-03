package main

import (
	"math"
	"strings"
	"testing"
	"time"
)

const sampleMetrics = `# HELP agentgateway_gen_ai_client_token_usage tokens
agentgateway_gen_ai_client_token_usage_sum{gen_ai_token_type="input",route="agentgateway-system/openai-heist"} 1000000.0
agentgateway_gen_ai_client_token_usage_sum{gen_ai_token_type="input_cache_read",route="agentgateway-system/openai-heist"} 600000.0
agentgateway_gen_ai_client_token_usage_sum{gen_ai_token_type="output",route="agentgateway-system/openai-heist"} 50000.0
agentgateway_gen_ai_client_token_usage_sum{gen_ai_token_type="input",route="agentgateway-system/openai"} 2000.0
agentgateway_gen_ai_client_token_usage_count{gen_ai_token_type="input",route="agentgateway-system/openai"} 9
agentgateway_requests_total{route="arcade/arcade",status="200"} 5
`

func TestParseTokensAndView(t *testing.T) {
	r := parseTokens(strings.NewReader(sampleMetrics))
	if got := r["openai-heist"]; got != (tokenCount{In: 1e6, Cached: 6e5, Out: 5e4}) {
		t.Fatalf("heist = %+v", got)
	}
	if got := r["openai"]; got != (tokenCount{In: 2000}) {
		t.Fatalf("openai = %+v (count lines must not be added)", got)
	}
	tk := &Tokens{prices: prices{In: 0.40, Cached: 0.10, Out: 1.60}}
	now := time.Now()
	tk.add(tokenSample{at: now.Add(-time.Minute), routes: map[string]tokenCount{"openai-heist": {In: 9e5, Cached: 6e5, Out: 4e4}}})
	tk.add(tokenSample{at: now, routes: r})
	v := tk.View()
	// heist: 400k uncached at 0.40 + 600k cached at 0.10 + 50k out at 1.60 = 0.16+0.06+0.08 = 0.30
	if math.Abs(v.Routes[0].USD-0.30) > 1e-9 || v.Routes[0].Game != "Substrate Siege" {
		t.Fatalf("heist route = %+v", v.Routes[0])
	}
	// heist went from 940k to 1.05M tokens in a minute; openai is new (counted from zero).
	if math.Abs(v.Routes[0].PerMin-110000) > 1 || math.Abs(v.PerMin-112000) > 1 {
		t.Fatalf("per minute: heist %v total %v", v.Routes[0].PerMin, v.PerMin)
	}
	if math.Abs(v.Tokens-1052000) > 1e-6 {
		t.Fatalf("total tokens %v", v.Tokens)
	}
}

func TestTokensRestartedGateway(t *testing.T) {
	tk := &Tokens{prices: pricesFromEnv()}
	now := time.Now()
	tk.add(tokenSample{at: now.Add(-time.Minute), routes: map[string]tokenCount{"openai-flow": {In: 50000}}})
	tk.add(tokenSample{at: now, routes: map[string]tokenCount{"openai-flow": {In: 3000}}})
	if v := tk.View(); v.PerMin != 3000 || v.USDMin != 0 {
		t.Fatalf("after a restart: perMin %v usd/min %v", v.PerMin, v.USDMin)
	}
}
