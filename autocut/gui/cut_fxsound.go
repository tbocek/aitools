package main

// What the sound does over a speed effect.
//
// It used to be one tick on one rate: a stop asked whether to keep playing the
// footage's sound under the held frame, and every other rate simply took the
// sound with the picture. That left two things unreachable. Silence, at any
// rate but a stop -- fast-forwarded chatter is the first thing most editors
// throw away, and doing it meant dragging a second effect (🔊 Volume 0%) over
// the same seconds. And the opposite: the sound going on at its own speed
// while the picture runs off without it, which is what a montage is.
//
// So it is a choice with five answers, on every rate.
//
//	with the picture, voice held   the clock takes the sound too, pitch kept
//	with the picture, pitch and all the tape-speed version of the same thing
//	at 1×, until the speed change ends
//	at 1×, until the scene ends
//	silent
//
// ---- the two that let the sound run on ---------------------------------------
//
// Think of two read-heads on the recording: the picture's and the sound's.
// Normally they are the same head. Under a speed effect that keeps its sound at
// 1× they come apart -- the picture's runs four times as fast, or half -- and a
// GAP opens between them. The two entries are where that gap is closed.
//
// The gap costs something either way, and which it costs is the rate:
//
//	×4 over 20 s   5 s on screen, the sound 15 s BEHIND. Closing the gap
//	               skips forward: 15 s of sound is never heard.
//	×0.5 over 20 s 40 s on screen, the sound 20 s AHEAD -- it has read past
//	               what the picture has reached. Closing it jumps back: 20 s
//	               is heard twice.
//
// Neither is a fault to be designed out. A fast stretch under a voice that
// keeps talking IS the thing being asked for, and the seconds it costs are the
// seconds the speed-up was there to skip. What matters is that the cost lands
// somewhere the hand chose, which is what the two entries are: at the end of
// the effect, or at the end of the scene it is in.
//
// The close itself is a splice in the sound, so it is dipped rather than cut
// (sndDip). Not crossfaded: clips are encoded one at a time and joined with a
// stream copy, so nothing can cross the join -- what is possible is the tail of
// one clip fading down and the head of the next fading up, which over 150 ms
// reads as a soft join rather than the click a hard splice makes.

import (
	"fmt"
	"math"

	"github.com/diamondburned/gotk4/pkg/cairo"
)

// The five answers. Stored as words (cutFx.Snd) rather than a number, so a cut
// written by a version that knows an answer this one does not reads back as
// the effect it was rather than as whatever that number now means.
const (
	sndWith  = ""      // the sound travels with the picture, pitch held
	sndPitch = "pitch" // ...and with the pitch riding the rate
	sndFx    = "own"   // 1×, back in sync when the speed change ends
	sndScene = "scene" // 1×, back in sync when the scene ends
	sndMute  = "mute"  // nothing is heard over these seconds
)

// sndKinds is the order they are offered in and stored as; sndNames is what
// the dropdown says. The two lists are one list read twice -- an index into
// either is an index into the other.
var (
	sndKinds = []string{sndWith, sndPitch, sndFx, sndScene, sndMute}
	// short, because a dropdown is as wide as the longest thing in it and this
	// one shares a line with two other questions. What each answer COSTS is
	// the line under the box (sndNote), which is the part worth reading twice.
	sndNames = []string{
		"With the picture",
		"With the picture, pitched",
		"1× to the effect's end",
		"1× to the scene's end",
		"Silent",
	}
)

// sound is the effect's answer, normalised. An unmigrated stop's tick reads as
// silence (migrateFx does this once on load, and this is the same reading for
// anything that never went through it), and a word this version does not know
// reads as the default, which is what every cut did before there was a choice.
func (f cutFx) sound() string {
	k := f.Snd
	if k == "" && f.Mute {
		k = sndMute
	}
	for _, w := range sndKinds {
		if k == w {
			return k
		}
	}
	return sndWith
}

// sndShifts is whether an answer lets the sound come away from the picture --
// the two the render has to plan for rather than filter (produce_snd.go).
func sndShifts(k string) bool { return k == sndFx || k == sndScene }

// sndIndex is where an answer sits in the dropdown, and sndKindOf is the way
// back.
func sndIndex(k string) uint {
	for i, w := range sndKinds {
		if w == k {
			return uint(i)
		}
	}
	return 0
}

func sndKindOf(i uint) string {
	if int(i) >= len(sndKinds) {
		return sndWith
	}
	return sndKinds[i]
}

// sndAt is the sound answer in force at session second t, and which effect
// gave it.
//
// Two speed effects over one second average their rates (cut_speedmix.go), and
// there is no averaging two answers about the sound: the EARLIER effect's
// answer holds, because it is the one whose sound is already running by the
// time the second arrives. Nothing at t at all is the plain answer, which is
// also what a stretch with no speed effect over it gets.
func sndAt(fx []cutFx, t float64) (string, cutFx, bool) {
	for _, f := range speedsOf(fx) { // sorted by T
		if f.Dur > 0 && t >= f.T-1e-9 && t < f.T+f.Dur-1e-9 {
			return f.sound(), f, true
		}
	}
	return sndWith, cutFx{}, false
}

