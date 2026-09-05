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

import "github.com/diamondburned/gotk4/pkg/gdk/v4"

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

// fxHush is whether the preview's sound is owed silence at session time t: a
// speed effect covering it whose sound is Silent (cut_fxsound.go). It used to
// be a stop's question alone -- the tick under the stop dialog was the only
// way to ask for silence -- and it is every rate's now, so this asks about
// every rate.
//
// What the preview cannot follow is the other pair of answers: a sound left on
// its own clock is a second read of the same recording at an offset, which the
// player has one pipeline for and it is glued to the picture. Those stretches
// preview at the picture's speed and render as they are asked to; the lane
// draws the difference (drawSndTail).
func fxHush(fx []cutFx, t float64) bool {
	for _, f := range fx {
		if f.Kind != "speed" || f.sound() != sndMute || f.Dur <= 0 {
			continue
		}
		if t >= f.T && t < f.T+f.Dur {
			return true
		}
	}
	return false
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
