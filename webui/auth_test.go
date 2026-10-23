package webui

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// login follows a fresh !web link for user and returns the session cookie it sets
func login(t *testing.T, s *Server, user string) *http.Cookie {
	t.Helper()
	link, _, err := s.IssueLink(user)
	if err != nil {
		t.Fatal(err)
	}
	rec := serve(s, http.MethodGet, link, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("following link: expected redirect to /, got %d %q", rec.Code, rec.Header().Get("Location"))
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != sessionCookie {
		t.Fatalf("expected a session cookie, got %v", cookies)
	}
	return cookies[0]
}

func serve(s *Server, method, target string, cookie *http.Cookie) *httptest.ResponseRecorder {
	var body *strings.Reader
	if method == http.MethodPost {
		body = strings.NewReader(`{}`)
	} else {
		body = strings.NewReader("")
	}
	req := httptest.NewRequest(method, target, body)
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func TestUnauthenticatedRequestsAreRejected(t *testing.T) {
	s, _ := newTestServer()
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/"},
		{http.MethodGet, "/api/state"},
		{http.MethodPost, "/api/stop"},
		{http.MethodPost, "/api/remove"},
		{http.MethodGet, "/api/art/" + artID("/music/album/a.mp3")},
	} {
		if rec := serve(s, tc.method, tc.path, nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: expected 401, got %d", tc.method, tc.path, rec.Code)
		}
	}

	bogus := &http.Cookie{Name: sessionCookie, Value: "not-a-token"}
	if rec := serve(s, http.MethodGet, "/api/state", bogus); rec.Code != http.StatusUnauthorized {
		t.Errorf("bogus session: expected 401, got %d", rec.Code)
	}
	if rec := serve(s, http.MethodGet, "/?token=not-a-token", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("bogus token: expected 401, got %d", rec.Code)
	}
}

func TestLinkStartsSession(t *testing.T) {
	s, _ := newTestServer()
	s.URL = "http://music.example.com/"
	link, expires, err := s.IssueLink("alice")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(link, "http://music.example.com/?token=") {
		t.Fatalf("unexpected link %q", link)
	}
	if d := time.Until(expires); d < DefaultTokenTTL-time.Minute || d > DefaultTokenTTL {
		t.Fatalf("expected link to expire in %v, got %v", DefaultTokenTTL, d)
	}

	other, _, _ := s.IssueLink("alice")
	if other == link {
		t.Fatal("each !web should create a new token")
	}

	cookie := login(t, s, "alice")
	if !cookie.HttpOnly || cookie.Secure || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("unexpected cookie attributes: %+v", cookie)
	}

	if rec := serve(s, http.MethodGet, "/", cookie); rec.Code != http.StatusOK {
		t.Fatalf("index with session: expected 200, got %d", rec.Code)
	}
	rec := serve(s, http.MethodGet, "/api/state", cookie)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"user":"alice"`) {
		t.Fatalf("state with session: %d %s", rec.Code, rec.Body.String())
	}
}

func TestSecureCookieForHTTPS(t *testing.T) {
	s, _ := newTestServer()
	s.URL = "https://music.example.com"
	if cookie := login(t, s, "alice"); !cookie.Secure {
		t.Fatal("session cookie should be Secure when the web UI is served over https")
	}
}

func TestTokensExpire(t *testing.T) {
	s, _ := newTestServer()
	now := time.Now()
	s.clock = func() time.Time { return now }
	s.TokenTTL = time.Hour

	link, _, _ := s.IssueLink("alice")
	cookie := login(t, s, "alice")

	now = now.Add(time.Hour - time.Second)
	if rec := serve(s, http.MethodGet, "/api/state", cookie); rec.Code != http.StatusOK {
		t.Fatalf("session before expiry: expected 200, got %d", rec.Code)
	}

	now = now.Add(time.Second)
	if rec := serve(s, http.MethodGet, "/api/state", cookie); rec.Code != http.StatusUnauthorized {
		t.Fatalf("session after expiry: expected 401, got %d", rec.Code)
	}
	if rec := serve(s, http.MethodGet, link, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("link after expiry: expected 401, got %d", rec.Code)
	}

	// Issuing a new link clears out expired tokens
	if _, _, err := s.IssueLink("bob"); err != nil {
		t.Fatal(err)
	}
	if n := len(s.tokens.tokens); n != 1 {
		t.Fatalf("expected expired tokens to be pruned, %d remain", n)
	}
}

func TestLinkTokenIsURLSafe(t *testing.T) {
	s, _ := newTestServer()
	link, _, _ := s.IssueLink("alice")
	u, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	if token := u.Query().Get("token"); len(token) < 43 || url.QueryEscape(token) != token {
		t.Fatalf("token should be long and URL safe, got %q", token)
	}
}
