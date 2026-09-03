package main

// | Split, between ＋ Add and － Remove.
//
// The three of them are one question asked three ways about the same span: Add
// keeps it, Remove drops it, Split cuts it free of what it lies in. Until this
// button there was no way to say the third: a stretch in the middle of a kept
// scene could be removed, and it could be kept (it already was), but it could
// not be made into a scene of its own -- and a scene is the unit everything
// else on this page acts on. Its own camera, its own sound, its own place in
// the order, its own removal later: all of them need a border either side of
// the seconds they are about, and the only way to draw one was to remove those
// seconds and add them back, which is two presses that undo each other and an
// undo history that says a removal happened.
//
// Nothing is removed and nothing moves. The cut keeps exactly what it kept a
// moment ago, to the frame: the finished video shows the same seconds in the
// same order, and the only thing that changed is how many scenes the page
// thinks that footage is. (Produce encodes a clip at a time, so the border is
// a join in the render as well -- the same frames, cut and put back together.)
//
// The borders it draws are DELIBERATE, which the cut has to store (cutSeg.Split)
// because two touching clips of one camera are otherwise the same clip: the
// list is sorted and merged by coalesce on nearly every edit, and a border with
// nothing to distinguish it from two selections that happened to meet would be
// swallowed by the next press somewhere else on the page. The flag is only ever
// set here, and only ever cleared by a drag that puts the two back together
// (mergeDropped), which is the same act in reverse.

import (
	"fmt"
	"math"
	"strings"
)

// splitSelRange is the button: a border at each end of the selection.
//
// The guards are － Remove's, in the same words, for the same reasons -- the
// band rather than its numbers, and a selection pointed at a waveform is not
// about footage. What it adds is its own no-op case: a selection that lies in a
// gap, or one whose ends fall exactly where borders already are, is a press
// that would draw nothing, and a status line that says "split" over an
// unchanged cut is worse than one that says why not.
func (a *App) splitSelRange() {
	ed := a.ed
	if !ed.sel.active {
		// no band drawn: the red line is the border. A clip in hand and a
		// press on Split is a razor, which is what the button looks like it
		// is -- and refusing because no REGION was drawn is the page insisting
		// on a gesture for a cut that needs one number, not two.
		a.splitAtLine()
		return
	}
	if ed.sel.aud != "" {
		a.setStatus(fmt.Sprintf("| Split cuts footage, and the selection is %s's sound — "+
			"drag on the pictures instead — a selection is of what it was drawn on", ed.sel.aud))
		return
	}
	t0, t1 := math.Min(ed.sel.t0, ed.sel.t1), math.Max(ed.sel.t0, ed.sel.t1)
	if !ed.spanTouches(t0, t1) {
		a.setStatus(fmt.Sprintf("nothing to split: the cut keeps nothing between %s and %s",
			mmss(t0), mmss(t1)))
		return
	}
	// asked before the undo entry is pushed: a press that draws no border must
	// not leave a step in the history that undoes nothing
	if ed.splitIdx(t0) < 0 && ed.splitIdx(t1) < 0 {
		a.setStatus(fmt.Sprintf("nothing to split: %s – %s is already a scene of its own, "+
			"or the pieces would be under %.0f s", mmss(t0), mmss(t1), minSegLn))
		return
	}
	before := len(ed.segs)
	ed.pushUndo()
	// the LATER border first: splitting at t0 renumbers everything after it,
	// and a search for t1 that runs before any of that happens is one fewer
	// thing to be right about.
	//
	// The borders that were actually drawn are what the status names, and a
	// selection with one end already on a border draws one: "split at 1:20 and
	// 1:45" over a single cut is a sentence the page can be caught out on.
	var at []string
	if ed.splitBorder(t1) {
		at = append(at, mmss(t1))
	}
	if ed.splitBorder(t0) {
		at = append([]string{mmss(t0)}, at...)
	}
	ed.persist()
	ed.redrawTracks()
	// the selection stays up. It is exactly the scene that was just made, and
	// the reason for making one is nearly always the next press -- ⧉ Copy, a
	// camera, a lane switched off -- which wants that span still in hand.
	a.setStatus(fmt.Sprintf("split at %s — %d scene(s), was %d; the footage is untouched "+
		"(↶ Undo takes it back)", strings.Join(at, " and "), len(ed.segs), before))
	ed.syncSelBtns()
}

