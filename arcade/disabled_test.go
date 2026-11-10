package main

import (
	"strings"
	"testing"

	"github.com/sebbycorp/kubecon-adventures/arcade/internal/platform"
)

func TestParseDisabled(t *testing.T) {
	got := parseDisabled(" substrate-siege, /flappy ,,")
	if len(got) != 2 || !got["/substrate-siege"] || !got["/flappy"] {
		t.Fatalf("parseDisabled = %v", got)
	}
	if len(parseDisabled("")) != 0 {
		t.Fatal("empty list should disable nothing")
	}
}

func TestDisabledHidesBoardAndPage(t *testing.T) {
	disabled = parseDisabled("substrate-siege")
	defer func() { disabled = map[string]bool{} }()
	var z zeroTop
	for _, b := range arcadeBoards(5, z, z, z, z, z, z, z, z) {
		if b.Path == "/substrate-siege" {
			t.Fatal("disabled game still has a board")
		}
	}
	page := string(withDisabled([]byte("<html><head><title>x</title></head></html>")))
	if !strings.Contains(page, `window.ARCADE_DISABLED=["/substrate-siege"]`) {
		t.Fatalf("page = %s", page)
	}
}

type zeroTop struct{}

func (zeroTop) Top(int) []platform.BoardRow { return nil }
