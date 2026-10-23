package playlist

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/iotku/mumzic/helper"
	"github.com/iotku/mumzic/search"
	"github.com/iotku/mumzic/youtubedl"
)

const Directory = "playlists/" // Directory for saving/loading playlists

// List contains a 2D slice of "Human Friendly" titles and raw paths as well as its position along the playlist
type List struct {
	Playlist [][]string
	Position int

	mu sync.RWMutex
}

// Save writes the remaining tracks to the playlist file for hostname, starting at the current track if
// includeCurrent is true and otherwise the one after it. With nothing remaining the file is removed.
func (list *List) Save(hostname string, includeCurrent bool) {
	list.mu.RLock()
	defer list.mu.RUnlock()
	start := list.Position
	if !includeCurrent {
		start++
	}
	var saveList [][]string
	for i := start; i < len(list.Playlist); i++ {
		saveList = append(saveList, list.Playlist[i])
	}

	if saveList == nil {
		// Remove any playlist from a previous session, otherwise it would be loaded again
		if err := os.Remove(Directory + hostname); err != nil && !os.IsNotExist(err) {
			log.Println("Failed to remove old playlist file:", err)
		}
		return
	}

	output, err := json.Marshal(saveList)
	if err != nil {
		log.Fatalln("failed to marshal playlist json")
	}

	if _, err := os.Stat(Directory); os.IsNotExist(err) {
		if err = os.Mkdir(Directory, 0700); err != nil {
			log.Fatalln("failed to create"+Directory+"directory:", err)
		}
	}
	if fileInfo, _ := os.Stat(Directory); !fileInfo.IsDir() {
		log.Fatalln("Playlist path, ", Directory, "is not a directory.")
	}

	if err := os.WriteFile(Directory+hostname, output, 0600); err != nil {
		log.Fatalln("Failed to write playlist file:", err.Error())
	}
}

func (list *List) Load(hostname string) {
	if _, err := os.Open(Directory + hostname); //#nosec G304 - hostname considered rusted source
	err == nil {
		file, err := os.ReadFile(Directory + hostname) //#nosec G304 - hostname considered rusted source
		if err != nil {
			log.Fatalln(err.Error())
		}
		var pList [][]string
		err = json.Unmarshal(file, &pList)
		if err != nil {
			log.Fatalln("json Unmarshal failed", err.Error())
		}
		log.Println("Applying previous playlist file.")
		list.mu.Lock()
		list.Playlist = pList
		list.mu.Unlock()
	}
}

// GetCurrentPath gets the raw path for the current item in the playlist
func (list *List) GetCurrentPath() string {
	list.mu.RLock()
	defer list.mu.RUnlock()
	return list.Playlist[list.Position][0]
}

// GetCurrentHuman gets the "Human Friendly" title for the current item in the playlist
func (list *List) GetCurrentHuman() string {
	list.mu.RLock()
	defer list.mu.RUnlock()
	return list.Playlist[list.Position][1]
}

func (list *List) GetNextHuman() string {
	list.mu.RLock()
	defer list.mu.RUnlock()
	if len(list.Playlist) == 0 {
		return ""
	} else if len(list.Playlist) == list.Position+1 {
		return list.Playlist[list.Position][1]
	}
	return list.Playlist[list.Position+1][1]
}

// GetList returns a list of items from the current to the end of the playlist
// Note: Older items aren't removed immediately however aren't guaranteed to remain forever.
func (list *List) GetList(max int) []string {
	list.mu.RLock()
	defer list.mu.RUnlock()
	var trackList []string
	for i := list.Position; i < list.Position+max; i++ {
		if list.Position+max > len(list.Playlist) {
			return trackList
		}
		trackList = append(trackList, list.Playlist[i][1])
	}

	return trackList
}

// HasNext returns true if there is another item remaining in the playlist
func (list *List) HasNext() bool {
	list.mu.RLock()
	defer list.mu.RUnlock()
	return list.hasNext()
}

func (list *List) hasNext() bool {
	return len(list.Playlist) > list.Position+1
}

// Next shifts the playlist position forward by one if there is at least one more item in the playlist remaining
func (list *List) Next() string {
	list.mu.Lock()
	defer list.mu.Unlock()
	if !list.hasNext() {
		return ""
	}
	list.Position++
	return list.Playlist[list.Position][0]
}

