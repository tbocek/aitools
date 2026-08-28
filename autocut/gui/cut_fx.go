package main

// The camera, the clock and the words: effects a cut can carry beyond which
// seconds it keeps.
//
// Three kinds, one list. A "zoom" says which part of the picture the finished
// video shows -- the tool that makes a vertical short out of widescreen
// footage, where somebody has to say which slice of the frame the action is
// in, and say it again when the action moves. When its seconds are up the
// camera either comes back out on its own or stays on the region until the
// next zoom (Stay), which is the whole difference between a passing close-up
// and a reframing that holds. A "speed" is the clock instead of the camera: footage put on a
// rate of its own -- slowed to a crawl, or run up to a hundred times faster so
// a twenty-minute stretch of nothing passes in a few seconds -- or one frame
// held still for a moment. A "text" is words over the picture for a while, in
// a box drawn on it (fxtext.go, which is also what draws them).
//
// None of them touch the segments. The cut says WHAT is shown; the effects say
// HOW -- where the camera is, how fast the clock runs, what is written over it
// -- and the two lists edit independently: trimming a scene does not move the
// camera, and moving the camera does not re-cut the scene. An effect whose
// moment is cut out of the footage simply never fires (the render walks the
// kept segments; see applyFx, buildCam and textCues).
//
// The rectangle a zoom points the camera at is stored normalized --
// centre as a fraction of the source frame, height as a fraction of the source
// height -- so it survives the source being probed at different sizes, and so
// the same numbers drive the preview overlay (cut_fxview.go) and the render
// (produce_fx.go). Its WIDTH is never stored: a camera rectangle is always
// exactly the cut's aspect ratio, so the width is hf*sh*A px by construction
// and cannot drift out of shape. A text's box is normalized too, but against
// the OUTPUT frame and with a width of its own -- see the header of fxtext.go
// for why the two rectangles cannot be the same kind of thing.

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/diamondburned/gotk4/pkg/cairo"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// cutFx is one effect. Which fields mean anything depends on Kind; everything
// is omitempty so a cut without effects writes exactly the cut.json it always
// wrote.
type cutFx struct {
	Kind string  `json:"kind"` // "zoom", "speed", "text" or "svg"
	T    float64 `json:"t"`    // session time it happens at
	// Every kind reads these two the same way: the seconds the effect fades
	// in at its start and out at its end, both inside Dur. For a zoom that is
	// the camera gliding to the region and back off it, for a speed the clock
	// ramping to the rate and back, for a text and a stop the words and the
	// still fading up and down. 0 either side is a hard cut.
	Trans float64 `json:"trans,omitempty"`
	Tout  float64 `json:"tout,omitempty"`
	// The shape both fades travel in: "" is the straight ramp, which is the
	// only shape anything here draws (fxEases). Stored by name rather than by
	// number so a cut written by a version that knows a curve this one does
	// not still reads back as the effect it was.
	Ease string `json:"ease,omitempty"`
	// How long the effect runs, its fades included -- the bar on the lane,
	// for every kind. speed: the session seconds it covers, and for a stop
	// (Rate 0) the seconds the still stands over footage that keeps running,
	// so the cut is the same length with or without it.
	Dur float64 `json:"dur,omitempty"`
	// zoom: what the camera does when its seconds are up. false pulls back to
	// where the camera was before it; true stays on the region until the next
	// zoom says otherwise -- the reframing a vertical cut is made of.
	Stay bool `json:"stay,omitempty"`
	// zoom: the camera rectangle. Centre as a fraction of the source
	// frame's width and height; Hf is rect height over source height. May
	// reach past the edges (that is zooming OUT past the frame; the render
	// pads with black), and may exceed 1 (wider shot than the frame is tall).
	// text: the box the words are fitted into -- and these four are fractions
	// of the OUTPUT frame instead, so the words stay put while the camera
	// moves under them (fxtext.go).
	Cx float64 `json:"cx,omitempty"`
	Cy float64 `json:"cy,omitempty"`
	Hf float64 `json:"hf,omitempty"`
	// text: the box's width, output frames. Only text has one: a camera
	// window's width is its height times the cut's aspect and is never stored.
	Wf float64 `json:"wf,omitempty"`
	// speed: 1 is the footage's own clock, 0.5 is half speed, 8 is eight
	// times faster. 0 is the whole of the stop effect: the picture stands
	// still on the frame at T while the footage runs on underneath, so the
	// cut is the same length with or without it.
	Rate float64 `json:"rate,omitempty"`
	// speed at rate 0: what the sound does under the still. false -- the
	// default, and what every cut written before this asked for -- leaves it
	// running at its own speed, so the still fades out onto footage that is
	// where the clock says it is. true takes those seconds out, for a stop
	// that is meant to be a held beat rather than a held frame. At any other
	// rate the sound follows the picture and this says nothing.
	Mute bool `json:"mute,omitempty"`
	// text: the words. Newlines are kept and always break a line; everything
	// else is wrapped to the box.
	Text string `json:"text,omitempty"`
	// svg: the drawing's file, on this machine. A drawing is the other thing
	// that goes OVER the picture rather than into it (fxsvg.go), so it reads
	// the four box fields above exactly as a text does; this is only where
	// the ink comes from. Its own file rather than Text, because the two
	// answer different questions and a path that is sometimes words is a bug
	// waiting for the first caption that looks like a filename.
	Src string `json:"src,omitempty"`
}

const (
	// fxMaxRate is as fast as the clock goes. A hundred times is a minute of
	// footage every 0.6 seconds -- past that a stretch worth marking at all
	// comes out shorter than the frames it is made of.
	fxMaxRate = 100.0
	// fxMinRate is the other end: a twentieth, past which the sound stops
	// being sound.
	fxMinRate = 0.05
	// fxMinPlay is how little of the finished video a speed may come out as.
	// It is the render's own floor (minClipLn) rather than a number of its
	// own: below it the clip is not a fast stretch, it is a stretch of footage
	// that never reaches the video. It used to be less than the render's, so
	// anything past about ×2 over a short band was dropped in silence.
	fxMinPlay = minClipLn
)

// clampSpeed keeps a speed inside what the render can make of it: a rate
// between fxMinRate and fxMaxRate, over a stretch that still lasts fxMinPlay
// on screen once the rate has had it. When those two fight it is the RATE that
// gives way -- the seconds are what the user marked and can see.
func clampSpeed(rate, dur float64) (float64, float64) {
	dur = math.Max(minClipLn, dur)
	rate = math.Max(fxMinRate, math.Min(fxMaxRate, rate))
	if dur/rate < fxMinPlay {
		rate = dur / fxMinPlay
	}
	return rate, dur
}

// fxSpan is the stretch of session time an effect owns on the timeline, and it
// is the same stretch for every kind: T to T+Dur, fades included. The bar on
// the lane IS the seconds the effect is doing something -- the camera away
// from where it was, the clock off its own rate, the words or the still on
// screen -- which is what makes two 3 s effects the same width whatever their
// transitions.
func (f cutFx) fxSpan() (float64, float64) {
	switch f.Kind {
	case "zoom", "speed", "text", "svg":
		return f.T, f.T + math.Max(f.Dur, 0)
	}
	return f.T, f.T
}

func (f cutFx) frozenFx() bool { return f.Kind == "speed" && f.Rate <= 0 }

// fxLabel is how an effect introduces itself in the status line and the lane.
func (f cutFx) fxLabel() string {
	switch f.Kind {
	case "zoom":
		var extra []string
		if f.Trans > 0 {
			extra = append(extra, fmt.Sprintf("%.1fs in", f.Trans))
		}
		if !f.Stay && f.Tout > 0 {
			extra = append(extra, fmt.Sprintf("%.1fs out", f.Tout))
		}
		if f.Stay {
			extra = append(extra, "stays")
		}
		if len(extra) > 0 {
			return fmt.Sprintf("zoom at %s for %.1fs (%s)", mmss(f.T), f.Dur, strings.Join(extra, ", "))
		}
		return fmt.Sprintf("zoom at %s for %.1fs", mmss(f.T), f.Dur)
	case "speed":
		if f.frozenFx() {
			var extra []string
			if f.Trans > 0 || f.Tout > 0 {
				extra = append(extra, fmt.Sprintf("%.1fs in", math.Max(f.Trans, 0)),
					fmt.Sprintf("%.1fs out", math.Max(f.Tout, 0)))
			}
			if f.Mute {
				extra = append(extra, "silent")
			}
			if len(extra) > 0 {
				return fmt.Sprintf("stop at %s for %.1fs (%s)",
					mmss(f.T), f.Dur, strings.Join(extra, ", "))
			}
			return fmt.Sprintf("stop at %s for %.1fs", mmss(f.T), f.Dur)
		}
		verb := "slowed"
		if f.Rate > 1 {
			verb = "sped up"
		}
		ramp := ""
		if f.Trans > 0 || f.Tout > 0 {
			ramp = fmt.Sprintf(" (%.1fs in, %.1fs out)", math.Max(f.Trans, 0), math.Max(f.Tout, 0))
		}
		return fmt.Sprintf("%s %s ×%s for %.1fs%s", mmss(f.T), verb, fxNum(f.Rate), f.Dur, ramp)
	case "text":
		return fmt.Sprintf("text %s at %s for %.1fs", quoteFirst(f.Text, 24), mmss(f.T), f.Dur)
	case "svg":
		return fmt.Sprintf("svg %s at %s for %.1fs", svgName(f), mmss(f.T), f.Dur)
	}
	return "effect"
}

// quoteFirst is the opening of a text effect, for a label that has to fit on a
// status line: one line of it, cut at n characters with an ellipsis, quoted so
// an empty one still reads as something rather than as a gap.
func quoteFirst(s string, n int) string {
	s = strings.Join(strings.Fields(strings.ReplaceAll(s, "\n", " ")), " ")
	if r := []rune(s); len(r) > n {
		s = strings.TrimSpace(string(r[:n])) + "…"
	}
	return "“" + s + "”"
}

// ---- aspect -----------------------------------------------------------------

// fxAspects is what the toolbar offers. "source" is the absence of a choice:
// the cut comes out the shape the footage is, which is what every cut did
// before this control existed.
var fxAspects = []string{"source", "9:16", "1:1", "4:5", "16:9"}

// parseAspect turns "9:16" into 9.0/16, or 0 for "source"/"" -- 0 meaning "the
// footage's own", which nothing here needs a number for.
func parseAspect(s string) float64 {
	var w, h float64
	if n, _ := fmt.Sscanf(s, "%f:%f", &w, &h); n == 2 && w > 0 && h > 0 {
		return w / h
	}
	return 0
}