// sndDebt is how far apart the two read-heads are by the end of an effect that
// keeps its sound at 1×, in seconds of the recording. Positive is sound behind
// the picture (a fast stretch), negative is sound ahead (slow motion).
//
// The picture eats the effect's whole span; the sound eats only as many
// seconds as the effect LASTS on screen, because it is reading at 1× and the
// screen is all the time it has.
func sndDebt(f cutFx) float64 {
	if f.Kind != "speed" || f.Dur <= 0 {
		return 0
	}
	out := 0.0
	for _, st := range speedSteps(f) {
		out += st.outLn()
	}
	if out <= 0 {
		return 0
	}
	return f.Dur - out
}

// sndTail is the stretch of the timeline where an effect's sound is still out
// of step with the picture AFTER the effect's own seconds are over, and by how
// much. Only "until the scene ends" has one -- the other answer closes the gap
// on the effect's own last frame -- and it is what the lane draws past the end
// of the band (cut_fx.go), because this is the one effect on the page whose
// reach is not its own bar.
func (ed *cutEditor) sndTail(f cutFx) (float64, float64, float64, bool) {
	if f.Kind != "speed" || f.sound() != sndScene {
		return 0, 0, 0, false
	}
	debt := sndDebt(f)
	if math.Abs(debt) < 0.05 {
		return 0, 0, 0, false // ×1 with a choice on it: nothing comes apart
	}
	end := f.T + f.Dur
	i := ed.segAt(end - 1e-6)
	if i < 0 {
		if i = ed.segAt(f.T); i < 0 {
			return 0, 0, 0, false // the effect is over footage the cut drops
		}
	}
	if ed.segs[i].E <= end+0.05 {
		return 0, 0, 0, false // the scene ends with the effect: nothing to run on into
	}
	return end, ed.segs[i].E, debt, true
}

// sndDip is how long the sound takes to fade down into a close and back up out
// of it. Short enough not to be heard as a fade, long enough not to click.
const sndDip = 0.15

// sndNote is the line under the dropdown: what the answer in the box costs at
// the rate in the box. The arithmetic is the whole of what makes these five
// answers different from each other, and it is arithmetic nobody should be
// asked to do while deciding.
func sndNote(kind string, rate, dur float64) string {
	if rate <= 0 {
		if kind == sndMute {
			return ""
		}
		// applied(): the footage under a still runs at 1× whatever the rate
		// says, so the picture is the only thing standing still
		return "A stop's footage runs on at 1× under the held frame, so every answer " +
			"but Silent sounds the same here."
	}
	if !sndShifts(kind) || dur <= 0 {
		return ""
	}
	debt := dur - dur/rate
	switch {
	case math.Abs(debt) < 0.05:
		return ""
	case debt > 0:
		return fmt.Sprintf("%.0f s on screen: the sound ends %.0f s behind the picture, "+
			"and going back in sync skips those seconds.", dur/rate, debt)
	default:
		return fmt.Sprintf("%.0f s on screen: the sound runs %.0f s ahead of the picture, "+
			"and going back in sync plays those seconds again.", dur/rate, -debt)
	}
}

// drawSndTail marks how far past its own bar an effect's sound reaches.
//
// This is the one effect on the page whose reach is not its band. "At 1× until
// the scene ends" leaves the sound running over the clips that follow, out of
// step with them by a fixed number of seconds, and none of that is visible
// anywhere: the bar stops where the picture stops speeding up. So the bar
// grows a tail -- a thin line at the height of the band, to the second the
// sound goes back in sync, with how far apart the two are written on it.
//
// Drawn from inside drawFxLane's translation, so x is timeline px like
// everything around it.
func (ed *cutEditor) drawSndTail(cr *cairo.Context, f cutFx, y float64) {
	t0, t1, debt, ok := ed.sndTail(f)
	if !ok {
		return
	}
	x0, x1 := ed.xOf(t0), ed.xOf(t1)
	if x1-x0 < 2 {
		return
	}
	mid := y + fxLaneH/2
	cr.SetSourceRGBA(0.92, 0.42, 0.6, 0.75)
	cr.SetLineWidth(1)
	cr.SetDash([]float64{3, 3}, 0)
	cr.MoveTo(x0, mid)
	cr.LineTo(x1, mid)
	cr.Stroke()
	cr.SetDash(nil, 0)
	// a stop at the far end, where the sound goes back in sync: the tail has
	// an end and the end is the point of it
	cr.MoveTo(x1-0.5, y+3)
	cr.LineTo(x1-0.5, y+fxLaneH-3)
	cr.Stroke()
	if x1-x0 < 60 {
		return // no room for the number; the line still says how far
	}
	word := fmt.Sprintf("sound %.0fs behind", debt)
	if debt < 0 {
		word = fmt.Sprintf("sound %.0fs ahead", -debt)
	}
	markPlate(cr, x0+4, y+fxLaneH-4, "snd", word)
}
