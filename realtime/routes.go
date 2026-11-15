package realtime

import (
	"net/http"
	"strings"
)

// ServeHTTP routes the Pusher endpoints under the configured path: /up,
// the WebSocket at /app/{key}, and the HTTP API's /apps/{id}/events and
// /apps/{id}/batch_events.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if s.cfg.Path != "" {
		if !strings.HasPrefix(path, s.cfg.Path+"/") {
			plain(w, http.StatusNotFound, "Not found.")

			return
		}

		path = strings.TrimPrefix(path, s.cfg.Path)
	}

	segments := strings.Split(strings.Trim(path, "/"), "/")

	switch {
	case path == "/up":
		s.route(w, r, http.MethodGet, func() {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"health":"OK"}`))
		})
	case len(segments) == 2 && segments[0] == "app":
		s.route(w, r, http.MethodGet, func() { s.serveSocket(w, r, segments[1]) })
	case len(segments) == 3 && segments[0] == "apps" && segments[2] == "events":
		s.route(w, r, http.MethodPost, func() { s.serveEvents(w, r, segments[1], path) })
	case len(segments) == 3 && segments[0] == "apps" && segments[2] == "batch_events":
		s.route(w, r, http.MethodPost, func() { s.serveBatchEvents(w, r, segments[1], path) })
	default:
		plain(w, http.StatusNotFound, "Not found.")
	}
}

func (s *Server) route(w http.ResponseWriter, r *http.Request, method string, handle func()) {
	if r.Method != method {
		w.Header().Set("Allow", method)
		plain(w, http.StatusMethodNotAllowed, "Method not allowed.")

		return
	}

	handle()
}

// plain answers with a status and a short text body.
func plain(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(message)) //nolint:gosec // G705: served as text/plain, so an echoed app id can never render as markup
}
