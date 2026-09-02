package main

import (
	"fmt"
	"math"
	"path/filepath"
	"sort"
)

// ---- moving a recording, and moving the cut on it ---------------------------
//
// Two clocks are never the same clock. A second camera started by hand, a
// phone a minute out, a recorder that writes no date at all: srcClock puts
// every file on one wall clock off its name, which is roughly right about the
// seconds for anything that names a moment and is an outright guess -- the
// session's own start -- for anything that does not. The lanes are what make
// that visible: the same shout in two waveforms, a column apart. Nothing else
// on this page can fix it, because everything else on this page is measured
// against exactly the placement that is wrong.
//
// So the right button drags. On the pictures it drags a whole row along the
// timeline until the waveforms line up; on a lane it drags that one recording.
// The offset is remembered per source in cut.json and re-applied on every load:
// the file itself is untouched, and the render re-derives the same placement
// from the same map, so what you lined up is what gets cut.
//
// Over a selection the same button means the other thing you would want to drag
// along the timeline: the KEPT spans inside it. There the footage stays put and
// the green slides across it -- the same scene a beat later, which is moving the
// cut rather than moving the camera.

// shiftOf is one source's correction, and zero for a source that has none.
func (ed *cutEditor) shiftOf(base string) float64 { return ed.shift[base] }

// applyShift puts the saved corrections onto a freshly loaded timeline. reload
// builds every start from srcClock, which knows nothing about them, so this
// is the one place they go on -- before the gaps are worked out and before
// relayout measures anything.
func (ed *cutEditor) applyShift() {
	for b, d := range ed.shift {
		ed.slideSrc(b, d)
	}
}

// slideSrc moves everything that belongs to one source along the timeline: the
// footage, the sound that came out of the same file, and the speech-gap points
// that were worked out against it. It does NOT touch ed.shift -- applyShift
// calls it with what is already in there, and the drag calls it with a delta and
// writes the new total itself.
//
// The gap hints another camera derived partly from this one's speech are left
// where they were. They are hints for a snap, they cost a transcript re-read
// each, and a drag that stopped to re-read three TSVs per frame would not be a
// drag.
func (ed *cutEditor) slideSrc(base string, d float64) {
	if d == 0 {
		return
	}
	for i := range ed.vids {
		if ed.vids[i].base == base {
			ed.vids[i].start += d
		}
	}
	for i := range ed.auds {
		// by name, and also every further track of the video with that name: a
		// multi-track capture's second track is glued to the pictures it was
		// recorded with, so dragging the row to correct its clock has to take
		// the track with it or the correction is a desync nobody asked for. Its
		// own name still works as a name, so it can be nudged apart afterwards.
		if ed.auds[i].base == base || ed.auds[i].base == trackName(base, ed.auds[i].track) {
			ed.auds[i].start += d
		}
	}
	for i := range ed.gaps[base] {
		ed.gaps[base][i] += d
	}
}

// laneSrcs is every source drawn on one row of the picture band. A camera
// stopped and started again is several files on one row and one clock, and
// correcting it means correcting all of them by the same seconds -- shifting
// only the file under the pointer would move that camera's own seam.
func (ed *cutEditor) laneSrcs(lane int) []string {
	var out []string
	for _, v := range ed.vids {
		if v.lane == lane && !contains(out, v.base) {
			out = append(out, v.base)
		}
	}
	return out
}

// setShift puts one source's correction at exactly want seconds and moves the
// source to match. Absolute rather than by a delta on purpose: a drag is a
// hundred updates and each one carries the whole gesture so far, so nothing
// accumulates, the clamp cannot eat its way inwards, and letting go where you
// started leaves the timeline exactly as it was found.
//
// The rows are frozen first. A shift changes which recordings overlap, and the
// rows are worked out FROM the overlaps -- without this, two cameras dragged
// apart until they no longer overlap would quietly collapse onto one row, and
// every scene that said "camera 2" would point at a row that is not there.
func (ed *cutEditor) setShift(base string, want float64) {
	want = ed.clampShift(want)
	cur := ed.shift[base]
	if want == cur {
		return
	}
	ed.freezeRows()
	if ed.shift == nil {
		ed.shift = map[string]float64{}
	}
	ed.slideSrc(base, want-cur)
	if want == 0 {
		delete(ed.shift, base) // back where it started: nothing to save
	} else {
		ed.shift[base] = want
	}
}

