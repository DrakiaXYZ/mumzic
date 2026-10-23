package playback

import (
	"context"
	"errors"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/iotku/mumzic/database"
	"github.com/iotku/mumzic/helper"
	"github.com/iotku/mumzic/messages"
	"github.com/iotku/mumzic/playlist"
	"github.com/iotku/mumzic/search"
	"github.com/iotku/mumzic/youtubedl"
	"layeh.com/gumble/gumble"
	"layeh.com/gumble/gumble/MumbleProto"
	"layeh.com/gumble/gumbleffmpeg"
	_ "layeh.com/gumble/opus"
)

type Player struct {
	stream   *gumbleffmpeg.Stream
	Client   *gumble.Client
	targets  []*gumble.User
	Playlist playlist.List
	Volume   float32
	IsRadio  bool
	Config   *database.Config

	// Syncronization
	mu         sync.RWMutex
	stopCtx    context.Context
	stopCancel context.CancelFunc
	stopDone   chan struct{}
	isPlaying  bool
	finished   bool // the last track in the playlist ended or was skipped
}

func (player *Player) AddTarget(username string) {
	user := player.Client.Users.Find(username)
	if user == nil {
		return // user not found
	}
	for _, v := range player.targets {
		if v.UserID == user.UserID {
			player.RemoveTarget(v.Name)
			break
		}
	}
	player.targets = append(player.targets, user)
	player.TargetUsers()
}

func (player *Player) RemoveTarget(username string) {
	user := player.Client.Users.Find(username)
	for i, v := range player.targets {
		if v.UserID == user.UserID {
			player.targets = append(player.targets[:i], player.targets[i+1:]...)
			break
		}
	}
	player.TargetUsers()
}

func (player *Player) TargetUsers() {
	if len(player.targets) == 0 {
		player.Client.VoiceTarget = nil
		return
	}
	player.Client.VoiceTarget = &gumble.VoiceTarget{ID: uint32(2)}
	ownChannel := player.Client.Self.Channel
	player.Client.VoiceTarget.AddChannel(ownChannel, false, false, "radio")
	packet := MumbleProto.VoiceTarget{
		Id:      &player.Client.VoiceTarget.ID,
		Targets: make([]*MumbleProto.VoiceTarget_Target, 0, len(player.targets)+1),
	}
	for _, v := range player.targets {
		player.Client.VoiceTarget.AddUser(v)
		packet.Targets = append(packet.Targets, &MumbleProto.VoiceTarget_Target{
			Session: []uint32{v.Session},
		})
	}

	packet.Targets = append(packet.Targets, &MumbleProto.VoiceTarget_Target{
		ChannelId: &ownChannel.ID,
	})

	err := player.Client.Conn.WriteProto(&packet)
	if err != nil {
		log.Println(err)
	}
}

func NewPlayer(client *gumble.Client, config *database.Config) *Player {
	ctx, cancel := context.WithCancel(context.Background())
	return &Player{
		stream:  nil,
		Client:  client,
		targets: make([]*gumble.User, 0),
		Playlist: playlist.List{
			Playlist: make([][]string, 0),
			Position: 0,
		},
		Volume:     config.Volume,
		IsRadio:    false,
		Config:     config,
		stopCtx:    ctx,
		stopCancel: cancel,
		stopDone:   make(chan struct{}),
		isPlaying:  false,
	}
}

// currentStream returns the stream of the current track, which may be nil
func (player *Player) currentStream() *gumbleffmpeg.Stream {
	player.mu.RLock()
	defer player.mu.RUnlock()
	return player.stream
}

// setStream replaces the stream of the current track and starts playing it
func (player *Player) setStream(stream *gumbleffmpeg.Stream) error {
	stream.Volume = player.Volume
	player.mu.Lock()
	player.stream = stream
	player.mu.Unlock()
	return stream.Play()
}

// IsStopped returns true if the Stream exists and claims to be stopped
func (player *Player) IsStopped() bool {
	stream := player.currentStream()
	return stream == nil || stream.State() == gumbleffmpeg.StateStopped
}

// IsPlaying returns true if the Stream exists and claims to be playing
func (player *Player) IsPlaying() bool {
	player.mu.RLock()
	defer player.mu.RUnlock()
	return player.isPlaying && player.stream != nil && player.stream.State() == gumbleffmpeg.StatePlaying
}

// IsPaused returns true if the Stream exists and claims to be paused
func (player *Player) IsPaused() bool {
	stream := player.currentStream()
	return stream != nil && stream.State() == gumbleffmpeg.StatePaused
}

