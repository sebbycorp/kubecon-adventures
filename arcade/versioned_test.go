package main

import (
	"mime"
	"strings"
	"testing"
	"testing/fstest"
)

func TestVersionedPage(t *testing.T) {
	root := fstest.MapFS{
		"page.html":              {Data: []byte(`<script src="/static/arcade.js?v=2"></script><script src="/agent-flow/static/scene.js"></script><img src="/static/shots/${img}.webp?v=2"><script src="/static/gone.js"></script><video><source src="/static/loops/game.webm"><source src="/static/loops/game.mp4"></video>`)},
		"shared/arcade.js":       {Data: []byte("a")},
		"agent-flow/scene.js":    {Data: []byte("b")},
		"shared/loops/game.webm": {Data: []byte("c")},
		"shared/loops/game.mp4":  {Data: []byte("d")},
	}
	got := string(versionedPage(root, "page.html"))
	for _, want := range []string{`/static/arcade.js?v=ca978112"`, `/agent-flow/static/scene.js?v=3e23e816"`, `/static/shots/${img}.webp?v=2"`, `/static/gone.js"`, `/static/loops/game.webm?v=2e7d2c03"`, `/static/loops/game.mp4?v=18ac3e73"`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
	if strings.Contains(got, "arcade.js?v=2") {
		t.Errorf("hand-written version kept: %s", got)
	}
}

func TestVideoTypes(t *testing.T) {
	for ext, want := range map[string]string{".webm": "video/webm", ".mp4": "video/mp4"} {
		if got := mime.TypeByExtension(ext); got != want {
			t.Errorf("%s: %q, want %q", ext, got, want)
		}
	}
}
