package webui

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/iotku/mumzic/database"
	"github.com/iotku/mumzic/playback"
	"github.com/iotku/mumzic/playlist"
)

func newTestServer() (*Server, *playback.Player) {
	player := &playback.Player{
		Playlist: playlist.List{
			Playlist: [][]string{{"/music/album/a.mp3", "A"}, {"https://www.youtube.com/watch?v=x", "B"}, {"c.mp3", "C"}},
		},
		Volume: 0.3,
	}
	var s Server
	s.SetPlayer(player)
	return &s, player
}

func post(handler http.HandlerFunc, body, contentType string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

func TestState(t *testing.T) {
	s, _ := newTestServer()
	rec := httptest.NewRecorder()
	s.handleState(rec, httptest.NewRequest(http.MethodGet, "/api/state", nil))

	var st state
	if err := json.NewDecoder(rec.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	if !st.Connected || st.Status != "stopped" || st.Volume != 30 {
		t.Fatalf("unexpected state: %+v", st)
	}
	if st.Current == nil || st.Current.Title != "A" || st.Current.Type != "file" || st.Current.Source != "a.mp3" {
		t.Fatalf("unexpected current track: %+v", st.Current)
	}
	if len(st.Queue) != 2 || st.Queue[0].Index != 1 || st.Queue[0].Type != "link" || st.Queue[0].Source != "https://www.youtube.com/watch?v=x" || st.Queue[0].Art != "/api/art/"+artID("https://www.youtube.com/watch?v=x") {
		t.Fatalf("unexpected queue: %+v", st.Queue)
	}
}

func TestStateDisconnected(t *testing.T) {
	var s Server
	rec := httptest.NewRecorder()
	s.handleState(rec, httptest.NewRequest(http.MethodGet, "/api/state", nil))
	if !strings.Contains(rec.Body.String(), `"connected":false`) {
		t.Fatalf("expected disconnected state, got %s", rec.Body.String())
	}

	if rec := post(s.action(s.removeTrack), `{}`, "application/json"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 before connecting, got %d", rec.Code)
	}
}

func TestRemove(t *testing.T) {
	s, player := newTestServer()
	rec := post(s.action(s.removeTrack), `{"index":2,"title":"C"}`, "application/json")
	if rec.Code != http.StatusOK {
		t.Fatalf("remove failed: %d %s", rec.Code, rec.Body.String())
	}
	if player.Playlist.Size() != 2 {
		t.Fatalf("track was not removed: %v", player.Playlist.Playlist)
	}

	if rec := post(s.action(s.removeTrack), `{"index":0,"title":"A"}`, "application/json"); rec.Code != http.StatusBadRequest {
		t.Fatalf("removing the current track should fail, got %d", rec.Code)
	}
}

func TestActionRequiresJSON(t *testing.T) {
	s, player := newTestServer()
	rec := post(s.action(s.removeTrack), `{"index":2,"title":"C"}`, "application/x-www-form-urlencoded")
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("expected 415, got %d", rec.Code)
	}
	if player.Playlist.Size() != 3 {
		t.Fatal("playlist changed despite rejected request")
	}
}

func TestRoutes(t *testing.T) {
	s, _ := newTestServer()
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	// log in by following a !web link, the jar keeps the session cookie
	s.URL = srv.URL
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	link, _, _ := s.IssueLink("alice")
	if res, err := client.Get(link); err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("following link failed: %v %v", err, res)
	}

	s.Name = "DJ <Bot>"
	res, err := client.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("index: %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	if !strings.Contains(string(body), "<title>DJ &lt;Bot&gt;</title>") {
		t.Fatal("index should use the escaped bot name as its title")
	}

	res, err = client.Post(srv.URL+"/api/remove", "application/json", strings.NewReader(`{"index":1,"title":"B"}`))
	if err != nil {
		t.Fatal(err)
	}
	var st state
	if err := json.NewDecoder(res.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK || len(st.Queue) != 1 || st.Queue[0].Title != "C" {
		t.Fatalf("remove via route: %d %+v", res.StatusCode, st)
	}

	// pausing with nothing playing should be reported, not panic
	res, err = client.Post(srv.URL+"/api/pause", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("pause while stopped: expected 400, got %d", res.StatusCode)
	}

	for _, path := range []string{"/missing", "/api/state/x"} {
		res, err = client.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("%s: expected 404, got %d", path, res.StatusCode)
		}
	}
}

func TestYoutubeThumbnail(t *testing.T) {
	const want = "https://i.ytimg.com/vi/dQw4w9WgXcQ/mqdefault.jpg"
	for link, expected := range map[string]string{
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ":          want,
		"https://youtube.com/watch?list=x&v=dQw4w9WgXcQ&t=10s": want,
		"https://m.youtube.com/watch?v=dQw4w9WgXcQ":            want,
		"https://music.youtube.com/watch?v=dQw4w9WgXcQ":        want,
		"https://youtu.be/dQw4w9WgXcQ?si=abc":                  want,
		"https://www.youtube.com/shorts/dQw4w9WgXcQ":           want,
		"https://www.youtube.com/embed/dQw4w9WgXcQ":            want,
		"https://www.youtube.com/watch?v=short":                "",
		"https://www.youtube.com/watch?v=dQw4w9WgXc/../x":      "",
		"https://www.youtube.com/@channel":                     "",
		"https://soundcloud.com/artist/track":                  "",
		"https://evilyoutube.com/watch?v=dQw4w9WgXcQ":          "",
	} {
		if got := youtubeThumbnail(link); got != expected {
			t.Errorf("youtubeThumbnail(%q) = %q, want %q", link, got, expected)
		}
	}
}

func TestFinishedQueueClearsNowPlaying(t *testing.T) {
	player := playback.NewPlayer(nil, &database.Config{})
	player.Playlist.Playlist = [][]string{{"a.mp3", "A"}}
	var s Server
	s.SetPlayer(player)
	if s.snapshot().Current == nil {
		t.Fatal("expected the track to be shown before it has played")
	}

	player.Skip(1) // skipping the last track finishes the queue
	if st := s.snapshot(); st.Current != nil || len(st.Queue) != 0 {
		t.Fatalf("expected nothing playing after skipping the last track, got %+v", st)
	}

	// Playing would need a Mumble connection, so this would panic if the finished track was replayed
	if rec := post(s.action(func(p *playback.Player, _ *http.Request) error { p.PlayCurrent(); return nil }), `{}`, "application/json"); rec.Code != http.StatusOK {
		t.Fatalf("play after finishing: expected 200, got %d", rec.Code)
	}
	if player.IsPlaying() || !player.IsFinished() {
		t.Fatal("play should do nothing once the queue has finished")
	}
}
