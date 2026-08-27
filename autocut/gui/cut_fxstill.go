package main

// Showing a stop effect in the preview.
//
// A stop effect is a still standing over footage that keeps running: the frame
// at its marker covers the session seconds its bar covers, faded on and off,
// while the picture underneath -- and its sound -- run on. The render makes it
// as an overlay (freezeCues, encodeClip), and the preview shows the same thing
// the same way: a picture layered over the video (buildFxOverlay), carrying
// the frame at T, with the widget's opacity as the fades.
//
// The trigger is POSITION, not a crossing. The still is owed to the screen
// whenever the playhead is inside the bar -- playing into it, seeking into it,
// or parked in it -- which is exactly what the bar on the lane promises. The
// old crossing-based hold missed seeks entirely and could miss played
// crossings too; a position test has nothing to miss.
//
// The frame is rendered by ffmpeg from the recording itself (ffmpegPNG), the
// same one-frame cut the render's overlay input makes, so the preview and the
// finished video stand on the same picture.

import (
	"fmt"

	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
)

// freezeNow is the stop effect standing over session time t -- the still the
// preview owes the screen -- or nil when the playhead is on running footage.
//
// A stop's bar is not quite the answer on its own. A stop is the ×0 in the
// mean every overlap is settled by (cut_speedmix.go), so a ×2 laid across
// part of it makes the mean ×1 there and the picture runs for those seconds:
// the still is owed exactly where the mean is nought.
func freezeNow(fx []cutFx, t float64) *cutFx {
	if fxMeanRate(fx, t) > 0 {
		return nil
	}
	for i := range fx {
		f := &fx[i]
		if f.frozenFx() && f.Dur > 0 && t >= f.T && t < f.T+f.Dur {
			return f
		}
	}
	return nil
}

// freezeHush is whether the preview's sound is owed silence at session time t:
// a stop is standing there and it asked for its seconds to be taken out. The
// render does this with a volume filter over the same window (stillMute), and
// the preview does it by muting the session the way a card does -- so what you
// hear scrubbing through the stop is what the finished video has.
func freezeHush(fx []cutFx, t float64) bool {
	f := freezeNow(fx, t)
	return f != nil && f.Mute
}

// fxStill is one stop effect's frame, rendered once and kept while the
// playhead is anywhere in its bar. One at a time: the playhead is in at most
// one bar, and a re-entered bar costs one re-render, which is what the first
// entry cost anyway.
type fxStill struct {
	t      float64 // the session moment the frame is cut at (the effect's T)
	tex    *gdk.Texture
	shown  bool
	busy   bool
	failed bool // it cannot be drawn; said once, then left alone
}

// syncFxStill settles the still layer for wherever the playhead is now.
// Called from showInsert, which every path that moves the playhead already
// calls -- a tick, a click, a frame step, an edit dropping the effect.
func (ed *cutEditor) syncFxStill() {
	pic, box := ed.fxStillPic, ed.fxStillBox
	if pic == nil || box == nil || ed.player == nil {
		return
	}
	f := freezeNow(ed.fx, ed.playhead)
	// a card owns the whole picture while it is up; the still yields to it
	if f == nil || ed.player.still {
		if ed.fstill != nil {
			ed.fstill.shown = false
		}
		box.SetVisible(false)
		return
	}
	st := ed.fstill
	if st == nil || st.t != f.T {
		st = &fxStill{t: f.T}
		ed.fstill = st
	}
	if st.tex == nil {
		ed.renderStill(st)
		box.SetVisible(false) // nothing to put up yet; the render will call back
		return
	}
	if !st.shown {
		pic.SetPaintable(st.tex)
		st.shown = true
	}
	// the fades, evaluated the way the render's fade filters will (textAlpha
	// reads the same Trans/Tout bargain for a freeze as for a title)
	box.SetOpacity(textAlpha(*f, ed.playhead))
	ed.fitStill() // under whatever camera is over the footage right now
	box.SetVisible(true)
}

// renderStill draws the stop's frame in the background and puts it up when it
// arrives -- by asking syncFxStill again rather than by showing it directly,
// because by then the playhead may have left the bar.
func (ed *cutEditor) renderStill(st *fxStill) {
	if st.busy || st.failed {
		return
	}
	v := ed.videoAt(st.t)
	if v == nil {
		st.failed = true // a stop in a gap has no frame to stand on
		return
	}
	st.busy = true
	a, local, path := ed.a, st.t-v.start, v.path
	go func() {
		png, err := ffmpegPNG("-ss", fmt.Sprintf("%.3f", local), "-i", path)
		glib.IdleAdd(func() {
			st.busy = false
			if ed.fstill != st {
				return // a different stop owns the layer now
			}
			var tex *gdk.Texture
			if err == nil {
				tex, err = gdk.NewTextureFromBytes(glib.NewBytes(png))
			}
			if err != nil {
				st.failed = true
				if a != nil {
					a.logf(">>> the stop frame at %s cannot be shown in the preview: %v", mmss(st.t), err)
				}
				return
			}
			st.tex = tex
			ed.syncFxStill()
		})
	}()
}
