// Package webui serves a small web interface for controlling the player's queue and playback.
package webui

import (
	_ "embed"
	"encoding/json"
	"errors"
	"html/template"
	"log"
	"math"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/iotku/mumzic/playback"
)

//go:embed index.html
var indexHTML string
var indexTmpl = template.Must(template.New("index").Parse(indexHTML))

type Server struct {
	// Name is shown as the page title, it must be set before serving
	Name string
	// URL is the externally reachable address of the web UI, used for the links handed out by IssueLink
	URL string
	// TokenTTL is how long a link from IssueLink stays valid, defaults to DefaultTokenTTL
	TokenTTL time.Duration

	player atomic.Pointer[playback.Player]
	art    artCache
	tokens tokenStore
	clock  func() time.Time // for tests, defaults to time.Now
}

type track struct {
	Index  int    `json:"index"`
	Title  string `json:"title"`
	Type   string `json:"type"`          // link or file
	Source string `json:"source"`        // URL for links, file name (without its directory) for local files
	Art    string `json:"art,omitempty"` // URL of the cover art / thumbnail, unless the track is known to have none
}

type state struct {
	Connected bool    `json:"connected"`
	User      string  `json:"user"`   // Mumble user the session belongs to
	Status    string  `json:"status"` // playing, paused or stopped
	Radio     bool    `json:"radio"`
	Volume    int     `json:"volume"`
	Current   *track  `json:"current"`
	Queue     []track `json:"queue"`
}

// SetPlayer sets the player controlled by the web interface, until it is set the API reports as disconnected
func (s *Server) SetPlayer(player *playback.Player) {
	s.player.Store(player)
}

// ListenAndServe starts serving the web interface on addr, it only returns on error
func (s *Server) ListenAndServe(addr string) error {
	log.Println("Web UI listening on", addr)
	return http.ListenAndServe(addr, s.Handler()) // #nosec G114 -- small control panel, timeouts not required
}

// Handler returns the http.Handler serving the web interface and its API
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/state", s.handleState)
	mux.HandleFunc("/api/art/", s.handleArt)
	mux.HandleFunc("/api/queue", s.action(s.addTrack))
	mux.HandleFunc("/api/remove", s.action(s.removeTrack))
	mux.HandleFunc("/api/play", s.action(func(p *playback.Player, _ *http.Request) error {
		p.PlayCurrent()
		return nil
	}))
	mux.HandleFunc("/api/pause", s.action(func(p *playback.Player, _ *http.Request) error {
		return p.Pause()
	}))
	mux.HandleFunc("/api/stop", s.action(func(p *playback.Player, _ *http.Request) error {
		p.Stop(true)
		return nil
	}))
	mux.HandleFunc("/api/skip", s.action(func(p *playback.Player, _ *http.Request) error {
		return p.Next(1)
	}))

	return s.requireAuth(mux)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := indexTmpl.Execute(w, struct{ Name string }{s.name()}); err != nil {
		log.Println("[error] webui: rendering index:", err)
	}
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	writeJSON(w, http.StatusOK, s.snapshotFor(r))
}

// action wraps a player command as a POST endpoint which responds with the resulting state.
// Requiring a JSON content type forces a CORS preflight, so other sites can't trigger actions from a visitor's browser.
func (s *Server) action(fn func(*playback.Player, *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		if mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mediaType != "application/json" {
			writeError(w, http.StatusUnsupportedMediaType, errors.New("expected application/json"))
			return
		}

		player := s.player.Load()
		if player == nil {
			writeError(w, http.StatusServiceUnavailable, errors.New("not connected to mumble yet"))
			return
		}

		log.Printf("WEB (%s): %s", userFrom(r), strings.TrimPrefix(r.URL.Path, "/api/"))
		if err := fn(player, r); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, s.snapshotFor(r))
	}
}

func (s *Server) addTrack(player *playback.Player, r *http.Request) error {
	var req struct {
		Track string `json:"track"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return errors.New("invalid request")
	}
	req.Track = strings.TrimSpace(req.Track)
	if req.Track == "" {
		return errors.New("enter a track ID, URL or search")
	}

	_, err := player.Enqueue(req.Track)
	return err
}

func (s *Server) removeTrack(player *playback.Player, r *http.Request) error {
	var req struct {
		Index int    `json:"index"`
		Title string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return errors.New("invalid request")
	}

	return player.Playlist.Remove(req.Index, req.Title)
}

func (s *Server) snapshot() state {
	player := s.player.Load()
	if player == nil {
		return state{Status: "stopped", Queue: []track{}}
	}

	st := state{
		Connected: true,
		Status:    "stopped",
		Radio:     player.IsRadio,
		Volume:    int(math.Round(float64(player.Volume * 100))),
		Queue:     []track{},
	}
	if player.IsPlaying() {
		st.Status = "playing"
	} else if player.IsPaused() {
		st.Status = "paused"
	}

	entries, position := player.Playlist.Snapshot()
	for i := position; i < len(entries); i++ {
		t := track{Index: i, Title: entries[i][1], Type: "file", Source: filepath.Base(entries[i][0])}
		if strings.HasPrefix(entries[i][0], "http") {
			t.Type, t.Source = "link", entries[i][0]
		}
		if id := artID(entries[i][0]); !s.art.missing(id) {
			t.Art = "/api/art/" + id
		}
		if i == position {
			if !player.IsFinished() {
				st.Current = &t
			}
		} else {
			st.Queue = append(st.Queue, t)
		}
	}

	return st
}

func (s *Server) name() string {
	if s.Name == "" {
		return "mumzic"
	}
	return s.Name
}

// snapshotFor returns the current state as seen by the user making request r
func (s *Server) snapshotFor(r *http.Request) state {
	st := s.snapshot()
	st.User = userFrom(r)
	return st
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Println("[error] webui: writing response:", err)
	}
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
