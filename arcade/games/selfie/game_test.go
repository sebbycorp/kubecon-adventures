package selfie

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sebbycorp/kubecon-adventures/arcade/internal/kagent"
	"github.com/sebbycorp/kubecon-adventures/arcade/internal/platform"
)

// fakeOps is kagent in memory: sessions are RUNNING or SUSPENDED.
type fakeOps struct {
	mu                         sync.Mutex
	state                      map[string]string
	seq, resumes, suspends     int
	failResume, unsettledFirst bool
}

func newFake() *fakeOps { return &fakeOps{state: map[string]string{"old-1": "RUNNING"}} }

func (f *fakeOps) CreateSession(_ context.Context, name string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	id := fmt.Sprintf("s%d-%s", f.seq, name)
	f.state[id] = "RUNNING"
	return id, nil
}
func (f *fakeOps) Prompt(context.Context, string, string) (kagent.Reply, error) {
	return kagent.Reply{}, nil
}
func (f *fakeOps) Suspend(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.unsettledFirst {
		f.unsettledFirst = false
		return errors.New("FailedPrecondition: lifecycle work is not yet settled")
	}
	f.suspends++
	f.state[id] = "SUSPENDED"
	return nil
}
func (f *fakeOps) Resume(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failResume {
		return errors.New("resume broke")
	}
	f.resumes++
	f.state[id] = "RUNNING"
	return nil
}
func (f *fakeOps) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.state, id)
	return nil
}
func (f *fakeOps) ListSessionIDs(context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []string
	for id := range f.state {
		ids = append(ids, id)
	}
	return ids, nil
}
func (f *fakeOps) running() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, s := range f.state {
		if s == "RUNNING" {
			n++
		}
	}
	return n
}

// fakeLLM answers with good captions, or err.
type fakeLLM struct {
	err   error
	delay time.Duration
}

func (l fakeLLM) Captions(ctx context.Context, img []byte, player string) (Captions, Usage, error) {
	if l.delay > 0 {
		time.Sleep(l.delay)
	}
	if l.err != nil {
		return Captions{}, Usage{}, l.err
	}
	c, _ := parseCaptions(goodReply())
	return c, Usage{Input: 1200, Cached: 0, Output: 300, USD: 0.00096}, nil
}

func goodReply() string {
	m := map[string]string{}
	for _, s := range Scenes {
		m[s.ID] = "A caption for " + s.Title
	}
	b, _ := json.Marshal(map[string]any{"look": "Waving, blue hoodie", "captions": m})
	return string(b)
}

var jpeg = append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, make([]byte, 100)...)

func ready(t *testing.T, ops *fakeOps, llm Captioner) *Game {
	t.Helper()
	g := New(nil, ops, llm)
	g.sweep(context.Background())
	g.fillPool(context.Background())
	if len(g.slots) != poolSize || ops.running() != 0 {
		t.Fatalf("pool: %d slots, %d running", len(g.slots), ops.running())
	}
	return g
}

