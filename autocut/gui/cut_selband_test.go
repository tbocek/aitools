package main

// The selection band.
//
// The selection used to be a tint over the thumbnails: something you could make
// and act on, and nothing else. What is pinned here is that it is now an object
// -- it has a row, ends that move on their own, a middle that moves the whole
// of it, and a ✕ -- and that the ends land on the things a selection is
// actually aimed at rather than wherever the hand let go.

import (
	"math"
	"os"
	"strings"
	"testing"
)

func bandEd(t *testing.T) *cutEditor {
	t.Helper()
	ed := newTestEd(t) // pps 4: a session second is four px
	ed.vids = []tlVideo{{base: "v", path: "v.mkv", start: 0, dur: 300, interval: 5, fps: 30}}
	ed.relayout()
	ed.segs = []cutSeg{{S: 20, E: 60}, {S: 100, E: 140}}
	ed.sel.t0, ed.sel.t1, ed.sel.active = 200, 230, true
	return ed
}

// ---- where it is ------------------------------------------------------------

// Under the ruler's clock and over the pictures, which is where it was asked
// for and also the only place it can go: the clock has to stay legible and the
// band has to be next to the frames it is over. Between the two sits the ▲▼
// scope strip -- the one row that reads across the whole page, over the bands
// it scopes.
func TestTheBandSitsBetweenTheClockAndThePictures(t *testing.T) {
	ed := bandEd(t)
	if got, want := ed.selBandTop(), float64(rulerH)+scopeH; got != want {
		t.Errorf("the band starts at %g, want it under the ▲▼ strip at %g", got, want)
	}
	if got, want := ed.picTop(), float64(rulerH)+scopeH+float64(selBandH); got != want {
		t.Errorf("the pictures start at %g, want %g — the band has no room", got, want)
	}
	for _, c := range []struct {
		y    float64
		want bool
		what string
	}{
		{float64(rulerH) - 1, false, "in the ruler"},
		{float64(rulerH) + 1, false, "in the ▲▼ strip"},
		{ed.selBandTop() + 1, true, "the top of the band"},
		{ed.picTop() - 1, true, "the bottom of the band"},
		{ed.picTop() + 1, false, "on the pictures"},
	} {
		if got := ed.hitSelBand(c.y); got != c.want {
			t.Errorf("y %g (%s) hits the band = %v, want %v", c.y, c.what, got, c.want)
		}
	}
	// and the effects lane moved down with everything else rather than staying
	// where it was and being drawn over
	if ed.fxLaneTop() < ed.picTop()+float64(ed.thumbHt) {
		t.Errorf("the effects lane starts at %g, inside the pictures that end at %g",
			ed.fxLaneTop(), ed.picTop()+float64(ed.thumbHt))
	}
}

// ---- what a press lands on --------------------------------------------------

func TestTheEndsOfTheBandAreItsHandles(t *testing.T) {
	ed := bandEd(t) // 200..230 s, 4 px/s, so 120..920 px, 120 px wide
	x0, x1 := ed.selSpanPx()
	for _, c := range []struct {
		px   float64
		want int
		what string
	}{
		{x0, selStart, "on the left end"},
		{x0 + selGripPx - 1, selStart, "just inside the left end"},
		{x1, selEnd, "on the right end"},
		{x1 - selGripPx + 1, selEnd, "just inside the right end"},
		{(x0 + x1) / 2, selWhole, "in the middle"},
		{x1 - bandKillIn, selKill, "on the ✕"},
		{x0 - 20, selNone, "clear of it on the left"},
		{x1 + 20, selNone, "clear of it on the right"},
	} {
		if got := ed.selPartAt(c.px); got != c.want {
			t.Errorf("a press %s (px %g) takes part %d, want %d", c.what, c.px, got, c.want)
		}
	}
	// the ✕ is inboard of the right grip and not under it: "throw it away" must
	// not be reachable by a hand aiming at "make it a bit shorter"
	if ed.selPartAt(x1-selGripPx+1) == selKill {
		t.Error("the ✕ and the right handle are the same pixels")
	}
	// a band with no middle is all ends, rather than a middle you cannot escape
	ed.sel.t1 = ed.sel.t0 + 1 // 4 px
	if got := ed.selPartAt(ed.xOf(ed.sel.t0) + 2); got == selKill || got == selWhole {
		t.Errorf("a four-pixel band answered %d in its middle, want an end", got)
	}
	// and when there is no selection there is nothing to press
	ed.sel.active = false
	if got := ed.selPartAt(x0); got != selNone {
		t.Errorf("a press with no selection took part %d, want none", got)
	}
}

