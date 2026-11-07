package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// keepAlive is shorter than the idle timeouts browsers and proxies use on open responses.
const keepAlive = 25 * time.Second

// stream sends every session change as a server-sent event until the page goes away.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.serverError(w, r, fmt.Errorf("response writer cannot flush"))
		return
	}
	changes, stop := s.cfg.Sessions.Subscribe()
	defer stop()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ticker := time.NewTicker(keepAlive)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case change, open := <-changes:
			if !open {
				return
			}
			data, err := json.Marshal(change)
			if err != nil {
				s.cfg.Logger.Error("encode session change", "err", err)
				continue
			}
			if _, err := fmt.Fprintf(w, "event: change\ndata: %s\n\n", data); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
