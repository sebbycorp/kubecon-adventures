package main

import (
	"bytes"
	"encoding/json"
	"strings"
)

// disabled holds the games left out of this deploy, by path ("/substrate-siege"), from
// DISABLED_GAMES. Empty means every game runs.
var disabled = map[string]bool{}

// parseDisabled reads a comma-separated list of game paths, with or without the leading slash.
func parseDisabled(s string) map[string]bool {
	out := map[string]bool{}
	for _, g := range strings.Split(s, ",") {
		if g = strings.Trim(strings.TrimSpace(g), "/"); g != "" {
			out["/"+g] = true
		}
	}
	return out
}

// withDisabled puts the disabled list in a page as window.ARCADE_DISABLED, before any of
// its scripts run, so the front page and TV can drop those games' cards.
func withDisabled(page []byte) []byte {
	paths := []string{}
	for p := range disabled {
		paths = append(paths, p)
	}
	js, _ := json.Marshal(paths)
	tag := append(append([]byte("<head><script>window.ARCADE_DISABLED="), js...), []byte(";</script>")...)
	return bytes.Replace(page, []byte("<head>"), tag, 1)
}
