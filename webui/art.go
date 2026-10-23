package webui

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/jpeg"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/iotku/mumzic/messages"
	"github.com/iotku/mumzic/youtubedl"
	"github.com/nfnt/resize"
)

const (
	maxArtEntries = 256     // art cache entries kept before pruning tracks that left the playlist
	maxArtBytes   = 2 << 20 // largest thumbnail accepted from YouTube
	artSize       = 320     // longest side of art generated from local files and yt-dlp
)

var artClient = &http.Client{Timeout: 10 * time.Second}

// artCache keeps cover art / thumbnails in memory, keyed by artID
type artCache struct {
	mu      sync.Mutex
	entries map[string]*artEntry
}

type artEntry struct {
	ready chan struct{} // closed once data has been loaded
	data  []byte        // nil if the track has no art
}

// artID identifies a playlist path without exposing it
func artID(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:12])
}

// missing reports whether art for id has already been looked up and none was found
func (c *artCache) missing(id string) bool {
	c.mu.Lock()
	e := c.entries[id]
	c.mu.Unlock()
	if e == nil {
		return false
	}
	select {
	case <-e.ready:
		return e.data == nil
	default:
		return false
	}
}

// get returns the art for path, loading it on first use. Concurrent callers for the same id share one load.
func (c *artCache) get(id, path string) []byte {
	c.mu.Lock()
	if c.entries == nil {
		c.entries = make(map[string]*artEntry)
	}
	e, ok := c.entries[id]
	if !ok {
		e = &artEntry{ready: make(chan struct{})}
		c.entries[id] = e
	}
	c.mu.Unlock()

	if ok {
		<-e.ready
		return e.data
	}

	defer close(e.ready)
	data, err := loadArt(path)
	if err != nil {
		log.Println("webui: no art for", path+":", err)
	}
	e.data = data
	return data
}

// prune drops entries for tracks no longer in the playlist once the cache grows past maxArtEntries
func (c *artCache) prune(keep map[string]bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) <= maxArtEntries {
		return
	}
	for id := range c.entries {
		if !keep[id] {
			delete(c.entries, id)
		}
	}
}

func (s *Server) handleArt(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/art/")
	player := s.player.Load()
	if r.Method != http.MethodGet || player == nil {
		http.NotFound(w, r)
		return
	}

	// Only serve art for tracks in the playlist, so the id can't be used to read arbitrary files
	entries, _ := player.Playlist.Snapshot()
	var path string
	keep := make(map[string]bool, len(entries))
	for _, entry := range entries {
		entryID := artID(entry[0])
		keep[entryID] = true
		if entryID == id {
			path = entry[0]
		}
	}
	if path == "" {
		http.NotFound(w, r)
		return
	}

	data := s.art.get(id, path)
	s.art.prune(keep)
	if data == nil {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", http.DetectContentType(data))
	w.Header().Set("Cache-Control", "private, max-age=86400")
	_, _ = w.Write(data)
}

// loadArt finds the cover art or thumbnail for a playlist path
func loadArt(path string) ([]byte, error) {
	if strings.HasPrefix(path, "http") {
		if thumb := youtubeThumbnail(path); thumb != "" {
			return fetchImage(thumb)
		}
		img, err := youtubedl.GetYtDLThumbnail(path)
		if err != nil {
			return nil, err
		}
		return encodeArt(img)
	}

	img, err := messages.GetEmbdedImage(path)
	if err != nil {
		img, err = messages.DecodeImage(messages.FindCoverArtPath(path))
	}
	if err != nil {
		return nil, err
	}
	return encodeArt(img)
}

func encodeArt(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	err := jpeg.Encode(&buf, resize.Thumbnail(artSize, artSize, img, resize.Bilinear), &jpeg.Options{Quality: 85})
	return buf.Bytes(), err
}

func fetchImage(url string) ([]byte, error) {
	resp, err := artClient.Get(url) // #nosec G107 -- URL is built from a validated YouTube video ID
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("thumbnail request failed: " + resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxArtBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxArtBytes || !strings.HasPrefix(http.DetectContentType(data), "image/") {
		return nil, errors.New("thumbnail response was not a usable image")
	}
	return data, nil
}

var youtubeID = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

// youtubeThumbnail returns the thumbnail URL for a YouTube video link, or "" if link isn't one
func youtubeThumbnail(link string) string {
	u, err := url.Parse(link)
	if err != nil {
		return ""
	}

	var id string
	switch strings.TrimPrefix(u.Hostname(), "www.") {
	case "youtu.be":
		id = strings.Trim(u.Path, "/")
	case "youtube.com", "m.youtube.com", "music.youtube.com":
		if u.Path == "/watch" {
			id = u.Query().Get("v")
		} else if parts := strings.Split(strings.Trim(u.Path, "/"), "/"); len(parts) == 2 {
			switch parts[0] {
			case "shorts", "embed", "live":
				id = parts[1]
			}
		}
	}

	if !youtubeID.MatchString(id) {
		return ""
	}
	return "https://i.ytimg.com/vi/" + id + "/mqdefault.jpg"
}
