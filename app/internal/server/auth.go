package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
)

const (
	tokenBytes  = 32
	tokenCookie = "inbox_token"
	tokenQuery  = "token"
)

// NewToken makes the per-run secret the page has to present.
func NewToken() (string, error) {
	buf := make([]byte, tokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// guard blocks everything that is not this user's page on this machine: other Host names (DNS rebinding), other
// origins for anything that changes state (cross-site requests), and API calls without the token cookie. It also
// tells the browser which pages may frame the app (clickjacking).
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", s.frameAncestors)
		if !s.allowedHosts[r.Host] {
			writeError(w, http.StatusForbidden, codeForbidden, "Unknown host.")
			return
		}
		if changesState(r) && !s.allowedOrigins[r.Header.Get("Origin")] {
			writeError(w, http.StatusForbidden, codeForbidden, "Requests from other sites are refused.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// frameAncestors lets only the app itself, and embedOrigin when set, show the app in a frame.
func frameAncestors(embedOrigin string) string {
	policy := "frame-ancestors 'self'"
	if embedOrigin != "" {
		policy += " " + embedOrigin
	}
	return policy
}

func (s *Server) requireToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.hasToken(r) {
			writeError(w, http.StatusUnauthorized, codeAccessDenied, "Open the link that claude-inbox serve printed.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) hasToken(r *http.Request) bool {
	cookie, err := r.Cookie(tokenCookie)
	return err == nil && s.tokenMatches(cookie.Value)
}

func (s *Server) tokenMatches(candidate string) bool {
	return subtle.ConstantTimeCompare([]byte(candidate), []byte(s.cfg.Token)) == 1
}

// changesState covers every method but GET and HEAD, and the terminal socket, whose upgrade is a GET that lets
// the page type into a session.
func changesState(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return true
	}
	return r.Header.Get("Upgrade") != ""
}

func hostsFor(port int) map[string]bool {
	p := strconv.Itoa(port)
	return map[string]bool{"127.0.0.1:" + p: true, "localhost:" + p: true}
}

func originsFor(port int) map[string]bool {
	p := strconv.Itoa(port)
	return map[string]bool{"http://127.0.0.1:" + p: true, "http://localhost:" + p: true}
}
