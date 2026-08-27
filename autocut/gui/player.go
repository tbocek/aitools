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

	p := &Player{pb: pb, Picture: pic, video: paintable, pendStart: -1, pendStop: -1, rate: 1, seekRate: 1}

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

// PlaySegment cues [start, stop) seconds of file; stop < 0 means to the end.
// play=false prerolls only: the first frame shows, paused, until Toggle.
func (p *Player) PlaySegment(file string, start, stop float64, play bool) {
	p.pb.SetState(gst.StateReady)
	p.pb.SetObjectProperty("uri", "file://"+file)
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
		a := newAux(fmt.Sprintf("mix%d", i), t)
		if a == nil {
			return
		}
		a.pb.SetObjectProperty("mute", p.muted)
		p.mix = append(p.mix, a)
	}
}

// newAux builds one audio-only pipeline for a file and the bus watch that does
// its seeking. nil when GStreamer will not give us a playbin, which is the one
// failure a caller can do nothing about.
func newAux(name string, t mixTrack) *auxAudio {
	// playbin3 with no video sink of its own would put up a window; a fake
	// one is how "audio only" is spelled without touching flags
	pb := gst.ElementFactoryMake("playbin3", name)
	if pb == nil {
		return nil
	}
	if fake := gst.ElementFactoryMake("fakesink", name+"novideo"); fake != nil {
		pb.SetObjectProperty("video-sink", fake)
	}
	pb.SetObjectProperty("uri", "file://"+t.path)
	a := &auxAudio{pb: pb, delta: t.delta, dur: t.dur, pend: -1, rate: 1}
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

// SetMuted cuts the session's sound -- the footage and every recording heard
// under it -- without stopping any of it. The clock still runs, the timeline
// still scrolls, and the preview is simply not claiming that what you are
// looking at is what you would be hearing.
//
// This is the preview's half of what an insert means. The render never puts
// session audio under a card (clipMixes), so a preview that does is telling you
// about a cut that will not exist.
func (p *Player) SetMuted(v bool) {
	if p.muted == v {
		return
	}
	p.muted = v
	p.pb.SetObjectProperty("mute", v)
	for _, a := range p.mix {
		a.pb.SetObjectProperty("mute", v)
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
		a := newAux("cardsound", mixTrack{path: file})
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