// shiftTo moves a set of sources to d seconds off the corrections they had at
// the start of the drag (from), and puts the page back together around them.
func (ed *cutEditor) shiftTo(srcs []string, from map[string]float64, d float64) {
	rows := map[int]bool{}
	for _, v := range ed.vids {
		if contains(srcs, v.base) {
			rows[v.lane] = true
		}
	}
	// how far they actually got, which is not d when the clamp bit. Read off
	// any one of them: every source in a drag moves by the same seconds.
	var was, now float64
	if len(srcs) > 0 {
		was = ed.shift[srcs[0]]
	}
	for _, b := range srcs {
		ed.setShift(b, from[b]+d)
	}
	if len(srcs) > 0 {
		now = ed.shift[srcs[0]]
	}
	ed.followCopies(rows, srcs, now-was)
	sort.SliceStable(ed.vids, func(i, j int) bool { return ed.vids[i].start < ed.vids[j].start })
	sortLanes(ed.auds)
	ed.relayout()
}

// followCopies drags the copied stretches along with the footage they were
// copied from. A copy is stored as the session second its footage starts at
// (copyScheme), and that second only means anything relative to where the
// recording sits -- so moving the recording and leaving the copy behind would
// quietly repoint it at whatever is under that second now.
//
// Only when the WHOLE row moved: a copy names a camera, not a file, and a row
// half of which was corrected is a row where "the second the footage starts at"
// no longer has one answer. And by the delta actually applied, so a drag that
// hit the bound moves the copies not at all rather than out of step.
func (ed *cutEditor) followCopies(rows map[int]bool, srcs []string, d float64) {
	if d == 0 {
		return
	}
	whole := map[int]bool{}
	for lane := range rows {
		whole[lane] = true
		for _, b := range ed.laneSrcs(lane) {
			if !contains(srcs, b) {
				whole[lane] = false
			}
		}
	}
	for i, s := range ed.segs {
		if at, ok := copySrc(s.Ins); ok && whole[s.Cam] {
			ed.segs[i].Ins = fmt.Sprintf("%s%.3f", copyScheme, math.Max(0, at+d))
		}
	}
}

// clampShift bounds a correction to the length of all the material there is, in
// each direction. A drag is in pixels and a session is minutes, so the hand
// cannot run away with this in one go -- but drags add up, and past that bound
// the recording overlaps nothing: a lane with nothing beside it is a lane there
// is no longer anything to read it against, which is the one thing this whole
// row is for.
func (ed *cutEditor) clampShift(want float64) float64 {
	span := 60.0
	for _, v := range ed.vids {
		span += v.dur
	}
	for _, au := range ed.auds {
		if !au.master {
			span += au.dur
		}
	}
	return math.Max(-span, math.Min(span, want))
}

// freezeRows writes the rows down as they are now, once per project. Until the
// first drag they stay derived, so adding a camera on Prepare still puts
// it where the arithmetic says; from the first drag on they are the project's,
// because cutSeg.Cam is a row number and it has to keep meaning the same camera.
func (ed *cutEditor) freezeRows() {
	if ed.rows != nil {
		return
	}
	ed.rows = map[string]int{}
	for _, v := range ed.vids {
		ed.rows[v.base] = v.lane
	}
}

// slideGreen moves the kept scenes inside the selection by d seconds, and only
// those: the footage stays where it is and the cut travels across it. from is
// the cut as it was when the drag began, so every update is computed from the
// same starting point rather than from the last one -- a drag is a hundred
// updates, and one that clamped its way inwards a hundred times would not come
// back when the hand came back.
//
// They move as a GROUP and they keep their lengths. Every scene may only travel
// as far as the recording it was cut from allows -- its frames are that file's
// frames, the same rule a left-dragged clip obeys -- and the shortest of those
// allowances is the one the whole set gets, because "these scenes, a beat later"
// means the beat between them as well. One scene stopping at the end of its
// camera while the others walked on would not be moving the cut; it would be
// rewriting it.
//
// Only what is WHOLLY inside travels, and the cards inside travel with the
// scenes: a scene half in the selection would have to be split to move, and a
// right drag that quietly cut the cut in two is not what the hand asked for --
// while a card left standing where the scenes around it walked away would come
// out somewhere else in the finished video than where it was put.
func (ed *cutEditor) slideGreen(from []cutSeg, d float64) bool {
	a, b := ed.selSpan()
	in := func(s cutSeg) bool { return s.S >= a-1e-9 && s.E <= b+1e-9 }
	lo, hi, any := math.Inf(-1), math.Inf(1), false
	for _, s := range from {
		if !in(s) {
			continue
		}
		any = true
		// asked of where it came FROM, not of where it lands: on a row where
		// the camera was stopped and started again there are seconds nothing
		// covers, and a scene over one of those would be clamped by nothing.
		// A card has no footage of its own to run off the end of, and answers
		// nil here.
		if s.isInsert() {
			continue
		}
		if v := videoOn(ed.vids, s.Cam, s.S); v != nil {
			lo = math.Max(lo, v.start-s.S)
			hi = math.Min(hi, v.start+v.dur-s.E)
		}
	}
	if d = math.Max(lo, math.Min(hi, d)); !any || math.Abs(d) < 1e-9 {
		return false
	}
	out := make([]cutSeg, 0, len(from))
	for _, s := range from {
		if in(s) {
			s.S, s.E = s.S+d, s.E+d
		}
		out = append(out, s)
	}
	ed.segs = out
	ed.coalesce()
	return true
}

