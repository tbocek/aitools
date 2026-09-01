package main

// Video preview: a GStreamer playbin3 rendering into gtk4paintablesink, whose
// GdkPaintable is shown by a GtkPicture. Arch ships no GTK media backend
// (GtkVideo is inert here), so this go-gst bridge IS the playback path.
//
// go-gst v1 is girgen-generated on its own glib fork (go-gst/go-glib), so its
// objects and gotk4's are different Go wrappers around the same C GObjects.
// The paintable crosses that boundary as a raw pointer: take the C pointer
// out of the go-gst wrapper, ref it into gotk4's world, and hand GtkPicture a
// gdk.Paintable built around it.

import (
	"fmt"
	"math"
	"strings"

	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	gobject "github.com/go-gst/go-glib/pkg/gobject/v2"
	"github.com/go-gst/go-gst/pkg/gst"
)

type Player struct {
	pb      gst.Element
	Picture *gtk.Picture

	// OnState is called whenever playback starts or stops, including the stream
	// ending on its own -- the run bar draws ▶ or ⏸ from this, and the end of a
	// clip is a change it has no other way to hear about. Called on the GTK
	// thread: every state change here happens either on it or on the bus watch,
	// which dispatches there.
	OnState func()

	// OnError receives GStreamer's own words when the pipeline gives up on a
	// file. These used to go to stdout, which for a GUI started from a launcher
	// is nowhere at all: a wav the decoder refused looked exactly like a wav
	// playing silently, and the transport button sat there claiming ⏸. Also on
	// the GTK thread -- the bus watch dispatches there.
	OnError func(string)

	// the sink's own paintable, kept so that a picture put over the video (an
	// insert; see ShowStill) can be taken away again
	video   gdk.Paintabler
	still   bool
	loaded  string // file currently cued, so a caller can seek instead of reload
	playing bool
	// the separate recordings heard under this file, each its own pipeline
	// (SetMix). Empty for every player but the cut editor's.
	mix []*auxAudio
	// the session's sound is off: the picture is not the session's. Set while a
	// card is on the preview, where hearing the footage carry on underneath is
	// how an insert ends up sounding exactly like the overwrite it is not.
	muted bool
	// the lanes the scene under the playhead does not hear (Hush): hushOwn for
	// the footage's own sound, and a name for each recording mixed under it.
	// Separate from muted because they answer different questions -- muted is
	// "none of this is the session", hush is "this much of the session" -- and
	// a scene can silence the camera while still hearing the microphone.
	hushOwn bool
	hush    map[string]bool
	// what was last written to each pipeline's mute property, so the ten calls
	// a second this is asked from settle into nothing when nothing changed
	ownMute bool
	// an inserted video's own sound, its own pipeline for the same reason the
	// recordings have theirs. Empty unless a video insert is on screen.
	card     *auxAudio
	cardFile string
	// the stream ran to its end and is sitting on its last frame. Resuming
	// there plays nothing, so ▶ starts the same segment over instead.
	ended               bool
	lastStart, lastStop float64
	// pending segment, applied once the new uri has prerolled
	pendStart, pendStop int64 // nanoseconds; pendStart < 0 = nothing pending
	pendPlay            bool  // false = preroll only, show the first frame paused
	// the clock the stream runs on: 1 is the footage's own, and a cut with
	// speed effects in it moves this as the line crosses them (SetRate). Every
	// seek in this file carries it, the separate recordings' seeks included,
	// or the sound would walk away from the picture at the first slow stretch.
	rate float64
	// the rate the master pipeline is ACTUALLY running at, which is whatever
	// the last seek carried. A rate set while paused does not reach the
	// pipeline until something seeks, and ▶ on its own does not seek -- so
	// resuming has to notice the two have drifted apart and put that right.
	seekRate float64
	// the volume effects' say over this pipeline: the gain the cut asks for at
	// the second under the playhead (fxGainAt), 1 where no volume effect
	// covers it. Kept per player because it is a property of what that player
	// is showing, unlike previewVol below, which is a property of the room.
	// The two are multiplied together at every write (applyVol).
	fxGain float64
}