// ---- moving it --------------------------------------------------------------

func TestTheWholeBandSlidesWithoutChangingLength(t *testing.T) {
	ed := bandEd(t)
	ed.moveSelTo(250)
	a, b := ed.selSpan()
	if math.Abs(a-250) > 1e-9 || math.Abs(b-a-30) > 1e-9 {
		t.Errorf("the band moved to %.2f..%.2f, want 250.00..280.00", a, b)
	}
	// and it cannot be pushed off the front of the session
	ed.moveSelTo(-50)
	if a, _ := ed.selSpan(); a < 0 {
		t.Errorf("the band moved to %.2f, before the session starts", a)
	}
}

func TestEitherEndMovesOnItsOwn(t *testing.T) {
	ed := bandEd(t)
	ed.resizeSelTo(true, 245) // the right end
	if a, b := ed.selSpan(); a != 200 || b != 245 {
		t.Errorf("moving the right end gave %.2f..%.2f, want 200.00..245.00", a, b)
	}
	ed.resizeSelTo(false, 210) // the left end
	if a, b := ed.selSpan(); a != 210 || b != 245 {
		t.Errorf("moving the left end gave %.2f..%.2f, want 210.00..245.00", a, b)
	}
	// dragged past the far end it stops short of it: a band of no length is
	// invisible, cannot be grabbed again, and every action on it does nothing,
	// so overshooting must not silently destroy the thing being adjusted
	ed.resizeSelTo(true, 100)
	a, b := ed.selSpan()
	if b <= a {
		t.Errorf("the right end was dragged past the left: %.3f..%.3f", a, b)
	}
	if b-a > 2*selMinLen {
		t.Errorf("the band stopped %.3f s short, want about %.3f", b-a, selMinLen)
	}
}

// The point of the snapping. A selection is nearly always aimed at something
// that is already on the page -- a border of the cut, the seam between two
// recordings, where the playhead is standing -- and at four pixels a second
// none of those can be hit by hand.
func TestTheEndsLandOnTheCuts(t *testing.T) {
	ed := bandEd(t) // cuts at 20, 60, 100, 140; the recording runs 0..300
	ed.hasPlay, ed.playhead = true, 250
	tol := snapPx / ed.pps // 2 s at this zoom
	for _, c := range []struct {
		drag, want float64
		what       string
	}{
		{60 + tol/2, 60, "a border of the cut"},
		{140 - tol/2, 140, "the far border of the cut"},
		{300 - tol/2, 300, "the end of the recording"},
		{250 + tol/2, 250, "the playhead"},
		{175, 175, "nothing in particular"},
	} {
		ed.sel.t0, ed.sel.t1 = 10, 15 // the end is dragged rightward from here
		ed.resizeSelTo(true, c.drag)
		if _, b := ed.selSpan(); math.Abs(b-c.want) > 1e-9 {
			t.Errorf("an end let go at %.2f (%s) landed at %.2f, want %.2f",
				c.drag, c.what, b, c.want)
		}
	}
}

// A band being slid along offers BOTH its ends to the landmarks. Snapping only
// the leading end would mean a selection can be put flush against the cut on
// its left and never on its right, which is half a feature -- and usually the
// wrong half, because a selection is dragged leftward as often as rightward.
func TestASlidingBandLandsByEitherEnd(t *testing.T) {
	ed := bandEd(t)
	ed.sel.t0, ed.sel.t1 = 150, 170 // twenty seconds long
	tol := snapPx / ed.pps
	// its END brought near the cut at 100: the band lands so the end is on it
	ed.moveSelTo(80 - tol/2)
	a, b := ed.selSpan()
	if math.Abs(b-100) > 1e-9 {
		t.Errorf("slid so its end was near the cut at 100, the band sits %.2f..%.2f", a, b)
	}
	if math.Abs(b-a-20) > 1e-9 {
		t.Errorf("the band changed length while being slid: %.2f s, want 20", b-a)
	}
}

