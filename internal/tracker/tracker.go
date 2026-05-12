package tracker

import (
	_ "embed"
	"net/http"
)

//go:embed tracker.js
var trackerJS []byte

func Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write(trackerJS)
	}
}
