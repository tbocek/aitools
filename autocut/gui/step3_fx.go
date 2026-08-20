package main

// The camera, the clock and the words: effects a cut can carry beyond which
// seconds it keeps.
//
// Four kinds, one list. A "view" says which part of the picture the finished
// video shows from a moment on -- the tool that makes a vertical short out of
// widescreen footage, where somebody has to say which slice of the frame the
// action is in, and say it again when the action moves. A "zoom" is the same
// choice made temporarily: a region, a length, and the picture comes back out
// on its own. A "speed" is the clock instead of the camera: footage put on a
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
// The rectangle a view or zoom points the camera at is stored normalized --
// centre as a fraction of the source frame, height as a fraction of the source
// height -- so it survives the source being probed at different sizes, and so
// the same numbers drive the preview overlay (step3_fxview.go) and the render
// (step5_fx.go). Its WIDTH is never stored: a camera rectangle is always
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
	Kind string  `json:"kind"` // "view", "zoom", "speed" or "text"
	T    float64 `json:"t"`    // session time it happens at
	// view: seconds the camera glides from where it was (0 = a hard cut).
	// zoom: seconds it glides in; Tout the seconds it glides out again, its
	// own number (0 = a hard cut back). Both inside Dur.
	// text: seconds the words fade in and out again, both inside Dur.
	Trans float64 `json:"trans,omitempty"`
	Tout  float64 `json:"tout,omitempty"`
	// zoom: how long the zoom holds, glides included.
	// speed: the session seconds it covers -- or, for a freeze (Rate 0), how
	// long the finished video stands on the frame at T.
	// text: how long the words are on screen, fades included.
	Dur float64 `json:"dur,omitempty"`
	// view, zoom: the camera rectangle. Centre as a fraction of the source
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
	// times faster. 0 with Dur > 0 is a freeze.
	Rate float64 `json:"rate,omitempty"`
	// text: the words. Newlines are kept and always break a line; everything
	// else is wrapped to the box.
	Text string `json:"text,omitempty"`
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
	// The render drops clips shorter than a couple of frames, so a fast rate
	// over a short stretch would silently be no effect at all.
	fxMinPlay = 0.2
)

// clampSpeed keeps a speed inside what the render can make of it: a rate
// between fxMinRate and fxMaxRate, over a stretch that still lasts fxMinPlay
// on screen once the rate has had it. When those two fight it is the RATE that
// gives way -- the seconds are what the user marked and can see.
func clampSpeed(rate, dur float64) (float64, float64) {
	dur = math.Max(0.2, dur)
	rate = math.Max(fxMinRate, math.Min(fxMaxRate, rate))
	if dur/rate < fxMinPlay {
		rate = dur / fxMinPlay
	}
	return rate, dur
}

// fxSpan is the stretch of session time an effect owns on the timeline: the
// glide of a view, the whole arc of a zoom, the footage a speed covers. A
// freeze covers no session time (like a spliced card) and gets the same kind
// of point-marker treatment; the width here is its minimum.
func (f cutFx) fxSpan() (float64, float64) {
	switch f.Kind {
	case "view":
		return f.T, f.T + math.Max(f.Trans, 0)
	case "zoom":
		return f.T, f.T + f.Dur
	case "speed":
		if f.Rate > 0 {
			return f.T, f.T + f.Dur
		}
		return f.T, f.T // a freeze is a point; the lane draws it wider
	case "text":
		return f.T, f.T + f.Dur
	}
	return f.T, f.T
}

func (f cutFx) frozenFx() bool { return f.Kind == "speed" && f.Rate <= 0 }