// The band and the marks readout are one object seen twice: the marks are how
// the rest of the page reads the band, so a band that could be dragged away
// from them would leave the clock describing a selection that is nowhere.
func TestMovingTheBandMovesTheMarks(t *testing.T) {
	ed := bandEd(t)
	ed.moveSelTo(250)
	if !ed.hasIn || !ed.hasOut || ed.markIn != 250 || ed.markOut != 280 {
		t.Errorf("after sliding the band the marks are %.2f..%.2f (%v,%v), want 250..280 set",
			ed.markIn, ed.markOut, ed.hasIn, ed.hasOut)
	}
	ed.resizeSelTo(true, 260)
	if ed.markOut != 260 {
		t.Errorf("after moving the end the out mark is %.2f, want 260", ed.markOut)
	}
}

// ✕ removes the SELECTION. ⌦ removes what the selection is over. Two different
// removes on one object is worth keeping straight, so this pins the harmless
// one: the footage must survive it.
func TestTheXThrowsAwayTheSelectionAndNotTheFootage(t *testing.T) {
	ed := bandEd(t)
	before := len(ed.segs)
	ed.killSel()
	if ed.sel.active || ed.selOn {
		t.Error("the selection survived its own ✕")
	}
	if ed.hasIn || ed.hasOut {
		t.Error("the in/out marks survived the selection they came from")
	}
	if len(ed.segs) != before {
		t.Errorf("✕ took %d segments of footage with it", before-len(ed.segs))
	}
}

// ---- and it is on the page --------------------------------------------------

// In pixels, because "it has a row" is a claim about the page.
func TestTheBandIsDrawnInItsRow(t *testing.T) {
	ed := bandEd(t)
	ed.sel.t0, ed.sel.t1 = 20, 50 // 80..200 px, inside the 400 px we render
	const w, h = 400, 200
	at := renderTrack(t, ed, w, h)
	x := int(ed.xOf(35))                     // the middle of the selection
	mid := int(ed.selBandTop()) + selBandH/2 // the middle of the band's row
	isBand := func(y int) bool {
		r, _, b := at(x, y)
		return b > 130 && int(b) > int(r)+40
	}
	if !isBand(mid) {
		t.Error("the selection is not drawn in its own row")
	}
	if isBand(rulerH - 4) {
		t.Error("the band is drawn over the ruler's clock")
	}
	// clear of the selection the row is still there, as ground: an empty band
	// is a place a selection could go, not a gap in the page
	rx := int(ed.xOf(70))
	r, g, b := at(rx, mid)
	if r < 25 || r > 60 || g < 25 || g > 60 || b < 25 || b > 70 {
		t.Errorf("the empty part of the row painted rgb(%d,%d,%d), want the row's ground", r, g, b)
	}
	// and the handles are brighter than the fill, because what can be dragged
	// has to look like it can be dragged
	green := func(x, y int) uint8 { _, g, _ := at(x, y); return g }
	fill := func(y int) uint8 { return green(x, y) }
	end := func(y int) uint8 { return green(int(ed.xOf(50)), y) }
	if end(mid) <= fill(mid) {
		t.Error("the end of the band is no brighter than its middle — the handles do not read as handles")
	}
}

func TestTheSelectionBandIsWired(t *testing.T) {
	b, err := os.ReadFile("cut.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{
		// the row is drawn inside the source track, above the effects lane
		"ed.drawSelBand(cr, vx0, vx1)",
		// a press in the row is about the selection: ✕ first, then a part
		"if area == ed.srcArea && ed.hitSelBand(y) {",
		"if selPart = ed.selPartAt(x + ed.viewX); selPart == selKill {",
		"ed.killSel()",
		"ed.holdSel(selPart)",
		// ...and on the green bar the ✕ removes the clip it stands for: the
		// page's one "drop that scene", now that the badge over the
		// thumbnails is gone
		"ed.killSeg(i) // the page's one \"drop that scene\"",
		// ...and a press clear of it in the row starts a new selection, which
		// is what the fall-through to the rubber band does
		"ed.dropSel() // clear of it: this is a new selection",
		// the drag moves the whole or one end
		"ed.moveSelTo(ed.tAtView(dragStartX+ox) - grabAt)",
		"ed.resizeSelTo(selPart == selEnd, ed.tAtView(dragStartX+ox))",
		// Esc puts it down, like everything else that can be held
		"ed.dropSel()",
		// and the pointer says what a press would do before it is pressed
		"hover.ConnectMotion(func(x, y float64) { ed.hoverTracks(x, y) })",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("the cut page no longer contains %q", want)
		}
	}
}

