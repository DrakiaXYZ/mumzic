package webui

import (
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/iotku/mumzic/playback"
	"github.com/iotku/mumzic/playlist"
)

func newArtServer(t *testing.T) (*Server, string, string) {
	dir := t.TempDir()
	withArt := filepath.Join(dir, "album", "track.mp3")
	withoutArt := filepath.Join(dir, "loose", "track.mp3")
	for _, p := range []string{withArt, withoutArt} {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("not really audio"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	f, err := os.Create(filepath.Join(dir, "album", "cover.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, image.NewRGBA(image.Rect(0, 0, 600, 600))); err != nil {
		t.Fatal(err)
	}
	f.Close()

	var s Server
	s.SetPlayer(&playback.Player{Playlist: playlist.List{
		Playlist: [][]string{{withArt, "With Art"}, {withoutArt, "Without Art"}},
	}})
	return &s, withArt, withoutArt
}

func getArt(t *testing.T, s *Server, id string) *httptest.ResponseRecorder {
	return serve(s, http.MethodGet, "/api/art/"+id, login(t, s, "alice"))
}

func TestArtFromCoverFile(t *testing.T) {
	s, withArt, _ := newArtServer(t)
	rec := getArt(t, s, artID(withArt))
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("expected jpeg art, got %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}

	img, _, err := image.Decode(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != artSize || b.Dy() != artSize {
		t.Fatalf("art should be scaled to %dpx, got %v", artSize, b)
	}
}

func TestArtMissingIsRemembered(t *testing.T) {
	s, _, withoutArt := newArtServer(t)
	id := artID(withoutArt)
	if s.snapshot().Queue[0].Art == "" {
		t.Fatal("art URL should be offered before the track has been looked up")
	}
	if rec := getArt(t, s, id); rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for track without art, got %d", rec.Code)
	}
	if s.snapshot().Queue[0].Art != "" {
		t.Fatal("art URL should be omitted once the track is known to have none")
	}
}

func TestArtOnlyForPlaylistTracks(t *testing.T) {
	s, _, _ := newArtServer(t)
	for _, id := range []string{artID("/etc/passwd"), "", "../../etc/passwd"} {
		if rec := getArt(t, s, id); rec.Code == http.StatusOK {
			t.Errorf("art %q should not be served", id)
		}
	}
}