// ---- where the camera is ----------------------------------------------------

// fxRect is a camera rectangle, normalized to the source frame (see cutFx).
type fxRect struct{ cx, cy, hf float64 }

func lerpRect(a, b fxRect, k float64) fxRect {
	k = math.Min(1, math.Max(0, k))
	return fxRect{a.cx + (b.cx-a.cx)*k, a.cy + (b.cy-a.cy)*k, a.hf + (b.hf-a.hf)*k}
}

// fullFill is the framing nobody has to place: the output frame carved out of
// the middle of the source at the footage's own scale.
// A portrait cut of widescreen footage gets the full height with the sides
// cropped; the other way round gets the full width with top and bottom
// cropped. NOT a letterbox -- the picture stays its own size and a zoom is
// how the user picks WHICH slice, not whether there is one. srcA and outA
// are width over height; the same shape in and out is the exact frame.
func fullFill(srcA, outA float64) fxRect {
	if srcA <= 0 || outA <= 0 {
		return fxRect{0.5, 0.5, 1}
	}
	return fxRect{0.5, 0.5, math.Min(1, srcA/outA)}
}

func (r fxRect) rect() (cx, cy, hf float64) { return r.cx, r.cy, r.hf }

// fxRectAt is where the camera is at session time t. This one function answers
// for the preview overlay and for every breakpoint of the render's camera path
// (produce_fx.go), so the two cannot disagree.
//
// The zooms chain. Each one glides from wherever the camera actually was at
// its T -- mid-move included, so a zoom placed inside another's glide starts
// from the picture that is on screen rather than teleporting -- to its own
// rectangle, holds it for the rest of its seconds, and then either comes back
// off it over the fade out to the framing it departed from, or (Stay) keeps
// it: a staying zoom becomes the framing every later zoom departs from and
// returns to. Between zooms the camera is parked on that settled framing,
// which begins as the whole frame (fullFill).
func fxRectAt(fx []cutFx, t float64, srcA, outA float64) fxRect {
	return camRectAt(zoomsOf(fx), t, srcA, outA)
}

// camRectAt is fxRectAt over the camera effects alone, already in order.
func camRectAt(zooms []cutFx, t float64, srcA, outA float64) fxRect {
	settled := fullFill(srcA, outA)
	for _, z := range zooms {
		if z.Stay {
			// the earliest staying zoom frames the video from the very
			// beginning: the centred crop only stands while no region has
			// been chosen at all, so the opening seconds are never stuck on
			// the default slice just because the region was framed a minute
			// in. Its own glide in is then a no-op, there being nowhere else
			// to glide from, which is exactly what it should be.
			settled = fxRect{z.Cx, z.Cy, z.Hf}
			break
		}
	}
	// the zoom being walked: where it goes, where it came from, where it goes
	// back to, and its shape in time
	to, from, back := settled, settled, settled
	t0, trans, dur, tout, stay := math.Inf(-1), 0.0, 0.0, 0.0, true
	at := func(tt float64) fxRect {
		if trans > 0 && tt < t0+trans {
			return lerpRect(from, to, (tt-t0)/trans) // still arriving
		}
		if !stay {
			if tt >= t0+dur {
				return back
			}
			// held for its seconds and now letting go: the same glide as the
			// way in, run backwards to where the camera came from
			if tout > 0 {
				if u := (tt - (t0 + dur - tout)) / tout; u > 0 {
					return lerpRect(to, back, u)
				}
			}
		}
		return to
	}
	for _, z := range zooms {
		if z.T > t {
			break
		}
		tin, tback := z.zoomGlides()
		// at(z.T) and not `to`: a zoom placed while the one before it is
		// still moving starts from where the camera actually is, so the two
		// never fight over the same second
		from, back = at(z.T), settled
		to = fxRect{z.Cx, z.Cy, z.Hf}
		t0, trans, dur, tout, stay = z.T, tin, math.Max(z.Dur, 0), tback, z.Stay
		if stay {
			settled = to
		}
	}
	return at(t)
}

// clampFades holds an effect's two fades inside its own band: neither below
// zero, and never together longer than the seconds they have to happen in.
// Asked for more than there is, both give way in proportion -- cutting one
// side back to nothing and leaving the other at its full length would turn a
// fade the hand drew as symmetric into a lopsided one, for a reason nothing
// on screen explains.
//
// This is the one fade rule, and every place that sets a fade goes through it:
// the three dialogs, the drag that resizes a band, and the pass that holds a
// suggestion to the final cut. That matters because zoomGlides, textFades and
// speedRamps all clamp again at render time -- so a number stored outside the
// band is a number the dialog shows you and the render quietly ignores.
func clampFades(f *cutFx) {
	f.Trans = math.Max(f.Trans, 0)
	f.Tout = math.Max(f.Tout, 0)
	dur := math.Max(f.Dur, 0)
	if sum := f.Trans + f.Tout; sum > dur {
		if sum <= 0 {
			return // both already zero: nothing to share out
		}
		k := dur / sum
		f.Trans, f.Tout = f.Trans*k, f.Tout*k
	}
}

// trimFades is what a band being cut shorter does to the two fades inside it,
// where was is the seconds it covered before. They shrink with it, in the same
// proportion, so the effect keeps the shape it was given: two seconds of glide
// on a six-second zoom is a third of it spent arriving, and a third of it is
// what it stays when the cut underneath leaves only three seconds.
//
// Holding them at their old lengths instead is what made a trimmed effect stop
// working. Six seconds cut to two, and 2/2 fills the whole band -- the camera
// arrives and leaves again having never once held the region it was placed to
// show. Scaling keeps whatever hold there was, because a shape with a hold in
// the middle still has one at half the size.
func trimFades(f *cutFx, was float64) {
	if was > 0 && f.Dur < was {
		k := math.Max(f.Dur, 0) / was
		f.Trans, f.Tout = f.Trans*k, f.Tout*k
	}
	clampFades(f)
}

// zoomGlides is the in and out glide a zoom actually gets: each at least 0,
// together no longer than the zoom itself, the way in winning any overlap.
func (z cutFx) zoomGlides() (tin, tout float64) {
	dur := math.Max(z.Dur, 0)
	tin = math.Min(math.Max(z.Trans, 0), dur)
	tout = math.Min(math.Max(z.Tout, 0), dur-tin)
	return
}

// zoomsOf is the cut's camera effects, in the order the camera obeys them.
func zoomsOf(fx []cutFx) []cutFx {
	var out []cutFx
	for _, f := range fx {
		if f.Kind == "zoom" {
			out = append(out, f)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].T < out[j].T })
	return out
}

// firstStay reports whether f is, or would become, the cut's earliest STAYING
// zoom -- the one the opening rule pins to the very beginning. Its glide in
// has nowhere to travel from, so it has no arrival to speak of. Works for a
// zoom not yet placed (nothing earlier exists) and for one already in the list
// (itself is not earlier than itself).
func (ed *cutEditor) firstStay(f cutFx) bool {
	if f.Kind != "zoom" || !f.Stay {
		return false
	}
	for _, z := range zoomsOf(ed.fx) {
		if z.Stay && z.T < f.T-1e-9 {
			return false
		}
	}
	return true
}

// hasStay is whether the cut has been given a lasting framing at all -- one
// zoom that stays on its region. Without one the finished video is the plain
// centred slice of the recording from beginning to end.
func (ed *cutEditor) hasStay() bool {
	for _, f := range ed.fx {
		if f.Kind == "zoom" && f.Stay {
			return true
		}
	}
	return false
}

// migrateFx brings a cut.json written before the camera effects became one
// kind up to date. A "view" was a region the camera kept until the next one --
// a zoom that stays -- with its hold stored WITHOUT the glides; a view that
// glided back out to the whole frame was already a zoom that pulls back. The
// numbers move over so the finished video is frame for frame what it was.
func migrateFx(fx []cutFx) []cutFx {
	for i := range fx {
		f := &fx[i]
		if f.Kind != "view" {
			continue
		}
		f.Kind = "zoom"
		f.Dur = math.Max(f.Trans, 0) + math.Max(f.Dur, 0) + math.Max(f.Tout, 0)
		f.Stay = f.Tout <= 0
	}
	return fx
}

// fxHasCamera is whether anything would move or crop the picture: an aspect
// that is not the source's, or any zoom at all. It is the render's "is the
// whole camera machinery needed at all" question.
func fxHasCamera(aspect string, fx []cutFx) bool {
	if parseAspect(aspect) > 0 {
		return true
	}
	for _, f := range fx {
		if f.Kind == "zoom" {
			return true
		}
	}
	return false
}

// ---- the clock: speed effects on the segment list ---------------------------

// applyFx rewrites a render sequence (splitSpliced's output) with the speed
// effects in it: footage under one gets its Rate.
//
// Only footage changes, and only its clock. Cards keep their own clocks, a
// freeze is not here at all -- its still is an overlay on the picture
// (freezeCues), the footage under it running on untouched, so the segments
// have nothing to learn from it -- and an effect whose moment is not in any
// kept segment does nothing: the scene it was set in has been cut, and the
// effect waits (harmlessly, invisibly) for Undo to bring the scene back.
func applyFx(segs []cutSeg, fx []cutFx) []cutSeg {
	out := segs
	for _, st := range rateSpans(fx) {
		if r := st.applied(); math.Abs(r-1) > 1e-9 {
			out = rateSpan(out, st.t0, st.t1, r)
		}
	}
	return out
}

func speedsOf(fx []cutFx) []cutFx {
	var out []cutFx
	for _, f := range fx {
		if f.Kind == "speed" {
			out = append(out, f)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].T < out[j].T })
	return out
}

// rateStep is one constant-rate stretch of a speed effect.
type rateStep struct{ t0, t1, rate float64 }

// rampStep is how long one stair of a ramp lasts IN THE FINISHED VIDEO. It is
// not how much footage the stair covers, and the difference is the whole point:
// at ×8 a stair has to run through eight seconds of footage to last one second
// on screen, so the footage a ramp needs grows with the rate it ramps to.
//
// Measured in footage instead -- which is what it used to be -- every stair of
// a fast ramp came out under the render's floor and was dropped, so the ramp
// did not merely play wrong, the seconds under it went missing from the video.
//
// It is not a smoothness dial either. Finer stairs are clips too short to
// render, which is the same hole seen from the other side.
const rampStep = 0.6

