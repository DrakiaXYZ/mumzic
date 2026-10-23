package commands

import (
	"testing"
	"time"

	"github.com/iotku/mumzic/database"
)

var Config database.Config

func init() {
	Config = *database.NewConfig("") // TODO: cleanup created database on disk
}

func TestGetCommandAndArg(t *testing.T) {
	// message with nickname as prefix
	got1, got2 := getCommandAndArg("MusicButt command argument", "MusicButt", &Config)
	if got1 != "command" || got2 != "argument" {
		t.Errorf("got %q %q  wanted %q %q", got1, got2, "command", "argument")
	}

	got1, got2 = getCommandAndArg("MusicButt command", "MusicButt", &Config)
	if got1 != "command" || got2 != "" {
		t.Errorf("got %q %q, wanted %q %q", got1, got2, "command", "")
	}

	got1, got2 = getCommandAndArg("MusicButt", "MusicButt", &Config)
	if got1 != "" || got2 != "" {
		t.Errorf("got %q %q, wanted %q %q", got1, got2, "", "")
	}

	got1, got2 = getCommandAndArg("!command argument", "MusicButt", &Config)
	if got1 != "command" || got2 != "argument" {
		t.Errorf("got %q %q, wanted %q %q", got1, got2, "command", "argument")
	}
	got1, got2 = getCommandAndArg("!command", "MusicButt", &Config)
	if got1 != "command" || got2 != "" {
		t.Errorf("got %q %q, wanted %q %q", got1, got2, "command", "")
	}

	got1, got2 = getCommandAndArg("!", "MusicButt", &Config)
	if got1 != "" || got2 != "" {
		t.Errorf("got %q %q, wanted %q %q", got1, got2, "", "")
	}

	got1, got2 = getCommandAndArg("command argument", "MusicButt", &Config)
	if got1 != "command" || got2 != "argument" {
		t.Errorf("got %q %q, wanted %q %q", got1, got2, "command", "argument")
	}

	got1, got2 = getCommandAndArg("command", "MusicButt", &Config)
	if got1 != "command" || got2 != "" {
		t.Errorf("got %q %q, wanted %q %q", got1, got2, "command", "")
	}

	got1, got2 = getCommandAndArg("", "MusicButt", &Config)
	if got1 != "" || got2 != "" {
		t.Errorf("got %q %q, wanted %q %q", got1, got2, "", "")
	}
}

func TestPlayNowAlias(t *testing.T) {
	// Test that "p" command is properly parsed as "playnow"
	got1, got2 := getCommandAndArg("!p song name", "MusicButt", &Config)
	if got1 != "p" || got2 != "song name" {
		t.Errorf("got %q %q, wanted %q %q", got1, got2, "p", "song name")
	}

	got1, got2 = getCommandAndArg("MusicButt p song name", "MusicButt", &Config)
	if got1 != "p" || got2 != "song name" {
		t.Errorf("got %q %q, wanted %q %q", got1, got2, "p", "song name")
	}
}

func TestFormatDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		12*time.Hour - time.Millisecond: "12 hours",
		time.Hour:                       "1 hour",
		90 * time.Minute:                "1 hour 30 minutes",
		time.Minute:                     "1 minute",
		45 * time.Minute:                "45 minutes",
		0:                               "0 minutes",
	} {
		if got := formatDuration(d); got != want {
			t.Errorf("formatDuration(%v) = %q, want %q", d, got, want)
		}
	}
}
