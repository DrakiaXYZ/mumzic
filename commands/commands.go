package commands

import (
	"fmt"
	"html"
	"log"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/iotku/mumzic/database"
	"github.com/iotku/mumzic/helper"
	"github.com/iotku/mumzic/messages"
	"github.com/iotku/mumzic/playback"
	"github.com/iotku/mumzic/search"
	"github.com/iotku/mumzic/youtubedl"
)

// WebLink creates a personal web UI link for user, it is nil when the web UI is disabled
var WebLink func(user string) (link string, expires time.Time, err error)

func IsCommand(message string, isPrivate bool, username string, config *database.Config) bool {
	message = strings.TrimSpace(message)
	return strings.HasPrefix(message, config.Prefix) || strings.HasPrefix(message, username) || isPrivate
}

func CommandDispatch(player *playback.Player, msg string, isPrivate bool, sender string) {
	helper.DebugPrintln("IsPlaying:", player.IsPlaying(), "Len:", len(player.Playlist.Playlist), "Count", player.Playlist.Count(), "PlPos:", player.Playlist.Position, "HasNext:", player.Playlist.HasNext())
	command, arg := getCommandAndArg(msg, player.Client.Self.Name, player.Config)

	switch command {
	case "play", "add":
		play(arg, sender, isPrivate, player)
	case "playnow":
		playNow(player, sender, isPrivate, arg)
	case "playnext", "addnext":
		err := player.Playlist.AddNext(arg)
		if err != nil {
			helper.MsgDispatch(player.Client, isPrivate, sender, "Not Added: "+err.Error())
			return
		}

		helper.MsgDispatch(player.Client, isPrivate, sender, "Added: "+player.Playlist.GetNextHuman())
	case "stop":
		player.Stop(true)
	case "pause":
		if err := player.Pause(); err != nil {
			helper.MsgDispatch(player.Client, isPrivate, sender, "Not Paused: "+err.Error())
		}
	case "skip", "next":
		skip(player, sender, isPrivate, arg)
	case "vol", "volume":
		vol(player, sender, isPrivate, arg)
	case "list":
		list(player, sender, isPrivate)
	case "retarget":
		player.TargetUsers()
	case "target":
		player.AddTarget(sender)
	case "untarget":
		player.RemoveTarget(sender)
	case "help":
		helper.MsgDispatch(player.Client, isPrivate, sender,
			"https://github.com/iotku/mumzic/blob/master/USAGE.md")
	case "rand", "random":
		rand(player, sender, isPrivate, arg)
	case "radio":
		toggleRadio(player, sender, isPrivate)
	case "search", "find":
		find(player, sender, isPrivate, arg)
	case "saveconf":
		player.Config.Channel = player.Client.Self.Channel.Name
		player.Config.Save()
	case "reload":
		helper.LogErr(youtubedl.LoadAllowedURLPrefixesFromFile(), "YTDL Reload")
	case "more":
		helper.MsgDispatch(player.Client, isPrivate, sender, messages.GetMoreTable(sender, player.Config.MaxLines))
	case "less":
		helper.MsgDispatch(player.Client, isPrivate, sender, messages.GetLessTable(sender, player.Config.MaxLines))
	case "summon":
		joinUserChannel(player, sender)
	case "web":
		webLink(player, sender)
	case "uinfo":
		helper.MsgDispatch(player.Client, isPrivate, sender, player.Client.Self.Hash)
	}
}

func skip(player *playback.Player, sender string, isPrivate bool, arg string) {
	value, err := strconv.Atoi(arg)
	if err != nil {
		value = 1
	}

	if err := player.Next(value); err != nil {
		helper.MsgDispatch(player.Client, isPrivate, sender, "Not Skipped: "+err.Error())
	}
}

// webLink privately sends sender a link to the web UI, even when asked in the channel
func webLink(player *playback.Player, sender string) {
	if WebLink == nil {
		helper.UserMsg(player.Client, sender, "The web UI is not enabled.")
		return
	}

	link, expires, err := WebLink(sender)
	if err != nil {
		log.Println("[error] creating web UI link:", err)
		helper.UserMsg(player.Client, sender, "Could not create a web UI link, try again.")
		return
	}

	helper.UserMsg(player.Client, sender, fmt.Sprintf(
		`<br><a href="%s">Open the web UI</a><br>This link is just for you and is valid for %s, don't share it.`,
		html.EscapeString(link), formatDuration(time.Until(expires))))
}

// formatDuration formats d for chat, e.g. "12 hours" or "1 hour 30 minutes"
func formatDuration(d time.Duration) string {
	d = d.Round(time.Minute)
	hours, minutes := int(d.Hours()), int(d.Minutes())%60

	var parts []string
	if hours > 0 {
		parts = append(parts, plural(hours, "hour"))
	}
	if minutes > 0 || hours == 0 {
		parts = append(parts, plural(minutes, "minute"))
	}
	return strings.Join(parts, " ")
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return strconv.Itoa(n) + " " + unit + "s"
}