func TestParseCaptions(t *testing.T) {
	c, err := parseCaptions(goodReply())
	if err != nil || c.Fallback || len(c.Captions) != len(Scenes) || c.Look != "Waving, blue hoodie" {
		t.Fatalf("good reply: %+v %v", c, err)
	}
	// Missing, overlong and unkind captions are fixed up; the rest are kept.
	long := strings.Repeat("pod ", 40)
	c, err = parseCaptions(`{"look":"hi","captions":{"keynote":"` + long + `","wanted":"you look old","card":"  Rare\ncard  "}}`)
	if err != nil || !c.Fallback {
		t.Fatalf("partial reply: %+v %v", c, err)
	}
	if r := []rune(c.Captions["keynote"]); len(r) > maxCaption || !strings.HasSuffix(c.Captions["keynote"], "…") {
		t.Fatalf("long caption not trimmed: %q", c.Captions["keynote"])
	}
	if c.Captions["wanted"] != Scenes[1].Fallback || c.Captions["card"] != "Rare card" || c.Captions["lake"] != Scenes[5].Fallback {
		t.Fatalf("captions = %v", c.Captions)
	}
	for _, bad := range []string{"not json", `{"look":"x"}`, "```json\n[1]\n```"} {
		if _, err := parseCaptions(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	if c, err := parseCaptions("```json\n" + goodReply() + "\n```"); err != nil || c.Fallback {
		t.Fatalf("fenced reply: %v", err)
	}
	p := systemPrompt()
	for _, s := range Scenes {
		if !strings.Contains(p, `"`+s.ID+`"`) {
			t.Fatalf("prompt lacks scene %s", s.ID)
		}
	}
	if !strings.Contains(p, "Salt Lake City") || !strings.Contains(p, "NEVER guess") {
		t.Fatal("prompt lacks the venue or the sensitive-inference rule")
	}
}

func TestMakeWakesAndSnapshotsAnAgent(t *testing.T) {
	ops := newFake()
	ops.unsettledFirst = true
	g := ready(t, ops, fakeLLM{})
	ops.unsettledFirst = true // the job's suspend settles once before it lands
	res, err := g.Make(context.Background(), platform.Player{ID: "p1", Nick: "Ada"}, jpeg)
	if err != nil || res.Fallback || res.Agent.State != "snapshotted" || res.Usage.Input != 1200 || res.Taken != 1 {
		t.Fatalf("result: %+v %v", res, err)
	}
	if ops.running() != 0 || ops.resumes != 1 {
		t.Fatalf("after a job: %d running, %d resumes", ops.running(), ops.resumes)
	}
	s := g.Snapshot()
	if s.Taken != 1 || s.Tokens != 1500 || len(s.Events) != 1 || s.Events[0].Caption == "" || s.Awake != 0 {
		t.Fatalf("snapshot: %+v", s)
	}
}

func TestMakeFallsBackWithoutBlocking(t *testing.T) {
	ops := newFake()
	g := ready(t, ops, fakeLLM{err: errors.New("agentgateway's rate limit: the booth is busy")})
	ops.failResume = true
	res, err := g.Make(context.Background(), platform.Player{ID: "p1"}, jpeg)
	if err != nil || !res.Fallback || res.Captions.Captions["keynote"] != Scenes[0].Fallback || res.Agent.State != "none" {
		t.Fatalf("fallback: %+v %v", res, err)
	}
	if g.Snapshot().Fallback != 1 {
		t.Fatal("fallback not counted")
	}
}

func TestAdmitLimits(t *testing.T) {
	g := ready(t, newFake(), fakeLLM{})
	big := append([]byte{0xFF, 0xD8}, make([]byte, maxImage)...)
	if _, err := g.Make(context.Background(), platform.Player{ID: "p"}, big); !errors.Is(err, ErrTooBig) {
		t.Fatalf("big: %v", err)
	}
	if _, err := g.Make(context.Background(), platform.Player{ID: "p"}, []byte("GIF89a hello there")); !errors.Is(err, ErrNotImage) {
		t.Fatalf("gif: %v", err)
	}
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 20)...)
	for i := range perPlayer {
		if _, err := g.Make(context.Background(), platform.Player{ID: "p"}, png); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if _, err := g.Make(context.Background(), platform.Player{ID: "p"}, jpeg); !errors.Is(err, ErrSlowDown) {
		t.Fatalf("fourth call: %v", err)
	}
	g.now = func() time.Time { return time.Now().Add(perWindow + time.Second) }
	if _, err := g.Make(context.Background(), platform.Player{ID: "p"}, jpeg); err != nil {
		t.Fatalf("after the window: %v", err)
	}
	// The arcade-wide cap: maxJobs callers in flight, the next is turned away.
	g.mu.Lock()
	g.jobs = maxJobs
	g.mu.Unlock()
	if _, err := g.Make(context.Background(), platform.Player{ID: "q"}, jpeg); !errors.Is(err, ErrBusy) {
		t.Fatalf("over the cap: %v", err)
	}
}

func TestLLMThroughGateway(t *testing.T) {
	var got struct {
		Model    string `json:"model"`
		Messages []struct {
			Content any `json:"content"`
		} `json:"messages"`
	}
	var player string
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		player = r.Header.Get("X-Selfie-Player")
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		reply, _ := json.Marshal(goodReply())
		fmt.Fprintf(w, `{"choices":[{"message":{"content":%s}}],"usage":{"prompt_tokens":1100,"completion_tokens":250,"prompt_tokens_details":{"cached_tokens":400}}}`, reply)
	}))
	defer srv.Close()
	l := &LLM{URL: srv.URL, Client: srv.Client()}
	c, u, err := l.Captions(context.Background(), jpeg, "p9")
	if err != nil || c.Fallback || u.Input != 1100 || u.Cached != 400 || u.Output != 250 || player != "p9" || got.Model != "gpt-4.1-mini" {
		t.Fatalf("captions %+v usage %+v err %v player %q", c, u, err, player)
	}
	if want := (700*0.40 + 400*0.10 + 250*1.60) / 1e6; u.USD != want {
		t.Fatalf("usd %v want %v", u.USD, want)
	}
	if b, _ := json.Marshal(got.Messages[1].Content); !strings.Contains(string(b), "data:image/jpeg;base64,") {
		t.Fatal("the photo wasn't sent as an image part")
	}
	status = http.StatusForbidden
	if _, _, err := l.Captions(context.Background(), jpeg, "p9"); err == nil || !strings.Contains(err.Error(), "prompt guard") {
		t.Fatalf("403: %v", err)
	}
	status = http.StatusTooManyRequests
	if _, _, err := l.Captions(context.Background(), jpeg, "p9"); err == nil || !strings.Contains(err.Error(), "rate limit") {
		t.Fatalf("429: %v", err)
	}
}