// speedRamps is the seconds a speed effect actually spends ramping each way:
// under one stair rounds to none at all (see rampStep), and ramps asked to
// cover more than the effect does share it in proportion, the flat middle
// disappearing between them. The lane draws exactly these, so the triangles
// on the marker are the stairs the render makes and not the numbers as typed.
func (f cutFx) speedRamps() (in, out float64) {
	in, out = f.rampAsk()
	if in <= 0 && out <= 0 {
		return 0, 0
	}
	// and the staircase they imply has to be one the render will keep whole.
	// A dropped stair does not play at the wrong speed, it takes its footage
	// out of the video -- so a staircase that cannot be built entirely is not
	// built at all, and the effect falls back to the plain change of speed it
	// would have been with no ramps, which clampSpeed has already made sure
	// is long enough to render.
	for _, st := range f.stairs(in, out) {
		if (st.t1-st.t0)/st.rate < minClipLn-1e-9 {
			return 0, 0
		}
	}
	return in, out
}

// rampAsk is the ramps as asked for, held to what a stair costs: the arithmetic
// before the staircase is checked.
func (f cutFx) rampAsk() (in, out float64) {
	in, out = math.Max(f.Trans, 0), math.Max(f.Tout, 0)
	// the footage one stair costs at this rate. A single stair of a ramp to
	// ×R runs at √R -- the geometric middle of 1 and R -- so that is the rate
	// the price is set at, and a ramp that cannot pay it gets none.
	least := rampStep * math.Sqrt(math.Max(1, f.Rate))
	if in < least {
		in = 0
	}
	if out < least {
		out = 0
	}
	if in+out > f.Dur {
		k := f.Dur / (in + out)
		in, out = in*k, out*k
		// the share-out can leave a ramp under the price again
		if in < least {
			in = 0
		}
		if out < least {
			out = 0
		}
		return
	}
	// a flat middle too short to render is worse than no flat middle at all:
	// the ramps grow to meet where it was, rather than the render dropping it
	// and the seconds between the two ramps going missing
	if mid := f.Dur - in - out; mid > 0 && mid/f.Rate < minClipLn {
		k := f.Dur / (in + out)
		in, out = in*k, out*k
	}
	return
}

// speedSteps breaks a speed effect into the constant-rate stretches the render
// can actually make. ffmpeg holds one setpts and one atempo chain per clip, so
// a rate that changes over time has to be a staircase of clips rather than a
// curve, and this is where the curve is turned into stairs.
//
// The rates climb geometrically -- ×1 to ×4 passes through ×2, not ×2.5 --
// because speed is seen and heard in ratios: the halfway point of a doubling
// is another doubling, and a linear ramp lurches at the slow end and crawls at
// the fast one. Each stair is given the rate at its own middle, which is the
// closest a constant rate can sit to the curve it stands in for.
//
// How many stairs a ramp gets is set by its FASTEST end, because that is the
// stair that comes out shortest: at ×8 a stair needs eight times the footage
// to clear the render's floor, so a ramp to ×8 gets an eighth of the stairs a
// ramp to ×1 would over the same seconds. Asked for less than one, it gets one
// -- a single step at the middle rate, which is a coarse ramp but is footage
// that reaches the video.
func speedSteps(f cutFx) []rateStep {
	if f.Rate <= 0 || f.Dur <= 0 {
		return nil
	}
	in, out := f.speedRamps()
	if in <= 0 && out <= 0 {
		return []rateStep{{f.T, f.T + f.Dur, f.Rate}}
	}
	return f.stairs(in, out)
}

// stairs builds the staircase for ramps of exactly in and out seconds. It is
// split from speedSteps because speedRamps has to look at the stairs a pair of
// ramps would make before it can say whether those ramps happen at all, and a
// function that called speedRamps to answer that would be asking itself.
func (f cutFx) stairs(in, out float64) []rateStep {
	var steps []rateStep
	ramp := func(t0, d, from, to float64) {
		n := rampStairs(d, from, to)
		for i := 0; i < n; i++ {
			u := (float64(i) + 0.5) / float64(n)
			steps = append(steps, rateStep{
				t0: t0 + d*float64(i)/float64(n),
				t1: t0 + d*float64(i+1)/float64(n),
				// from·(to/from)^u: the geometric middle of the stair
				rate: from * math.Pow(to/from, u),
			})
		}
	}
	if in > 0 {
		ramp(f.T, in, 1, f.Rate)
	}
	if f.Dur-in-out > 1e-6 {
		steps = append(steps, rateStep{f.T + in, f.T + f.Dur - out, f.Rate})
	}
	if out > 0 {
		ramp(f.T+f.Dur-out, out, f.Rate, 1)
	}
	return steps
}

// rampStairs is how many stairs a ramp of d seconds of footage from rate from
// to rate to is cut into: as many as the render will keep whole.
//
// The stair that decides it is the one nearest the fast end, since that is the
// one whose output comes out shortest -- but it runs at ITS OWN rate, half a
// stair short of the ramp's top, and not at the top itself. The gap is widest
// where it hurts most: asked for one stair, that stair sits at the geometric
// middle, which at ×8 is ×2.83 and not ×8.
//
// Charged the top rate instead, an eight-second ramp to ×8 could afford a
// single stair where it can plainly afford two, so the "ramp" was one constant
// speed and the video stepped ×1, ×2.83, ×8 -- picture and sound both -- where
// it was meant to climb. Nothing about a slow ramp changes: at ×1 to ×0.5 the
// fastest stair IS very nearly the top rate, and the count comes out the same.
func rampStairs(d, from, to float64) int {
	n := 1
	for rampFits(d, from, to, n+1) {
		n++
	}
	return n
}

// rampFits asks whether all n stairs of a ramp clear rampStep on screen, by
// measuring the fastest of them: the last stair going up, the first coming
// down, and the same rate either way.
func rampFits(d, from, to float64, n int) bool {
	lo, hi := math.Min(from, to), math.Max(from, to)
	if lo <= 0 || n < 1 {
		return false
	}
	// lo·(hi/lo)^((n-½)/n): the middle of the stair that ends at hi
	fast := lo * math.Pow(hi/lo, (float64(n)-0.5)/float64(n))
	return (d/float64(n))/fast >= rampStep
}

// fxRateAt is the clock the footage at session time t runs on: what the speed
// effects covering it ask for between them, ramps included, or 1 where none
// does. It reads the same stretches applyFx applies, so the preview and the
// render agree on what a second of session time is worth.
//
// Frozen seconds come back as 1 rather than as the 0 they mean. Under a still
// the footage's speed cannot be seen and 0 is not a clip anything can build,
// so the footage runs on at full speed -- which is what lets the still fade
// out onto footage exactly where the clock says it is. fxMeanRate is the same
// answer with the 0 left in, for the things that need to know.
func fxRateAt(fx []cutFx, t float64) float64 {
	for _, st := range rateSpans(fx) {
		if t >= st.t0 && t < st.t1 {
			return st.applied()
		}
	}
	return 1
}

// rateSpan gives every piece of footage inside [t0, t1) the rate, splitting
// clips at the boundaries so the rest of them play at their own speed.
func rateSpan(segs []cutSeg, t0, t1, rate float64) []cutSeg {
	var out []cutSeg
	for _, s := range segs {
		if s.isInsert() || s.Dur > 0 || s.E <= t0 || s.S >= t1 {
			out = append(out, s)
			continue
		}
		if s.S < t0 {
			head := s
			head.E = t0
			out = append(out, head)
		}
		mid := s
		mid.S, mid.E, mid.Rate = math.Max(s.S, t0), math.Min(s.E, t1), rate
		out = append(out, mid)
		if s.E > t1 {
			tail := s
			tail.S = t1
			out = append(out, tail)
		}
	}
	return out
}

// ---- the effects on the page -------------------------------------------------
//
// Everything below is the editor's half: the lane under the video track where
// effects are seen and picked up, the toolbar controls that create them, and
// the dialogs that ask the one or two numbers each kind needs. The gestures
// are the timeline's own -- the right button picks an effect up exactly as it
// picks up a clip, a left drag slides a held one along the lane, ‹f/f› nudge
// it, Del removes it, Esc puts it down, and the Insert button reads ✎ Edit
// while one is held. New nouns, old verbs.

// fxLaneH is the height of the effects lane drawn under the picture band.
// Always there, even empty: a lane that appears when the first effect does
// would move the audio lanes down at the moment you are aiming at them.
//
// It is deliberately generous. A short effect is a bar a few pixels wide, so
// height is the only dimension it has left to be aimed at with; a lane sized
// to the long bands alone makes the short ones fiddly.
const fxLaneH = 26

// fxLaneTop is the lane's y inside the source-track area.
func (ed *cutEditor) fxLaneTop() float64 { return ed.picTop() + float64(ed.thumbHt) + 6 }

// fxHitLane is whether a press in the source-track area lands in the lane.
func (ed *cutEditor) fxHitLane(y float64) bool { return y >= ed.fxLaneTop() }

// fxPackMin is the shortest span the packing gives width to. A very short
// effect is nearly a point in time, so two of them a frame apart would pack
// into the same row and draw through each other -- which is the thing rows
// exist to stop. A floor of a few tenths of a second makes "these are two effects, not
// one" a fact about the cut rather than about how far in you happen to be
// zoomed.
const fxPackMin = 0.4

// fxRows gives every effect the row it is drawn in, and says how many rows the
// lane needs.
//
// One row per effect is the obvious answer to overlap and the wrong one: ten
// effects would be ten rows, nearly all of them empty nearly all of the way
// across, and one busy minute in an afternoon's cut would push the audio lanes
// off the bottom of the page. Rows are for OVERLAP, not for effects. An effect
// goes in the first row whose contents it does not touch, so everything that
// does not overlap shares row 0 and the lane grows only as deep as the deepest
// pile -- a cut whose effects do not collide looks exactly as it did before
// this existed, and a cut with one pair of overlapping bands pays two rows for
// it rather than N.
//
// Sorted by start and greedy is not a heuristic here, it is the answer: spans
// on a line are an interval graph, and first-fit in start order colours one
// with the fewest colours there are. So the lane is never deeper than the
// number of effects genuinely live at some one instant.
//
// The packing is in seconds rather than pixels so that a row is a fact about
// the cut and not about the view. Rows that reshuffled as the wheel turned
// would slide the thing you were aiming at out from under the pointer, and the
// lane height -- which everything below it sits under -- would change with the
// zoom.
func fxRows(fx []cutFx) ([]int, int) {
	rows := make([]int, len(fx))
	order := make([]int, len(fx))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		ta, _ := fx[order[a]].fxSpan()
		tb, _ := fx[order[b]].fxSpan()
		return ta < tb
	})
	var ends []float64 // how far right each row reaches so far
	for _, i := range order {
		t0, t1 := fx[i].fxSpan()
		t1 = math.Max(t1, t0+fxPackMin)
		r := 0
		for ; r < len(ends); r++ {
			if ends[r] <= t0 {
				break
			}
		}
		if r == len(ends) {
			ends = append(ends, 0)
		}
		ends[r], rows[i] = t1, r
	}
	return rows, max(1, len(ends))
}