func NewPlayer() (*Player, error) {
	gst.Init()

	pb := gst.ElementFactoryMake("playbin3", "playbin")
	if pb == nil {
		return nil, fmt.Errorf("playbin3 not available (gst-plugins-base)")
	}
	sink := gst.ElementFactoryMake("gtk4paintablesink", "videosink")
	if sink == nil {
		return nil, fmt.Errorf("gtk4paintablesink not available (pacman -S gst-plugin-gtk4)")
	}
	pb.SetObjectProperty("video-sink", sink)

	pv := sink.ObjectProperty("paintable")
	gobj, ok := pv.(gobject.Object)
	if !ok {
		return nil, fmt.Errorf("paintable property is %T, expected a gobject.Object", pv)
	}
	// ToGlibNone hands out the pointer without a transfer; Take refs it into
	// gotk4's world, so both wrappers legitimately co-own the C object.
	paintable := &gdk.Paintable{Object: coreglib.Take(gobject.UnsafeObjectToGlibNone(gobj))}

	pic := gtk.NewPicture()
	pic.SetPaintable(paintable)
	pic.SetContentFit(gtk.ContentFitContain)

	// scaletempo is what makes a rate other than 1 listenable: without it a
	// stream at half speed drops an octave. It is the same trade atempo makes
	// in the render (produce_fx.go), so the preview and the finished video sound
	// like each other. Missing plugin is a worse preview, not a dead one.
	if tempo := gst.ElementFactoryMake("scaletempo", "tempo"); tempo != nil {
		pb.SetObjectProperty("audio-filter", tempo)
	}

	p := &Player{pb: pb, Picture: pic, video: paintable, pendStart: -1, pendStop: -1,
		rate: 1, seekRate: 1, fxGain: 1}
	// born at the volume the sliders say, like every pipeline in the app --
	// and on the roll it visits when one of them moves
	p.applyVol()
	allPlayers = append(allPlayers, p)

	// signal watch dispatches on the default main context, i.e. the GTK loop
	bus := pb.GetBus()
	bus.AddSignalWatch()
	bus.ConnectMessage(func(_ gst.Bus, msg *gst.Message) {
		switch msg.Type() {
		case gst.MessageAsyncDone:
			if p.pendStart >= 0 {
				start, stop := p.pendStart, p.pendStop
				p.pendStart, p.pendStop = -1, -1
				p.seekRate = p.rate
				if stop > 0 {
					p.pb.Seek(p.rate, gst.FormatTime,
						gst.SeekFlagFlush|gst.SeekFlagAccurate,
						gst.SeekTypeSet, start, gst.SeekTypeSet, stop)
				} else {
					p.pb.Seek(p.rate, gst.FormatTime,
						gst.SeekFlagFlush|gst.SeekFlagAccurate,
						gst.SeekTypeSet, start, gst.SeekTypeNone, 0)
				}
				if p.pendPlay {
					p.pb.SetState(gst.StatePlaying)
					p.setPlaying(true)
				}
			}
		case gst.MessageEOS:
			// freeze on the last frame instead of tearing the stream down
			p.pb.SetState(gst.StatePaused)
			p.ended = true
			p.setPlaying(false)
		case gst.MessageError:
			errMsg, _ := msg.ParseError()
			// the pipeline is done for; say so, and stop drawing ⏸ over a
			// stream that has stopped
			p.setPlaying(false)
			if p.OnError != nil {
				p.OnError(fmt.Sprint(errMsg))
				return
			}
			fmt.Println("gst error:", errMsg)
		}
	})

	return p, nil
}

