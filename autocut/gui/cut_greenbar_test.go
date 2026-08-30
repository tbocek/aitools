package main

// The green bar, grown from a reading into a control. It has always said where
// the kept clip under the playhead begins and ends; now it answers the same
// verbs as the blue bar it shares the row with -- drag an end to move that
// end, drag the middle to move the whole -- except that what those verbs act
// on is the CLIP, through the same trimming and moving machinery the picture
// band's gestures drive. These tests hold who the bar stands for, what a press
// on it takes hold of, that holding it is holding the clip, the cursor, the
// pixels, and the wiring.

import (
	"os"
	"strings"
	"testing"
)

func greenEd(t *testing.T) *cutEditor {
	t.Helper()
	ed := bandEd(t) // clips 20-60 and 100-140, at 4 px a second
	ed.sel.active = false
	ed.hasPlay, ed.playhead = true, 120
	return ed
}

// Whose clip the bar is: the one in hand first -- held whole or by an edge,
// the bar stays on it, so trimming a clip's end does not make the bar vanish
// the moment the playhead lands on the border being dragged -- and otherwise
// the kept clip under the playhead, which is what it has always shown.
func TestTheBarKnowsWhoseClipItIs(t *testing.T) {
	ed := greenEd(t)
	if got := ed.bandClipIdx(); got != 1 {
		t.Errorf("playhead at 120 and the bar stands for clip %d, want 1", got)
	}
	ed.playhead = 80
	if got := ed.bandClipIdx(); got != -1 {
		t.Errorf("playhead in a dropped stretch and the bar stands for clip %d", got)
	}
	ed.hasPlay = false
	ed.playhead = 120
	if got := ed.bandClipIdx(); got != -1 {
		t.Errorf("no playhead on the page and the bar stands for clip %d", got)
	}

	// the clip in hand wins over the playhead's
	ed.hasPlay = true
	ed.segOn, ed.segSel = true, 0
	if got := ed.bandClipIdx(); got != 0 {
		t.Errorf("clip 1 is held whole and the bar stands for clip %d, want 0", got)
	}
	ed.segOn = false
	ed.edgeOn, ed.edgeSeg = true, 0
	if got := ed.bandClipIdx(); got != 0 {
		t.Errorf("clip 1's border is held and the bar stands for clip %d, want 0", got)
	}
	ed.edgeOn = false

	// an insert is not a kept clip: held or under the playhead, the bar is not it
	ed.segs[1].Ins = "card.svg"
	if got := ed.bandClipIdx(); got != -1 {
		t.Errorf("the playhead sits in an insert and the bar stands for clip %d", got)
	}
	ed.segOn, ed.segSel = true, 1
	if got := ed.bandClipIdx(); got != -1 {
		t.Errorf("an insert is held and the bar stands for clip %d", got)
	}
}

// What a press on the bar takes hold of: the blue's parts, told apart the
// blue's way -- ends first, then the ✕, then the middle. The ✕ was left off
// this bar at first, on the grounds that the only thing it could mean here is
// deleting footage; it is on it now because the scene badge over the pictures
// already offers exactly that verb in exactly that corner, and the blue bar
// drawn on top of this one is what hides the badge underneath.
func TestTheBarHasEndsAMiddleAndAKill(t *testing.T) {
	ed := greenEd(t) // the bar is clip 100-140: px 400-560
	for _, c := range []struct {
		px        float64
		seg, part int
		what      string
	}{
		{ed.xOf(100), 1, selStart, "the left end"},
		{ed.xOf(100) + selGripPx, 1, selStart, "the edge of the left grip"},
		{ed.xOf(100) + selGripPx + 1, 1, selWhole, "just inboard of the grip"},
		{ed.xOf(140), 1, selEnd, "the right end"},
		{ed.xOf(120), 1, selWhole, "the middle"},
		// the blue bar's ✕ spot, answering the blue bar's way
		{ed.xOf(140) - selKillIn - selKillW/2, 1, selKill, "the ✕"},
		{ed.xOf(80), -1, selNone, "clear of it"},
	} {
		seg, part := ed.bandClipPartAt(c.px)
		if seg != c.seg || part != c.part {
			t.Errorf("a press at %s lands on clip %d part %d, want %d/%d",
				c.what, seg, part, c.seg, c.part)
		}
	}
	ed.hasPlay = false
	if _, part := ed.bandClipPartAt(ed.xOf(120)); part != selNone {
		t.Errorf("no bar on the page and a press still landed on part %d", part)
	}
}

