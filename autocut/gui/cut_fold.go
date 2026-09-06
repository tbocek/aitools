package main

// Folding the stretches the cut throws away.
//
// A session is mostly not in the video. Between two kept clips there can be
// twenty minutes of walking back, and at this page's zoom that is a screen and
// a half of footage nobody will ever watch, sitting between the two things
// being compared. The timeline already collapses one kind of dead space -- a
// stretch nobody FILMED is drawn as one fixed hatch however many minutes it
// stands for -- and this is the same trick for the other kind: seconds that
// exist and that the cut drops.
//
// So a dropped stretch can be folded, the way an IDE folds a function body: a
// − where it is, a + where it was, and everything else on the page follows,
// because everything measures itself through xOf and tAt and those walk the
// cells (layoutPx).
//
// What a fold is NOT is an edit. The cut is the same cut, the render is the
// same render; it changes what you are looking at, so it pushes no undo step
// -- and it is kept with the cut all the same (cutFile.Folds), because it is
// about the gaps between that cut's segments and would otherwise be undone by
// closing the project.
//
// ---- and drags open it -------------------------------------------------------
//
// Folded, two clips end up drawn border against border with minutes between
// them.
// Every hard case in this feature comes from that: which of the two touching
// borders a press means, what a pixel of dragging is worth inside a fold,
// where the page goes when a clip grows into one. They all go away with one
// rule: a press that can MOVE something opens the folds around it for as long
// as the button is down (foldOpen/foldShut). The drag then runs on the
// ordinary proportional timeline, and the merge at the end of it is the same
// merge a clip dragged against its neighbour has always had.
//
// The view is anchored on the pressed second across both, so the thing under
// the pointer stays under the pointer while the rest of the timeline slides
// around it -- the trick the wheel already uses to zoom about the cursor.

import (
	"fmt"
	"math"

	"github.com/diamondburned/gotk4/pkg/cairo"
)

const (
	// foldPx is how wide a folded stretch is drawn: NOTHING. The two clips
	// either side of one meet, border against border, exactly as they do when
	// there is no time between them at all -- which is the truth a fold is
	// telling. A strip left standing in there, whatever was drawn on it, is a
	// stretch of page standing for footage that is not on the page.
	//
	// It was 32 px for a while, wide enough that the two borders and the +
	// between them each had their own pixels. That bought a tidy press at the
	// cost of the one thing the feature is for, and it is not needed: a press
	// on the seam OPENS it (foldOpen), and everything after that is an
	// ordinary drag on an ordinary timeline. Which of the two borders a press
	// on the seam means is answered by the side it lands on (bandClipPartAt).
	foldPx = 0.0
	// a gap with no room to draw the − in is not offered one: the badge would
	// be wider than the thing it folds, and would sit on both its neighbours.
	foldMin = 2 * segKillHit
)

// foldGap is one dropped stretch and whether it is folded: what the − and +
// are drawn on, and what a press on one toggles.
type foldGap struct {
	t0, t1 float64
	on     bool
}

// foldGaps is every stretch the cut drops, in order, each saying whether it is
// folded. The head of a run before its first clip and the tail after its last
// are dropped stretches like any other and fold like any other.
func (ed *cutEditor) foldGaps() []foldGap {
	var out []foldGap
	for _, g := range ed.droppedSpans() {
		out = append(out, foldGap{g[0], g[1], ed.foldedGap(g[0], g[1])})
	}
	return out
}

// foldedGap is whether a stretch is folded: a stored fold that OVERLAPS it,
// rather than one that matches its ends.
//
// Trimming a clip moves the edge of the gap beside it, splitting one moves a
// gap's whole middle, and a fold matched by its ends would be lost to both. It
// is the gap between these two clips that is folded, and overlap is how that
// survives the cut being worked on.
func (ed *cutEditor) foldedGap(t0, t1 float64) bool {
	for _, f := range ed.folds {
		if f[1] > t0 && f[0] < t1 {
			return true
		}
	}
	return false
}

// syncFolds rewrites the stored list to the gaps as they now are: one entry
// per folded gap, at that gap's own bounds, and nothing for a gap that no
// longer exists. Called wherever the cut changes shape, so what is stored
// cannot drift away from what is drawn.
func (ed *cutEditor) syncFolds() {
	if len(ed.folds) == 0 {
		return
	}
	var out [][2]float64
	for _, g := range ed.foldGaps() {
		if g.on && !ed.wholeRun(g.t0, g.t1) {
			out = append(out, [2]float64{g.t0, g.t1})
		}
	}
	ed.folds = out
}