// Pause pauses the current stream, it can be continued with Resume
func (player *Player) Pause() error {
	stream := player.currentStream()
	if stream == nil || stream.State() != gumbleffmpeg.StatePlaying {
		return errors.New("nothing is playing")
	}

	// gumbleffmpeg's Pause blocks forever if the track ends at the same moment, so don't wait on it indefinitely
	done := make(chan error, 1)
	go func() { done <- stream.Pause() }()
	select {
	case err := <-done:
		return err
	case <-time.After(time.Second):
		return errors.New("pause timed out")
	}
}

// Resume continues a paused stream from where it left off
func (player *Player) Resume() error {
	if !player.IsPaused() {
		return errors.New("nothing is paused")
	}
	return player.currentStream().Play()
}

// PlayCurrent plays the playlist at the current position should the player not already be playing.
// A paused stream is resumed rather than restarted, and nothing happens once the last track has finished.
func (player *Player) PlayCurrent() {
	if player.IsFinished() {
		return
	}
	if player.IsPaused() {
		helper.LogErr(player.Resume(), "PlayCurrent resume")
		return
	}
	if !player.Playlist.IsEmpty() && !player.IsPlaying() {
		player.Play(player.Playlist.GetCurrentPath())
	}
}

// waitForStop waits for the given playback stream to end and performs the upcoming action.
// The stream and stopCtx are passed in rather than read from player, which is changed when the next track starts.
func (player *Player) waitForStop(stream *gumbleffmpeg.Stream, stopCtx context.Context) {
	if stream.State() == gumbleffmpeg.StateStopped {
		return
	}

	// Wait for either stream completion or stop signal
	done := make(chan struct{})
	go func() {
		defer close(done)
		stream.Wait()
	}()

	select {
	case <-done: // Stream finished
	case <-stopCtx.Done(): // Stop requested
		ensureStreamStopped(stream)
		return
	}

	if !waitForActualStop(stream, 3*time.Second) {
		log.Println("WARNING: Stream did not stop properly within timeout")
		return
	}

	player.mu.RLock()
	shouldContinue := player.isPlaying
	player.mu.RUnlock()

	if !shouldContinue {
		player.markStopped()
		return
	}

	if player.IsRadio {
		err := player.Playlist.AddNext(strconv.Itoa(search.GetRandomTrackIDs(1)[0]))
		if err != nil {
			helper.ChanMsg(player.Client, "<b style=\"color:red\">Error Adding Radio Track: </b>"+err.Error())
			log.Println("Radio failed to Playlist.AddNext a random track ID, stale database?: ", err)
		}
	}

	if player.Playlist.HasNext() {
		player.Playlist.Next()
		player.PlayCurrent()
	} else {
		player.requestStop()
		player.markFinished()
	}
}

func (player *Player) Play(path string) {
	if player.IsPlaying() || player.IsPaused() {
		player.requestStop()
		waitForActualStop(player.currentStream(), 3*time.Second)
	}

	path = helper.StripHTMLTags(path)
	var err error
	if strings.HasPrefix(path, "http") {
		err = player.PlayYT(path)
	} else {
		err = player.PlayFile(path)
	}

	if err != nil {
		helper.ChanMsg(player.Client, "<b style=\"color:red\">Error: </b>"+err.Error())
		return
	}

	player.markPlaying()
	nowPlaying := player.NowPlaying()
	helper.ChanMsg(player.Client, nowPlaying)
	helper.SetComment(player.Client, nowPlaying)
	player.mu.RLock()
	stopCtx := player.stopCtx
	player.mu.RUnlock()
	go player.waitForStop(player.currentStream(), stopCtx)
}

func (player *Player) PlayNow(track string) error {
	player.Stop(true)
	err := player.Playlist.AddNext(track)
	if player.IsStopped() && err == nil {
		if !player.Playlist.HasNext() {
			player.PlayCurrent()
		} else {
			player.Playlist.Skip(1)
			player.clearFinished()
			player.PlayCurrent()
		}
	}

	return err
}

func (player *Player) NowPlaying() string {
	currentPath := player.Playlist.GetCurrentPath()
	currentHuman := player.Playlist.GetCurrentHuman()
	isRadio := player.IsRadio
	count := player.Playlist.Count()

	return messages.NowPlaying(currentPath, currentHuman, isRadio, count)
}

func (player *Player) Stop(shouldStop bool) {
	if shouldStop {
		player.requestStop()
	} else {
		player.markStopped()
	}

	if stream := player.currentStream(); stream != nil {
		ensureStreamStopped(stream)
		helper.SetComment(player.Client, "Not Playing.")

		if !waitForActualStop(stream, 3*time.Second) {
			log.Println("WARNING: Stream did not stop properly within timeout")
		}
	}
}

