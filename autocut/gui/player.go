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

	loaded  string // file currently cued, so a caller can seek instead of reload
	playing bool
	// the stream ran to its end and is sitting on its last frame. Resuming
	// there plays nothing, so ▶ starts the same segment over instead.
	ended               bool
	lastStart, lastStop float64
	// pending segment, applied once the new uri has prerolled
	pendStart, pendStop int64 // nanoseconds; pendStart < 0 = nothing pending
	pendPlay            bool  // false = preroll only, show the first frame paused
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

	p := &Player{pb: pb, Picture: pic, pendStart: -1, pendStop: -1}

	// signal watch dispatches on the default main context, i.e. the GTK loop
	bus := pb.GetBus()
	bus.AddSignalWatch()
	bus.ConnectMessage(func(_ gst.Bus, msg *gst.Message) {
		switch msg.Type() {
		case gst.MessageAsyncDone:
			if p.pendStart >= 0 {
				start, stop := p.pendStart, p.pendStop
				p.pendStart, p.pendStop = -1, -1
				if stop > 0 {
					p.pb.Seek(1.0, gst.FormatTime,
						gst.SeekFlagFlush|gst.SeekFlagAccurate,
						gst.SeekTypeSet, start, gst.SeekTypeSet, stop)
				} else {
					p.pb.Seek(1.0, gst.FormatTime,
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
		p.setPlaying(false)
	case p.ended && p.loaded != "":
		p.PlaySegment(p.loaded, p.lastStart, p.lastStop, true)
	default:
		p.pb.SetState(gst.StatePlaying)
		p.setPlaying(true)
	}
}

// Stop tears the stream down and forgets the file, so the next ▶ is a fresh
// start rather than a resume of something the user already ended.
func (p *Player) Stop() {
	p.pb.SetState(gst.StateReady)
	p.loaded = ""
	p.ended = false
	p.setPlaying(false)
}

func (p *Player) Pause() {
	p.pb.SetState(gst.StatePaused)
	p.setPlaying(false)
}

// SeekTo jumps within the currently loaded file; while paused the new frame
// still renders, which is what frame-stepping relies on.
func (p *Player) SeekTo(t float64) {
	p.ended = false // wherever we land, there is stream ahead of it again
	p.pb.Seek(1.0, gst.FormatTime,
		gst.SeekFlagFlush|gst.SeekFlagAccurate,
		gst.SeekTypeSet, int64(t*1e9), gst.SeekTypeNone, 0)
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