// Every kept clip is green in the band, always -- that is what makes the row
// worth reading: the tint that says "kept" is drawn over the thumbnails, so on
// a dark capture it is green on black, and the band's own flat ground is where
// it can actually be seen. The clip the red line is inside is drawn taller than
// the rest, because that is the one the hand can take hold of (bandClipPartAt).
func TestTheBandShowsEveryKeptClipAndSingsOutTheOneUnderThePlayhead(t *testing.T) {
	ed := bandEd(t) // clips 20-60 and 100-140; selection 200-230
	ed.sel.active = false
	ed.playhead, ed.hasPlay = 40, true
	const w, h = 1300, 200
	mid := int(ed.selBandTop()) + selBandH/2
	top := int(ed.selBandTop()) + 3 // only the tall bar reaches this high
	green := func(at func(x, y int) (uint8, uint8, uint8), x, y int) bool {
		r, g, b := at(x, y)
		return int(g) > int(r)+30 && int(g) > int(b)+30
	}

	at := renderTrack(t, ed, w, h)
	// sampled a few px off 40 s: the playhead's own red line is drawn over the
	// band at exactly xOf(40), and a red pixel is the line, not a missing bar
	if !green(at, int(ed.xOf(40))+3, mid) {
		t.Error("the playhead sits at 40 s inside the 20-60 clip and the band shows no green there")
	}
	// the bar is the clip's whole span, not a marker at the playhead...
	if !green(at, int(ed.xOf(21)), mid) || !green(at, int(ed.xOf(59)), mid) {
		t.Error("the green bar does not span its clip from 20 to 60")
	}
	// ...the OTHER kept clip is green too, which is the point of the row...
	if !green(at, int(ed.xOf(120)), mid) {
		t.Error("a kept clip the playhead is not inside has no bar, so the band says nothing about the rest of the cut")
	}
	// ...and what the cut threw away is still not green
	if green(at, int(ed.xOf(80)), mid) {
		t.Error("the band is green over a stretch the cut does not keep")
	}
	// the tall one is the clip in hand's reach, and only that one
	if !green(at, int(ed.xOf(21)), top) {
		t.Error("the clip under the playhead is not drawn taller, so nothing says which bar answers the hand")
	}
	if green(at, int(ed.xOf(120)), top) {
		t.Error("a clip the hand cannot reach is drawn at the full height that promises handles")
	}

	// no playhead: the bars stay -- "always" is the rule -- and none of them is
	// the tall one, because none of them is in reach
	ed.hasPlay = false
	at = renderTrack(t, ed, w, h)
	if !green(at, int(ed.xOf(40)), mid) || !green(at, int(ed.xOf(120)), mid) {
		t.Error("the band went blank when the playhead left the page")
	}
	if green(at, int(ed.xOf(40)), top) || green(at, int(ed.xOf(120)), top) {
		t.Error("a bar is drawn as if the hand could reach it while nothing is in hand")
	}
	// a playhead in a dropped stretch takes the tall bar with it
	ed.playhead, ed.hasPlay = 80, true
	if green(renderTrack(t, ed, w, h), int(ed.xOf(40)), top) {
		t.Error("a clip the playhead is not inside is still drawn as the one in hand")
	}
}