// wholeRun is whether a gap has swallowed an entire recording: nothing kept
// before it, nothing after, the run itself dropped end to end.
//
// A fold is remembered by overlap, which is what carries it through trims --
// and what would otherwise carry it through the cut being emptied. Clear the
// cut, or undo back to before there was one, and the gap beside a fold grows
// to the whole session; folded, that is the whole page collapsed to one strip
// by a press nobody made. A fold is a gap BETWEEN scenes: with no scene either
// side of it, it is not one.
func (ed *cutEditor) wholeRun(t0, t1 float64) bool {
	for _, r := range ed.runs() {
		if t0 <= r.t0+0.01 && t1 >= r.t1-0.01 {
			return true
		}
	}
	return false
}

// cells is the filmed runs cut into the pieces the page lays out: a run with a
// folded gap in it becomes the stretch before, the fold, and the stretch
// after. Only folds are cut on -- an unfolded gap is drawn to scale like the
// footage around it, and cutting there would be a boundary nothing needs.
func (ed *cutEditor) cells() []tlSpan {
	var out []tlSpan
	for i, run := range ed.runs() {
		at, first := run.t0, true
		for _, g := range ed.foldGaps() {
			if !g.on || g.t1 <= at || g.t0 >= run.t1 {
				continue
			}
			t0, t1 := math.Max(g.t0, at), math.Min(g.t1, run.t1)
			if t0 > at {
				out = append(out, tlSpan{t0: at, t1: t0, hole: first && i > 0})
				first = false
			}
			out = append(out, tlSpan{t0: t0, t1: t1, fold: true, hole: first && i > 0})
			first, at = false, t1
		}
		if at < run.t1 || first {
			out = append(out, tlSpan{t0: at, t1: run.t1, hole: first && i > 0})
		}
	}
	return out
}

// spanW is how wide a cell is drawn: its seconds at the current zoom, or the
// one fixed width when it is folded.
func (ed *cutEditor) spanW(s tlSpan) float64 {
	if s.fold {
		return foldPx
	}
	return s.dur() * ed.pps
}

// foldAt is the folded cell covering a session second, or nil.
func (ed *cutEditor) foldAt(t float64) *tlSpan {
	for i := range ed.spans {
		if s := &ed.spans[i]; s.fold && t >= s.t0 && t < s.t1 {
			return s
		}
	}
	return nil
}

// cellsOf is the cells a stretch of session time is drawn across: one for a
// stretch with no fold in it, and the pieces either side of every fold that
// cuts through it. What is drawn IN each is the caller's own business -- the
// pictures skip the folded ones, the waveforms skip them, and both walk the
// rest at that cell's own origin, because px is only linear in time inside a
// cell (xOf).
func (ed *cutEditor) cellsOf(t0, t1 float64) []tlSpan {
	var out []tlSpan
	for _, s := range ed.spans {
		if s.fold || s.t1 <= t0 || s.t0 >= t1 {
			continue
		}
		out = append(out, s)
	}
	return out
}

// ---- the − and the + ---------------------------------------------------------

// foldBadge is one gap's control: where it is drawn, and which way it goes.
// The + of a folded gap sits on the seam itself -- the two bars either side
// meet at one x, and that x is the only mark on the page saying the footage
// between them is still there; the − of an unfolded one sits in the middle of
// the gap, where there is nothing else to press.
type foldBadge struct {
	gap    foldGap
	cx, cy float64 // timeline x, and the middle of the selection band
}

// foldBadges is one per gap worth offering: a gap with no room for the badge
// itself gets none (foldMin).
func (ed *cutEditor) foldBadges() []foldBadge {
	var out []foldBadge
	cy := ed.selBandTop() + selBandH/2
	for _, g := range ed.foldGaps() {
		x0, x1 := ed.xOf(g.t0), ed.xOf(g.t1)
		if !g.on && x1-x0 < foldMin {
			continue
		}
		out = append(out, foldBadge{g, ed.foldBadgeX(g, x0, x1), cy})
	}
	return out
}