// fxLaneHeight is what the lane asks the page for: one row's worth even when
// there is nothing in it, because a lane that appeared with the first effect
// would move everything under it at the moment you were aiming there.
func (ed *cutEditor) fxLaneHeight() float64 {
	_, n := fxRows(ed.fx)
	return float64(n) * fxLaneH
}

// fxSpanPx is where an effect is drawn and grabbed, in timeline px. Like
// spliceSpan: a thing with no width of its own still has to be a mouse
// target, so everything gets a floor.
func (ed *cutEditor) fxSpanPx(f cutFx) (float64, float64) {
	t0, t1 := f.fxSpan()
	x0, x1 := ed.xOf(t0), ed.xOf(t1)
	return x0, math.Max(x1, x0+10)
}

// fxBandSpan is the effect's band as DRAWN, in seconds -- fxSpan for every
// kind, now that a freeze's bar is the session seconds its still stands over.
func (ed *cutEditor) fxBandSpan(f cutFx) (float64, float64) {
	return f.fxSpan()
}

// which part of an effect's band a press has hold of.
const (
	fxWhole = iota
	fxStart
	fxEnd
)

// fxMinBand is how wide a bar must be drawn before its ends become handles.
// A short effect on a zoomed-out timeline is a sliver ten pixels across and is
// meant to be picked up and slid; if its ends were grips, every attempt to
// move one would stretch it instead. Ends are for things that are already
// bands on screen.
const fxMinBand = 30.0

// fxMinDur is the shortest a band may be dragged down to. A zoom or a title of
// no length does nothing, cannot be grabbed again, and looks exactly like the
// effect having been deleted -- so the hand that overshoots gets a very short
// effect rather than an invisible one.
const fxMinDur = 0.1

// fxMinSel is how much of the timeline has to be under the band before ⏩ Speed
// treats it as a chosen stretch rather than a click that slipped. It is not
// minSegLn: that asks whether something is worth keeping as a scene, and a
// fifth of a second of footage on a clock of its own is a perfectly good
// effect. It only asks whether the hand meant to drag.
const fxMinSel = 0.2

// fxPartAt is what a press at timeline-x px takes hold of on effect i.
func (ed *cutEditor) fxPartAt(i int, px float64) int {
	if i < 0 || i >= len(ed.fx) {
		return fxWhole
	}
	x0, x1 := ed.fxSpanPx(ed.fx[i])
	if x1-x0 < fxMinBand {
		return fxWhole
	}
	switch {
	case math.Abs(px-x0) <= edgeGrab:
		return fxStart
	case math.Abs(px-x1) <= edgeGrab:
		return fxEnd
	}
	return fxWhole
}

// resizeFxTo moves one end of the held effect's band to t and leaves the other
// end where it is.
//
// What changes is the length, never the transitions. The fades are seconds you
// chose for how the effect should arrive and leave -- dragging the end of the
// band means "hold it longer", not "take longer getting there" -- so Trans and
// Tout keep their numbers and Dur absorbs the whole change. Dragging the START
// moves T as well, because a band that begins later begins later.
//
// They keep their numbers only as far as the band still has room for them: a
// ten-second effect with two seconds of fade either side, dragged down to one
// second, cannot fade for four. clampFades shares the second out between them,
// so the band that ends up on the lane is the band the render draws.
//
// It snaps where a moved effect snaps -- the borders of the cut -- for the same
// reason: an effect that stops a third of a second before the clip does is a
// mistake nobody makes on purpose.
func (ed *cutEditor) resizeFxTo(end bool, t float64) {
	f := ed.heldFx()
	if f == nil {
		return
	}
	t0, t1 := ed.fxBandSpan(*f)
	t = math.Max(0, ed.snapFx(t))
	if end {
		t1 = math.Max(t, t0+fxMinDur)
	} else {
		t0 = math.Max(0, math.Min(t, t1-fxMinDur))
	}
	dur := math.Max(fxMinDur, t1-t0)
	if t0 == f.T && dur == f.Dur {
		return
	}
	if !ed.fxDirty {
		ed.pushUndo()
		ed.fxDirty = true
	}
	f.T, f.Dur = t0, dur
	clampFades(f)
	ed.fxStatus()
	ed.redrawTracks()
}

// heldFx is the effect being held, or nil. The counterpart of heldSeg.
func (ed *cutEditor) heldFx() *cutFx {
	if !ed.fxOn || ed.fxSel >= len(ed.fx) {
		return nil
	}
	return &ed.fx[ed.fxSel]
}

// fxHoldSlack is how far the line may stand from the effect it is holding
// before the hold is over: a frame and a bit, which is a seek landing on a
// keyframe rather than a hand going somewhere else.
const fxHoldSlack = 1.0 / 24

// syncFxHold ends the hold when the line has walked off the effect.
//
// Holding a zoom is what puts the WHOLE frame on the preview with
// the effect's box outlined on it: you are being shown everything the camera
// could see, so that you can aim it. That picture is only honest while the
// line is standing on the effect -- and holdFx puts it there. Once the line is
// somewhere else, the outline draws one moment's framing over another
// moment's frame, and worse, the framed preview stays off, so the framing
// actually in force at the line is not shown at all.
//
// That is the "it keeps the last effect" you get from picking up the last
// zoom, clicking back to the start of the timeline, and finding the box still
// on the right where that zoom left it, over footage the opening framing shows
// down the middle. A hand on the line is a hand off the effect -- the same
// rule a held clip and a held edge already follow when a click lands clear of
// them.
//
// Moving the effect is not the line leaving it. Both paths that do it write
// the new time onto the effect and then carry the line to it (nudgeFx and the
// drag along the lane, through showFx), so the line arrives back on the effect
// and the hold survives -- except mid-drag, where showFx is throttled and the
// line can lag the effect by a scrub interval. ed.fxMoving covers that gap.
func (ed *cutEditor) syncFxHold() {
	if ed.fxHoldLost() {
		ed.dropFx()
	}
}

// fxHoldLost is the question syncFxHold acts on, split out so it can be asked
// without a window to answer it in.
func (ed *cutEditor) fxHoldLost() bool {
	f := ed.heldFx()
	if f == nil || ed.fxMoving {
		return false
	}
	t0, t1 := f.fxSpan()
	return ed.playhead < t0-fxHoldSlack || ed.playhead > t1+fxHoldSlack
}

func (ed *cutEditor) dropFx() {
	if !ed.fxOn {
		return
	}
	ed.fxOn = false
	ed.syncInsertBtn()
	ed.redrawTracks()
}

// fxIndexAt is the effect under a press in the lane, or -1. The narrowest one
// wins, so a short effect sitting inside a longer one's band is still reachable.
func (ed *cutEditor) fxIndexAt(px, y float64) int {
	// only the row the press landed in. That is the whole benefit of rows: two
	// effects that overlap in time no longer have to be told apart by which of
	// them is narrower, because they are not in the same place any more.
	rows, n := fxRows(ed.fx)
	r := int((y - ed.fxLaneTop()) / fxLaneH)
	if r < 0 || r >= n {
		return -1
	}
	best, bw := -1, math.MaxFloat64
	for i, f := range ed.fx {
		if rows[i] != r {
			continue
		}
		x0, x1 := ed.fxSpanPx(f)
		if px >= x0 && px <= x1 && x1-x0 < bw {
			best, bw = i, x1-x0
		}
	}
	return best
}

// holdFx takes hold of effect i and puts the playhead on it.
//
// The playhead move is the point. A zoom says what the picture shows from its
// own moment on, and the overlay draws the held one on the preview -- so
// picking up a zoom half a minute away used to leave the framing of a moment
// you are not looking at drawn over the frame you are. With two zooms that
// reads as the first one never being shown at all. Landing on the zoom's own
// moment makes the rectangle and the picture under it the same instant again,
// which is also what the frame buttons do once it is in hand (showFx).
func (ed *cutEditor) holdFx(i int) {
	if i < 0 || i >= len(ed.fx) {
		return
	}
	ed.dropEdge() // one thing is held at a time, and this is now it
	ed.dropSeg()
	ed.fxOn, ed.fxSel, ed.fxDirty = true, i, false
	ed.setPlayhead(ed.fx[i].T)
	ed.fxStatus()
	ed.syncInsertBtn()
	ed.redrawTracks()
}

// hoverFx remembers which effect the pointer is over so the lane can say so.
// Off-lane, or clear of every marker, is "none"; (-1, -1) is the pointer
// having left the widget altogether.
//
// The lane is the only place on this page that answers the pointer without
// being pressed. It has to: an effect's marker is a few pixels wide, several
// of them can overlap, and fxIndexAt breaks the tie by span rather than by
// which one looks nearest. Showing the answer first turns a press into a
// choice instead of a guess.
func (ed *cutEditor) hoverFx(x, y float64) {
	i := -1
	if x >= 0 && ed.fxHitLane(y) {
		i = ed.fxIndexAt(x+ed.viewX, y)
	}
	if (i >= 0) == ed.fxHovOn && (i < 0 || i == ed.fxHov) {
		return // nothing the lane draws has changed
	}
	ed.fxHovOn, ed.fxHov = i >= 0, i
	ed.srcArea.QueueDraw()
}

// snapFxTime pulls an effect's moment onto the nearest border of the cut --
// the edges of the green -- or onto one of the extra marks, when the hand
// brings it within snap seconds of one. An effect is nearly always meant for
// a cut point: a zoom is the frame the next clip is seen in, and a framing
// that changes a third of a second into the clip is a mistake nobody makes on
// purpose. Nudging is left alone (see nudgeFx): a frame at a time is the tool
// for saying "not quite there".
func snapFxTime(segs []cutSeg, marks []float64, t, snap float64) float64 {
	best, bd := t, snap
	for _, s := range segs {
		for _, e := range [2]float64{s.S, s.E} {
			if d := math.Abs(t - e); d < bd {
				best, bd = e, d
			}
		}
	}
	for _, m := range marks {
		if d := math.Abs(t - m); d < bd {
			best, bd = m, d
		}
	}
	return best
}

