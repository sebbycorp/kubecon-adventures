package selfie

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Scene is one of the ten pictures the phone composes around a selfie. The phone draws
// them (web/selfie/selfie.js uses the same IDs); the model writes one caption for each.
type Scene struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Brief    string `json:"-"` // what the scene is, for the model
	Fallback string `json:"fallback"`
}

// Scenes is the booth's ten scenes, in gallery order. KubeCon + CloudNativeCon North
// America 2026 is November 9-12 at the Salt Palace in Salt Lake City, Utah.
var Scenes = []Scene{
	{"keynote", "Keynote Jumbotron", "the person on the giant keynote screen at KubeCon + CloudNativeCon North America 2026 in Salt Lake City, the octopus photobombing", "Live on the KubeCon keynote screen. The octopus did not rehearse."},
	{"wanted", "Wanted poster", "an old-west WANTED poster: wanted for running 10,000 agents on 3 nodes", "WANTED for running 10,000 agents on 3 nodes. Reward: more nodes."},
	{"card", "Trading card", "a collectible trading card with funny stats like snapshot speed and pod charisma", "Rare holo card. Special move: snapshot in 140 ms."},
	{"magazine", "Magazine cover", "the cover of a tech magazine naming them cloud native person of the year", "Cloud Native Person of the Year. Agents: countless. Nodes: three."},
	{"billboard", "Salt Lake billboard", "a huge billboard on a Salt Lake City highway with the Wasatch mountains behind", "Now showing in Salt Lake City: one agent wrangler, zero idle pods."},
	{"lake", "Great Salt Lake", "a selfie with the octopus at the Great Salt Lake, very salty, very scenic", "Me and the octopus at the Great Salt Lake. It wanted to swim. It's very salty."},
	{"employee", "Employee of the month", "a framed kagent employee of the month plaque", "kagent Employee of the Month: never cold-started a day in their life."},
	{"badge", "Pod ID badge", "a Kubernetes pod ID badge on a lanyard, with a funny pod name and status", "Status: Running. Restarts: 0. Vibes: Ready."},
	{"arcade", "8-bit high score", "an 8-bit arcade high-score screen where they hold first place", "NEW HIGH SCORE. Please insert one more agent."},
	{"snapshot", "Snapshot polaroid", "a polaroid frozen in a Substrate snapshot, filed in a Snapshot Storage drawer", "Snapshotted in 140 ms. Will wake up exactly this good-looking."},
}

// Captions is the model's answer, validated: one short caption per scene.
type Captions struct {
	Look     string            `json:"look"`     // a short, kind description of what's visible
	Captions map[string]string `json:"captions"` // by scene ID
	Fallback bool              `json:"fallback"` // true when any caption is a stock one
	Why      string            `json:"why,omitempty"`
}

const (
	maxCaption = 90
	maxLook    = 100
)

// fallbackCaptions is the stock set, used when the model can't be reached or answers badly.
func fallbackCaptions(why string) Captions {
	c := Captions{Captions: map[string]string{}, Fallback: true, Why: why}
	for _, s := range Scenes {
		c.Captions[s.ID] = s.Fallback
	}
	return c
}

// blocked are words a family-friendly booth never prints; a caption with one is replaced.
var blocked = regexp.MustCompile(`(?i)\b(fuck|shit|bitch|damn|sexy|nude|naked|drunk|kill|ugly|fat|old|young)\b`)

// clean trims s to one line of at most n characters (runes), or "" if it's unusable.
func clean(s string, n int) string {
	s = strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if r < 32 {
			return ' '
		}
		return r
	}, s)), " ")
	s = strings.Trim(s, `"' `)
	if s == "" || blocked.MatchString(s) {
		return ""
	}
	if utf8.RuneCountInString(s) > n {
		r := []rune(s)
		s = strings.TrimSpace(string(r[:n-1])) + "…"
	}
	return s
}

