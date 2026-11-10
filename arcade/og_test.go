package main

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestOGImages checks every page's link-preview image is embedded and served from
// /static/og/ as a PNG, as the pages' og:image tags point there.
func TestOGImages(t *testing.T) {
	shared, err := fs.Sub(webFS, "web/shared")
	if err != nil {
		t.Fatal(err)
	}
	h := http.StripPrefix("/static/", http.FileServerFS(shared))
	for _, key := range []string{"arcade", "cluster-defense", "gateway-defense", "agent-flow", "flappy", "pod-packer", "selfie"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/og/"+key+".png", nil))
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" || rec.Body.Len() < 10000 {
			t.Errorf("%s: %d %q %d bytes", key, rec.Code, rec.Header().Get("Content-Type"), rec.Body.Len())
		}
	}
	// Each page that has a preview names its own image.
	for page, key := range map[string]string{"web/index.html": "arcade",
		"web/cluster-defense/phone.html": "cluster-defense", "web/gateway-defense/phone.html": "gateway-defense",
		"web/agent-flow/phone.html": "agent-flow", "web/flappy/phone.html": "flappy", "web/pod-packer/phone.html": "pod-packer", "web/selfie/phone.html": "selfie"} {
		b, err := fs.ReadFile(webFS, page)
		if err != nil {
			t.Fatal(err)
		}
		if want := `property="og:image" content="https://kubecon.agentpwned.ai/static/og/` + key + `.png"`; !strings.Contains(string(b), want) {
			t.Errorf("%s: no %s", page, want)
		}
		if !strings.Contains(string(b), `name="twitter:card" content="summary_large_image"`) {
			t.Errorf("%s: no twitter card", page)
		}
	}
}