// ---- moving a part onto another row -----------------------------------------

// rowAt is the row of the picture band under y, counting a row's wave strip as
// the row: for moving things between rows, grabbing a part by its sound is
// grabbing the part.
func (ed *cutEditor) rowAt(y float64) int {
	if l := ed.laneAt(y); l >= 0 {
		return l
	}
	return ed.pairAt(y)
}

// rowFits says whether every dragged source could sit on row `to` without
// lying over something already there. Overlap in TIME is the one thing a row
// cannot draw -- there is no x where both could be shown -- which is exactly
// why assignLanes stacked them apart in the first place; everything else about
// a row move is bookkeeping.
func (ed *cutEditor) rowFits(srcs []string, to int) bool {
	if len(srcs) == 0 || to < 0 || to >= ed.laneN {
		return false
	}
	for i := range ed.vids {
		m := &ed.vids[i]
		if !contains(srcs, m.base) {
			continue
		}
		if m.lane == to {
			return false // already there: nothing to do, nothing to undo
		}
		for j := range ed.vids {
			v := &ed.vids[j]
			if v.lane != to || contains(srcs, v.base) {
				continue
			}
			if m.start < v.start+v.dur-1e-9 && v.start < m.start+m.dur-1e-9 {
				return false
			}
		}
	}
	return true
}

// moveRow puts the dragged sources onto row `to`, when there is room. The row
// is pinned (freezeRows) for the reason every hand-placed row is: cutSeg.Cam
// is a row number, and a derived colouring would put the part right back the
// moment the overlaps said so.
//
// The kept scenes that showed the moved footage go on showing it: a scene
// wholly inside a moved source's stretch, naming the row that source is
// leaving, is repointed at the row it lands on. A time drag deliberately does
// NOT do this -- sliding a clock under the cut is what that gesture is FOR --
// but a row drag changes no seconds, so a scene left behind would show a hole
// where its own footage used to be, and nobody dragged it to mean that.
//
// The row left behind may come out empty. It stays: the drag that emptied it
// can be dragged back, and closing it would renumber every scene on the rows
// below (killLane does that work, on purpose, when a row is actually killed).
func (ed *cutEditor) moveRow(srcs []string, to int) bool {
	if !ed.rowFits(srcs, to) {
		return false
	}
	from := -1
	for i := range ed.vids {
		if contains(srcs, ed.vids[i].base) {
			from = ed.vids[i].lane
			break
		}
	}
	ed.freezeRows()
	// the shape as it stands becomes the floor, so the vacated row survives
	// the relayout below even when it is the bottom one: emptied on purpose,
	// it stays until its ✕ says otherwise (killRow) -- the same wait an
	// emptied middle row already gets from the pins holding the gap open
	ed.nRows = max(ed.nRows, ed.laneN)
	for _, b := range srcs {
		ed.rows[b] = to
	}
	for i := range ed.segs {
		s := &ed.segs[i]
		if s.Cam != from || s.isInsert() {
			continue
		}
		for j := range ed.vids {
			m := &ed.vids[j]
			if contains(srcs, m.base) && s.S >= m.start-1e-9 && s.E <= m.start+m.dur+1e-9 {
				s.Cam = to
				break
			}
		}
	}
	ed.relayout()
	return true
}

// ---- snapping the drag ------------------------------------------------------