func playNow(player *playback.Player, sender string, isPrivate bool, track string) {
	err := player.PlayNow(track)
	if err != nil {
		helper.MsgDispatch(player.Client, isPrivate, sender, "Not Added: "+err.Error())
		return
	}
}

func toggleRadio(player *playback.Player, sender string, isPrivate bool) {
	if !player.IsRadio {
		helper.MsgDispatch(player.Client, isPrivate, sender, "Enabled Radio Mode, Shuffling forever.")
		player.IsRadio = true
		if !player.IsPlaying() {
			playNow(player, sender, isPrivate, strconv.Itoa(search.GetRandomTrackIDs(1)[0]))
		}
	} else {
		player.IsRadio = false
		helper.MsgDispatch(player.Client, isPrivate, sender, "Disabled Radio Mode.")
	}
}

func joinUserChannel(player *playback.Player, sender string) {
	client := player.Client
	user := client.Users.Find(sender)
	if user == nil || user.Channel == nil {
		return
	}

	chanTarget := user.Channel

	client.Self.Move(chanTarget)
	if client.Self.Channel == chanTarget {
		player.TargetUsers()
	}
}

func getCommandAndArg(msg, name string, conf *database.Config) (command, arg string) {
	var skipUserName = 0
	msg = strings.TrimSpace(msg)
	if strings.HasPrefix(msg, conf.Prefix) {
		msg = msg[len(conf.Prefix):]
	} else if strings.HasPrefix(msg, name) {
		skipUserName = 1
	}

	split := strings.Split(msg, " ")
	for i := 1 + skipUserName; i < len(split); i++ {
		arg += split[i] + " "
	}
	if strings.HasPrefix(msg, name) && len(split) == 1 {
		return "", ""
	}
	return strings.ToLower(split[skipUserName]), strings.TrimSpace(arg)
}

func play(id string, sender string, isPrivate bool, player *playback.Player) {
	if id == "" {
		player.PlayCurrent() // returns if playing already
		return
	}

	wasRadio := player.IsRadio
	human, err := player.Enqueue(id)
	if err != nil {
		helper.MsgDispatch(player.Client, isPrivate, sender, err.Error())
		return
	}
	helper.MsgDispatch(player.Client, isPrivate, sender, "Queued: "+human)

	if wasRadio {
		helper.MsgDispatch(player.Client, isPrivate, sender, "Disabled Radio Mode.")
	}
}

func vol(player *playback.Player, sender string, isPrivate bool, arg string) {
	if arg != "" {
		argInt, err := strconv.Atoi(arg)
		if err != nil || argInt <= 0 || argInt > 100 {
			helper.MsgDispatch(player.Client, isPrivate, sender, "Invalid Volume: Valid range <b>[1-100]</b>")
			return
		}
		player.SetVolume(0.01 * float32(argInt))
	}
	helper.MsgDispatch(player.Client, isPrivate, sender, "Current Volume: "+fmt.Sprintf("%d", int(math.Ceil(float64(player.Volume*100)))))
}

func list(player *playback.Player, sender string, isPrivate bool) {
	playlist := player.Playlist.GetList(player.Playlist.Count())

	output := messages.MakeTable("Playlist", "# Track Name")
	messages.SaveMoreRows(sender, player.Config.MaxLines, playlist, output)
	output.AddRow("---")
	output.AddRow(strconv.Itoa(player.Playlist.Count()) + " Track(s) queued.")

	helper.MsgDispatch(player.Client, isPrivate, sender, output.String())
}

func find(player *playback.Player, sender string, isPrivate bool, arg string) {
	results := search.FindArtistTitle(arg)

	output := messages.MakeTable("Search Results")
	messages.SaveMoreRows(sender, player.Config.MaxLines, results, output)

	helper.MsgDispatch(player.Client, isPrivate, sender, output.String())
}

func rand(player *playback.Player, sender string, isPrivate bool, arg string) {
	value, err := strconv.Atoi(arg)
	if err != nil || value < 1 {
		value = 1
	} else if value > player.Config.MaxLines {
		value = player.Config.MaxLines
	}

	plistOrigSize := player.Playlist.Size()
	hadNext := player.Playlist.HasNext()

	output := messages.MakeTable("Randomly Added")
	idList := search.GetRandomTrackIDs(value)
	for _, v := range idList {
		human := player.Playlist.QueueID(v)
		if human != "" {
			output.AddRow("Added: <b>" + human + "</b>")
		} else {
			output.AddRow("Error: <b>" + "failed to add ID#" + strconv.Itoa(v) + "</b>")
		}
	}
	helper.MsgDispatch(player.Client, isPrivate, sender, output.String())

	if !player.IsPlaying() && plistOrigSize == 0 {
		player.PlayCurrent()
	} else if !player.IsPlaying() && !hadNext {
		player.Skip(1)
	}
}