// Holding the bar IS holding the clip: an end press is the clip's border in
// hand exactly as grabEdge leaves it, a middle press the whole clip exactly as
// grabSeg does -- so the drag that follows runs through moveEdgeTo/moveSegTo
// with their clamps, and the blue bar is put down first.
func TestHoldingTheBarHoldsTheClip(t *testing.T) {
	ed := greenEd(t)
	ed.sel.active, ed.selOn = true, true

	ed.holdBandClip(1, selStart)
	if !ed.edgeOn || ed.edgeSeg != 1 || ed.edgeEnd || ed.segOn {
		t.Fatalf("the left end held edge=%v seg=%d end=%v segOn=%v, want clip 2's start",
			ed.edgeOn, ed.edgeSeg, ed.edgeEnd, ed.segOn)
	}
	if ed.selOn {
		t.Error("taking the green bar did not put the blue selection down")
	}
	ed.moveEdgeTo(95, true)
	if ed.segs[1].S != 95 {
		t.Errorf("dragging the left end to 95 left the clip starting at %g", ed.segs[1].S)
	}
	// the clamp is the border's own: never past the neighbour
	ed.moveEdgeTo(30, true)
	if ed.segs[1].S < ed.segs[0].E {
		t.Errorf("the left end went to %g, onto the clip before it", ed.segs[1].S)
	}

	ed.holdBandClip(1, selEnd)
	if !ed.edgeOn || !ed.edgeEnd {
		t.Fatal("the right end did not hold the clip's end border")
	}
	ed.moveEdgeTo(150, true)
	if ed.segs[1].E != 150 {
		t.Errorf("dragging the right end to 150 left the clip ending at %g", ed.segs[1].E)
	}

	// the clamp above walked clip 2's start onto clip 1's end; give the move room
	ed.segs[1] = cutSeg{S: 100, E: 140}
	ed.holdBandClip(0, selWhole)
	if !ed.segOn || ed.segSel != 0 || ed.edgeOn {
		t.Fatalf("the middle held seg=%v sel=%d edge=%v, want clip 1 whole",
			ed.segOn, ed.segSel, ed.edgeOn)
	}
	ed.moveSegTo(30, true)
	if ed.segs[0].S != 30 || ed.segs[0].E != 70 {
		t.Errorf("dragging the middle to 30 left the clip at %g-%g, want 30-70",
			ed.segs[0].S, ed.segs[0].E)
	}
	// and each hold that changed the cut is one undo step, pushed by the move
	if len(ed.undo) == 0 {
		t.Error("three drags pushed no undo at all")
	}
}

// The pointer says which verb a press would get, and the blue answers first
// where the two bars overlap -- the same precedence the press itself has.
func TestTheCursorSpeaksForTheGreenBar(t *testing.T) {
	ed := greenEd(t)
	y := ed.selBandTop() + selBandH/2
	for _, c := range []struct {
		x    float64
		want string
		what string
	}{
		{ed.xOf(100), "ew-resize", "the bar's left end"},
		{ed.xOf(140), "ew-resize", "the bar's right end"},
		{ed.xOf(120), "grab", "the bar's middle"},
		{ed.xOf(80), "", "clear of the bar"},
	} {
		if got := ed.wantCursor(c.x, y); got != c.want {
			t.Errorf("over %s the cursor is %q, want %q", c.what, got, c.want)
		}
	}
	// a blue selection over the same clip: its ✕ wins over the green middle
	ed.sel.t0, ed.sel.t1, ed.sel.active = 100, 130, true
	kx := ed.xOf(130) - selKillIn - selKillW/2
	if got := ed.wantCursor(kx, y); got != "pointer" {
		t.Errorf("over the blue's ✕ the cursor is %q — the green middle answered first", got)
	}
}

// The bar wears its verbs: end handles brighter than the fill, and the same
// white ring the blue bar puts on when what it stands for is in hand.
func TestTheBarWearsItsVerbs(t *testing.T) {
	ed := greenEd(t)
	const w, h = 1300, 200
	midY := int(ed.selBandTop()) + selBandH/2

	at := renderTrack(t, ed, w, h)
	_, fg, _ := at(int(ed.xOf(110)), midY)
	hr, hg, _ := at(int(ed.xOf(100)), midY)
	if fg < 100 || fg > 180 {
		t.Errorf("the bar's fill reads green %d — the fixture stopped drawing the bar", fg)
	}
	if hg < 200 || hr > 180 {
		t.Errorf("the end handle reads r=%d g=%d — no brighter than the fill, it does not read as a handle", hr, hg)
	}
	// unheld, the ring's row is the band's own dark background
	rx, ry := int(ed.xOf(110)), int(ed.selBandTop())+1
	if r, g, b := at(rx, ry); r > 100 && g > 100 && b > 100 {
		t.Errorf("an unheld bar wears a ring (%d,%d,%d at its rim)", r, g, b)
	}
	ed.segOn, ed.segSel = true, 1
	if r, g, b := renderTrack(t, ed, w, h)(rx, ry); r < 180 || g < 180 || b < 180 {
		t.Errorf("the held bar's rim reads (%d,%d,%d), want the white ring", r, g, b)
	}
}

// The wiring: the press lands in the selection-band branch of the one drag
// gesture, after the blue has had its say, and what it starts is the picture
// band's own trimming and moving -- not a third copy of either.
func TestTheGreenBarIsWired(t *testing.T) {
	b, err := os.ReadFile("cut.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	grab := strings.Index(src, "if i, part := ed.bandClipPartAt(x + ed.viewX); part != selNone {")
	if grab < 0 {
		t.Fatal("the drag gesture no longer asks the green bar")
	}
	if blue := strings.Index(src, "ed.holdSel(selPart)"); blue < 0 || blue > grab {
		t.Error("the green bar is asked before the blue selection — the bar on top answers second")
	}
	fall := strings.Index(src, "ed.dropSel() // clear of it: this is a new selection")
	if fall < grab {
		t.Fatal("the green branch does not sit before the new-selection fall-through")
	}
	branch := src[grab:fall]
	for _, want := range []string{
		"ed.holdBandClip(i, part)",
		"trimming = true", // an end press is the picture band's own trim...
		"moving = true",   // ...and a middle press its own clip move
	} {
		if !strings.Contains(branch, want) {
			t.Errorf("the green-bar branch no longer contains %q", want)
		}
	}
	// hovering it says so before the press: the row's motion handler feeds
	// bandHov, and the drawing wears it
	sb, err := os.ReadFile("cut_selband.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ed.bandHov", "case ed.bandHov:"} {
		if !strings.Contains(string(sb), want) {
			t.Errorf("cut_selband.go lost the hover feedback pinned by %q", want)
		}
	}
}