// slideSnapSet is worked out once, at the press: which session seconds this
// drag MOVES, and which still ones it might want to land exactly on. The
// moving set is the edges of the dragged recordings -- or, on a green drag,
// the borders of the scenes that will travel -- and the still set is
// everything else a hand lines things up against: the selection's two edges
// (the reason this exists: a part dragged along one row wants to start where
// the selection above it does), the playhead, the effects' bands, and the
// edges of every recording and scene that is staying put. Both are frozen
// here because the drag recomputes from the press each update -- a target
// that moved with the drag would drag the snap along with it.
func (ed *cutEditor) slideSnapSet(srcs []string, green bool) (edges, targets []float64) {
	// the session's own start, always: everything else here is only a target
	// while something still sits on it, and the one recording that anchors 0
	// stops offering it the moment it is the thing being dragged
	targets = append(targets, 0)
	a0, a1 := ed.selSpan()
	if ed.sel.active {
		targets = append(targets, a0, a1)
	}
	if ed.hasPlay {
		targets = append(targets, ed.playhead)
	}
	for _, f := range ed.fx {
		t0, t1 := f.fxSpan()
		targets = append(targets, t0)
		if t1 > t0 {
			targets = append(targets, t1)
		}
	}
	for _, v := range ed.vids {
		if contains(srcs, v.base) {
			edges = append(edges, v.start, v.start+v.dur)
		} else {
			targets = append(targets, v.start, v.start+v.dur)
		}
	}
	for _, au := range ed.auds {
		if au.master {
			continue // it moves with its footage, and the footage was counted
		}
		if contains(srcs, au.base) {
			edges = append(edges, au.start, au.start+au.dur)
		} else {
			targets = append(targets, au.start, au.start+au.dur)
		}
	}
	for _, s := range ed.segs {
		// the same wholly-inside rule slideGreen moves by: a scene that will
		// travel offers its borders to the still ones, never to itself
		if green && ed.sel.active && s.S >= a0-1e-9 && s.E <= a1+1e-9 {
			edges = append(edges, s.S, s.E)
		} else {
			targets = append(targets, s.S, s.E)
		}
	}
	return edges, targets
}

// slideSnap pulls a drag of d seconds onto the nearest exact meeting of a
// moving edge with a still one, when the hand brings any pair within tol
// seconds -- and otherwise changes nothing. Every moving edge is offered to
// every target and the closest pair wins, the same bargain the selection band
// and the effect bands make (snapSelSpan, snapFxSpan): flush on the left is
// worth as much as flush on the right.
func slideSnap(d float64, edges, targets []float64, tol float64) float64 {
	best, bd := d, tol
	for _, e := range edges {
		for _, t := range targets {
			if diff := math.Abs(t - (e + d)); diff < bd {
				best, bd = t-e, diff
			}
		}
	}
	return best
}

// shiftLabel is what the drag leaves in the status line. Signed seconds,
// because lining two waveforms up by eye is arithmetic nobody wants to do twice
// -- the number is what makes it repeatable on the next project shot with the
// same two devices.
func shiftLabel(what string, d float64) string {
	if d == 0 {
		return what + " is back where it started"
	}
	return fmt.Sprintf("%s moved %+.2f s", what, d)
}

// The two maps are copied in and out of every undo snapshot rather than shared,
// for the reason every other list in cutState is: a snapshot that pointed at the
// live map would be rewritten by the next drag and would then put the timeline
// back exactly where it already was.
func copyShift(m map[string]float64) map[string]float64 {
	if m == nil {
		return nil
	}
	out := make(map[string]float64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func copyRows(m map[string]int) map[string]int {
	if m == nil {
		return nil
	}
	out := make(map[string]int, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func sameShift(a, b map[string]float64) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// The rows are compared as well as the seconds, because a drag out and back
// leaves no seconds behind and still leaves the rows frozen -- and an undo that
// did not unfreeze them would leave the project nailed to a shape by an edit
// that is no longer in it.
func sameRows(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// sessionRows is the session timeline every step reads, put onto the timeline
// the cut is actually made against. session.tsv is written by Prepare off
// the raw file clocks, and a clock corrected by hand afterwards is exactly the
// thing it cannot know about: a line it places at 4:10 is where that recording
// USED to be, so Suggest would be told the wrong seconds, and Narrate and
// the upload text would gather the wrong lines under each clip. Which recording each
// line came off is in the file (tsvRow.src), so the correction is one addition
// per line.
func (a *App) sessionRows() []tsvRow {
	rows := loadTSVRows(filepath.Join(a.transcriptDir(), "session.tsv"))
	shift := a.produceCut().Shift
	if len(shift) == 0 {
		return rows
	}
	for i := range rows {
		d := shift[rows[i].src]
		rows[i].s, rows[i].e = rows[i].s+d, rows[i].e+d
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].s < rows[j].s })
	return rows
}