func (player *Player) PlayFile(path string) error {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		log.Println("[Error] file not found:", path)
		return errors.New("not found")
	}

	return player.setStream(gumbleffmpeg.New(player.Client, gumbleffmpeg.SourceFile(path)))
}

func (player *Player) Skip(amount int) {
	if player.Playlist.HasNext() && !player.IsRadio {
		player.Stop(true)
		player.Playlist.Skip(amount)
		player.clearFinished()
		player.PlayCurrent()
	} else if player.IsRadio {
		player.Stop(false)
	} else {
		player.Stop(true)
		player.markFinished()
	}
}

// Next skips amount tracks ahead, in radio mode a new random track is played instead.
func (player *Player) Next(amount int) error {
	if player.IsRadio {
		ids := search.GetRandomTrackIDs(1)
		if len(ids) == 0 {
			return errors.New("no tracks available for radio")
		}
		return player.PlayNow(strconv.Itoa(ids[0]))
	}

	player.Skip(amount)
	return nil
}

// Enqueue adds an ID, URL or search query to the end of the playlist and starts playback if nothing is playing.
// Radio mode is disabled, and a paused player stays paused. Returns the human friendly title of the added track.
func (player *Player) Enqueue(arg string) (string, error) {
	playNext := !player.Playlist.IsEmpty() && !player.Playlist.HasNext()

	human, err := player.Playlist.AddToQueue(arg)
	if err != nil {
		return "", err
	}

	player.IsRadio = false
	if player.IsPaused() {
		return human, nil
	}

	if !player.IsPlaying() && playNext {
		player.Skip(1)
	}
	player.PlayCurrent()
	return human, nil
}

// PlayYT streams a URL through ytdl
func (player *Player) PlayYT(url string) error {
	url = helper.StripHTMLTags(url)
	if !youtubedl.IsWhiteListedURL(url) {
		return errors.New("URL Doesn't Meet whitelist")
	}

	return player.setStream(gumbleffmpeg.New(player.Client, youtubedl.GetYtDLSource(url)))
}

func (player *Player) SetVolume(value float32) {
	player.Volume = value
	player.Config.Volume = value
	if stream := player.currentStream(); stream != nil {
		stream.Volume = value
	}
}

// markPlaying marks the player as actively playing
func (player *Player) markPlaying() {
	player.mu.Lock()
	defer player.mu.Unlock()

	// Reset context for new playback session
	if player.stopCtx.Err() != nil {
		player.stopCtx, player.stopCancel = context.WithCancel(context.Background())
	}
	player.isPlaying = true
	player.finished = false
}

// markFinished records that there is nothing left to play, the current track has already ended or been skipped
func (player *Player) markFinished() {
	player.mu.Lock()
	defer player.mu.Unlock()
	player.finished = true
}

// clearFinished is called when the playlist moves past the finished track onto a new one
func (player *Player) clearFinished() {
	player.mu.Lock()
	defer player.mu.Unlock()
	player.finished = false
}

// SavePlaylist saves the tracks that are still to be played, leaving out the current track if it already finished
func (player *Player) SavePlaylist(hostname string) {
	player.Playlist.Save(hostname, !player.IsFinished())
}

// IsFinished returns true once the last track in the playlist has ended or been skipped, until something plays again
func (player *Player) IsFinished() bool {
	player.mu.RLock()
	defer player.mu.RUnlock()
	return player.finished
}

// requestStop signals that playback should stop
func (player *Player) requestStop() {
	player.mu.Lock()
	defer player.mu.Unlock()
	player.isPlaying = false
	player.stopCancel()
}

// markStopped marks the player as stopped without requesting a stop
func (player *Player) markStopped() {
	player.mu.Lock()
	defer player.mu.Unlock()
	player.isPlaying = false
}

// ensureStreamStopped aggressively stops the stream if it's running
func ensureStreamStopped(stream *gumbleffmpeg.Stream) {
	if stream == nil {
		return
	}
	if state := stream.State(); state == gumbleffmpeg.StatePlaying || state == gumbleffmpeg.StatePaused {
		stream.Stop() //#nosec G104 -- Only error this will respond with is stream not playing.
	}
}

// waitForActualStop waits for the stream to actually stop for the supplied timeout
func waitForActualStop(stream *gumbleffmpeg.Stream, timeout time.Duration) bool {
	if stream == nil {
		return true
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if stream.State() == gumbleffmpeg.StateStopped {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}

	return stream.State() == gumbleffmpeg.StateStopped
}