// TestTheGreenBarsXRemovesThatClip: the green bar answers every verb the blue
// one does, and that includes the ✕. It is the page's only "drop that scene"
// since the badge over the thumbnails went: the bar has a flat row of its own,
// so the mark is legible over any footage, and the row is deep enough to give
// it the plate every other ✕ on the page wears.
func TestTheGreenBarsXRemovesThatClip(t *testing.T) {
	ed := bandEd(t) // clips 20-60 and 100-140
	ed.sel.active = false
	ed.playhead, ed.hasPlay = 40, true
	x0, x1 := ed.xOf(20), ed.xOf(60)
	for _, c := range []struct {
		px   float64
		want int
		what string
	}{
		{x1 - bandKillIn, selKill, "on the ✕"},
		{(x0 + x1) / 2, selWhole, "in the middle"},
		{x0, selStart, "on the left end"},
		{x1, selEnd, "on the right end"},
	} {
		if _, got := ed.bandClipPartAt(c.px); got != c.want {
			t.Errorf("a press %s (px %g) takes part %d, want %d", c.what, c.px, got, c.want)
		}
	}
	// the same guard the blue bar has: "remove this footage" must not be
	// reachable by a hand aiming at "make it a bit shorter"
	if _, got := ed.bandClipPartAt(x1 - selGripPx + 1); got == selKill {
		t.Error("the green ✕ and the right handle are the same pixels")
	}
	// pressing it drops that clip and nothing else, undoably
	before := len(ed.segs)
	ed.killSeg(0)
	if len(ed.segs) != before-1 {
		t.Fatalf("%d clips left, want %d", len(ed.segs), before-1)
	}
	if ed.segs[0].S != 100 {
		t.Errorf("the wrong clip went: the survivor starts at %g, want 100", ed.segs[0].S)
	}
	ed.undoLast()
	if len(ed.segs) != before {
		t.Error("↶ did not bring the removed clip back")
	}
}

// TestTheGreenBarWearsItsXOnScreen: a verb the hand cannot see is a verb
// nobody uses, so the mark is drawn always -- plate under arms, so it survives
// whatever the bar is drawn over -- and only on a bar with the room to hold it
// clear of its own middle (bandKillMin).
func TestTheGreenBarWearsItsXOnScreen(t *testing.T) {
	ed := bandEd(t)
	ed.sel.active = false
	ed.playhead, ed.hasPlay = 40, true
	const w, h = 1300, 200
	y := int(ed.selBandTop()) + selBandH/2
	kx := int(ed.xOf(60) - bandKillIn)
	whitish := func(at func(x, y int) (uint8, uint8, uint8)) bool {
		for dx := -3; dx <= 3; dx++ {
			for dy := -3; dy <= 3; dy++ {
				r, g, b := at(kx+dx, y+dy)
				if r > 200 && g > 200 && b > 200 {
					return true
				}
			}
		}
		return false
	}
	if !whitish(renderTrack(t, ed, w, h)) {
		t.Error("no ✕ is drawn on the green bar")
	}
	// a clip too narrow to aim at keeps its colour instead, and goes with ⌦
	// like anything else too small to hit. The playhead moves inside the
	// shrunken clip -- outside it there would be no bar at all, and no bar
	// draws no ✕ for the wrong reason.
	ed.segs[0].E = ed.segs[0].S + (bandKillMin-4)/ed.pps
	ed.playhead = (ed.segs[0].S + ed.segs[0].E) / 2
	if got := ed.bandClipIdx(); got != 0 {
		t.Fatalf("the shrunken clip is not the bar (idx %d) -- the check below would pass for nothing", got)
	}
	kx = int(ed.xOf(ed.segs[0].E) - bandKillIn)
	if whitish(renderTrack(t, ed, w, h)) {
		t.Error("a bar under the width floor still drew a ✕")
	}
}

// The badge's target is bigger than its plate, and neither reaches the grip
// that trims the clip: a control you can see but not comfortably hit is worse
// than no control, and one that overlaps a different verb is worse than that.
func TestTheGreenBarsXIsEasierToHitThanToSeeAndClearOfTheGrip(t *testing.T) {
	if segKillHit <= segKillR+segKillPad {
		t.Errorf("the ✕'s target (%.1f) is no bigger than its plate (%.1f)",
			segKillHit, segKillR+segKillPad)
	}
	// the plate stops before the grip does: drawn over it, the mark would sit
	// on pixels that resize the clip
	if bandKillIn-(segKillR+segKillPad) <= selGripPx {
		t.Errorf("the plate reaches %.1f px in from the end, inside the %.1f px grip",
			bandKillIn-(segKillR+segKillPad), selGripPx)
	}
	ed := bandEd(t)
	ed.sel.active = false
	ed.playhead, ed.hasPlay = 40, true
	x1 := ed.xOf(60)
	for _, dx := range []float64{-(segKillR + segKillPad), segKillR + segKillPad} {
		if _, part := ed.bandClipPartAt(x1 - bandKillIn + dx); part != selKill {
			t.Errorf("the plate's %+.0f px edge takes part %d, not the ✕", dx, part)
		}
	}
	// and it stops: the bar is not a remove button
	if _, part := ed.bandClipPartAt(x1 - bandKillIn - 3*segKillHit); part == selKill {
		t.Error("the ✕ answers from most of the way along the bar")
	}
}