// parseCaptions validates the model's JSON. Missing or unusable captions become stock
// ones; a reply that isn't the JSON at all is an error.
func parseCaptions(raw string) (Captions, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(strings.TrimSuffix(raw, "```"), "```json")
	var v struct {
		Look     string            `json:"look"`
		Captions map[string]string `json:"captions"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &v); err != nil {
		return Captions{}, errors.New("reply was not JSON")
	}
	if len(v.Captions) == 0 {
		return Captions{}, errors.New("reply had no captions")
	}
	c := Captions{Look: clean(v.Look, maxLook), Captions: map[string]string{}}
	for _, s := range Scenes {
		if t := clean(v.Captions[s.ID], maxCaption); t != "" {
			c.Captions[s.ID] = t
		} else {
			c.Captions[s.ID], c.Fallback = s.Fallback, true
		}
	}
	return c, nil
}

// systemPrompt tells the model what to write and, above all, what not to.
func systemPrompt() string {
	var b strings.Builder
	b.WriteString(`You write captions for a photo booth at KubeCon + CloudNativeCon North America 2026 (November 9-12, Salt Palace, Salt Lake City, Utah). ` +
		`The booth puts the visitor into ten funny pictures with the Agent Substrate octopus mascot (a friendly green octopus holding little robot puppets) and the kagent logo. ` +
		`Look at the photo and describe ONLY fun, visible things: pose, expression, gestures, clothing colours, hats, glasses, text printed on a shirt, badge or lanyard, props. ` +
		`NEVER guess or mention age, gender, race, ethnicity, body shape or size, attractiveness, health, disability, religion or anything else sensitive, and never identify who the person is. ` +
		`If there is no person, joke about whatever is in the photo. Keep it kind, family-friendly and work-safe; tease the cloud, never the person. ` +
		"Puns about Kubernetes, pods, agents, snapshots, Salt Lake City and the octopus are welcome.\n" +
		`Reply with JSON only: {"look": "<max 80 chars>", "captions": {`)
	for i, s := range Scenes {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, `"%s": "<max %d chars: %s>"`, s.ID, maxCaption-10, s.Brief)
	}
	b.WriteString("}}")
	return b.String()
}

// Usage is the model's real token count for one photo, as it reported it.
type Usage struct {
	Input  int     `json:"input"`  // prompt tokens: the image and the instructions
	Cached int     `json:"cached"` // of which served from OpenAI's prompt cache
	Output int     `json:"output"` // the reply
	USD    float64 `json:"usd"`    // estimated at gpt-4.1-mini prices
}

// cost is u at gpt-4.1-mini prices (USD per million: input 0.40, cached 0.10, output 1.60).
func (u Usage) cost() float64 {
	return (float64(max(0, u.Input-u.Cached))*0.40 + float64(u.Cached)*0.10 + float64(u.Output)*1.60) / 1e6
}

// Captioner writes the captions for one photo.
type Captioner interface {
	Captions(ctx context.Context, jpeg []byte, player string) (Captions, Usage, error)
}

// LLM calls the model through agentgateway's /selfie/v1 route (in-cluster only), which
// adds the API key, guards the prompt and caps the token budget.
type LLM struct {
	URL    string
	Client *http.Client
}

// NewLLM uses SELFIE_LLM_URL, or the in-cluster /selfie/v1 route.
func NewLLM() *LLM {
	url := os.Getenv("SELFIE_LLM_URL")
	if url == "" {
		url = "http://arcade.agentgateway-system.svc.cluster.local/selfie/v1/chat/completions"
	}
	return &LLM{URL: url, Client: &http.Client{Timeout: 25 * time.Second}}
}

// Captions sends the photo (a data URL, never stored) and parses the reply. The reply and
// the image are never logged; errors carry only status codes.
func (l *LLM) Captions(ctx context.Context, jpeg []byte, player string) (Captions, Usage, error) {
	body, _ := json.Marshal(map[string]any{
		"model":           "gpt-4.1-mini",
		"temperature":     0.9,
		"max_tokens":      700,
		"response_format": map[string]string{"type": "json_object"},
		"messages": []any{
			map[string]any{"role": "system", "content": systemPrompt()},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "Here is my photo booth picture. Write my ten captions."},
				map[string]any{"type": "image_url", "image_url": map[string]string{
					"url": "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(jpeg), "detail": "low"}},
			}},
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.URL, bytes.NewReader(body))
	if err != nil {
		return Captions{}, Usage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Selfie-Player", player) // agentgateway's per-player limit keys on it
	resp, err := l.Client.Do(req)
	if err != nil {
		return Captions{}, Usage{}, fmt.Errorf("model unreachable")
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	switch {
	case resp.StatusCode == http.StatusForbidden:
		return Captions{}, Usage{}, errors.New("agentgateway's prompt guard blocked the request")
	case resp.StatusCode == http.StatusTooManyRequests:
		return Captions{}, Usage{}, errors.New("agentgateway's rate limit: the booth is busy")
	case resp.StatusCode != http.StatusOK:
		return Captions{}, Usage{}, fmt.Errorf("model answered %d", resp.StatusCode)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			Prompt     int `json:"prompt_tokens"`
			Completion int `json:"completion_tokens"`
			Details    struct {
				Cached int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	if json.Unmarshal(b, &out) != nil || len(out.Choices) == 0 {
		return Captions{}, Usage{}, errors.New("unreadable model answer")
	}
	u := Usage{Input: out.Usage.Prompt, Cached: out.Usage.Details.Cached, Output: out.Usage.Completion}
	u.USD = u.cost()
	c, err := parseCaptions(out.Choices[0].Message.Content)
	return c, u, err
}
