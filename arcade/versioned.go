package main

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"regexp"
	"strings"
)

// The game cards' gameplay loops: Go's built-in table lacks these, and the distroless
// image has no /etc/mime.types, so register them for http.FileServerFS.
func init() {
	_ = mime.AddExtensionType(".webm", "video/webm")
	_ = mime.AddExtensionType(".mp4", "video/mp4")
}

// assetRE matches a page's links to the arcade's shared and Agent Flow static files, with
// any hand-written ?v= already on them.
var assetRE = regexp.MustCompile(`(/(?:agent-flow/)?static/[\w./-]+\.(?:js|css|svg|webp|png|webm|mp4))(?:\?v=\w+)?`)

// versionedPage is page (from root, the embedded web folder) with each static link
// carrying a hash of the file it points at, so Cloudflare, which caches .js and images
// for hours, serves every deploy at once. Links to files it can't find are left as they are.
func versionedPage(root fs.FS, page string) []byte {
	b, err := fs.ReadFile(root, page)
	if err != nil {
		panic(err)
	}
	return assetRE.ReplaceAllFunc(b, func(m []byte) []byte {
		link := string(assetRE.FindSubmatch(m)[1])
		file := strings.Replace(strings.Replace(link, "/agent-flow/static/", "agent-flow/", 1), "/static/", "shared/", 1)
		f, err := fs.ReadFile(root, file)
		if err != nil {
			return m
		}
		sum := sha256.Sum256(f)
		return fmt.Appendf(nil, "%s?v=%x", link, sum[:4])
	})
}

// servePage serves a versioned page, never cached, so a new deploy's links are seen at once.
func servePage(body []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(body)
	}
}