// fileURI is how a path is handed to playbin, and it is not "file://"+path --
// which is what this was, and what a file could be lost behind. A path is
// bytes; a uri is text with punctuation, and the two disagree about '#'.
// Everything after a '#' in a uri is a fragment, dropped before the file is
// ever opened, so a voice sample named for hand-picked takes
// (own@-0.5#3a1f2b_9c2b1e4d.wav) reached filesrc as ".../own@-0.5" and failed
// with "No such file" -- a name the app itself had written. '?' would cut the
// same way and a '%' in a name would be read as an escape of whatever came
// after it.
//
// So every byte outside the unreserved set is percent-escaped, which GStreamer
// undoes on the way back to a filename. '/' is the exception: it is the one
// piece of punctuation that means the same thing on both sides.
func fileURI(path string) string {
	var b strings.Builder
	b.WriteString("file://")
	for i := 0; i < len(path); i++ {
		switch c := path[i]; {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~', c == '/':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// PlaySegment cues [start, stop) seconds of file; stop < 0 means to the end.
// play=false prerolls only: the first frame shows, paused, until Toggle.
func (p *Player) PlaySegment(file string, start, stop float64, play bool) {
	p.pb.SetState(gst.StateReady)
	p.pb.SetObjectProperty("uri", fileURI(file))
	p.loaded = file
	p.pendStart = int64(start * 1e9)
	p.pendStop = -1
	if stop > 0 {
		p.pendStop = int64(stop * 1e9)
	}
	p.pendPlay = play
	p.ended = false
	p.lastStart, p.lastStop = start, stop
	// preroll paused; the bus watch seeks (and maybe plays) on AsyncDone
	p.pb.SetState(gst.StatePaused)
	p.setPlaying(false)
	for _, a := range p.mix {
		a.cue(start, play, p.rate)
	}
}

// ---- a picture in front of the video ----------------------------------------

// ShowStill puts a picture where the video was: the card, title or still that
// an insert plays instead of the footage. The stream underneath is not touched
// -- it is what the timeline is scrolling against and what the clock is read
// from, and the seconds it is playing are seconds the cut has already given
// away to the insert.
//
// The swap is the paintable rather than a widget stacked over the picture: one
// GtkPicture with one thing in it cannot get out of step with itself, and the
// video's paintable is a live object that keeps rendering whether it is on
// screen or not.
func (p *Player) ShowStill(tex gdk.Paintabler) {
	if tex == nil {
		return
	}
	p.Picture.SetPaintable(tex)
	p.still = true
}

// ShowVideo takes that picture away again. Cheap to call on every tick: it is
// the playhead leaving an insert that has to reach it, and the playhead does not
// know when that was.
func (p *Player) ShowVideo() {
	if !p.still {
		return
	}
	p.Picture.SetPaintable(p.video)
	p.still = false
}

// ---- the separate recordings ------------------------------------------------

// Everything below is one sentence: what the cut plays is the session at that
// moment, not the file that happens to have the pictures in it.
//
// The footage is a capture card's idea of the room -- game sound, and whoever
// was close enough to the console -- and the recording that has the voices in it
// is a different file with a different clock. Watching the cut while hearing
// half of it is how a cut gets made against the wrong second, and the waveform
// lanes underneath make that worse rather than better: they show a shout you
// cannot hear.
//
// GStreamer offers no way to add a second file to a playbin, so each recording
// is its own audio-only pipeline, seeked to ITS second of the same instant and
// driven by the same transport. Two pipelines on one machine share the audio
// clock, so they stay together for as long as anyone watches a preview; this is
// a monitor mix, and the render still does its own arithmetic (clipMixes).
type auxAudio struct {
	pb gst.Element
	// the lane's name, which is how a scene names the lanes it does not hear
	// (cutSeg.Quiet). Empty for a card's own sound, which no scene silences.
	base string
	mute bool // last written to the pipeline; see applyMute
	// what to add to a time in the master's file to get the same instant in
	// this one: (master's session start) - (this recording's session start).
	delta float64
	dur   float64 // so a seek past its end is simply not played
	pend  int64   // nanoseconds; < 0 = nothing pending
	play  bool
	// the master's rate, copied here at every seek: this pipeline's deferred
	// seek fires from a bus callback that has no player to ask
	rate float64
}

// mixTrack is one recording to be heard under the footage, as the cut editor
// knows it: where the file is, and how the two clocks differ.
type mixTrack struct {
	base  string
	path  string
	delta float64
	dur   float64
}

// SetMix replaces the recordings heard under whatever this player shows. The
// pipelines are rebuilt rather than reused: the set changes when the session's
// sources do, which is rare, and a stale uri playing under the wrong footage is
// the one failure that would be hard to notice.
func (p *Player) SetMix(tracks []mixTrack) {
	for _, a := range p.mix {
		a.pb.SetState(gst.StateNull)
	}
	p.mix = nil
	for i, t := range tracks {
		a := newAux(fmt.Sprintf("mix%d", i), t, p.vol())
		if a == nil {
			return
		}
		p.mix = append(p.mix, a)
	}
	// after the loop, so a lane the scene already silences is born silent
	// rather than saying its first ten milliseconds out loud
	p.applyMute()
}

// newAux builds one audio-only pipeline for a file and the bus watch that does
// its seeking. nil when GStreamer will not give us a playbin, which is the one
// failure a caller can do nothing about.
func newAux(name string, t mixTrack, vol float64) *auxAudio {
	// playbin3 with no video sink of its own would put up a window; a fake
	// one is how "audio only" is spelled without touching flags
	pb := gst.ElementFactoryMake("playbin3", name)
	if pb == nil {
		return nil
	}
	if fake := gst.ElementFactoryMake("fakesink", name+"novideo"); fake != nil {
		pb.SetObjectProperty("video-sink", fake)
	}
	pb.SetObjectProperty("uri", fileURI(t.path))
	// born at the loudness its player is already running at, which is the
	// slider and any volume effect under the playhead together (Player.vol)
	pb.SetObjectProperty("volume", vol)
	a := &auxAudio{pb: pb, base: t.base, delta: t.delta, dur: t.dur, pend: -1, rate: 1}
	bus := pb.GetBus()
	bus.AddSignalWatch()
	bus.ConnectMessage(func(_ gst.Bus, msg *gst.Message) {
		if msg.Type() != gst.MessageAsyncDone || a.pend < 0 {
			return
		}
		at := a.pend
		a.pend = -1
		a.pb.Seek(a.rate, gst.FormatTime, gst.SeekFlagFlush|gst.SeekFlagAccurate,
			gst.SeekTypeSet, at, gst.SeekTypeNone, 0)
		if a.play {
			a.pb.SetState(gst.StatePlaying)
		}
	})
	return a
}

// The preview's loudness, one number for the whole app. Package state rather
// than a Player field because it is how loud the ROOM is, and the room does
// not change when a tab does -- there is a slider on the run bar and one
// beside each transport that can play video (volumeCtl), and all of them are
// hands on this one number. 1 is the footage as recorded; playbin treats it as
// a plain linear gain.
var previewVol = 1.0

// every player ever built, so a volume set on any of the sliders reaches
// pipelines that already exist. Players are made once per page at startup and
// never torn down, so the roll only ever holds those few.
var allPlayers []*Player

// SetPreviewVolume turns the whole preview up or down: every player, and inside
// each one the footage, the separate recordings mixed under it and an insert's
// own sound alike. One gain for all of them, because the slider's promise is
// "quieter", not "rebalanced".
func SetPreviewVolume(v float64) {
	previewVol = math.Max(0, math.Min(1, v))
	for _, p := range allPlayers {
		p.applyVol()
	}
}

// every volume slider on screen. There is one wherever a video can be played
// -- the run bar, the Cut transport, the Narrate transport -- and they are
// hands on the one number, so a slider pulled down on Cut has to be found
// pulled down on Narrate. Otherwise the second page would show 100% while
// playing at 40, which is a control lying about the state it is in.
var volScales []*gtk.Scale

// volSyncing is up while one slider is writing the others, so their own
// value-changed handlers know not to write back. GtkAdjustment stays quiet
// when the value does not actually change, so the loop would end anyway; the
// flag is here because "would end anyway" is a thing to have to work out, and
// the rounding through 0..100 and back is exactly where it would stop being
// true.
var volSyncing bool

// volumeCtl is the preview volume control: a speaker and a slider, built fresh
// for each place a video can be played from. Built rather than shared because
// a GTK widget has one parent, and the alternative to one per transport is
// re-parenting it on every tab switch.
func volumeCtl() *gtk.Box {
	icon := gtk.NewImageFromIconName("audio-volume-high-symbolic")
	sc := gtk.NewScaleWithRange(gtk.OrientationHorizontal, 0, 100, 1)
	sc.SetValue(previewVol * 100)
	sc.SetSizeRequest(120, -1)
	tip := "preview volume — the players only; nothing that is rendered, and the " +
		"same setting wherever it is shown"
	icon.SetTooltipText(tip)
	sc.SetTooltipText(tip)
	sc.ConnectValueChanged(func() {
		if volSyncing {
			return
		}
		SetPreviewVolume(sc.Value() / 100)
		volSyncing = true
		for _, o := range volScales {
			if o != sc {
				o.SetValue(previewVol * 100)
			}
		}
		volSyncing = false
	})
	volScales = append(volScales, sc)
	box := gtk.NewBox(gtk.OrientationHorizontal, 4)
	box.SetVAlign(gtk.AlignCenter)
	box.Append(icon)
	box.Append(sc)
	return box
}

// SetFxGain is the cut's own say over this player's loudness: the volume
// effect under the playhead, which the preview obeys for the same reason it
// obeys a speed effect's rate -- a preview that does not is telling you about
// a video that will not exist.
//
// Multiplied with the slider rather than replacing it, so turning the preview
// down still turns a boosted stretch down. Nothing is done when the gain has
// not moved: this is called from a tick ten times a second, and writing a
// property that already holds that value on every one of them is a message
// through the pipeline for nothing.
func (p *Player) SetFxGain(g float64) {
	g = math.Max(0, math.Min(fxMaxGain, g))
	if math.Abs(p.fxGain-g) < 1e-6 {
		return
	}
	p.fxGain = g
	p.applyVol()
}

// vol is the one loudness every pipeline this player owns runs at: the room's
// setting (previewVol) and the cut's own say over the seconds under the
// playhead (fxGain), multiplied and held to what the property will take. One
// function, because a pipeline built later must start at the same number the
// ones built earlier are already at.
func (p *Player) vol() float64 {
	return math.Max(0, math.Min(fxMaxGain, previewVol*p.fxGain))
}

// applyVol writes the two gains, multiplied, onto every pipeline this player
// owns -- the footage, the separate recordings heard under it, and an insert's
// own sound. playbin's volume runs to 10, which is exactly as far as a volume
// effect goes (fxMaxGain), so a boosted stretch played at the slider's full
// travel still lands inside what the property will take.
func (p *Player) applyVol() {
	v := p.vol()
	p.pb.SetObjectProperty("volume", v)
	for _, a := range p.mix {
		a.pb.SetObjectProperty("volume", v)
	}
	if p.card != nil {
		p.card.pb.SetObjectProperty("volume", v)
	}
}

// SetMuted cuts the session's sound -- the footage and every recording heard
// under it -- without stopping any of it. The clock still runs, the timeline
// still scrolls, and the preview is simply not claiming that what you are
// looking at is what you would be hearing.
//
// This is the preview's half of what an insert means. The render never puts
// session audio under a card (clipMixes), so a preview that does is telling you
// about a cut that will not exist.
func (p *Player) SetMuted(v bool) {
	p.muted = v
	p.applyMute()
}

// Hush silences the parts of the session the scene under the playhead does not
// hear: own for the footage's own sound, quiet for the recordings mixed under
// it, named the way the scene names them (cutSeg.Quiet).
//
// The pipelines stay where they are and only their mute property moves. The set
// of recordings changes with the FILE and the answer about them changes with
// the SCENE, which is ten times a second while the preview runs -- rebuilding
// them at that rate would be a gap in the sound at every clip boundary, and the
// lane would come back a beat late even where it is heard.
//
// Without this the badge was a control over the render alone: the lane went
// grey, the wash went grey, and the preview went on playing it, so the one
// place the choice could be checked by ear disagreed with the finished video.
func (p *Player) Hush(own bool, quiet []string) {
	p.hushOwn, p.hush = own, hushSet(quiet)
	p.applyMute()
}

// hushSet turns the scene's list into what the pipelines are checked against.
// nil for a scene that hears everything, which is most of them: the absence of
// the map IS that answer, so the common case costs nothing and a scene that
// stops silencing a lane cannot leave the old set standing behind it.
func hushSet(quiet []string) map[string]bool {
	if len(quiet) == 0 {
		return nil
	}
	m := make(map[string]bool, len(quiet))
	for _, q := range quiet {
		m[q] = true
	}
	return m
}

// hushes is the whole decision about one pipeline's sound, split out from the
// writing of it so it can be asked directly -- the pipelines are GStreamer and
// stay out of a unit test. own marks the footage's own sound, which is a lane
// the scene can silence like any other and is not one of p.mix.
func (p *Player) hushes(base string, own bool) bool {
	if p.muted {
		return true // not the session's picture at all, so none of its sound
	}
	if own {
		return p.hushOwn
	}
	return p.hush[base]
}

// applyMute writes that answer onto every pipeline, and only where it changed:
// this runs from the playback tick, and a property written ten times a second
// with the value it already holds is ten notifications a second for nothing.
func (p *Player) applyMute() {
	if m := p.hushes("", true); m != p.ownMute {
		p.ownMute = m
		p.pb.SetObjectProperty("mute", m)
	}
	for _, a := range p.mix {
		if m := p.hushes(a.base, false); m != a.mute {
			a.mute = m
			a.pb.SetObjectProperty("mute", m)
		}
	}
}

// CardSound plays an inserted video's own audio, at seconds into it, which is
// the other half: a sting that says something is a sting that says it out loud.
// An empty file takes the sound away again.
//
// Its own pipeline, unmuted by SetMuted -- it is not the session's sound, it is
// the insert's, and it is the one thing that should be audible while a card is
// up. A card with no audio track simply plays nothing, so nothing here asks
// whether it has one.
func (p *Player) CardSound(file string, at float64, play bool) {
	if file != p.cardFile {
		p.dropCard()
		if file == "" {
			return
		}
		a := newAux("cardsound", mixTrack{path: file}, p.vol())
		if a == nil {
			return
		}
		p.card, p.cardFile = a, file
	}
	if p.card == nil {
		return
	}
	p.card.cue(at, play, p.rate)
}

// dropCard tears the insert's audio pipeline down. Not merely paused: the next
// card is a different file, and a uri cannot be changed under a live pipeline.
func (p *Player) dropCard() {
	if p.card != nil {
		p.card.pb.SetState(gst.StateNull)
	}
	p.card, p.cardFile = nil, ""
}

// cue puts this recording at the master's time t and either holds it there or
// lets it run. A time this recording was not running at is silence, and silence
// is a pipeline left in PAUSED rather than one seeked to its own edge, which
// would play the wrong minute quietly under the picture.
func (a *auxAudio) cue(t float64, play bool, rate float64) {
	at := t + a.delta
	a.rate = rate // the deferred seek fires with no player in reach
	if at < 0 || (a.dur > 0 && at > a.dur) {
		a.pend = -1
		a.pb.SetState(gst.StatePaused)
		return
	}
	a.pend = int64(at * 1e9)
	a.play = play
	a.pb.SetState(gst.StatePaused)
}

// running says whether this one has something to play at the master's time t,
// which is what keeps a resume from starting a recording that had already
// stopped when this second happened.
func (a *auxAudio) running(t float64) bool {
	at := t + a.delta
	return at >= 0 && (a.dur <= 0 || at <= a.dur)
}

// setPlaying records the state and tells whoever is drawing a transport button
// that it changed. Nothing else may write p.playing.
func (p *Player) setPlaying(v bool) {
	if p.playing == v {
		return
	}
	p.playing = v
	if p.OnState != nil {
		p.OnState()
	}
}

// Playing reports whether the stream is running right now; Cued reports that
// something is loaded and paused, i.e. that ▶ should resume it rather than
// start whatever the page does when idle.
func (p *Player) Playing() bool { return p.playing }
func (p *Player) Cued() bool    { return p.loaded != "" && !p.playing }

// Loaded is whether this player has been pointed at a file at all, playing or
// not. The question an overlay asks before it draws anything on the picture:
// there is no framing to show over a player that has never been given footage.
func (p *Player) Loaded() bool { return p.loaded != "" }

// Toggle is what every play button in the app does: pause what is running,
// resume what is paused -- and, on a stream that has run to its end, play it
// again from the top. Resuming at the last frame is silence and a still
// picture, i.e. a play button that looks broken.
func (p *Player) Toggle() {
	switch {
	case p.playing:
		p.pb.SetState(gst.StatePaused)
		p.syncMix(false)
		p.cardState(gst.StatePaused)
		p.setPlaying(false)
	case p.ended && p.loaded != "":
		p.PlaySegment(p.loaded, p.lastStart, p.lastStop, true)
	default:
		// a rate chosen while paused has not reached the pipeline yet, and
		// syncMix below is about to seek the recordings at it: without this
		// the picture would run at the old clock and the sound at the new one
		if math.Abs(p.rate-p.seekRate) > 1e-6 {
			if pos, ok := p.Position(); ok {
				p.SeekTo(pos)
			}
		}
		p.pb.SetState(gst.StatePlaying)
		p.syncMix(true)
		// an insert's sound stops and starts with the transport like everything
		// else on the page; where it is in the card is where it was left
		p.cardState(gst.StatePlaying)
		p.setPlaying(true)
	}
}

// syncMix puts every recording back on the master's clock. It is called on
// every transport change rather than only on the first one: two pipelines
// started a minute apart agree to the millisecond and then drift as slowly as
// their clocks differ, and a resync that costs a seek nobody hears is cheaper
// than reasoning about how long that takes to be audible.
func (p *Player) syncMix(play bool) {
	if len(p.mix) == 0 {
		return
	}
	pos, ok := p.Position()
	if !ok {
		return
	}
	for _, a := range p.mix {
		if !a.running(pos) {
			a.pb.SetState(gst.StatePaused)
			continue
		}
		a.pend, a.rate = -1, p.rate
		a.pb.Seek(p.rate, gst.FormatTime, gst.SeekFlagFlush|gst.SeekFlagAccurate,
			gst.SeekTypeSet, int64((pos+a.delta)*1e9), gst.SeekTypeNone, 0)
		if play {
			a.pb.SetState(gst.StatePlaying)
		} else {
			a.pb.SetState(gst.StatePaused)
		}
	}
}

// Stop tears the stream down and forgets the file, so the next ▶ is a fresh
// start rather than a resume of something the user already ended.
func (p *Player) Stop() {
	p.pb.SetState(gst.StateReady)
	for _, a := range p.mix {
		a.pend = -1
		a.pb.SetState(gst.StateReady)
	}
	p.loaded = ""
	p.ended = false
	p.dropCard()
	p.SetMuted(false) // whatever was covering the sound is over with the stream
	p.setPlaying(false)
}

func (p *Player) Pause() {
	p.pb.SetState(gst.StatePaused)
	for _, a := range p.mix {
		a.pb.SetState(gst.StatePaused)
	}
	p.cardState(gst.StatePaused)
	p.setPlaying(false)
}

// cardState moves the insert's audio pipeline, if there is one, and is silence
// about it when there is not.
func (p *Player) cardState(s gst.State) {
	if p.card != nil {
		p.card.pb.SetState(s)
	}
}

// SeekTo jumps within the currently loaded file; while paused the new frame
// still renders, which is what frame-stepping relies on.
func (p *Player) SeekTo(t float64) {
	p.ended = false // wherever we land, there is stream ahead of it again
	p.seekRate = p.rate
	p.pb.Seek(p.rate, gst.FormatTime,
		gst.SeekFlagFlush|gst.SeekFlagAccurate,
		gst.SeekTypeSet, int64(t*1e9), gst.SeekTypeNone, 0)
	// the recordings land on the same instant, told in their own seconds --
	// asking the master where it is would ask before this seek has taken
	for _, a := range p.mix {
		if !a.running(t) {
			a.pb.SetState(gst.StatePaused)
			continue
		}
		a.pend, a.rate = -1, p.rate
		a.pb.Seek(p.rate, gst.FormatTime, gst.SeekFlagFlush|gst.SeekFlagAccurate,
			gst.SeekTypeSet, int64((t+a.delta)*1e9), gst.SeekTypeNone, 0)
		if p.playing {
			a.pb.SetState(gst.StatePlaying)
		}
	}
}

// SetRate stores the clock the stream is to run on and says whether that is a
// change. It does NOT seek, because a rate only takes effect at a seek and
// every caller here is about to make one anyway -- setting it first means one
// seek where storing it afterwards would mean two.
//
// A caller that changes the rate mid-playback, with nowhere to seek to, has to
// seek to where the stream already is. That is the editor's job (syncPlayRate)
// rather than this one's: only it knows whether the line is about to move.
// Rate is the clock the picture is actually running on -- the rate the last
// seek went out at, not one that has been asked for and not yet taken hold.
func (p *Player) Rate() float64 {
	if p.seekRate <= 0 {
		return 1
	}
	return p.seekRate
}

func (p *Player) SetRate(r float64) bool {
	if r <= 0 || math.IsNaN(r) || math.IsInf(r, 0) {
		r = 1
	}
	if math.Abs(r-p.rate) < 1e-6 {
		return false
	}
	p.rate = r
	return true
}

// Position reports the current playback position in seconds.
func (p *Player) Position() (float64, bool) {
	ns, ok := p.pb.QueryPosition(gst.FormatTime)
	if !ok || ns < 0 {
		return 0, false
	}
	return float64(ns) / 1e9, true
}

// videoFrame is the box a preview sits in, and all three pages that show video
// use it so they cannot drift apart.
//
// gtk.NewFrame("") is a frame WITH a label: the empty string still builds a
// GtkLabel, and an empty label is a whole line of text high. Measured, not
// guessed -- 640x361 frame, child allocated at y=26 with a height of 333 and
// the top 26 pixels gone. Every video in the app was sitting under an invisible
// caption. A frame with no label widget hands its child the whole inside.
//
// The dark fill is the other half of the same complaint. A picture keeps the
// video's aspect, so whatever the frame has spare turns into bars beside it.
// In the window's own background those bars read as an over-wide border; in
// black they read as letterboxing, which is what they are.
func videoFrame(child gtk.Widgetter) *gtk.Frame {
	f := gtk.NewFrame("")
	f.SetLabelWidget(nil)
	f.AddCSSClass("videoframe")
	if child != nil {
		f.SetChild(child)
	}
	return f
}

// playerErr is what every player's OnError is set to. Named, because there are
// five of them and "playback failed" in a log shared by the whole pipeline
// answers none of the questions you have at that point. The status line gets it
// too: a failure the log records and the window does not mention is a failure
// nobody notices until they wonder why the video is silent.
func (a *App) playerErr(who string) func(string) {
	return func(m string) {
		a.logf("!!! %s: playback failed — %s", who, m)
		if a.status == nil {
			return // a player that failed before the window finished; stderr has it
		}
		a.setStatus(who + " would not play — see log")
		a.updateRunControls()
	}
}
