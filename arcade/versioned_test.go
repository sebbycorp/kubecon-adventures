package main

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestVersionedPage(t *testing.T) {
	root := fstest.MapFS{
		"page.html":           {Data: []byte(`<script src="/static/arcade.js?v=2"></script><script src="/agent-flow/static/scene.js"></script><img src="/static/shots/${img}.webp?v=2"><script src="/static/gone.js"></script>`)},
		"shared/arcade.js":    {Data: []byte("a")},
		"agent-flow/scene.js": {Data: []byte("b")},
	}
	got := string(versionedPage(root, "page.html"))
	for _, want := range []string{`/static/arcade.js?v=ca978112"`, `/agent-flow/static/scene.js?v=3e23e816"`, `/static/shots/${img}.webp?v=2"`, `/static/gone.js"`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
	if strings.Contains(got, "arcade.js?v=2") {
		t.Errorf("hand-written version kept: %s", got)
	}
}
