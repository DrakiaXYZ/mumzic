package webui

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"html/template"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	sessionCookie   = "mumzic_session"
	DefaultTokenTTL = 12 * time.Hour
)

// tokenStore holds the access tokens handed out with !web, a token is also used as the session cookie value
type tokenStore struct {
	mu     sync.Mutex
	tokens map[string]session
}

type session struct {
	user    string
	expires time.Time
}

type userKey struct{}

var deniedTmpl = template.Must(template.New("denied").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Name}}</title>
<style>
  body { margin: 0; background: #f6f6f4; color: #1d1d1b; font: 15px/1.5 system-ui, -apple-system, "Segoe UI", sans-serif; }
  main { max-width: 440px; margin: 15vh auto 0; padding: 0 16px; }
  .card { background: #fff; border: 1px solid #e2e1dc; border-radius: 10px; padding: 20px; }
  h1 { font-size: 20px; margin: 0 0 8px; }
  p { margin: 0; color: #6b6a65; }
  code { color: #1d1d1b; font-weight: 600; }
  @media (prefers-color-scheme: dark) {
    body { background: #141413; color: #ecebe6; }
    .card { background: #1e1e1c; border-color: #2f2f2c; }
    p { color: #9a9993; }
    code { color: #ecebe6; }
  }
</style>
</head>
<body>
<main>
  <div class="card">
    <h1>{{.Name}}</h1>
    <p>{{.Message}} Type <code>!web</code> in the bot's Mumble channel and it will message you a link.</p>
  </div>
</main>
</body>
</html>
`))

// IssueLink creates an access token for user and returns the web UI link containing it, and when it expires
func (s *Server) IssueLink(user string) (string, time.Time, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", time.Time{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(buf)

	ttl := s.TokenTTL
	if ttl <= 0 {
		ttl = DefaultTokenTTL
	}
	now := s.now()
	expires := now.Add(ttl)

	s.tokens.mu.Lock()
	if s.tokens.tokens == nil {
		s.tokens.tokens = make(map[string]session)
	}
	for t, sess := range s.tokens.tokens {
		if !now.Before(sess.expires) {
			delete(s.tokens.tokens, t)
		}
	}
	s.tokens.tokens[token] = session{user: user, expires: expires}
	s.tokens.mu.Unlock()

	return strings.TrimRight(s.URL, "/") + "/?token=" + token, expires, nil
}

// lookup returns the session for a token if it exists and hasn't expired
func (s *Server) lookup(token string) (session, bool) {
	s.tokens.mu.Lock()
	defer s.tokens.mu.Unlock()
	sess, ok := s.tokens.tokens[token]
	if !ok {
		return session{}, false
	}
	if !s.now().Before(sess.expires) {
		delete(s.tokens.tokens, token)
		return session{}, false
	}
	return sess, true
}

func (s *Server) now() time.Time {
	if s.clock != nil {
		return s.clock()
	}
	return time.Now()
}

// requireAuth only lets requests with a valid session through. Visiting /?token= with a valid token starts a session.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Referrer-Policy", "no-referrer")

		if r.URL.Path == "/" && r.URL.Query().Has("token") {
			s.startSession(w, r)
			return
		}

		var sess session
		var ok bool
		if c, err := r.Cookie(sessionCookie); err == nil {
			sess, ok = s.lookup(c.Value)
		}
		if !ok {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				w.Header().Set("Cache-Control", "no-store")
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			s.deny(w, "You need a link from the bot to use this page.")
			return
		}

		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, sess.user)))
	})
}

// startSession swaps the token in the link for a session cookie and redirects, so the token leaves the address bar
func (s *Server) startSession(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	sess, ok := s.lookup(token)
	if !ok {
		s.deny(w, "This link is invalid or has expired.")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		Expires:  sess.expires,
		HttpOnly: true,
		Secure:   strings.HasPrefix(s.URL, "https://"),
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) deny(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusUnauthorized)
	_ = deniedTmpl.Execute(w, struct{ Name, Message string }{s.name(), message})
}

// userFrom returns the Mumble user a request was authenticated as
func userFrom(r *http.Request) string {
	user, _ := r.Context().Value(userKey{}).(string)
	return user
}
