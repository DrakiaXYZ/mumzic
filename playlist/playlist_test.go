package playlist

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func newTestList() *List {
	return &List{
		Playlist: [][]string{{"a.mp3", "A"}, {"b.mp3", "B"}, {"c.mp3", "C"}, {"d.mp3", "D"}},
		Position: 1,
	}
}

func TestRemove(t *testing.T) {
	list := newTestList()
	if err := list.Remove(2, "C"); err != nil {
		t.Fatalf("Remove upcoming track: %v", err)
	}

	entries, position := list.Snapshot()
	if position != 1 || len(entries) != 3 || entries[2][1] != "D" {
		t.Fatalf("unexpected playlist after remove: %v (position %d)", entries, position)
	}
}

func TestRemoveRejectsCurrentAndHistory(t *testing.T) {
	list := newTestList()
	for _, index := range []int{-1, 0, 1, 4} {
		if err := list.Remove(index, list.Playlist[min(max(index, 0), 3)][1]); err == nil {
			t.Errorf("Remove(%d) should fail", index)
		}
	}
	if list.Size() != 4 {
		t.Fatalf("playlist should be unchanged, got %v", list.Playlist)
	}
}

func TestRemoveRejectsStaleTitle(t *testing.T) {
	list := newTestList()
	if err := list.Remove(2, "D"); err == nil {
		t.Fatal("Remove with mismatched title should fail")
	}
}

func TestSnapshotIsACopy(t *testing.T) {
	list := newTestList()
	entries, _ := list.Snapshot()
	entries[0] = []string{"x", "X"}
	if list.Playlist[0][1] != "A" {
		t.Fatal("modifying the snapshot changed the playlist")
	}
}

// Run with -race to check reads and writes are synchronised
func TestConcurrentAccess(t *testing.T) {
	list := newTestList()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(3)
		go func() { defer wg.Done(); list.pAdd("e.mp3", "E") }()
		go func() { defer wg.Done(); list.Snapshot(); list.Count(); list.HasNext() }()
		go func() { defer wg.Done(); list.Next() }()
	}
	wg.Wait()
}

func TestSave(t *testing.T) {
	dir := t.TempDir()
	orig, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	list := newTestList() // positioned on B, with C and D after it
	list.Save("host", true)
	loaded := &List{}
	loaded.Load("host")
	if loaded.Size() != 3 || loaded.GetCurrentHuman() != "B" {
		t.Fatalf("expected B, C, D to be saved, got %v", loaded.Playlist)
	}

	list.Save("host", false)
	loaded = &List{}
	loaded.Load("host")
	if loaded.Size() != 2 || loaded.GetCurrentHuman() != "C" {
		t.Fatalf("expected C, D to be saved without the current track, got %v", loaded.Playlist)
	}

	// Nothing left to play removes the file, so an old playlist isn't loaded next time
	finished := &List{Playlist: [][]string{{"a.mp3", "A"}}}
	finished.Save("host", false)
	if _, err := os.Stat(filepath.Join(Directory, "host")); !os.IsNotExist(err) {
		t.Fatalf("expected the playlist file to be removed, got %v", err)
	}
}