// splitAtLine is | Split with nothing selected: one border, where the red line
// is. The selection's version draws two because a region has two ends; this
// draws the one the line names, which is how a razor works everywhere else and
// what a hand that has just put the line somewhere is asking for.
func (a *App) splitAtLine() {
	ed := a.ed
	if !ed.hasPlay {
		a.setStatus("| Split cuts where the red line is — click a track to put it " +
			"somewhere, or drag a region to cut that free instead")
		return
	}
	t := ed.playhead
	if ed.splitIdx(t) < 0 {
		a.setStatus(fmt.Sprintf("nothing to split at %s: the cut keeps nothing there, "+
			"or the pieces would be under %.0f s", mmss(t), minSegLn))
		return
	}
	before := len(ed.segs)
	ed.pushUndo()
	ed.splitBorder(t)
	ed.persist()
	ed.redrawTracks()
	// the right-hand half in hand, outlined: two scenes that touch are one
	// stretch of green with one more line drawn on it, and the white outline
	// is what says a press just made a scene rather than doing nothing
	if i := ed.segAt(t); i >= 0 {
		ed.segOn, ed.segSel, ed.segDirty = true, i, false
		ed.edgeOn, ed.fxOn = false, false
	}
	a.setStatus(fmt.Sprintf("split at %s — %d scene(s), was %d; the footage is untouched "+
		"(↶ Undo takes it back)", mmss(t), len(ed.segs), before))
	ed.syncSelBtns()
}

// splitBorder cuts the footage at t in two, and says whether it did.
//
// Only inside a clip, and only where both halves are worth having: a border
// within minSegLn of either end would leave a sliver that every other verb on
// this page refuses to make, and a scene shorter than a second is one nobody
// can aim at afterwards. Inserts are passed over -- a card is a file, not a
// stretch of the recording, and there is nothing in it to cut.
//
// The right-hand half carries the flag: it is the clip whose START is the new
// border, and the flag is about a start (coalesce).
func (ed *cutEditor) splitBorder(t float64) bool {
	i := ed.splitIdx(t)
	if i < 0 {
		return false
	}
	right := ed.segs[i] // every other field goes to both halves: same camera,
	right.S = t         // same silenced lanes -- it is the same footage
	right.Split = true
	ed.segs[i].E = t
	ed.segs = append(ed.segs, cutSeg{})
	copy(ed.segs[i+2:], ed.segs[i+1:]) // memmove: the tail shifts right by one
	ed.segs[i+1] = right
	// the halves are new items in the list, and whatever was held is an index
	// into the old one
	ed.dropSeg()
	ed.dropEdge()
	return true
}

// splitIdx is the clip a border at t would cut, or -1: the question splitBorder
// acts on and splitSelRange asks first, so the refusal and the edit cannot
// drift apart.
func (ed *cutEditor) splitIdx(t float64) int {
	for i := range ed.segs {
		s := ed.segs[i]
		if !s.isInsert() && t > s.S+minSegLn && t < s.E-minSegLn {
			return i
		}
	}
	return -1
}

// mergeDropped joins the clip just dropped to the one it was dragged against,
// and says whether it did.
//
// Dragging a clip up to its neighbour is how you say "these are one stretch
// again": the two ends meet, there is nothing between them, and a cut that
// still called them two scenes would be counting a border that is not visible
// anywhere on the page. It is | Split's inverse, and it is the only thing that
// clears a deliberate border -- which is why it clears it on the pair it joins
// and nowhere else, rather than letting one drag re-merge every split in the
// cut.
//
// Only same-camera neighbours, and only ones that actually touch. The seam
// between two cameras is the cut from one to the other and merging it away
// would throw the switch out and keep the seconds; that rule is coalesce's and
// this asks the same question before handing over to it.
func (ed *cutEditor) mergeDropped() bool {
	if !ed.segOn {
		return false
	}
	return ed.mergeTouching(ed.segSel)
}

// mergeTouching is the same join asked about a clip by index, which is how the
// other gesture that can close a gap reaches it.
//
// Dragging a clip against its neighbour is one way to say "these are one
// stretch"; dragging a clip's BORDER out to meet the next clip is the other,
// and it is the commoner one -- a gap between two kept stretches is closed by
// extending one of them over it, not by sliding a clip along the recording,
// which would move the footage rather than keep what is between. Both drops
// end here.
func (ed *cutEditor) mergeTouching(held int) bool {
	if held < 0 || held >= len(ed.segs) {
		return false
	}
	s := &ed.segs[held]
	if s.isInsert() {
		return false
	}
	for i := range ed.segs {
		n := &ed.segs[i]
		if i == held || n.isInsert() || n.Cam != s.Cam {
			continue
		}
		switch {
		case math.Abs(s.S-n.E) <= mergeTol:
			s.Split = false // this drag is the join, whatever put the border there
		case math.Abs(n.S-s.E) <= mergeTol:
			n.Split = false
		default:
			continue
		}
		a, b := math.Min(s.S, n.S), math.Max(s.E, n.E)
		ed.coalesce() // which is where two touching clips of one camera become one
		ed.a.setStatus(fmt.Sprintf("joined into one scene, %s – %s (%s) — "+
			"↶ Undo puts the border back", mmss(a), mmss(b), ed.spanSecs(a, b)))
		ed.redrawTracks()
		return true
	}
	return false
}

// mergeTol is how close two ends have to be to count as touching. coalesce's
// own number, named here because a drag has to ask the question before the
// merge rather than discover the answer afterwards: clampSeg snaps a dragged
// clip against its neighbour, so the everyday case is exactly 0 apart, and the
// tolerance is for the frame or two a hand-placed one lands out by.
const mergeTol = 0.25