// foldBadgeX is where a gap's badge goes along the timeline.
//
// The middle, for a gap between two clips: the ends are the clips' own grips,
// and the middle is the one part of it nothing else wants.
//
// But the stretch BEFORE the first clip and the one after the last are not
// between anything, and they are usually the longest gaps on the page --
// setting the capture going, and forgetting to stop it. The middle of those is
// an arbitrary point in the void, minutes from either edge and off screen at
// any zoom worth working at. So the head's − goes at the leftmost end of the
// page and the tail's at the rightmost, killIn in from the edge like every
// other badge drawn against one: "before all of this" and "after all of this"
// are read off the ends of a timeline, which is where they are.
//
// Never past the middle, though -- on a short head or tail that would put the
// badge inside the clip beside it -- and never so close to an end that half
// its plate hangs off the page, which is what the folded head's seam would
// otherwise do at x 0.
func (ed *cutEditor) foldBadgeX(g foldGap, x0, x1 float64) float64 {
	mid := (x0 + x1) / 2
	head, tail := ed.headTail(g)
	switch {
	case g.on: // a seam: one x, and there is no choice to make
	case head:
		mid = math.Min(x0+killIn, mid)
	case tail:
		mid = math.Max(x1-killIn, mid)
	}
	const plate = segKillR + segKillPad
	return math.Max(plate, math.Min(mid, ed.totalW-plate))
}

// headTail says whether a gap opens a filmed run or closes one: the stretch
// before that recording's first kept clip, and the one after its last.
func (ed *cutEditor) headTail(g foldGap) (head, tail bool) {
	for _, r := range ed.runs() {
		if math.Abs(g.t0-r.t0) < 0.01 {
			head = true
		}
		if math.Abs(g.t1-r.t1) < 0.01 {
			tail = true
		}
	}
	return head, tail
}

// foldBadgeAt is the gap whose badge is under a press at timeline-x px, or -1
// into foldBadges.
func (ed *cutEditor) foldBadgeAt(px, y float64) int {
	for i, b := range ed.foldBadges() {
		if math.Abs(px-b.cx) <= segKillHit && math.Abs(y-b.cy) <= segKillHit {
			return i
		}
	}
	return -1
}

// toggleFold folds a gap or opens it again, and keeps the second under the
// pointer under the pointer.
//
// Everything to the right of the gap moves -- that is what folding IS -- so
// without the anchor the press throws the page sideways by however many
// minutes the fold hides. anchor is the timeline x the press landed on
// (foldAnchor); the view is set so the second that was there is there again.
//
// No undo step: the cut is the same cut, and a history of what was looked at
// is not a history of what was done.
func (ed *cutEditor) toggleFold(i int, anchor float64) {
	badges := ed.foldBadges()
	if i < 0 || i >= len(badges) {
		return
	}
	g := badges[i].gap
	t := ed.tAt(anchor)
	if g.on {
		ed.unfold(g.t0, g.t1)
	} else {
		ed.folds = append(ed.folds, [2]float64{g.t0, g.t1})
	}
	ed.foldLayout(t, anchor)
	ed.persist()
	if g.on {
		ed.a.setStatus(fmt.Sprintf("unfolded %s – %s (%s)", mmss(g.t0), mmss(g.t1), ed.spanSecs(g.t0, g.t1)))
		return
	}
	ed.a.setStatus(fmt.Sprintf("folded %s – %s (%s)", mmss(g.t0), mmss(g.t1), ed.spanSecs(g.t0, g.t1)))
}

// unfold drops every stored fold overlapping a stretch.
func (ed *cutEditor) unfold(t0, t1 float64) {
	var out [][2]float64
	for _, f := range ed.folds {
		if f[1] > t0 && f[0] < t1 {
			continue
		}
		out = append(out, f)
	}
	ed.folds = out
}

// foldLayout re-lays the timeline out and puts session second t back at
// timeline x, which is the same trick the wheel zooms about the cursor with
// (zoomAt). Everything right of a fold moves when it opens or closes; this is
// what keeps the thing being pressed from moving with it.
func (ed *cutEditor) foldLayout(t, x float64) {
	ed.layoutPx()
	ed.syncScroll()
	ed.setOff(ed.xOf(t) - (x - ed.viewX))
	ed.redrawTracks()
}

// drawFoldBadges paints the − and the + on the selection band, over the bars.
func (ed *cutEditor) drawFoldBadges(cr *cairo.Context, vx0, vx1 float64) {
	for i, b := range ed.foldBadges() {
		if b.cx < vx0-segKillHit || b.cx > vx1+segKillHit {
			continue
		}
		mark := "−"
		if b.gap.on {
			mark = "+"
		}
		foldPlate(cr, b.cx, b.cy, mark, ed.foldHov == i)
	}
}