// fxSnapMarks are the landmarks a moved effect lands on beyond the cut's own
// borders: the ends of every OTHER effect's band. Effects are aimed at each
// other as often as at cuts -- a title that comes up the moment the zoom
// lands, a stop that begins where the slow motion ends. The held one is left
// out because its own ends travel with the drag: offering them as marks would
// snag the band against itself wherever it happens to be.
func (ed *cutEditor) fxSnapMarks(except *cutFx) []float64 {
	out := make([]float64, 0, 2*len(ed.fx))
	for i := range ed.fx {
		if except != nil && &ed.fx[i] == except {
			continue
		}
		t0, t1 := ed.fx[i].fxSpan()
		out = append(out, t0)
		if t1 > t0 {
			out = append(out, t1)
		}
	}
	return out
}

// snapFx is snapFxTime at the current zoom: the tolerance is a fixed handful
// of pixels, so it is the same reach for the hand whatever the scale.
func (ed *cutEditor) snapFx(t float64) float64 {
	return snapFxTime(ed.segs, ed.fxSnapMarks(ed.heldFx()), t,
		snapPx/math.Max(ed.pps, 0.001))
}

// snapFxSpan is snapFx for a band slid along whole: BOTH ends are offered to
// the landmarks and the closer fit wins, the same bargain the selection band
// makes (snapSelSpan) -- flush against a neighbour on the left is worth as
// much as flush against one on the right.
func (ed *cutEditor) snapFxSpan(t, ln float64) float64 {
	tol := snapPx / math.Max(ed.pps, 0.001)
	marks := ed.fxSnapMarks(ed.heldFx())
	for _, s := range ed.segs {
		marks = append(marks, s.S, s.E)
	}
	best, bd := t, tol
	for _, m := range marks {
		if d := math.Abs(t - m); d < bd {
			best, bd = m, d
		}
		if d := math.Abs(t + ln - m); d < bd {
			best, bd = m-ln, d
		}
	}
	return best
}

func (ed *cutEditor) fxStatus() {
	f := ed.heldFx()
	if f == nil {
		return
	}
	hint := "drag it along the lane to move it (it snaps to the cuts and the other effects), " +
		"‹f and f› nudge it a frame, ⌦ removes it, ✎ Edit changes it; " +
		"a click clear of it puts it down"
	switch f.Kind {
	case "zoom":
		// the picture is the direct way at the framing: inside slides the
		// rectangle, its border scales it, clear of it draws a new one
		hint = "on the video drag inside the box to move it, its border to " +
			"resize it, or clear of it to draw a new one — " + hint
	case "text":
		hint = "on the video drag inside the box to move it or its border to " +
			"resize it, and the words are re-fitted as you go — " + hint
	case "svg":
		hint = "on the video drag inside the box to move it or its border to " +
			"resize it, and the drawing is re-fitted as you go — " + hint
	}
	ed.a.setStatus(f.fxLabel() + " picked up — " + hint)
}

// moveFxTo slides the held effect to session time t. The undo snapshot is
// taken on the first movement of the hold, so picking one up and putting it
// down is not an edit -- the same bargain the edges and clips make.
func (ed *cutEditor) moveFxTo(t float64, live bool) {
	f := ed.heldFx()
	if f == nil {
		return
	}
	t = math.Max(0, t)
	// and no further out than the session goes. Nudged a frame at a time an
	// effect used to walk clean off the end of the footage, where it is drawn
	// nowhere, holds nothing and can never be taken hold of again -- the whole
	// band has to stay on the timeline, not just the moment it starts.
	if end := ed.sessEnd(); end > 0 {
		t = math.Max(0, math.Min(t, end-math.Max(f.Dur, 0)))
	}
	if t == f.T {
		return
	}
	if !ed.fxDirty {
		ed.pushUndo()
		ed.fxDirty = true
	}
	f.T = t
	if live {
		ed.redrawTracks()
		return
	}
	ed.persist()
	ed.a.setStatus(f.fxLabel())
}

// showFx puts the red line on the held effect, the way showEdge and showSeg
// follow the thing they move. Without it a nudge was invisible: one frame is a
// fraction of a pixel on the lane and the label reads the same to the second,
// so the press looked like a dead button. The line moving -- and the preview
// coming with it -- is the whole of the feedback.
func (ed *cutEditor) showFx(live bool) {
	f := ed.heldFx()
	if f == nil {
		return
	}
	if live {
		if time.Since(ed.lastScrub) < scrubEvery {
			return
		}
		ed.lastScrub = time.Now()
	}
	ed.setPlayhead(f.T)
}

// nudgeFx is ‹f/f› with an effect held: one frame of the recording under it.
// It answers whether it moved anything, so a hold left pointing at an effect
// that is no longer there gives the press back to the playhead.
func (ed *cutEditor) nudgeFx(n int) bool {
	f := ed.heldFx()
	if f == nil {
		ed.fxOn = false
		return false
	}
	fps := 30.0
	if v := ed.videoAt(f.T); v != nil && v.fps > 0 {
		fps = v.fps
	}
	ed.moveFxTo(f.T+float64(n)/fps, false)
	ed.showFx(false)
	ed.fxStatus()
	return true
}

// removeHeldFx is ⌦ with an effect held.
func (ed *cutEditor) removeHeldFx() {
	f := ed.heldFx()
	if f == nil {
		return
	}
	was := f.fxLabel()
	ed.pushUndo()
	ed.fx = append(ed.fx[:ed.fxSel], ed.fx[ed.fxSel+1:]...)
	ed.dropFx()
	ed.persist()
	ed.a.setStatus("removed " + was + " — ↶ Undo takes it back")
}

// addFx puts a new effect in and holds it, so what was just made is the thing
// Remove, Edit and the overlay now talk about.
func (ed *cutEditor) addFx(f cutFx) {
	ed.pushUndo()
	ed.fx = append(ed.fx, f)
	ed.dropEdge()
	ed.dropSeg()
	ed.fxOn, ed.fxSel, ed.fxDirty = true, len(ed.fx)-1, false
	ed.persist()
	ed.syncInsertBtn()
}

// ---- the lane ---------------------------------------------------------------

// laneBand fills an effect's band with its own envelope: a triangle climbing
// from the lane floor across the way in, the full bar while the effect fully
// holds, and the mirror triangle across the way out. The full bar begins
// exactly where the transition ends, so two effects of the same length always
// read the same length -- what differs is how much of each is still arriving.
func laneBand(cr *cairo.Context, x0, x1, inW, outW, y float64) {
	inW = math.Max(0, math.Min(inW, x1-x0))
	outW = math.Max(0, math.Min(outW, x1-x0-inW))
	cr.MoveTo(x0, y+fxLaneH-2)
	cr.LineTo(x0+inW, y+2)
	cr.LineTo(x1-outW, y+2)
	cr.LineTo(x1, y+fxLaneH-2)
	cr.ClosePath()
	cr.Fill()
}

// drawFxLane paints the effects lane. Called from drawTrack inside its
// translation, so x here is timeline px like everything drawn around it.
func (ed *cutEditor) drawFxLane(cr *cairo.Context, vx0, vx1 float64) {
	top := ed.fxLaneTop()
	rows, nrows := fxRows(ed.fx)
	// the lane itself, one shade up from the page, as wide as the recordings
	cr.SetSourceRGB(0.165, 0.165, 0.175)
	for _, v := range ed.vids {
		cr.Rectangle(v.pxOrigin, top, v.dur*ed.pps, float64(nrows)*fxLaneH)
	}
	cr.Fill()
	// a hairline between the rows, so a lane two deep reads as two rows rather
	// than as one tall row with things floating in it
	if nrows > 1 {
		cr.SetSourceRGBA(1, 1, 1, 0.07)
		cr.SetLineWidth(1)
		for r := 1; r < nrows; r++ {
			for _, v := range ed.vids {
				cr.MoveTo(v.pxOrigin, math.Round(top+float64(r)*fxLaneH)+0.5)
				cr.LineTo(v.pxOrigin+v.dur*ed.pps, math.Round(top+float64(r)*fxLaneH)+0.5)
			}
		}
		cr.Stroke()
	}
	cr.SetFontSize(10)
	for i, f := range ed.fx {
		x0, x1 := ed.fxSpanPx(f)
		if x1 < vx0 || x0 > vx1 {
			continue
		}
		y := top + float64(rows[i])*fxLaneH
		switch f.Kind {
		case "zoom":
			// the same envelope every other effect wears: climbing across the
			// fade in, full while the camera holds the region, falling across
			// the fade back out. A staying zoom wears the reframing colour and
			// has no falling edge -- there is no way back from it -- so the
			// bar shows at a glance which of the two kinds of camera move it
			// is without anything having to be read.
			tin, tout := f.zoomGlides()
			r, g, b := 0.25, 0.72, 0.82
			if f.Stay {
				r, g, b, tout = 0.95, 0.62, 0.15, 0
			}
			cr.SetSourceRGBA(r, g, b, 0.32)
			laneBand(cr, x0, x1, tin*ed.pps, tout*ed.pps, y)
			cr.SetSourceRGB(r, g, b)
			cr.SetLineWidth(1.5)
			cr.MoveTo(x0, y+fxLaneH-2)
			cr.LineTo(x0, y+2)
			cr.LineTo(x1, y+2)
			cr.LineTo(x1, y+fxLaneH-2)
			cr.Stroke()
			if x1-x0 > 40 {
				mark, label := laneLabel(f, 0)
				markPlate(cr, x0+3, y+fxLaneH-4, mark, label)
			}
		case "speed":
			cr.SetSourceRGBA(0.92, 0.42, 0.6, 0.4)
			if f.frozenFx() {
				// the still's own arc, drawn exactly where it happens: rising
				// over the fade in, full while the frame stands, falling over
				// the fade out -- the same envelope a text's fades draw
				fin, fout := f.zoomGlides()
				laneBand(cr, x0, x1, fin*ed.pps, fout*ed.pps, y)
			} else {
				// the ramps in and out as the envelope they are: the rate
				// climbs across the first and falls back across the last,
				// and the full bar is only the stretch fully at the rate
				in, out := f.speedRamps()
				laneBand(cr, x0, x1, in*ed.pps, out*ed.pps, y)
			}
			cr.SetSourceRGB(0.92, 0.42, 0.6)
			cr.SetLineWidth(1)
			cr.Rectangle(x0, y+2, x1-x0, fxLaneH-4)
			cr.Stroke()
			if x1-x0 > 34 {
				mark, label := laneLabel(f, 0)
				markPlate(cr, x0+3, y+fxLaneH-4, mark, label)
			}
		case "text", "svg":
			// a bracket like a zoom's, in its own colour, with the opening
			// words -- or the drawing's file -- in it: the lane is where you
			// look for "which title is that one", and a bracket that says
			// only "text" answers nothing. The fill is the fades' own
			// envelope (zoomGlides clamps a text's fades exactly as it clamps
			// a zoom's glides: inside Dur, the way in winning any overlap).
			// Two colours for the two kinds of overlay, because on the lane
			// they are the same shape and only the colour tells them apart.
			fin, fout := f.zoomGlides()
			r, g, b := 0.6, 0.55, 0.95
			if f.Kind == "svg" {
				r, g, b = 0.4, 0.8, 0.5
			}
			cr.SetSourceRGBA(r, g, b, 0.3)
			laneBand(cr, x0, x1, fin*ed.pps, fout*ed.pps, y)
			cr.SetSourceRGB(r, g, b)
			cr.SetLineWidth(1.5)
			cr.MoveTo(x0, y+fxLaneH-2)
			cr.LineTo(x0, y+2)
			cr.LineTo(x1, y+2)
			cr.LineTo(x1, y+fxLaneH-2)
			cr.Stroke()
			if x1-x0 > 40 {
				mark, label := laneLabel(f, int((x1-x0-16)/5))
				markPlate(cr, x0+3, y+fxLaneH-4, mark, label)
			}
		}
		// the ends, when there are ends: a band wide enough to have a middle
		// has grips, and they are drawn so that what can be dragged looks like
		// it can be dragged. Narrow markers get none, which is also the honest
		// picture -- they have no ends to take hold of (see fxPartAt).
		if x1-x0 >= fxMinBand {
			cr.SetSourceRGBA(1, 1, 1, 0.5)
			cr.Rectangle(x0-1, y+2, 2, fxLaneH-4)
			cr.Rectangle(x1-1, y+2, 2, fxLaneH-4)
			cr.Fill()
		}
		// held is a solid white ring; merely under the pointer is a faint one.
		// Two weights rather than two colours, so "this is the one a press
		// would take" reads as a quieter version of "this is the one I have".
		switch {
		case ed.fxOn && i == ed.fxSel:
			cr.SetSourceRGBA(1, 1, 1, 0.9)
			cr.SetLineWidth(2)
			cr.Rectangle(x0-1, y+0.5, x1-x0+2, fxLaneH-1)
			cr.Stroke()
		case ed.fxHovOn && i == ed.fxHov:
			cr.SetSourceRGBA(1, 1, 1, 0.4)
			cr.SetLineWidth(1.5)
			cr.Rectangle(x0-1, y+0.75, x1-x0+2, fxLaneH-1.5)
			cr.Stroke()
		}
	}
}