// fxLabel is how an effect introduces itself in the status line and the lane.
func (f cutFx) fxLabel() string {
	switch f.Kind {
	case "view":
		if f.Trans > 0 {
			return fmt.Sprintf("view at %s (%.1fs glide)", mmss(f.T), f.Trans)
		}
		return fmt.Sprintf("view at %s", mmss(f.T))
	case "zoom":
		return fmt.Sprintf("zoom at %s for %.1fs", mmss(f.T), f.Dur)
	case "speed":
		if f.frozenFx() {
			return fmt.Sprintf("stop at %s for %.1fs", mmss(f.T), f.Dur)
		}
		verb := "slowed"
		if f.Rate > 1 {
			verb = "sped up"
		}
		return fmt.Sprintf("%s %s ×%s for %.1fs", mmss(f.T), verb, fxNum(f.Rate), f.Dur)
	case "text":
		return fmt.Sprintf("text %s at %s for %.1fs", quoteFirst(f.Text, 24), mmss(f.T), f.Dur)
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

// fullFit is the very first view, the one nobody has to place: the whole
// source frame inside the cut's aspect, black either side. srcA and outA are
// width over height; with outA narrower than the source (a short from
// widescreen) the rect is wider than the frame is tall, hf > 1.
func fullFit(srcA, outA float64) fxRect {
	if srcA <= 0 || outA <= 0 {
		return fxRect{0.5, 0.5, 1}
	}
	return fxRect{0.5, 0.5, math.Max(1, srcA/outA)}
}

func (r fxRect) rect() (cx, cy, hf float64) { return r.cx, r.cy, r.hf }

// fxRectAt is where the camera is at session time t: the settled or gliding
// view underneath, and any zoom that is live on top of it. This one function
// answers for the preview overlay and for every breakpoint of the render's
// camera path, so the two cannot disagree.
//
// Views chain: each one glides from wherever the camera actually was at its T
// -- mid-glide included -- to its own rectangle, and stays. Zooms borrow the
// camera: in over Trans, hold, and back out over Tout to wherever the views
// say the camera belongs by then.
func fxRectAt(fx []cutFx, t float64, srcA, outA float64) fxRect {
	views := viewsOf(fx)
	base := viewRectAt(views, t, srcA, outA)
	// the last zoom whose arc covers t is the one on the camera
	var z *cutFx
	for i := range fx {
		f := &fx[i]
		if f.Kind == "zoom" && f.Dur > 0 && t >= f.T && t < f.T+f.Dur {
			z = f
		}
	}
	if z == nil {
		return base
	}
	tin, tout := z.zoomGlides()
	k := 1.0
	switch {
	case tin > 0 && t < z.T+tin:
		k = (t - z.T) / tin
	case tout > 0 && t > z.T+z.Dur-tout:
		k = (z.T + z.Dur - t) / tout
	}
	return lerpRect(base, fxRect{z.Cx, z.Cy, z.Hf}, k)
}

// zoomGlides is the in and out glide a zoom actually gets: each at least 0,
// together no longer than the zoom itself, the way in winning any overlap.
func (z cutFx) zoomGlides() (tin, tout float64) {
	tin = math.Min(math.Max(z.Trans, 0), z.Dur)
	tout = math.Min(math.Max(z.Tout, 0), z.Dur-tin)
	return
}

func viewsOf(fx []cutFx) []cutFx {
	var out []cutFx
	for _, f := range fx {
		if f.Kind == "view" {
			out = append(out, f)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].T < out[j].T })
	return out
}

// firstView reports whether f is, or would become, the cut's earliest view --
// the one the first-view rule pins to the very beginning. Its glide has
// nowhere to travel from, so it has no transition to speak of. Works for a
// view not yet added (nothing earlier exists) and for one already in the list
// (itself is not earlier than itself).
func (ed *cutEditor) firstView(f cutFx) bool {
	if f.Kind != "view" {
		return false
	}
	for _, v := range viewsOf(ed.fx) {
		if v.T < f.T-1e-9 {
			return false
		}
	}
	return true
}

// viewRectAt is the settled camera -- the views alone, zooms ignored. Walked
// in order, carrying the glide state, so a view placed in the middle of
// another view's glide starts from the camera's actual mid-flight position
// rather than teleporting to the previous view's end.
func viewRectAt(views []cutFx, t float64, srcA, outA float64) fxRect {
	from, to := fullFit(srcA, outA), fullFit(srcA, outA)
	if len(views) > 0 {
		// the first view frames the video from the very beginning: the
		// letterboxed full frame only stands while no region has been chosen
		// at all, so the opening seconds are never stuck on the mismatched
		// source shape just because the region was placed mid-video. Its
		// glide becomes a no-op -- there is nowhere else to glide from.
		first := fxRect{views[0].Cx, views[0].Cy, views[0].Hf}
		from, to = first, first
	}
	t0, trans := math.Inf(-1), 0.0
	at := func(tt float64) fxRect {
		if trans > 0 && tt < t0+trans {
			return lerpRect(from, to, (tt-t0)/trans)
		}
		return to
	}
	for _, v := range views {
		if v.T > t {
			break
		}
		from, to = at(v.T), fxRect{v.Cx, v.Cy, v.Hf}
		t0, trans = v.T, math.Max(v.Trans, 0)
	}
	return at(t)
}

// fxHasCamera is whether anything would move or crop the picture: an aspect
// that is not the source's, or any view or zoom. It is the render's "is the
// whole camera machinery needed at all" question.
func fxHasCamera(aspect string, fx []cutFx) bool {
	if parseAspect(aspect) > 0 {
		return true
	}
	for _, f := range fx {
		if f.Kind == "view" || f.Kind == "zoom" {
			return true
		}
	}
	return false
}

// ---- the clock: speed effects on the segment list ---------------------------

// applyFx rewrites a render sequence (splitSpliced's output) with the speed
// effects in it: footage under one gets its Rate, and a freeze becomes a
// zero-span segment with a Dur, cut into the clip that covers it -- the same
// shape as a spliced card, with the frame at T standing in for the card.
//
// Only footage changes. Cards keep their own clocks, and an effect whose
// moment is not in any kept segment does nothing at all -- the scene it was
// set in has been cut, and the effect waits (harmlessly, invisibly) for Undo
// to bring the scene back.
func applyFx(segs []cutSeg, fx []cutFx) []cutSeg {
	out := segs
	// rates first, then freezes, so a freeze inside a rated stretch lands in
	// a segment already carrying the rate and keeps it on both sides
	for _, f := range speedsOf(fx) {
		if f.Rate > 0 && f.Dur > 0 {
			out = rateSpan(out, f.T, f.T+f.Dur, f.Rate)
		}
	}
	for _, f := range speedsOf(fx) {
		if f.frozenFx() && f.Dur > 0 {
			out = freezeAt(out, f.T, f.Dur)
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

// freezeAt cuts the clip covering t open and stands a zero-span, Dur-long
// segment in the gap -- the frame at t, held. Exactly a spliced card's shape
// (see splitSpliced), so everything downstream already knows what to do with
// the halves.
func freezeAt(segs []cutSeg, t, d float64) []cutSeg {
	var out []cutSeg
	done := false
	for _, s := range segs {
		if done || s.isInsert() || s.Dur > 0 || t < s.S || t >= s.E {
			out = append(out, s)
			continue
		}
		fz := cutSeg{S: t, E: t, Dur: d}
		if t <= s.S {
			out = append(out, fz, s)
		} else {
			head, tail := s, s
			head.E, tail.S = t, t
			out = append(out, head, fz, tail)
		}
		done = true
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
const fxLaneH = 16

// fxLaneTop is the lane's y inside the source-track area.
func (ed *cutEditor) fxLaneTop() float64 { return float64(rulerH + ed.thumbHt + 6) }

// fxHitLane is whether a press in the source-track area lands in the lane.
func (ed *cutEditor) fxHitLane(y float64) bool { return y >= ed.fxLaneTop() }

// fxSpanPx is where an effect is drawn and grabbed, in timeline px. Like
// spliceSpan: a thing with no width of its own still has to be a mouse
// target, so everything gets a floor.
func (ed *cutEditor) fxSpanPx(f cutFx) (float64, float64) {
	t0, t1 := f.fxSpan()
	x0, x1 := ed.xOf(t0), ed.xOf(t1)
	if f.frozenFx() {
		// a freeze owns no session time but has a length, drawn rightward
		// from its point exactly as a spliced card's marker is
		x1 = x0 + math.Max(splicePx, f.Dur*ed.pps)
	}
	return x0, math.Max(x1, x0+10)
}

// heldFx is the effect being held, or nil. The counterpart of heldSeg.
func (ed *cutEditor) heldFx() *cutFx {
	if !ed.fxOn || ed.fxSel >= len(ed.fx) {
		return nil
	}
	return &ed.fx[ed.fxSel]
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
// wins, so a view marker sitting inside a zoom's bracket is still reachable.
func (ed *cutEditor) fxIndexAt(px float64) int {
	best, bw := -1, math.MaxFloat64
	for i, f := range ed.fx {
		x0, x1 := ed.fxSpanPx(f)
		if px >= x0 && px <= x1 && x1-x0 < bw {
			best, bw = i, x1-x0
		}
	}
	return best
}

// holdFx takes hold of effect i and puts the playhead on it.
//
// The playhead move is the point. A view says what the picture shows FROM ITS
// MOMENT ONWARDS, and the overlay draws the held one on the preview -- so
// picking up a view half a minute away used to leave the framing of a moment
// you are not looking at drawn over the frame you are. With two views that
// reads as the first one never being shown at all. Landing on the view's own
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

// pickFxAt answers a right press in the lane: take hold of the effect there,
// or put down whatever was held.
func (ed *cutEditor) pickFxAt(px float64) {
	best := ed.fxIndexAt(px)
	if best < 0 {
		ed.dropEdge()
		ed.dropSeg()
		ed.dropFx()
		return
	}
	// the right button on the effect already in hand asks its numbers -- the
	// same meaning it has on the rectangle over the picture
	if ed.fxOn && ed.fxSel == best {
		ed.a.editFx()
		return
	}
	ed.holdFx(best)
}

// snapFxTime pulls an effect's moment onto the nearest border of the cut --
// the edges of the green -- when the hand brings it within snap seconds of
// one. An effect is nearly always meant for a cut point: a view is the frame
// the next clip is seen in, and a frame that changes a third of a second into
// the clip is a mistake nobody makes on purpose. Nudging is left alone (see
// nudgeFx): a frame at a time is the tool for saying "not quite there".
func snapFxTime(segs []cutSeg, t, snap float64) float64 {
	best, bd := t, snap
	for _, s := range segs {
		for _, e := range [2]float64{s.S, s.E} {
			if d := math.Abs(t - e); d < bd {
				best, bd = e, d
			}
		}
	}
	return best
}

// snapFx is snapFxTime at the current zoom: the tolerance is a fixed handful
// of pixels, so it is the same reach for the hand whatever the scale.
func (ed *cutEditor) snapFx(t float64) float64 {
	return snapFxTime(ed.segs, t, snapPx/math.Max(ed.pps, 0.001))
}

func (ed *cutEditor) fxStatus() {
	f := ed.heldFx()
	if f == nil {
		return
	}
	hint := "drag it along the lane to move it (it snaps to the cuts), " +
		"‹f and f› nudge it a frame, ⌦ removes it, ✎ Edit changes it; " +
		"a click clear of it puts it down"
	switch f.Kind {
	case "view", "zoom":
		// the picture is the direct way at the framing: inside slides the
		// rectangle, its border scales it, clear of it draws a new one
		hint = "on the video drag inside the box to move it, its border to " +
			"resize it, or clear of it to draw a new one — " + hint
	case "text":
		hint = "on the video drag inside the box to move it or its border to " +
			"resize it, and the words are re-fitted as you go — " + hint
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

// removeHeldFx is Del or － Remove with an effect held.
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

// drawFxLane paints the effects lane. Called from drawTrack inside its
// translation, so x here is timeline px like everything drawn around it.
func (ed *cutEditor) drawFxLane(cr *cairo.Context, vx0, vx1 float64) {
	y := ed.fxLaneTop()
	// the lane itself, one shade up from the page, as wide as the recordings
	cr.SetSourceRGB(0.165, 0.165, 0.175)
	for _, v := range ed.vids {
		cr.Rectangle(v.pxOrigin, y, v.dur*ed.pps, fxLaneH)
	}
	cr.Fill()
	cr.SetFontSize(9)
	for i, f := range ed.fx {
		x0, x1 := ed.fxSpanPx(f)
		if x1 < vx0 || x0 > vx1 {
			continue
		}
		switch f.Kind {
		case "view":
			// a flag: the camera is HERE from now on. The wedge behind it is
			// the glide, fading in over exactly the seconds it takes.
			if f.Trans > 0 {
				cr.SetSourceRGBA(0.95, 0.62, 0.15, 0.35)
				cr.Rectangle(x0, y+2, math.Max(f.Trans*ed.pps, 4), fxLaneH-4)
				cr.Fill()
			}
			cr.SetSourceRGB(0.95, 0.62, 0.15)
			cr.Rectangle(x0, y, 2, fxLaneH)
			cr.Fill()
			cr.MoveTo(x0+2, y)
			cr.LineTo(x0+9, y+4)
			cr.LineTo(x0+2, y+8)
			cr.ClosePath()
			cr.Fill()
		case "zoom":
			// a bracket over the stretch it holds for
			cr.SetSourceRGBA(0.25, 0.72, 0.82, 0.3)
			cr.Rectangle(x0, y+2, x1-x0, fxLaneH-4)
			cr.Fill()
			cr.SetSourceRGB(0.25, 0.72, 0.82)
			cr.SetLineWidth(1.5)
			cr.MoveTo(x0, y+fxLaneH-2)
			cr.LineTo(x0, y+2)
			cr.LineTo(x1, y+2)
			cr.LineTo(x1, y+fxLaneH-2)
			cr.Stroke()
			if x1-x0 > 40 {
				plateText(cr, x0+3, y+fxLaneH-4, fmt.Sprintf("⊕ %.1fs", f.Dur))
			}
		case "speed":
			cr.SetSourceRGBA(0.92, 0.42, 0.6, 0.4)
			cr.Rectangle(x0, y+2, x1-x0, fxLaneH-4)
			cr.Fill()
			cr.SetSourceRGB(0.92, 0.42, 0.6)
			cr.SetLineWidth(1)
			cr.Rectangle(x0, y+2, x1-x0, fxLaneH-4)
			cr.Stroke()
			label := "×" + fxNum(f.Rate)
			if f.frozenFx() {
				label = fmt.Sprintf("⏸ %.1fs", f.Dur)
			}
			if x1-x0 > 34 {
				plateText(cr, x0+3, y+fxLaneH-4, label)
			}
		case "text":
			// a bracket like a zoom's, in its own colour, with the opening
			// words in it: the lane is where you look for "which title is
			// that one", and a bracket that says only "text" answers nothing
			cr.SetSourceRGBA(0.6, 0.55, 0.95, 0.3)
			cr.Rectangle(x0, y+2, x1-x0, fxLaneH-4)
			cr.Fill()
			cr.SetSourceRGB(0.6, 0.55, 0.95)
			cr.SetLineWidth(1.5)
			cr.MoveTo(x0, y+fxLaneH-2)
			cr.LineTo(x0, y+2)
			cr.LineTo(x1, y+2)
			cr.LineTo(x1, y+fxLaneH-2)
			cr.Stroke()
			if x1-x0 > 40 {
				plateText(cr, x0+3, y+fxLaneH-4, "❝ "+laneWords(f.Text, int((x1-x0-16)/5)))
			}
		}
		if ed.fxOn && i == ed.fxSel {
			cr.SetSourceRGBA(1, 1, 1, 0.9)
			cr.SetLineWidth(2)
			cr.Rectangle(x0-1, y+0.5, x1-x0+2, fxLaneH-1)
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
	ed.a.setStatus(fmt.Sprintf("aspect %s — the whole frame fits inside it until you frame a view "+
		"(▭ View); the outline on the video is what the finished video shows", s))
}

// armFx puts the next drag on the video in charge of creating an effect.
func (ed *cutEditor) armFx(kind string) {
	if ed.player == nil || ed.playVideo == nil || !ed.hasPlay {
		ed.a.setStatus("click a track first — the effect needs a moment to happen at")
		return
	}
	if ed.fxArm == kind {
		ed.fxArm = "" // the button again is "never mind"
		ed.syncFxCursor()
		ed.a.setStatus("cancelled")
		return
	}
	ed.fxArm = kind
	ed.syncFxCursor()
	if kind == "text" {
		// the one armed drag that is not a camera window: it is drawn on the
		// finished frame, in any shape, and a click alone is enough
		ed.a.setStatus("drag the box the words go in — anywhere inside the bright outline, " +
			"any shape. A click puts one across the lower third. Esc cancels")
		return
	}
	what := "the camera sits from here on (0 seconds of glide is a hard cut)"
	if kind == "zoom" {
		what = "the picture zooms there and comes back on its own"
	}
	ed.a.setStatus(fmt.Sprintf("drag a box on the video — %s. The box keeps the cut's shape; "+
		"let go near the full width or height to snap to it. Esc cancels", what))
}

// speedClicked is the ⏩ Speed entry: the selected stretch put on its own clock.
func (a *App) speedClicked() {
	ed := a.ed
	t0, t1 := ed.sel.t0, ed.sel.t1
	if t1 < t0 {
		t0, t1 = t1, t0
	}
	if !ed.sel.active || t1-t0 < 0.2 {
		a.setStatus("mark a stretch with the in and out buttons first — speed puts those " +
			"seconds on a clock of their own")
		return
	}
	a.askSpeedParams(cutFx{Kind: "speed", T: t0, Dur: t1 - t0, Rate: 0.5}, true, func(f cutFx) {
		ed.addFx(f)
		ed.sel.active = false
		a.setStatus(f.fxLabel() + " — the footage plays at that rate there and the cut " +
			"gets longer or shorter to match; ↶ Undo takes it back")
	})
}

// freezeClicked is the ⏸ Stop entry: the frame under the playhead held
// still for the seconds the dialog asks -- a stop frame.
func (a *App) freezeClicked() {
	ed := a.ed
	if !ed.hasPlay {
		a.setStatus("click a track first — the stop needs a frame to stand on")
		return
	}
	a.askSpeedParams(cutFx{Kind: "speed", T: ed.playhead, Dur: 2}, true, func(f cutFx) {
		ed.addFx(f)
		a.setStatus(f.fxLabel() + " — the picture stands still there while the clock runs; " +
			"↶ Undo takes it back")
	})
}

// editFx reopens the held effect's numbers -- the ✎ Edit button's other job.
func (a *App) editFx() {
	ed := a.ed
	f := ed.heldFx()
	if f == nil {
		return
	}
	switch f.Kind {
	case "view":
		a.askViewParams(*f, false, func(nf cutFx) { ed.updateHeldFx(nf) })
	case "zoom":
		a.askZoomParams(*f, false, func(nf cutFx) { ed.updateHeldFx(nf) })
	case "speed":
		a.askSpeedParams(*f, false, func(nf cutFx) { ed.updateHeldFx(nf) })
	case "text":
		a.askTextParams(*f, false, func(nf cutFx) { ed.updateHeldFx(nf) })
	}
}

// editFxAt opens the numbers of an effect that is NOT held -- the picture's
// right button on a rectangle it is offering directly (step3_fxview.go). The
// answer is written back through the same undo-and-save as every other edit,
// and nothing is picked up: right-clicking to read a number should not move
// the playhead or change what the frame buttons act on.
func (a *App) editFxAt(f *cutFx) {
	if f == nil {
		return
	}
	save := func(nf cutFx) {
		a.ed.pushUndo()
		*f = nf
		a.ed.persist()
		a.ed.redrawTracks()
		a.setStatus(nf.fxLabel())
	}
	switch f.Kind {
	case "view":
		a.askViewParams(*f, false, save)
	case "zoom":
		a.askZoomParams(*f, false, save)
	case "text":
		a.askTextParams(*f, false, save)
	}
}

// updateHeldFx writes a dialog's answer back onto the held effect.
func (ed *cutEditor) updateHeldFx(nf cutFx) {
	f := ed.heldFx()
	if f == nil {
		return
	}
	ed.pushUndo()
	*f = nf
	ed.persist()
	ed.a.setStatus(nf.fxLabel())
}

// ---- dialogs ----------------------------------------------------------------

// fxWin is the little modal every effect dialog is: a heading, a line of
// explanation, whatever rows the caller adds, and OK/Cancel. The verb is on
// the suggested button so Enter means it.
func (a *App) fxWin(title, sub, verb string, rows []gtk.Widgetter, ok func()) {
	win := gtk.NewWindow()
	win.SetTransientFor(&a.win.Window)
	win.SetModal(true)
	win.SetTitle(title)
	win.SetDefaultSize(460, -1)

	head := gtk.NewLabel(title)
	head.SetXAlign(0)
	head.AddCSSClass("heading")
	subL := gtk.NewLabel(sub)
	subL.SetXAlign(0)
	subL.SetWrap(true)
	subL.AddCSSClass("dim-label")

	okB := gtk.NewButtonWithLabel(verb)
	okB.AddCSSClass("suggested-action")
	okB.ConnectClicked(func() { win.Close(); ok() })
	cancel := gtk.NewButtonWithLabel("Cancel")
	cancel.ConnectClicked(func() { win.Close() })
	btns := gtk.NewBox(gtk.OrientationHorizontal, 8)
	btns.SetHAlign(gtk.AlignEnd)
	btns.SetMarginTop(8)
	btns.Append(cancel)
	btns.Append(okB)

	box := gtk.NewBox(gtk.OrientationVertical, 8)
	box.SetMarginTop(16)
	box.SetMarginBottom(16)
	box.SetMarginStart(16)
	box.SetMarginEnd(16)
	box.Append(head)
	box.Append(subL)
	for _, r := range rows {
		box.Append(r)
	}
	box.Append(btns)
	win.SetChild(box)
	win.SetVisible(true)
}

// fxNumRow is a labelled number entry, the one control these dialogs are made
// of. Activate (Enter) is wired by the caller through the returned entry.
func fxNumRow(label, tip string, val float64) (*gtk.Box, *gtk.Entry) {
	l := gtk.NewLabel(label)
	l.SetXAlign(1)
	e := gtk.NewEntry()
	e.SetText(strings.TrimSuffix(fmt.Sprintf("%.1f", val), ".0"))
	e.SetMaxWidthChars(5)
	e.SetWidthChars(5)
	e.SetInputPurpose(gtk.InputPurposeNumber)
	if tip != "" {
		e.SetTooltipText(tip)
		l.SetTooltipText(tip)
	}
	row := gtk.NewBox(gtk.OrientationHorizontal, 6)
	row.Append(l)
	row.Append(e)
	row.SetHAlign(gtk.AlignStart)
	return row, e
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

// askViewParams asks the one number a view has: how long the camera glides.
func (a *App) askViewParams(f cutFx, isNew bool, ok func(cutFx)) {
	verb := "Save"
	if isNew {
		verb = "Place"
	}
	if a.ed.firstView(f) {
		// the earliest view frames the video from the very start: there is
		// no earlier camera to glide from, so no transition to choose
		a.fxWin(fmt.Sprintf("View at %s", mmss(f.T)),
			"This is the cut's first view: the finished video is framed on "+
				"this region from the very start, so there is no transition "+
				"to choose. Place a view earlier to give this one a glide.", verb,
			nil, func() {
				f.Trans = 0
				ok(f)
			})
		return
	}
	row, e := fxNumRow("Glide seconds",
		"how long the camera takes to arrive: 0 cuts straight there, "+
			"1 glides over a second", f.Trans)
	a.fxWin(fmt.Sprintf("View at %s", mmss(f.T)),
		"From this moment the finished video shows the framed region. "+
			"It stays there until the next view.", verb,
		[]gtk.Widgetter{row}, func() {
			f.Trans = fxNumOf(e, f.Trans)
			ok(f)
		})
	e.ConnectActivate(func() {}) // Enter falls through to the suggested button
}

// askZoomParams asks a zoom's two numbers: the glide each way and the hold.
func (a *App) askZoomParams(f cutFx, isNew bool, ok func(cutFx)) {
	verb := "Save"
	if isNew {
		verb = "Place"
	}
	if f.Dur <= 0 {
		f.Dur = 3
	}
	gRow, g := fxNumRow("Glide in seconds",
		"how long the zoom takes to arrive: 0 cuts straight in", f.Trans)
	oRow, o := fxNumRow("Glide out seconds",
		"how long it takes to let go again: 0 cuts straight back", f.Tout)
	dRow, d := fxNumRow("Length seconds",
		"how long the zoom lasts altogether, glides included", f.Dur)
	a.fxWin(fmt.Sprintf("Zoom at %s", mmss(f.T)),
		"The picture closes in on the framed region, holds, and comes back "+
			"out on its own.", verb,
		[]gtk.Widgetter{gRow, oRow, dRow}, func() {
			f.Trans = fxNumOf(g, f.Trans)
			f.Tout = fxNumOf(o, f.Tout)
			f.Dur = math.Max(0.4, fxNumOf(d, f.Dur))
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
	dRow, d := fxNumRow("Seconds on screen",
		"how long the words stay up altogether, fades included", f.Dur)
	iRow, i := fxNumRow("Fade in seconds",
		"how long they take to appear: 0 cuts them straight on", f.Trans)
	oRow, o := fxNumRow("Fade out seconds",
		"how long they take to go again: 0 cuts them straight off", f.Tout)
	times := gtk.NewBox(gtk.OrientationHorizontal, 12)
	times.Append(dRow)
	times.Append(iRow)
	times.Append(oRow)
	a.fxWin(fmt.Sprintf("Text at %s", mmss(f.T)),
		"The words are drawn over the finished video, fitted to the box on the "+
			"preview — they keep their place on screen while the camera moves "+
			"under them.", verb,
		[]gtk.Widgetter{sc, times}, func() {
			b := tv.Buffer()
			f.Text = b.Text(b.StartIter(), b.EndIter(), false)
			f.Dur = math.Max(0.3, fxNumOf(d, f.Dur))
			f.Trans = fxNumOf(i, f.Trans)
			f.Tout = fxNumOf(o, f.Tout)
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

// askSpeedParams asks what the clock does: a rate over the covered stretch,
// or -- for a freeze -- how long the frame stands. The rate is typed rather
// than picked off a list of three: the useful slow rates are a handful, but
// the fast ones run from "trim the dead air a bit" to 100×, and no list that
// short covers both ends.
func (a *App) askSpeedParams(f cutFx, isNew bool, ok func(cutFx)) {
	verb := "Save"
	if isNew {
		verb = "Place"
	}
	if f.frozenFx() {
		row, e := fxNumRow("Seconds",
			"how long the finished video stands on this frame (half a second at least — "+
				"the render drops shorter clips)", f.Dur)
		a.fxWin(fmt.Sprintf("Stop at %s", mmss(f.T)),
			"The picture stops on the frame under the red line and holds it; "+
				"the footage carries on afterwards as if nothing happened. "+
				"The cut gets longer by exactly this.", verb,
			[]gtk.Widgetter{row}, func() {
				f.Dur = math.Max(0.5, fxNumOf(e, f.Dur))
				ok(f)
			})
		return
	}
	rRow, r := fxNumRow("Speed ×",
		"1 is the footage's own speed. Below it the footage is slowed (0.5 is half "+
			"speed, 0.25 a quarter); above it it runs fast (4 for a brisk walkthrough, "+
			"20 to skip through dead air, up to 100)", f.Rate)
	r.SetText(fxNum(f.Rate)) // a rate is finer than the row's one decimal
	lRow, l := fxNumRow("Length seconds",
		"the session seconds it covers, counted from its start", f.Dur)
	a.fxWin(fmt.Sprintf("Speed %s – %s", mmss(f.T), mmss(f.T+f.Dur)),
		fmt.Sprintf("Those %.1f seconds of footage play at the rate below — the sound "+
			"with them, held at pitch — and the cut gets longer or shorter by the "+
			"difference.", f.Dur), verb,
		[]gtk.Widgetter{rRow, lRow}, func() {
			f.Rate, f.Dur = clampSpeed(fxNumOf(r, f.Rate), fxNumOf(l, f.Dur))
			ok(f)
		})
}
