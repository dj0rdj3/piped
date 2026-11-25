package httpserver

import (
	"io/fs"
	"net/http"
	"strings"
)

func (s *Server) assets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(405)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/")
	switch r.URL.Path {
	case "/notifications", "/notifications/", "/api-keys", "/api-keys/", "/", "/login", "/login/", "/users", "/users/", "/credentials", "/credentials/", "/hosts", "/hosts/", "/jobs", "/jobs/":
		// Only known UI routes receive the SPA document. Missing assets and API
		// endpoints must retain their real 404s and content types.
		r = r.Clone(r.Context())
		r.URL.Path = "/"
		r.URL.RawPath = ""
		name = "index.html"
	}
	if s.config.Assets == nil || !fs.ValidPath(name) {
		http.NotFound(w, r)
		return
	}
	info, err := fs.Stat(s.config.Assets, name)
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	http.FileServer(http.FS(s.config.Assets)).ServeHTTP(w, r)
}