// ---- toolbar ----------------------------------------------------------------

// setAspect points the editor and its dropdown at an aspect without treating
// it as an edit: reload, undo and revert come through here.
func (ed *cutEditor) setAspect(s string) {
	if s == "source" {
		s = ""
	}
	ed.aspect = s
	if ed.aspectDD != nil {
		pos := 0
		for i, a := range fxAspects {
			if a == s {
				pos = i
			}
		}
		ed.aspectMu = true
		ed.aspectDD.SetSelected(uint(pos))
		ed.aspectMu = false
	}
	if ed.fxArea != nil {
		ed.fxArea.QueueDraw()
	}
}

// aspectChanged is the dropdown being used by hand: an edit like any other,
// undoable and saved.
func (ed *cutEditor) aspectChanged(s string) {
	if s == "source" {
		s = ""
	}
	if s == ed.aspect {
		return
	}
	ed.pushUndo()
	ed.aspect = s
	ed.persist()
	if ed.fxArea != nil {
		ed.fxArea.QueueDraw()
	}
	if s == "" {
		ed.a.setStatus("aspect: the source's own — the video comes out the shape it was filmed")
		return
	}
	ed.a.setStatus(fmt.Sprintf("aspect %s — the whole frame fits inside it until you frame a "+
		"region (⊕ Zoom); the outline on the video is what the finished video shows", s))
}

// armFx puts the next drag on the video in charge of creating an effect.
func (ed *cutEditor) armFx(kind string) {
	if ed.player == nil || ed.playVideo == nil || !ed.hasPlay {
		ed.a.setStatus("click a track first — the effect needs a moment to happen at")
		return
	}
	if ed.fxArm == kind {
		ed.disarmFx() // the button again is "never mind"
		return
	}
	ed.fxArm = kind
	ed.syncFxCursor()
	// arming a zoom drops the live layer (the box is drawn on
	// everything the camera could see); arming an overlay keeps it up (the
	// box is drawn on the finished frame). Either way the layer must follow
	// NOW, not at the next redraw.
	ed.syncPreviewZoom()
	ed.a.setStatus(ed.fxArmWords())
}

// disarmFx puts the armed drag down again, from Cancel or from picking the
// same kind twice.
func (ed *cutEditor) disarmFx() {
	if ed.fxArm == "" {
		return
	}
	ed.fxArm = ""
	ed.syncFxCursor() // which is also where the column's note is taken down
	ed.syncPreviewZoom()
	ed.a.setStatus("cancelled")
}

// fxArmWords is what an armed drag is waiting for, in one paragraph: the same
// words the column shows and the status line says, so there is one wording to
// keep true rather than two.
func (ed *cutEditor) fxArmWords() string {
	when := "It starts at the red line and runs 3 s; the form that opens says how long."
	if t0, dur := ed.fxSpanNow(3); ed.fxMarked() {
		when = fmt.Sprintf("It covers the marked stretch — %s – %s, %.1f s — which the "+
			"form that opens can change.", mmss(t0), mmss(t0+dur), dur)
	}
	switch ed.fxArm {
	case "text":
		return "Drag the box the words go in — anywhere on the picture, any shape. " +
			"A click puts one across the lower third. " + when
	case "svg":
		return fmt.Sprintf("Drag the box %s goes in — anywhere on the picture, any "+
			"shape; the drawing keeps its own shape inside it. A click puts one "+
			"across the middle. %s", svgBase(ed.fxSrc), when)
	}
	return "Drag a box on the video: the picture zooms there and comes back out on " +
		"its own, or stays on it — the form that opens is where that is said. The " +
		"box keeps the cut's shape; let go near the full width or height to snap " +
		"to it. " + when
}

// fxSpanNow is where an effect placed now goes: the marked stretch if there is
// one -- the rule ⏩ Speed already follows -- and def seconds from the red line
// if there is not. Marking a stretch and then framing it is how the region a
// zoom holds gets said without typing its length twice.
func (ed *cutEditor) fxSpanNow(def float64) (float64, float64) {
	if t0, t1 := ed.selOrdered(); ed.fxMarked() {
		return t0, t1 - t0
	}
	return ed.playhead, def
}

// fxMarked is whether the band is worth placing an effect on. The same floor
// ⏩ Speed uses: a band a fifth of a second wide is a click that shivered.
func (ed *cutEditor) fxMarked() bool {
	t0, t1 := ed.selOrdered()
	return ed.sel.active && t1-t0 >= fxMinSel
}

// selOrdered is the band's ends the way round they are read, which is not the
// way round they were drawn if the drag went right to left.
func (ed *cutEditor) selOrdered() (float64, float64) {
	if ed.sel.t1 < ed.sel.t0 {
		return ed.sel.t1, ed.sel.t0
	}
	return ed.sel.t0, ed.sel.t1
}

// syncFxArm shows the armed drag in the column, and takes the note away again.
//
// Arming changes nothing else you can see. The pointer becomes a crosshair over
// the preview and the words that say why go to setStatus -- a dim, ellipsized
// line in the log header at the bottom of the window, which is not somewhere a
// hand looks. Pick ⊕ Zoom with a region marked and, from the chair, nothing
// happens at all: that is what it was reported as, and it was accurate.
//
// Called from syncFxCursor, which is the one thing every path that arms or
// disarms already calls -- including Esc and the release that places the
// effect, neither of which knows this note exists.
func (ed *cutEditor) syncFxArm() {
	if ed.formBox == nil {
		return // headless, or a page with no column yet
	}
	if ed.fxArm == "" {
		if ed.formArm != "" {
			ed.hideForm()
		}
		return
	}
	if ed.formArm == ed.fxArm {
		return // already saying it
	}
	title := map[string]string{"zoom": "⊕ Zoom", "text": "❝ Text", "svg": "▨ SVG"}[ed.fxArm]
	words := gtk.NewLabel(ed.fxArmWords())
	words.SetXAlign(0)
	words.SetWrap(true)
	body := gtk.NewBox(gtk.OrientationVertical, 8)
	body.Append(words)
	cancel := gtk.NewButtonWithLabel("Cancel")
	cancel.SetTooltipText("put the drag down again — Esc does the same")
	cancel.ConnectClicked(func() { ed.disarmFx() })
	foot := gtk.NewBox(gtk.OrientationHorizontal, 8)
	foot.SetHAlign(gtk.AlignEnd)
	foot.Append(cancel)
	// after the show, not before: showing takes the last form out, and if that
	// was this note its gone would clear the flag we had just set
	ed.showFormFoot(title, body, foot, func() { ed.formArm = "" })
	ed.formArm = ed.fxArm
}

// speedClicked is the ⏩ Speed entry: a stretch of footage put on a clock of
// its own -- including the clock that does not run. A stop is a speed of ×0
// and nothing else, which is why there is no separate entry for one: the same
// dialog, the same bar on the lane, one number apart.
//
// A marked stretch is the seconds it covers. With nothing marked it takes a
// couple of seconds from the line, which is the shape a stop is usually asked
// for -- stand on this frame, here.
func (a *App) speedClicked() {
	ed := a.ed
	t0, t1 := ed.selOrdered()
	marked := ed.fxMarked()
	if !marked {
		if !ed.hasPlay {
			a.setStatus("click a track or mark a stretch first — speed needs seconds to work on")
			return
		}
		t0, t1 = ed.playhead, ed.playhead+2
	}
	f := cutFx{Kind: "speed", T: t0, Dur: t1 - t0, Rate: 0.5}
	if !marked {
		f.Rate, f.Trans, f.Tout = 0, 0.5, 0.5 // a stop, faded on and off
	}
	a.askSpeedParams(f, true, func(f cutFx) {
		ed.addFx(f)
		ed.sel.active = false
		what := "the footage plays at that rate there and the cut gets longer or shorter to match"
		if f.frozenFx() {
			what = "the picture stands still there while the clock runs"
		}
		a.setStatus(f.fxLabel() + " — " + what + "; ↶ Undo takes it back")
	})
}

