package main

// The selection band: the row between the ruler's clock and the thumbnails.
//
// The selection has always existed -- drag across the pictures, and Add keeps
// what you dragged over while Remove throws it away -- but it existed only as a
// tint over the thumbnails, and a tint is not a thing. You could make one and
// you could act on one, and that was the whole of it: a selection three seconds
// too short had to be drawn again from scratch, because there was nothing on
// the page to take hold of.
//
// So it gets a row of its own. In its row it is an object like every other
// object on this page: press it to pick it up, drag its middle to slide it,
// drag either end to move that end, ✕ to throw it away. The verbs are the ones
// the clips and the effects already answer to, and the ends snap to the same
// landmarks a dragged clip does -- the borders of the cut, the seams between
// recordings, the playhead -- because a selection is nearly always meant to
// start or stop at something, and lining it up by hand at 4 px per second is
// not a thing anyone should be asked to do.
//
// It is a row rather than a taller tint because the tint has a job the band
// cannot do. Over the pictures, the blue says WHICH FOOTAGE -- it is drawn on
// the frames it covers, and that is how you tell you have the right three
// seconds. The band says where the selection begins and ends and offers the
// handles. Both are drawn: the answer and the control.

import (
	"fmt"
	"math"

	"github.com/diamondburned/gotk4/pkg/cairo"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// what a press on the band lands on.
const (
	selNone  = iota // clear of it: a press here draws a new one
	selWhole        // the middle: the whole band travels
	selStart        // the left end
	selEnd          // the right end
	selKill         // the ✕
)

const (
	selGripPx = 6.0  // px either side of an end that grabs that end
	selKillW  = 12.0 // the ✕ target, inset from the right end
	selKillIn = 8.0
	// under this the band has no middle worth aiming at, so it is all grips and
	// the ✕ is not drawn: a selection of a few frames is moved by its ends or
	// thrown away with Esc.
	selMinBand = 40.0
)

// selBandTop is the band's y inside the source-track area: under the scope
// strip, which sits under the ruler (scopeTop).
func (ed *cutEditor) selBandTop() float64 { return float64(rulerH) + scopeH }

// hitSelBand is whether a press in the source-track area lands in the band.
func (ed *cutEditor) hitSelBand(y float64) bool {
	return y >= ed.selBandTop() && y < ed.picTop()
}

// selSpan is the selection in session time, low end first. The stored pair is
// in the order it was dragged in -- t1 is where the hand is -- and everything
// outside the dragging wants it sorted.
func (ed *cutEditor) selSpan() (float64, float64) {
	a, b := ed.sel.t0, ed.sel.t1
	if b < a {
		a, b = b, a
	}
	return a, b
}

// selSpanPx is the band as drawn and grabbed.
func (ed *cutEditor) selSpanPx() (float64, float64) {
	a, b := ed.selSpan()
	return ed.xOf(a), ed.xOf(b)
}

// selPartAt is what a press at timeline-x px takes hold of.
//
// The ends are asked about before the middle, so a band narrow enough that its
// grips meet is two ends and no middle rather than a middle you cannot get out
// of. The ✕ sits inboard of the right grip rather than under it -- they would
// otherwise be the same twelve pixels, and "throw it away" is not something a
// hand aiming at "make it a bit shorter" should be able to hit by accident.
func (ed *cutEditor) selPartAt(px float64) int {
	if !ed.sel.active {
		return selNone
	}
	x0, x1 := ed.selSpanPx()
	switch {
	case math.Abs(px-x0) <= selGripPx:
		return selStart
	case math.Abs(px-x1) <= selGripPx:
		return selEnd
	case x1-x0 >= selMinBand && px >= x1-selKillIn-selKillW && px <= x1-selKillIn:
		return selKill
	case px > x0 && px < x1:
		return selWhole
	}
	return selNone
}

// ---- the green bar ----------------------------------------------------------

// bandClipIdx is the clip the green bar stands for, or -1 when there is none.
// The clip in hand comes first -- held whole or by an edge, from this bar or
// from the picture band, the bar stays on it, so trimming a clip's end does not
// make the bar vanish the moment the playhead lands on the border being
// dragged -- and otherwise it is the kept clip the playhead sits in, which is
// what the bar has always shown.
func (ed *cutEditor) bandClipIdx() int {
	if ed.segOn && ed.segSel < len(ed.segs) && !ed.segs[ed.segSel].isInsert() {
		return ed.segSel
	}
	if ed.edgeOn && ed.edgeSeg < len(ed.segs) {
		return ed.edgeSeg
	}
	if !ed.hasPlay {
		return -1
	}
	for i, s := range ed.segs {
		if !s.isInsert() && ed.playhead >= s.S && ed.playhead < s.E {
			return i
		}
	}
	return -1
}

// bandClipPartAt is what a press at timeline-x px takes hold of on the green
// bar: which clip, and which part of it. The parts are the blue bar's own --
// ends first, then the ✕, then the middle -- because the two bars share a row
// and have to answer the hand the same way. The blue bar is asked before this
// one everywhere (press, cursor, hover), exactly as it is drawn on top.
//
// The ✕ removes the clip, which is what the scene's own ✕ over the pictures
// does (cut_segkill.go). It was left off this bar at first on the grounds that
// deleting footage is not the cheap, undoable nothing that throwing away a
// selection is -- but the scene badge already offers exactly that verb in
// exactly that corner, and it is the blue bar drawn on top of this one that
// splits the green in two and hides the badge underneath. A bar that answers
// every other verb of the blue's and not this one is the odd one out, so it
// answers this one too: same corner, same undo, same sentence in the status.
func (ed *cutEditor) bandClipPartAt(px float64) (int, int) {
	i := ed.bandClipIdx()
	if i < 0 {
		return -1, selNone
	}
	s := ed.segs[i]
	x0, x1 := ed.xOf(s.S), ed.xOf(s.E)
	switch {
	case math.Abs(px-x0) <= selGripPx:
		return i, selStart
	case math.Abs(px-x1) <= selGripPx:
		return i, selEnd
	case x1-x0 >= selMinBand && px >= x1-selKillIn-selKillW && px <= x1-selKillIn:
		return i, selKill
	case px > x0 && px < x1:
		return i, selWhole
	}
	return -1, selNone
}

// holdBandClip takes the green bar in hand. The bar IS its clip, so holding it
// is holding that: an end press picks up that clip border exactly as grabEdge
// does, a middle press the whole clip exactly as grabSeg does, and every verb
// downstream -- the drag's clamps and snaps, the throttled preview, one undo
// per drag, ‹f f› nudging afterwards -- is the picture band's own machinery
// rather than a second copy of it.
func (ed *cutEditor) holdBandClip(i, part int) {
	ed.dropSel() // one thing is held at a time, and this is now it
	ed.dropFx()
	if part == selWhole {
		ed.edgeOn = false
		ed.segOn, ed.segSel, ed.segDirty = true, i, false
	} else {
		ed.segOn = false
		ed.edgeOn, ed.edgeSeg, ed.edgeEnd, ed.edgeDirty = true, i, part == selEnd, false
	}
	ed.syncInsertBtn()
	ed.redrawTracks()
}

// ---- moving it --------------------------------------------------------------

// snapMarks are the landmarks a dragged selection lands on: every border of the
// cut, every seam between recordings, the ends of every effect's band, and the
// playhead.
//
// The cut's borders are the important ones. A selection is made in order to
// keep something or drop something, and what it is nearly always aimed at is a
// cut that already exists -- "drop from here to where that clip starts". The
// seams matter because a recording's first and last frame are the two moments
// no amount of dragging finds by hand. The effects are there because a
// selection is often about them -- "cut away exactly where the zoom ends".
// The playhead is there because it is where you just were.
func (ed *cutEditor) snapMarks() []float64 {
	out := make([]float64, 0, 2*len(ed.segs)+2*len(ed.vids)+2*len(ed.fx)+1)
	for _, s := range ed.segs {
		out = append(out, s.S, s.E)
	}
	for _, v := range ed.vids {
		out = append(out, v.start, v.start+v.dur)
	}
	for _, f := range ed.fx {
		t0, t1 := f.fxSpan()
		out = append(out, t0)
		if t1 > t0 {
			out = append(out, t1)
		}
	}
	if ed.hasPlay {
		out = append(out, ed.playhead)
	}
	return out
}

// snapSel pulls a single dragged end onto the nearest landmark within a few
// pixels' reach. Pixels rather than seconds, so the pull is the same distance
// for the hand at every zoom.
func (ed *cutEditor) snapSel(t float64) float64 {
	tol := snapPx / math.Max(ed.pps, 0.001)
	best, bd := t, tol
	for _, m := range ed.snapMarks() {
		if d := math.Abs(t - m); d < bd {
			best, bd = m, d
		}
	}
	return best
}

// snapSelSpan is snapSel for a band being slid along whole: BOTH ends are
// offered to the landmarks and the closer fit wins.
//
// Snapping only the leading end would mean a selection can be put flush against
// the cut on its left and never on its right, which is half a feature -- and
// the half you want is usually the other one, because a selection is dragged
// leftward as often as rightward.
func (ed *cutEditor) snapSelSpan(t0, ln float64) float64 {
	tol := snapPx / math.Max(ed.pps, 0.001)
	best, bd := t0, tol
	for _, m := range ed.snapMarks() {
		if d := math.Abs(t0 - m); d < bd {
			best, bd = m, d
		}
		if d := math.Abs(t0 + ln - m); d < bd {
			best, bd = m-ln, d
		}
	}
	return best
}

// selMinLen is the shortest a band may be dragged down to. Not zero: a
// selection with no length is invisible, cannot be grabbed again, and every
// action taken on it does nothing, so a hand that overshoots would silently
// destroy the thing it was adjusting.
const selMinLen = 0.04

// moveSelTo slides the whole band so it starts at t.
func (ed *cutEditor) moveSelTo(t float64) {
	a, b := ed.selSpan()
	ln := b - a
	t = math.Max(0, ed.snapSelSpan(t, ln))
	ed.sel.t0, ed.sel.t1 = t, t+ln
	ed.syncSelMarks()
}

// resizeSelTo moves one end of the band to t and leaves the other where it is.
func (ed *cutEditor) resizeSelTo(end bool, t float64) {
	a, b := ed.selSpan()
	t = math.Max(0, ed.snapSel(t))
	if end {
		b = math.Max(t, a+selMinLen)
	} else {
		a = math.Min(t, b-selMinLen)
	}
	ed.sel.t0, ed.sel.t1 = a, b
	ed.syncSelMarks()
}

// syncSelMarks keeps the selection readout telling the truth.
//
// The band and the marks are one object seen twice: the marks are how the rest
// of the page reads the band (the readout under Add, the flags on the picture
// band), so a band that could be dragged away from them would leave the clock
// describing a selection that is no longer anywhere. Every path that changes
// the band comes through here -- dragging a new one included.
func (ed *cutEditor) syncSelMarks() {
	ed.markIn, ed.markOut = ed.selSpan()
	ed.hasIn, ed.hasOut = true, true
	ed.showMarks()
	ed.redrawTracks()
}

// dropSel puts the band down without changing it.
func (ed *cutEditor) dropSel() {
	if !ed.selOn {
		return
	}
	ed.selOn = false
	ed.redrawTracks()
}

// killSel throws the selection away. The footage is untouched -- this is the ✕
// on the band, not ⌦, and the difference is worth keeping straight: ✕ removes
// the SELECTION, ⌦ removes what the selection is over.
func (ed *cutEditor) killSel() {
	ed.sel.active, ed.selOn = false, false
	ed.clearMarks()
	ed.a.setStatus("selection cleared — drag across a track for a new one")
	ed.redrawTracks()
}

// holdSel takes the band in hand and says what can be done to it.
func (ed *cutEditor) holdSel(part int) {
	ed.dropEdge() // one thing is held at a time, and this is now it
	ed.dropSeg()
	ed.dropFx()
	ed.selOn = true
	a, b := ed.selSpan()
	ed.a.setStatus(fmt.Sprintf("selection %s – %s (%.1f s) — drag its middle to move it, "+
		"either end to change that end (both snap to the cuts), ✕ throws it away, "+
		"⌦ removes the footage in it", mmss(a), mmss(b), b-a))
	ed.redrawTracks()
}

// hoverTracks answers the pointer for a whole band: which effect a press would
// take hold of, which clip border it would trim, whether it is over the
// selection row, and what the cursor should therefore be. One handler rather
// than four, because the cursor is one thing and two controllers setting it
// would fight over it.
//
// x below zero means the pointer has left: everything it was highlighting
// stops being highlighted.
func (ed *cutEditor) hoverTracks(x, y float64) {
	ed.hoverFx(x, y)
	ed.hoverFxKill(x, y)
	ed.hoverScope(x, y) // the ▲▼ handle lives in this area now
	on := x >= 0 && ed.hitSelBand(y) && ed.selPartAt(x+ed.viewX) != selNone
	gOn := false
	if x >= 0 && !on && ed.hitSelBand(y) {
		// the green bar highlights only where the blue does not answer first:
		// same precedence as the press and the cursor
		_, part := ed.bandClipPartAt(x + ed.viewX)
		gOn = part != selNone
	}
	if on != ed.selHov || gOn != ed.bandHov {
		ed.selHov, ed.bandHov = on, gOn
		ed.srcArea.QueueDraw()
	}
	ed.hoverSegKill(x, y)
	ed.hoverLaneKill(x, y)
	// no border glow under the ✕: the press there removes the scene rather
	// than trimming the border it overlaps, so the border must not offer
	ed.hoverEdge(x, x >= 0 && ed.hitPics(y) && !ed.killHovOn)
	ed.setCursor(ed.srcArea, ed.wantCursor(x, y))
}

// hoverLanes is the same answer for the waveform area, which has one question
// in it: the lanes are this timeline seen as sound, the cut points run through
// them, and there is nothing else down there to take hold of.
func (ed *cutEditor) hoverLanes(x, y float64) {
	_ = y // every row of the lanes is the same row, as far as the cut goes
	name := ""
	if x >= 0 {
		if _, _, ok := ed.edgeAt(x + ed.viewX); ok {
			name = "ew-resize"
		}
	}
	ed.hoverEdge(x, x >= 0)
	ed.setCursor(ed.audArea, name)
}

// hoverEdge highlights the clip border a press would take hold of. This is the
// half of the gesture that makes the other half safe: trimming and selecting
// are the same press over the same pixels, told apart only by landing within
// edgeGrab of a border, and a hand cannot aim at a tolerance it cannot see. The
// highlight is the tolerance, drawn.
func (ed *cutEditor) hoverEdge(x float64, cut bool) {
	on, t := false, 0.0
	if cut {
		if seg, end, ok := ed.edgeAt(x + ed.viewX); ok {
			on = true
			if t = ed.segs[seg].S; end {
				t = ed.segs[seg].E
			}
		}
	}
	if on == ed.edgeHovOn && t == ed.edgeHovT {
		return // nothing the bands draw has changed
	}
	ed.edgeHovOn, ed.edgeHovT = on, t
	// the two bands that draw the marker, and not redrawTracks: a hover happens
	// on the way past and must not drag the overlay's pointer-grabbing and the
	// track's height along behind it
	if ed.srcArea != nil {
		ed.srcArea.QueueDraw()
	}
	if ed.audArea != nil {
		ed.audArea.QueueDraw()
	}
}

// wantCursor is what a press at this point would do, said in the pointer.
//
// This is the important half of hovering a band. The three things the selection
// row does live in the same sixteen pixels and are told apart by which twelve
// pixels along the press lands in, which is not something a picture of a blue
// bar can communicate. A resize cursor over an end and an open hand over a
// middle say "this edge moves" and "this whole thing moves" before anything has
// been committed to, and that is the difference between a handle and a hazard.
// The effects lane answers the same way for the same reason.
func (ed *cutEditor) wantCursor(x, y float64) string {
	if x < 0 {
		return ""
	}
	switch {
	case ed.hitScope(y):
		// the ▲▼ handle is two buttons; everywhere else the strip is inert
		if ed.scopePartAt(x+ed.viewX, y-ed.scopeTop()) != scopeNone {
			return "pointer"
		}
	case ed.hitSelBand(y):
		switch ed.selPartAt(x + ed.viewX) {
		case selStart, selEnd:
			return "ew-resize"
		case selWhole:
			return "grab"
		case selKill:
			return "pointer"
		}
		// clear of the blue, the green bar answers with the same cursors: its
		// ends trim the clip's borders, its middle moves the clip, its ✕
		// removes it
		switch _, part := ed.bandClipPartAt(x + ed.viewX); part {
		case selStart, selEnd:
			return "ew-resize"
		case selWhole:
			return "grab"
		case selKill:
			return "pointer"
		}
	case ed.fxHitLane(y):
		i := ed.fxIndexAt(x+ed.viewX, y)
		if i < 0 {
			return ""
		}
		if p := ed.fxPartAt(i, x+ed.viewX); p == fxStart || p == fxEnd {
			return "ew-resize"
		}
		return "grab"
	case ed.hitPics(y):
		// the ✕ first, for the reason the press asks it first: it overlaps the
		// border it sits beside, and a resize arrow over a button is a lie
		if ed.segKillAt(x+ed.viewX, y) >= 0 || ed.laneKillAt(x+ed.viewX, y) != "" {
			return "pointer"
		}
		if _, _, ok := ed.edgeAt(x + ed.viewX); ok {
			return "ew-resize"
		}
	}
	return ""
}

// setCursor points a band's pointer at name, or back to the page's own for "".
// One slot per band, remembered, because SetCursorFromName on every motion
// event is a call into the display server for a cursor it already has.
func (ed *cutEditor) setCursor(area *gtk.DrawingArea, name string) {
	if area == nil {
		return
	}
	last := &ed.selCur
	if area == ed.audArea {
		last = &ed.audCur
	}
	if *last == name {
		return
	}
	*last = name
	if name == "" {
		area.SetCursor(nil)
		return
	}
	area.SetCursorFromName(name)
}

// ---- drawing ----------------------------------------------------------------

// drawSelBand paints the band. Called from drawTrack inside its translation, so
// x here is timeline px like everything drawn around it.
func (ed *cutEditor) drawSelBand(cr *cairo.Context, vx0, vx1 float64) {
	y := ed.selBandTop()
	// the row itself, as wide as the recordings, so that an empty band is
	// visibly a place a selection could go rather than a gap in the page
	cr.SetSourceRGB(0.155, 0.155, 0.165)
	for _, v := range ed.vids {
		cr.Rectangle(v.pxOrigin, y, v.dur*ed.pps, selBandH)
	}
	cr.Fill()
	// the clip under the red line, said in the band. The green tint over the
	// thumbnails says WHICH FOOTAGE the cut keeps; this bar says where the
	// kept clip the playhead sits in begins and ends -- the same sentence the
	// blue bar speaks for a selection, and what makes the clip's extent a
	// matter of reading one row rather than hunting the tint's edges. Drawn
	// first, so an actual selection lands on top of it. And it answers the
	// blue's verbs (bandClipPartAt), so it wears the blue's clothes: end
	// handles, and the same rings for held and hovered.
	if i := ed.bandClipIdx(); i >= 0 {
		s := ed.segs[i]
		if gx0, gx1 := ed.xOf(s.S), ed.xOf(s.E); gx1 >= vx0 && gx0 <= vx1 {
			cr.SetSourceRGBA(0.2, 0.8, 0.3, 0.5)
			cr.Rectangle(gx0, y+2, gx1-gx0, selBandH-4)
			cr.Fill()
			cr.SetSourceRGB(0.5, 0.92, 0.58)
			for _, hx := range []float64{gx0, gx1} {
				cr.Rectangle(hx-1.5, y, 3, selBandH)
			}
			cr.Fill()
			// the ✕ that removes the clip, in the corner the blue's sits in
			// and drawn to the same recipe, because the hand that has learnt
			// one bar has learnt both
			if gx1-gx0 >= selMinBand {
				kx := gx1 - selKillIn - selKillW/2
				cr.SetSourceRGBA(1, 1, 1, 0.85)
				cr.SetLineWidth(1.6)
				for _, d := range [][2]float64{{-1, -1}, {-1, 1}} {
					cr.MoveTo(kx+3*d[0], y+selBandH/2+3*d[1])
					cr.LineTo(kx-3*d[0], y+selBandH/2-3*d[1])
				}
				cr.Stroke()
			}
			switch {
			case (ed.segOn && ed.segSel == i) || (ed.edgeOn && ed.edgeSeg == i):
				cr.SetSourceRGBA(1, 1, 1, 0.9)
				cr.SetLineWidth(2)
				cr.Rectangle(gx0-1, y+1, gx1-gx0+2, selBandH-2)
				cr.Stroke()
			case ed.bandHov:
				cr.SetSourceRGBA(1, 1, 1, 0.4)
				cr.SetLineWidth(1.5)
				cr.Rectangle(gx0-1, y+1.25, gx1-gx0+2, selBandH-2.5)
				cr.Stroke()
			}
		}
	}
	if !ed.sel.active {
		return
	}
	x0, x1 := ed.selSpanPx()
	if x1 < vx0 || x0 > vx1 {
		return
	}
	a, b := ed.selSpan()

	cr.SetSourceRGBA(0.3, 0.55, 0.9, 0.75)
	cr.Rectangle(x0, y+2, x1-x0, selBandH-4)
	cr.Fill()
	// the ends, drawn as the handles they are: a bar the full height of the
	// row, brighter than the fill, on both sides of the border rather than
	// inside it, so that what is grabbable looks grabbable
	cr.SetSourceRGB(0.62, 0.82, 1)
	for _, x := range []float64{x0, x1} {
		cr.Rectangle(x-1.5, y, 3, selBandH)
	}
	cr.Fill()

	cr.SetFontSize(9)
	if x1-x0 >= selMinBand {
		// the ✕, inboard of the right handle
		kx := x1 - selKillIn - selKillW/2
		cr.SetSourceRGBA(1, 1, 1, 0.85)
		cr.SetLineWidth(1.6)
		for _, d := range [][2]float64{{-1, -1}, {-1, 1}} {
			cr.MoveTo(kx+3*d[0], y+selBandH/2+3*d[1])
			cr.LineTo(kx-3*d[0], y+selBandH/2-3*d[1])
		}
		cr.Stroke()
	}
	// how long it is, which is the number a selection is usually being adjusted
	// towards. Plain white on the blue rather than the plated text the rest of
	// the page uses: a plate is for words that land on thumbnails and have to
	// survive whatever is under them, and putting one here would black out most
	// of the band it is labelling. Drawn only when it fits clear of the ✕ --
	// truncated to "0:20 – 0:5" it would say nothing and cost the band its
	// colour.
	lbl := fmt.Sprintf("%s – %s  %.1fs", mmss(a), mmss(b), b-a)
	if e := cr.TextExtents(lbl); x0+6+e.Width < x1-selKillIn-selKillW-4 {
		cr.SetSourceRGB(1, 1, 1)
		cr.MoveTo(x0+6, y+selBandH-5)
		cr.ShowText(lbl)
	}
	// held is a solid white ring, the same two weights the effects lane uses
	switch {
	case ed.selOn:
		cr.SetSourceRGBA(1, 1, 1, 0.9)
		cr.SetLineWidth(2)
		cr.Rectangle(x0-1, y+1, x1-x0+2, selBandH-2)
		cr.Stroke()
	case ed.selHov:
		cr.SetSourceRGBA(1, 1, 1, 0.4)
		cr.SetLineWidth(1.5)
		cr.Rectangle(x0-1, y+1.25, x1-x0+2, selBandH-2.5)
		cr.Stroke()
	}
}