// Skip moves the position by amount, generally this should be called by a playback.Player
func (list *List) Skip(amount int) string {
	list.mu.Lock()
	defer list.mu.Unlock()
	if len(list.Playlist)+amount < 0 || !list.hasNext() {
		return ""
	}

	if list.Position+amount >= len(list.Playlist) {
		amount = 1 // only skip one track
	}
	list.Position += amount
	return list.Playlist[list.Position][0]
}

// Size returns an int of how many items the playlist contains
func (list *List) Size() int {
	list.mu.RLock()
	defer list.mu.RUnlock()
	return len(list.Playlist)
}

// IsEmpty returns whether the playlist contains any elements.
func (list *List) IsEmpty() bool {
	return list.Size() == 0
}

// Snapshot returns a copy of the playlist entries along with the current position
func (list *List) Snapshot() (entries [][]string, position int) {
	list.mu.RLock()
	defer list.mu.RUnlock()
	entries = make([][]string, len(list.Playlist))
	copy(entries, list.Playlist)
	return entries, list.Position
}

// Remove deletes the upcoming entry at index from the playlist. human must match the entry's title, which guards
// against removing the wrong track when the playlist has changed since the caller last looked at it.
func (list *List) Remove(index int, human string) error {
	list.mu.Lock()
	defer list.mu.Unlock()
	if index <= list.Position || index >= len(list.Playlist) {
		return errors.New("only upcoming tracks can be removed")
	}
	if list.Playlist[index][1] != human {
		return errors.New("playlist changed, try again")
	}

	list.Playlist = append(list.Playlist[:index:index], list.Playlist[index+1:]...)
	return nil
}

// AddToQueue ads either a filesystem ID or internet URL onto the Playlist queue. On success, it returns a human friendly
// title and err is nil. On failure (ID not found or not whitelisted URL) returns empty string "" and a respective error.
func (list *List) AddToQueue(path string) (string, error) {
	human, path, err := getHumanAndPath(path) // NOTE: we check for whitelist urls here
	if err != nil {
		return "", err
	} else if path == "" {
		return "", errors.New("nothing added. (Invalid ID?)")
	}

	if strings.HasPrefix(path, "http") {
		list.queueYT(path, human)
	} else {
		list.pAdd(path, human)
	}

	return human, nil
}

// AddNext adds a song to play directly after the current song in the Playlist
func (list *List) AddNext(arg string) error {
	human, path, err := getHumanAndPath(arg)
	if err != nil {
		return err
	}

	list.mu.Lock()
	defer list.mu.Unlock()
	if len(list.Playlist)-list.Position <= 1 || !list.hasNext() {
		list.Playlist = append(list.Playlist, []string{path, human})
		return nil
	}

	var newList [][]string
	newList = append(newList, list.Playlist[list.Position])
	newList = append(newList, []string{path, human})
	newList = append(newList, list.Playlist[list.Position+1:]...)

	// Copy New Playlist
	list.Playlist = newList
	list.Position = 0

	return nil
}

func getHumanAndPath(arg string) (human, path string, err error) {
	path = helper.StripHTMLTags(arg) // TODO: Might be redundant now that we Strip message beforehand
	if strings.HasPrefix(path, "http") && youtubedl.IsWhiteListedURL(path) == true {
		human, err = youtubedl.GetYtDLTitle(path)
		return
	} else if strings.HasPrefix(path, "http") {
		return "", "", errors.New("URL Doesn't meet whitelist")
	}

	// Try to parse as ID first
	if id, parseErr := strconv.Atoi(path); parseErr == nil {
		human, path = search.GetTrackById(id)
		if path != "" {
			return
		}
	}

	// If not a valid ID, try YouTube search
	path, err = youtubedl.SearchYouTube(arg)
	if err == nil {
		human, _ = youtubedl.GetYtDLTitle(path)
		return
	}

	return "", "", errors.New("id not found and search failed")
}

func (list *List) pAdd(path, human string) {
	list.mu.Lock()
	defer list.mu.Unlock()
	list.Playlist = append(list.Playlist, []string{path, human})
}

func (list *List) QueueID(trackID int) (human string) {
	human, path := search.GetTrackById(trackID)
	if path == "" {
		return ""
	}
	list.pAdd(path, human)

	return human
}

func (list *List) queueYT(url, human string) bool {
	list.pAdd(url, human)
	return true // TODO Check with API if video is valid for youtube links
}

// Count is the amount of songs enqueued on the playlist
func (list *List) Count() int {
	list.mu.RLock()
	defer list.mu.RUnlock()
	return len(list.Playlist) - list.Position
}