// foldPlate is the badge itself: the plate every control on this page wears
// (drawKillBadge), with a bar or a cross on it rather than the ✕. Lit under
// the pointer like the rest of them, and not red -- folding takes nothing
// away.
func foldPlate(cr *cairo.Context, cx, cy float64, mark string, hot bool) {
	if hot {
		cr.SetSourceRGBA(0.25, 0.55, 0.85, 0.95)
	} else {
		cr.SetSourceRGBA(0.06, 0.06, 0.07, 0.55)
	}
	cr.Arc(cx, cy, segKillR+segKillPad, 0, 2*math.Pi)
	cr.Fill()
	cr.SetSourceRGBA(1, 1, 1, 0.92)
	cr.SetLineWidth(1.6)
	cr.MoveTo(cx-segKillR, cy)
	cr.LineTo(cx+segKillR, cy)
	if mark == "+" {
		cr.MoveTo(cx, cy-segKillR)
		cr.LineTo(cx, cy+segKillR)
	}
	cr.Stroke()
}

// hoverFold lights the badge under the pointer, the way every other badge on
// the page lights.
func (ed *cutEditor) hoverFold(x, y float64) {
	i := -1
	if x >= 0 && ed.hitSelBand(y) {
		i = ed.foldBadgeAt(x+ed.viewX, y)
	}
	if i != ed.foldHov {
		ed.foldHov = i
		if ed.srcArea != nil {
			ed.srcArea.QueueDraw()
		}
	}
}

// ---- what opens a fold besides its own + -------------------------------------

// walkFold opens the fold the line has walked into. ▶ plays the footage, all
// of it, dropped stretches included -- so a page that shows a seam while the
// line is somewhere inside it is lying about where the line is. ▶✂ plays the
// cut, which never enters one, and this is not called for it.
//
// It stays open afterwards. The line came out of it, and refolding under a
// running line would pull the page sideways mid-playback; the + is right there
// when it is wanted again.
func (ed *cutEditor) walkFold() {
	s := ed.foldAt(ed.playhead)
	if s == nil {
		return
	}
	t0, t1 := s.t0, s.t1
	x := ed.xOf(ed.playhead)
	ed.unfold(t0, t1)
	ed.foldLayout(ed.playhead, x)
	ed.persist()
	ed.a.setStatus(fmt.Sprintf("unfolded %s — ▶ ran into it", mmss(t0)))
}

// foldOpen opens every fold a press could drag something into or out of, for
// as long as the button is down, and answers what foldShut needs to put them
// back.
//
// The reason is in this file's head: folded, two clips meet at one x with
// minutes between them, and a drag across that seam would have to invent what
// a pixel is worth inside it. Opening first means the drag runs on the
// ordinary timeline, where a pixel is a pixel.
//
// Only the folds the press can reach are opened -- the one under it and the
// one either side of whatever it grabbed -- because opening all of them would
// throw every other seam on the page open for a drag that cannot touch them.
func (ed *cutEditor) foldOpen(a, b, px float64) []foldGap {
	reach := foldReach / math.Max(ed.pps, 0.001)
	var shut []foldGap
	for _, g := range ed.foldGaps() {
		if g.on && g.t1 >= a-reach && g.t0 <= b+reach {
			shut = append(shut, g)
		}
	}
	if len(shut) == 0 {
		return nil
	}
	t := ed.tAt(px)
	for _, g := range shut {
		ed.unfold(g.t0, g.t1)
	}
	ed.foldLayout(t, px)
	return shut
}

// foldReach is how far past what is held a fold still counts as reachable, in
// px: the grab itself, so the seam AT the end of a clip being dragged opens
// with it and the next one along does not.
const foldReach = edgeGrab

// foldShut folds back what foldOpen opened, once the button is up, anchored on
// the second under the pointer so the page does not jump out from under the
// hand that just let go.
//
// A gap the drag CLOSED -- a clip grown across it, two clips merged -- is gone,
// and folding it back would fold whatever gap now overlaps where it was. So
// each one is only put back if it is still a dropped stretch of its own.
func (ed *cutEditor) foldShut(shut []foldGap, px float64) {
	if len(shut) == 0 {
		return
	}
	t := ed.tAt(px)
	gaps := ed.foldGaps()
	for _, g := range shut {
		for _, now := range gaps {
			if now.t0 < g.t1 && now.t1 > g.t0 && now.t1-now.t0 > 0.05 {
				ed.folds = append(ed.folds, [2]float64{now.t0, now.t1})
				break
			}
		}
	}
	ed.syncFolds()
	ed.foldLayout(t, px)
	ed.persist()
}