// The pointer lights it, and the bar's own ring is not that promise: the ring
// says this clip answers the hand, the red plate says this press removes it.
func TestTheGreenBarsXLightsUnderThePointer(t *testing.T) {
	ed := bandEd(t)
	ed.sel.active = false
	ed.playhead, ed.hasPlay = 40, true
	y := ed.selBandTop() + selBandH/2
	kx := ed.xOf(60) - bandKillIn

	ed.hoverTracks(kx-ed.viewX, y)
	if ed.bandKillHov != 0 {
		t.Errorf("the pointer on clip 1's ✕ lit bar %d", ed.bandKillHov)
	}
	if c := ed.wantCursor(kx-ed.viewX, y); c != "pointer" {
		t.Errorf("the pointer on the ✕ asked for %q, want a pointer", c)
	}
	// the ✕ on the OTHER bar lights that one, not this one: the mark is per
	// bar, as the effects lane's are
	if ed.hoverTracks(ed.xOf(140)-bandKillIn-ed.viewX, y); ed.bandKillHov != 1 {
		t.Errorf("the pointer on clip 2's ✕ lit bar %d, want 1", ed.bandKillHov)
	}
	// the middle of a bar holds the bar, and lights no ✕
	ed.hoverTracks(ed.xOf(30)-ed.viewX, y)
	if ed.bandKillHov >= 0 {
		t.Errorf("the middle of the bar lit bar %d's ✕", ed.bandKillHov)
	}
	if !ed.bandHov {
		t.Error("the bar itself did not light under the pointer")
	}
	// and the pointer leaving puts it out
	ed.hoverTracks(kx-ed.viewX, y)
	ed.hoverTracks(-1, -1)
	if ed.bandKillHov >= 0 || ed.bandHov {
		t.Error("the pointer left the page and the bar stayed lit")
	}

	// in pixels: red plate under the pointer, dark plate off it
	const w, h = 1300, 200
	red := func(at func(x, y int) (uint8, uint8, uint8)) bool {
		// off the centre by 5: the arms cross at the middle of the plate and
		// they are white on both plates, hot or not
		r, g, b := at(int(kx), int(y)-5)
		return r > 150 && int(r) > int(g)+60 && int(r) > int(b)+60
	}
	ed.bandKillHov = -1
	if red(renderTrack(t, ed, w, h)) {
		t.Error("the ✕ is red with the pointer elsewhere — every remove on the page reads as pressed")
	}
	ed.bandKillHov = 0
	if !red(renderTrack(t, ed, w, h)) {
		t.Error("the ✕ under the pointer is not red")
	}
}

// One recipe for the mark, wherever a remove is drawn: the bar's ✕, a cut
// lane's, an emptied row's and an effect's are one function, so a remove looks
// like a remove and there is one place to change it.
func TestEveryKillBadgeIsTheSameBadge(t *testing.T) {
	for _, f := range []string{"cut_selband.go", "cut_lane.go", "cut_fxkill.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), "drawKillBadge(") {
			t.Errorf("%s draws its ✕ some other way than drawKillBadge", f)
		}
		if strings.Contains(string(b), "cr.Arc(cx, cy, segKillR+segKillPad") {
			t.Errorf("%s still has a copy of the badge's own drawing", f)
		}
	}
	// and the verb － Remove was taken off the bar for is still the
	// selection's alone: a toolbar remove that guesses is what the ✕ ended
	b, err := os.ReadFile("cut.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "ed.remBtn.ConnectClicked(func() { a.removeSelRange() })") {
		t.Error("－ Remove is wired to something other than the selection's verb")
	}
}