// editFx reopens the held effect's numbers -- the ✎ Edit button's other job.
func (a *App) editFx() {
	ed := a.ed
	f := ed.heldFx()
	if f == nil {
		return
	}
	// which effect this is, taken as a value: the form opens in the page's
	// column and not in a window over it (cut_form.go), so the timeline stays
	// live under it -- the hold can be moved to another effect, and an undo can
	// renumber the list, between opening the form and pressing Save.
	was := *f
	switch f.Kind {
	case "zoom":
		a.askZoomParams(was, false, func(nf cutFx) { ed.updateFx(was, nf) })
	case "speed":
		a.askSpeedParams(was, false, func(nf cutFx) { ed.updateFx(was, nf) })
	case "text":
		a.askTextParams(was, false, func(nf cutFx) { ed.updateFx(was, nf) })
	case "svg":
		a.askSvgParams(was, false, func(nf cutFx) { ed.updateFx(was, nf) })
	}
}

// indexOfFx finds an effect again by what it was, the way indexOfSeg finds a
// segment. Same reason, too: the form is not a window and does not hold the
// timeline still, so "the held one" at Save time may be a different effect
// entirely -- and writing a zoom's numbers onto the caption somebody clicked
// while the form was open is the one mistake here that cannot be seen happening.
func (ed *cutEditor) indexOfFx(want cutFx) int {
	for i, f := range ed.fx {
		if f.Kind == want.Kind && math.Abs(f.T-want.T) < 0.001 {
			return i
		}
	}
	return -1
}

// updateFx writes a form's answer back onto the effect the form was opened for,
// which is not necessarily the one held now.
func (ed *cutEditor) updateFx(was, nf cutFx) {
	i := ed.indexOfFx(was)
	if i < 0 {
		ed.a.setStatus("that effect is no longer in the cut — nothing was changed")
		return
	}
	ed.pushUndo()
	ed.fx[i] = nf
	ed.persist()
	ed.a.setStatus(nf.fxLabel())
}

// ---- dialogs ----------------------------------------------------------------

// fxWin is the form every effect dialog is: whatever rows the caller adds, and
// Cancel/OK pinned under them. The verb is on the suggested button.
//
// It was a modal window until the Cut page had a column to put it in
// (cut_form.go), and an effect is the thing that most wanted the change: the
// numbers being typed are seconds of a band that is drawn on a lane four inches
// below, and a window over the page is exactly what stopped that band being
// looked at while its length was being decided.
//
// The buttons go in the column's pinned foot rather than at the bottom of the
// form. In the column they had scrolled with it, and a Place button below the
// fold of a panel full of entry boxes is one nobody finds: the effect is typed
// out, Enter does nothing visible, and the kind reads as broken.
func (a *App) fxWin(title, verb string, rows []gtk.Widgetter, ok func()) {
	form := a.cutForm()
	if form == nil {
		return // no page, so no column and no effect to be editing
	}
	okB := gtk.NewButtonWithLabel(verb)
	okB.AddCSSClass("suggested-action")
	okB.ConnectClicked(func() { form.hideForm(); ok() })
	cancel := gtk.NewButtonWithLabel("Cancel")
	cancel.ConnectClicked(func() { form.hideForm() })
	btns := gtk.NewBox(gtk.OrientationHorizontal, 8)
	btns.SetHAlign(gtk.AlignEnd)
	btns.Append(cancel)
	btns.Append(okB)

	box := gtk.NewBox(gtk.OrientationVertical, 8)
	for _, r := range rows {
		box.Append(r)
	}
	form.showFormFoot(title, box, btns, nil)
}

// fxField is one question in a form: the label and the control that answers
// it, kept apart until the grid puts them in. Apart is the point -- a column
// of labels lines up with the column beside it only if the grid can see the
// labels themselves, and a label sealed inside a row box is a label the grid
// cannot measure.
type fxField struct {
	lbl *gtk.Label
	ctl gtk.Widgetter
}

// setSensitive greys the whole question, not half of it: a live label over a
// dead entry reads as a field that is simply refusing to be typed in.
func (f fxField) setSensitive(on bool) {
	f.lbl.SetSensitive(on)
	gtk.BaseWidget(f.ctl).SetSensitive(on)
}

// fxGrid lays the questions out in two halves: what the effect IS on the left
// -- its rate, its length -- and how it arrives and leaves on the right.
// Stacked in one column instead, five rows are taller than the panel they are
// in.
//
// A grid, and not two boxes of rows. Boxes could only line the labels up by
// right-aligning them and letting them eat the slack, which put every entry in
// the middle of its own half with a hand's width of nothing either side. The
// grid gives the labels a column as wide as the longest of them and hands the
// slack to the controls, so the two halves read as two halves instead of four
// scattered pieces.
func fxGrid(left, right []fxField) *gtk.Grid {
	g := gtk.NewGrid()
	g.SetRowSpacing(6)
	g.SetColumnSpacing(8)
	for c, col := range [][]fxField{left, right} {
		for r, f := range col {
			if c > 0 {
				f.lbl.SetMarginStart(18) // the gutter down the middle
			}
			g.Attach(f.lbl, c*2, r, 1, 1)
			g.Attach(f.ctl, c*2+1, r, 1, 1)
		}
	}
	return g
}

// fxRowLabel is a field's label, with the field's own words on both halves of
// it: the explanation is wanted while the pointer is over either one.
func fxRowLabel(text, tip string, ctl gtk.Widgetter) *gtk.Label {
	l := gtk.NewLabel(text)
	l.SetXAlign(0)
	if tip != "" {
		l.SetTooltipText(tip)
		gtk.BaseWidget(ctl).SetTooltipText(tip)
	}
	return l
}

// fxNumRow is a labelled number entry, the control these dialogs are mostly
// made of. Activate (Enter) is wired by the caller through the returned entry.
func fxNumRow(label, tip string, val float64) (fxField, *gtk.Entry) {
	e := gtk.NewEntry()
	e.SetText(strings.TrimSuffix(fmt.Sprintf("%.1f", val), ".0"))
	e.SetMaxWidthChars(6)
	e.SetInputPurpose(gtk.InputPurposeNumber)
	e.SetHExpand(true)
	return fxField{fxRowLabel(label, tip, e), e}, e
}

// fxEases are the shapes a fade can travel in, and there is one of them: every
// fade in this app is a straight ramp, in the preview and in the render alike.
// It is asked on the form regardless, because "what shape does it fade in" is
// a question the form was answering on your behalf without saying so -- and a
// curve added later appears in this list rather than quietly changing what
// every effect already placed does.
var fxEases = []string{"Linear"}

// fxEaseOf is the name a picked shape is stored under. The straight ramp
// stores as "", so a cut saved with this row on disk is byte for byte the cut
// that was saved before the row existed.
func fxEaseOf(i uint) string {
	if int(i) <= 0 || int(i) >= len(fxEases) {
		return ""
	}
	return strings.ToLower(fxEases[i])
}

func fxEaseIndex(name string) uint {
	for i, e := range fxEases {
		if strings.EqualFold(e, name) {
			return uint(i)
		}
	}
	return 0 // a shape this version has never heard of falls back to straight
}

// fxEaseRow is the fade-shape row, the same one in all four dialogs.
func fxEaseRow(f cutFx) (fxField, *gtk.DropDown) {
	dd := gtk.NewDropDownFromStrings(fxEases)
	dd.SetSelected(fxEaseIndex(f.Ease))
	dd.SetHExpand(true)
	return fxField{fxRowLabel("Fade curve",
		"the shape both fades travel in. Straight is all there is so far: the "+
			"camera, the words or the clock move by the same amount every frame "+
			"of the fade, start to finish", dd), dd}, dd
}

// fxRates are the speeds worth having on a list. The two ends of the range are
// what makes the list a list and not the whole answer: the useful slow rates
// are a handful and sit close together, while the fast ones run from "trim the
// dead air a bit" to a hundred, so anything not here is typed instead.
var fxRates = []float64{0, 0.25, 0.5, 0.75, 1, 1.5, 2, 4, 8, 20, 100}

func fxRateLabels() []string {
	out := make([]string, 0, len(fxRates)+1)
	for _, r := range fxRates {
		s := "×" + fxNum(r)
		switch r {
		case 0:
			s += " — stop"
		case 1:
			s += " — as filmed"
		}
		out = append(out, s)
	}
	return append(out, "Custom…")
}

func fxRateIndex(v float64) uint {
	for i, r := range fxRates {
		if math.Abs(r-v) < 1e-9 {
			return uint(i)
		}
	}
	return uint(len(fxRates)) // Custom…
}

// ratePick is the Speed × control: the rates off a list, and a typed box for
// the ones that are not on it. The box is only there for Custom -- shown
// beside every pick it would be a second answer to a question already
// answered, and the two would have to be kept agreeing with each other.
type ratePick struct {
	box *gtk.Box
	dd  *gtk.DropDown
	e   *gtk.Entry
}

func newRatePick(val float64, changed func()) *ratePick {
	p := &ratePick{
		box: gtk.NewBox(gtk.OrientationHorizontal, 6),
		dd:  gtk.NewDropDownFromStrings(fxRateLabels()),
		e:   gtk.NewEntry(),
	}
	p.e.SetText(fxNum(val)) // a rate is finer than the other rows' one decimal
	p.e.SetInputPurpose(gtk.InputPurposeNumber)
	p.e.SetMaxWidthChars(6)
	p.e.SetWidthChars(6)
	p.dd.SetHExpand(true)
	p.box.SetHExpand(true)
	p.box.Append(p.dd)
	p.box.Append(p.e)
	p.dd.SetSelected(fxRateIndex(val))
	p.e.SetVisible(p.custom())
	p.dd.Connect("notify::selected", func() {
		if i := p.dd.Selected(); int(i) < len(fxRates) {
			// so that switching to Custom starts from the rate on screen
			// rather than from whatever was typed three picks ago
			p.e.SetText(fxNum(fxRates[i]))
		}
		p.e.SetVisible(p.custom())
		changed()
	})
	p.e.ConnectChanged(func() { changed() })
	return p
}

func (p *ratePick) custom() bool { return int(p.dd.Selected()) >= len(fxRates) }

// rate is the number the control is showing, def for a Custom box with
// nothing usable typed in it.
func (p *ratePick) rate(def float64) float64 {
	if i := p.dd.Selected(); int(i) < len(fxRates) {
		return fxRates[i]
	}
	return fxNumOf(p.e, def)
}

