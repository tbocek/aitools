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

	loaded  string // file currently cued, so a caller can seek instead of reload
	playing bool
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
			p.setPlaying(false)
		case gst.MessageError:
			errMsg, _ := msg.ParseError()
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

func (p *Player) Toggle() {
	if p.playing {
		p.pb.SetState(gst.StatePaused)
	} else {
		p.pb.SetState(gst.StatePlaying)
	}
	p.setPlaying(!p.playing)
}

// Stop tears the stream down and forgets the file, so the next ▶ is a fresh
// start rather than a resume of something the user already ended.
func (p *Player) Stop() {
	p.pb.SetState(gst.StateReady)
	p.loaded = ""
	p.setPlaying(false)
}

func (p *Player) Pause() {
	p.pb.SetState(gst.StatePaused)
	p.setPlaying(false)
}

// SeekTo jumps within the currently loaded file; while paused the new frame
// still renders, which is what frame-stepping relies on.
func (p *Player) SeekTo(t float64) {
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
