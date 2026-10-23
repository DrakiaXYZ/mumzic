package main

import (
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/iotku/mumzic/commands"
	"github.com/iotku/mumzic/database"
	"github.com/iotku/mumzic/helper"
	"github.com/iotku/mumzic/playback"
	"github.com/iotku/mumzic/webui"
	_ "github.com/mattn/go-sqlite3"
	"layeh.com/gumble/gumble"
	"layeh.com/gumble/gumbleutil"
)

func main() {
	var channelPlayer *playback.Player
	var bConfig *database.Config
	var hostname, username string

	webAddr := flag.String("web", "", "address to serve the web UI on, e.g. :8080 (disabled if empty)")
	webURL := flag.String("weburl", "", "external URL of the web UI used for !web links, e.g. https://music.example.com (default http://localhost:<port>)")
	webTokenTTL := flag.Duration("webtokenttl", webui.DefaultTokenTTL, "how long links from !web stay valid")
	var webServer webui.Server
	var startWeb sync.Once

	cleanUp := func() {
		if bConfig != nil {
			bConfig.Channel = channelPlayer.Client.Self.Channel.Name
			bConfig.Save()
		}

		if channelPlayer != nil {
			channelPlayer.SavePlaylist(bConfig.Hostname)
		}

		database.Close(database.ConfigDB)
		if database.MediaDB != nil {
			database.Close(database.MediaDB)
		}
	}

	// Capture shutdown signal (ctrl+c)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigCh
		log.Println("Received shutdown signal")
		cleanUp()
		os.Exit(0)
	}()

	gumbleutil.Main(gumbleutil.AutoBitrate, gumbleutil.Listener{
		Connect: func(e *gumble.ConnectEvent) {
			hostname = getValueFromFlag(flag.Lookup("server"))
			username = getValueFromFlag(flag.Lookup("username"))
			bConfig = database.NewConfig(hostname)
			if e.Client.Channels.Find(bConfig.Channel) != nil {
				e.Client.Self.Move(e.Client.Channels.Find(bConfig.Channel))
			}

			channelPlayer = playback.NewPlayer(e.Client, bConfig)
			channelPlayer.Playlist.Load(bConfig.Hostname)
			log.Printf("audio player loaded! (%d files)\n", database.GetMaxID())

			webServer.SetPlayer(channelPlayer)
			if *webAddr != "" {
				startWeb.Do(func() {
					webServer.Name = username
					webServer.URL = *webURL
					if webServer.URL == "" {
						webServer.URL = defaultWebURL(*webAddr)
						log.Println("No -weburl set, !web links will point to", webServer.URL)
					}
					webServer.TokenTTL = *webTokenTTL
					commands.WebLink = webServer.IssueLink
					go func() { log.Println("Web UI stopped:", webServer.ListenAndServe(*webAddr)) }()
				})
			}
		},
		TextMessage: func(e *gumble.TextMessageEvent) {
			if e.Sender == nil {
				return
			}

			isPrivate := len(e.TextMessage.Channels) == 0 // If no channels, is private message
			logMessage(e, isPrivate)

			strippedMessage := helper.StripHTMLTags(e.Message)
			if commands.IsCommand(strippedMessage, isPrivate, username, bConfig) {
				go func() {
					commands.CommandDispatch(channelPlayer, strippedMessage, isPrivate, e.Sender.Name)
				}()
			}
		},
		ChannelChange: func(e *gumble.ChannelChangeEvent) {
			if bConfig != nil && !e.Channel.IsRoot() {
				bConfig.Channel = e.Channel.Name
			}
			if channelPlayer != nil {
				channelPlayer.TargetUsers()
			}
		},
		Disconnect: func(e *gumble.DisconnectEvent) {
			log.Println("Disconnecting: ", e.Type)
			cleanUp()
		},
	})
}

func logMessage(e *gumble.TextMessageEvent, isPrivate bool) {
	if isPrivate {
		log.Printf("DMSG (%s): %s", e.Sender.Name, e.Message)
	} else {
		log.Printf("CMSG (%s) %s: %s", e.Sender.Channel.Name, e.Sender.Name, e.Message)
	}
}

func getValueFromFlag(lookup *flag.Flag) string {
	if lookup == nil {
		panic("getValueFromFlag: flagNotFound: nil")
	}

	return lookup.Value.String()
}

// defaultWebURL guesses the web UI's URL from its listen address, for when -weburl isn't set
func defaultWebURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, port)
}