// fxNum prints a number as short as it can without lying. The one decimal the
// other fields are shown with is too coarse for a rate -- a quarter speed that
// opens as 0.2 is a different effect from the one that was saved -- and %g
// turns a hundred into "1e+02" on a lane label two characters wide.
func fxNum(v float64) string {
	// FormatFloat always writes the point at this precision, so trimming
	// zeros can never eat the zeros of a round number like 100
	s := strings.TrimRight(strconv.FormatFloat(v, 'f', 2, 64), "0")
	return strings.TrimSuffix(s, ".")
}

func fxNumOf(e *gtk.Entry, def float64) float64 {
	if v, err := strconv.ParseFloat(strings.TrimSpace(e.Text()), 64); err == nil && v >= 0 {
		return v
	}
	return def
}

// askZoomParams asks a camera move's numbers: the fades either side, how long
// it lasts, and the one real choice a zoom makes -- whether the camera comes
// back off the region when its seconds are up or stays on it.
//
// The two are one effect because they are one gesture: draw a region, say how
// long. Whether the picture opens back out afterwards is a line in the dialog,
// not a second tool with its own button, its own lane colour and its own
// arithmetic to keep in step.
func (a *App) askZoomParams(f cutFx, isNew bool, ok func(cutFx)) {
	verb := "Save"
	if isNew {
		verb = "Place"
	}
	if f.Dur <= 0 {
		f.Dur = 3
	}
	gRow, g := fxNumRow("Fade in seconds",
		"how long the camera takes to arrive: 0 cuts straight to the region, "+
			"1 glides over a second", f.Trans)
	dRow, d := fxNumRow("Length seconds",
		"how long the camera move lasts altogether, fades included", f.Dur)
	oRow, o := fxNumRow("Fade out seconds",
		"how long it takes to come back off the region again: 0 cuts straight back", f.Tout)
	eRow, ec := fxEaseRow(f)

	back := gtk.NewCheckButtonWithLabel(
		"Pull back when it ends — the camera returns to the framing it came from")
	stay := gtk.NewCheckButtonWithLabel(
		"Stay on this region — it holds until the next zoom says otherwise")
	stay.SetGroup(back) // one group of two is a pair of radio buttons
	back.SetActive(!f.Stay)
	stay.SetActive(f.Stay)
	back.SetTooltipText("A passing close-up: the picture closes in, holds for its seconds " +
		"and opens back out on its own, leaving the rest of the video framed as it was.")
	stay.SetTooltipText("A reframing: from here on the finished video shows this region. " +
		"This is how a vertical short is made out of widescreen footage — say where the " +
		"action is, and say it again when it moves.")

	// the earliest staying zoom frames the video from its very start, so it
	// has nowhere to fade in FROM; and a camera that stays has no way back,
	// so it has no fade out. Both rows say so by going quiet, which is the
	// answer to "why has this effect only one of the two".
	first := a.ed.firstStay(cutFx{Kind: "zoom", T: f.T, Stay: true})
	note := gtk.NewLabel("")
	note.SetXAlign(0)
	note.SetWrap(true)
	note.AddCSSClass("dim-label")
	sync := func() {
		oRow.setSensitive(!stay.Active())
		gRow.setSensitive(!(stay.Active() && first))
		// the shape of a fade that does not happen: the earliest staying zoom
		// has neither fade, so the row that asks how they travel goes quiet
		// with them rather than sitting there live over two dead fields
		eRow.setSensitive(!(stay.Active() && first))
		msg := ""
		switch {
		case stay.Active() && first:
			msg = "This is the cut's earliest staying zoom: the finished video is framed " +
				"on this region from the very start, so there is nothing to fade in from. " +
				"Place one earlier to give this one a fade in."
		case stay.Active():
			msg = "A camera that stays has no way back, so it has no fade out."
		}
		note.SetText(msg)
		note.SetVisible(msg != "")
	}
	sync()
	back.ConnectToggled(sync)

	a.fxWin(fmt.Sprintf("Zoom at %s", mmss(f.T)), verb,
		[]gtk.Widgetter{fxGrid([]fxField{dRow}, []fxField{gRow, oRow, eRow}),
			back, stay, note}, func() {
			f.Trans = fxNumOf(g, f.Trans)
			f.Tout = fxNumOf(o, f.Tout)
			f.Ease = fxEaseOf(ec.Selected())
			f.Dur = math.Max(0.4, fxNumOf(d, f.Dur))
			f.Stay = stay.Active()
			clampFades(&f)
			ok(f)
		})
}

// askTextParams asks a text effect's four things: the words, how long they
// are on screen, and the two fades. The words get a real multi-line box --
// newlines are content here (they break a line, and nothing else does), so a
// single-line entry would quietly make them impossible to type.
func (a *App) askTextParams(f cutFx, isNew bool, ok func(cutFx)) {
	verb := "Save"
	if isNew {
		verb = "Place"
	}
	if f.Dur <= 0 {
		f.Dur = 3
	}
	tv := gtk.NewTextView()
	tv.SetWrapMode(gtk.WrapWordChar)
	tv.SetAcceptsTab(false) // Tab moves to the next field, as everywhere else
	tv.Buffer().SetText(f.Text)
	tv.SetTooltipText("what is written over the picture. The words are fitted to the box " +
		"you drew — a longer line comes out smaller, and Enter starts a new line")
	sc := gtk.NewScrolledWindow()
	sc.SetChild(tv)
	sc.SetSizeRequest(-1, 90)
	sc.SetPolicy(gtk.PolicyNever, gtk.PolicyAutomatic)
	sc.SetHasFrame(true)
	dRow, d := fxNumRow("Length seconds",
		"how long the words stay up altogether, fades included — the same "+
			"seconds their bar covers on the lane", f.Dur)
	iRow, i := fxNumRow("Fade in seconds",
		"how long they take to appear: 0 cuts them straight on", f.Trans)
	oRow, o := fxNumRow("Fade out seconds",
		"how long they take to go again: 0 cuts them straight off", f.Tout)
	eRow, ec := fxEaseRow(f)
	a.fxWin(fmt.Sprintf("Text at %s", mmss(f.T)), verb,
		[]gtk.Widgetter{sc, fxGrid([]fxField{dRow}, []fxField{iRow, oRow, eRow})}, func() {
			b := tv.Buffer()
			f.Text = b.Text(b.StartIter(), b.EndIter(), false)
			f.Dur = math.Max(0.3, fxNumOf(d, f.Dur))
			f.Trans = fxNumOf(i, f.Trans)
			f.Tout = fxNumOf(o, f.Tout)
			f.Ease = fxEaseOf(ec.Selected())
			clampFades(&f)
			ok(f)
		})
}

// laneWords is a text effect's opening, cut to what the lane's bracket has
// room for. n is a guess at the characters that fit; the label is drawn on a
// plate and a long one would run out over the effects beside it.
func laneWords(s string, n int) string {
	s = strings.Join(strings.Fields(strings.ReplaceAll(s, "\n", " ")), " ")
	if n < 3 {
		n = 3
	}
	if r := []rune(s); len(r) > n {
		return strings.TrimSpace(string(r[:n-1])) + "…"
	}
	return s
}

// askSpeedParams asks what the clock does over the seconds the effect covers.
// One dialog for the whole kind: ×0 is a stop, and everything about a stop --
// how long the frame stands, how it fades on and off, what the sound does --
// is this dialog with a zero typed into the first row.
//
// The rate is typed rather than picked off a list of three: the useful slow
// rates are a handful, but the fast ones run from "trim the dead air a bit" to
// 100×, and no list that short covers both ends.
func (a *App) askSpeedParams(f cutFx, isNew bool, ok func(cutFx)) {
	verb := "Save"
	if isNew {
		verb = "Place"
	}
	// the sound row below reads the rate, so the pick has to be able to tell
	// it something changed; sync is declared before the pick and assigned
	// after, because each of the two needs the other
	var sync func()
	rp := newRatePick(f.Rate, func() { sync() })
	rRow := fxField{fxRowLabel("Speed ×",
		"1 is the footage's own speed. Below it the footage is slowed (0.5 is half "+
			"speed, 0.25 a quarter); above it it runs fast (4 for a brisk walkthrough, "+
			"20 to skip through dead air, up to 100). 0 stops the picture altogether: "+
			"the frame at the start stands over footage that keeps running. Custom "+
			"takes any rate at all", rp.box), rp.box}
	lRow, l := fxNumRow("Length seconds",
		"the session seconds it covers, fades included — the same seconds its "+
			"bar covers on the lane", f.Dur)
	iRow, i := fxNumRow("Fade in seconds",
		fmt.Sprintf("how long it takes to get there instead of snapping to it: the "+
			"clock ramping up to the rate, or the still fading on over the moving "+
			"picture. 0 changes on one frame. A ramp needs about %.1fs of footage "+
			"for every × of the rate — %.1fs at ×4 — because each step of it has "+
			"to last long enough to render; under that it is treated as 0.",
			rampStep, 4*rampStep), f.Trans)
	oRow, o := fxNumRow("Fade out seconds",
		"how long it takes to come back at the end, on the same terms: the clock "+
			"easing back to normal speed, or the still fading off onto footage "+
			"that has moved on underneath", f.Tout)
	eRow, ec := fxEaseRow(f)
	// the sound under a stop. Nothing else in the dialog changes with the
	// rate, so this one row greys itself out when the rate is not 0 rather
	// than appearing and disappearing under the hand.
	snd := gtk.NewCheckButtonWithLabel("Keep playing the sound underneath")
	snd.SetActive(!f.Mute)
	snd.SetTooltipText("on, the footage's sound runs on at its own speed while the " +
		"picture stands still, so the still lifts off a moment that is where the " +
		"clock says it is. Off, those seconds are silent — a held beat rather " +
		"than a held frame")
	sync = func() { snd.SetSensitive(rp.rate(f.Rate) <= 0) }
	sync()
	a.fxWin(fmt.Sprintf("Speed %s – %s", mmss(f.T), mmss(f.T+f.Dur)), verb,
		[]gtk.Widgetter{fxGrid([]fxField{rRow, lRow}, []fxField{iRow, oRow, eRow}), snd}, func() {
			rate, dur := math.Max(0, rp.rate(f.Rate)), fxNumOf(l, f.Dur)
			f.Trans, f.Tout = math.Max(0, fxNumOf(i, f.Trans)), math.Max(0, fxNumOf(o, f.Tout))
			f.Ease = fxEaseOf(ec.Selected())
			if rate <= 0 {
				// a stop is not a rate: clampSpeed would hand it one, and the
				// fades belong inside the band the way a title's do
				f.Rate, f.Dur, f.Mute = 0, math.Max(0.5, dur), !snd.Active()
			} else {
				f.Rate, f.Dur = clampSpeed(rate, dur)
			}
			clampFades(&f)
			ok(f)
		})
}
