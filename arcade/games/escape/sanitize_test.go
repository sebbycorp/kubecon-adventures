package escape

import (
	"strings"
	"testing"

	"github.com/sebbycorp/kubecon-adventures/arcade/internal/kagent"
)

func TestSanitizeRedactsSecrets(t *testing.T) {
	in := []kagent.ToolCall{
		{Name: "bash", Args: map[string]any{"command": "env"}, Output: "OPENAI_API_KEY=kagent-credential-injected\nKAGENT_CONFIG_JSON={\"runtime\":{\"control_plane_url\":\"http://x\"}}\nHOME=/root"},
		{Name: "bash", Args: map[string]any{"command": "cat t"}, Output: "token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2lnbmF0dXJl and abcdefabcdefabcdefabcdefabcdefabcdef12"},
		{Name: "grep_file", Args: map[string]any{"pattern": "KEY", "path": "/etc"}, Output: "no matches found"},
	}
	out := sanitize(in)
	joined := out[0].Output + out[1].Output
	for _, leak := range []string{"control_plane_url", "credential-injected", "eyJhbGci", "abcdefabcdefabcdef"} {
		if strings.Contains(joined, leak) {
			t.Errorf("leaked %q in %q", leak, joined)
		}
	}
	if !strings.Contains(out[0].Output, "OPENAI_API_KEY=kagent-crede…<redacted>") || !strings.Contains(out[0].Output, "HOME=/root") {
		t.Errorf("unexpected env redaction: %q", out[0].Output)
	}
	if got := out[2].Args["command"]; got != "grep_file KEY /etc" {
		t.Errorf("file tool label = %q", got)
	}
}
