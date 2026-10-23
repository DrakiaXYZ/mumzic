package youtubedl

import (
	"bufio"
	_ "embed"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

//go:embed whitelist-test.txt
var whitelistExample string

func TestIsWhiteListedUrl(t *testing.T) {
	scanURLPrefixes(bufio.NewScanner(strings.NewReader(whitelistExample)))
	tests := []struct {
		url      string
		expected bool
	}{
		{"https://www.youtube.com/watch?v=dQw4w9WgXcQ", true},
		{"https://youtu.be/dQw4w9WgXcQ", true},
		{"https://notyoutube.com/watch?v=qwqwdqwd", false},
	}

	for _, tt := range tests {
		got := IsWhiteListedURL(tt.url)
		if got != tt.expected {
			t.Errorf("IsWhiteListedURL(%q) = %v, want %v", tt.url, got, tt.expected)
		}
	}
}

func useWhitelistFile(t *testing.T, path string) {
	orig := whitelistFile
	whitelistFile = path
	t.Cleanup(func() { whitelistFile = orig })
}

func TestMissingWhitelistUsesDefaults(t *testing.T) {
	useWhitelistFile(t, filepath.Join(t.TempDir(), "whitelist.txt"))
	if err := LoadAllowedURLPrefixesFromFile(); err != nil {
		t.Fatal(err)
	}
	if !IsWhiteListedURL("https://www.youtube.com/watch?v=RXclfM3i-Rw") || IsWhiteListedURL("https://notyoutube.com/watch?v=x") {
		t.Fatalf("defaults not applied, got %v", AllowedURLPrefixes)
	}

	// later reloads reuse AllowedURLPrefixes' backing array, which must not change the defaults
	scanURLPrefixes(bufio.NewScanner(strings.NewReader("https://example.com/\n")))
	if defaultURLPrefixes[0] != "https://www.youtube.com/" {
		t.Fatalf("defaults were modified: %v", defaultURLPrefixes)
	}
}

func TestEmptyWhitelistAllowsNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "whitelist.txt")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	useWhitelistFile(t, path)
	if err := LoadAllowedURLPrefixesFromFile(); err != nil {
		t.Fatal(err)
	}
	if IsWhiteListedURL("https://www.youtube.com/watch?v=RXclfM3i-Rw") {
		t.Fatal("an existing empty whitelist should not fall back to the defaults")
	}
}

func TestDefaultsMatchExampleFile(t *testing.T) {
	data, err := os.ReadFile("../whitelist-example.txt")
	if err != nil {
		t.Fatal(err)
	}
	var example []string
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			example = append(example, line)
		}
	}
	if !slices.Equal(example, defaultURLPrefixes) {
		t.Fatalf("whitelist-example.txt %v and defaultURLPrefixes %v differ", example, defaultURLPrefixes)
	}
}