// Every bar wears a live ✕, not just the one the red line is in. A mark that
// only works after you have clicked the clip is a control with a lock on it:
// the press that puts the line in a clip exists only to make the next press
// possible, and the effects lane has never asked for it (cut_fxkill.go). What
// stays the tall bar's alone is trimming and moving, which are drags that need
// to know which clip is anchored.
func TestEveryGreenBarsXIsLiveWhereverTheLineIs(t *testing.T) {
	ed := bandEd(t) // clips 20-60 and 100-140
	ed.sel.active = false
	ed.playhead, ed.hasPlay = 40, true // the line is in the FIRST clip

	// the second clip's ✕ answers anyway, and takes the second clip
	i, part := ed.bandClipPartAt(ed.xOf(140) - bandKillIn)
	if i != 1 || part != selKill {
		t.Fatalf("the ✕ on the bar the line is not in takes clip %d part %d, want 1/%d", i, part, selKill)
	}
	if got := ed.wantCursor(ed.xOf(140)-bandKillIn-ed.viewX, ed.selBandTop()+selBandH/2); got != "pointer" {
		t.Errorf("over that ✕ the cursor is %q, want a pointer", got)
	}
	// ...and only its ✕: the rest of that bar is not the hand's, because the
	// tall bar is the one with handles and there is only one of those
	if _, part := ed.bandClipPartAt(ed.xOf(120)); part != selNone {
		t.Errorf("the middle of a bar the line is not in takes part %d", part)
	}
	if _, part := ed.bandClipPartAt(ed.xOf(100)); part != selNone {
		t.Errorf("the end of a bar the line is not in takes part %d", part)
	}

	// no line on the page at all and both are still live: "put the line
	// somewhere first" is exactly the press this must not require
	ed.hasPlay = false
	if got := ed.bandKillAt(ed.xOf(60) - bandKillIn); got != 0 {
		t.Errorf("with no playhead the first clip's ✕ answers %d, want 0", got)
	}
	if got := ed.bandKillAt(ed.xOf(140) - bandKillIn); got != 1 {
		t.Errorf("with no playhead the second clip's ✕ answers %d, want 1", got)
	}
	ed.playhead, ed.hasPlay = 80, true // and in a stretch the cut dropped
	if got := ed.bandKillAt(ed.xOf(140) - bandKillIn); got != 1 {
		t.Errorf("with the line in a dropped stretch the ✕ answers %d, want 1", got)
	}
	// pressing it drops that clip, and it is the clip the mark was on
	ed.killSeg(ed.bandKillAt(ed.xOf(140) - bandKillIn))
	if len(ed.segs) != 1 || ed.segs[0].S != 20 {
		t.Errorf("the ✕ on the second bar left %+v", ed.segs)
	}
}

// And it is drawn on every bar, dim ones included -- a live control that is
// only drawn on one of them is worse than one that is drawn nowhere.
func TestEveryGreenBarWearsTheXOnScreen(t *testing.T) {
	ed := bandEd(t) // clips 20-60 and 100-140
	ed.sel.active = false
	ed.playhead, ed.hasPlay = 40, true // clip 1 is the tall bar; clip 2 is dim
	const w, h = 1300, 200
	at := renderTrack(t, ed, w, h)
	y := int(ed.selBandTop()) + selBandH/2
	for _, c := range []struct {
		end  float64
		what string
	}{{60, "the bar the line is in"}, {140, "a bar the line is not in"}} {
		kx := int(ed.xOf(c.end) - bandKillIn)
		white := false
		for dx := -3; dx <= 3 && !white; dx++ {
			for dy := -3; dy <= 3 && !white; dy++ {
				if r, g, b := at(kx+dx, y+dy); r > 200 && g > 200 && b > 200 {
					white = true
				}
			}
		}
		if !white {
			t.Errorf("%s has no ✕ drawn on it", c.what)
		}
	}
	// the dim bar is deep enough to hold the plate it now carries: its own
	// fill, not the row's ground, under the badge's top and bottom edge
	kx := int(ed.xOf(140) - bandKillIn)
	for _, dy := range []int{-int(segKillR+segKillPad) + 1, int(segKillR+segKillPad) - 1} {
		r, g, b := at(kx-int(segKillR+segKillPad)-1, y+dy)
		if !(int(g) > int(r)+20 && int(g) > int(b)+20) {
			t.Errorf("beside the badge at dy=%d the bar reads rgb(%d,%d,%d) — the plate hangs off it",
				dy, r, g, b)
		}
	}
}
