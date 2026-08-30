package main

// The ✕ on a kept scene.
//
// Removing footage was a toolbar button, and a toolbar button acts on whatever
// it can work out you meant: the selection, or the clip in hand, or the scene
// under the red line. The ✕ acts on the scene it is drawn in, which is the
// only thing about it worth pinning -- that, and that it does not quietly eat
// the border it is drawn beside.

import (
	"math"
	"os"
	"strings"
	"testing"
)

// killEd is two kept scenes wide enough to carry a ✕, and one that is not.
func killEd(t *testing.T) *cutEditor {
	t.Helper()
	ed := moveEd(t)
	// 80 px, 80 px and 16 px at moveEd's 4 px per second: two scenes wide
	// enough to carry a ✕ and one that is not
	ed.segs = []cutSeg{{S: 10, E: 30}, {S: 40, E: 60}, {S: 70, E: 74}}
	return ed
}

// The press lands on the scene it is drawn in, and only that one.
func TestTheKillBadgeRemovesTheSceneItIsDrawnIn(t *testing.T) {
	ed := killEd(t)
	cx, cy, ok := ed.segKillCentre(1)
	if !ok {
		t.Fatal("the second scene has no ✕")
	}
	if got := ed.segKillAt(cx, cy); got != 1 {
		t.Fatalf("the ✕ at the second scene's corner answered %d", got)
	}
	ed.killSeg(1)
	if len(ed.segs) != 2 || ed.segs[0].S != 10 || ed.segs[1].S != 70 {
		t.Errorf("pressing the second scene's ✕ left %+v", ed.segs)
	}
	// and Undo takes it back, because every edit on this page does
	ed.undoLast()
	if len(ed.segs) != 3 || ed.segs[1].S != 40 {
		t.Errorf("Undo after a ✕ left %+v", ed.segs)
	}
}

// It sits in the scene's TOP-RIGHT corner: at the right end, high in the band.
// Drawn anywhere else it would be a button floating over footage rather than a
// mark on the scene it belongs to.
func TestTheKillBadgeSitsInTheTopRightCorner(t *testing.T) {
	ed := killEd(t)
	cx, cy, ok := ed.segKillCentre(0)
	if !ok {
		t.Fatal("the first scene has no ✕")
	}
	x0, x1 := ed.xOf(10), ed.xOf(30)
	if cx <= (x0+x1)/2 || cx >= x1 {
		t.Errorf("the ✕ sits at x %.1f, want it inside the right half of %.1f–%.1f", cx, x0, x1)
	}
	if cy <= ed.picTop() || cy >= ed.picTop()+float64(ed.thumbHt)/2 {
		t.Errorf("the ✕ sits at y %.1f, want it in the top half of the band from %.1f",
			cy, ed.picTop())
	}
	// clear of the border it is drawn beside, or the plate would straddle it
	if x1-cx <= segKillR+segKillPad {
		t.Errorf("the ✕'s plate reaches the scene's border: centre %.1f, border %.1f", cx, x1)
	}
}

// The corner is the ✕'s, and the rest of the border is the border's. A scene's
// edge is trimmed by dragging it, and the badge overlaps the top of one -- so
// the press and the pointer have to agree about which of the two the hand is
// being offered, at every height.
func TestTheCornerBelongsToTheKillBadge(t *testing.T) {
	ed := killEd(t)
	cx, cy, _ := ed.segKillCentre(0)
	near := ed.xOf(30) - edgeGrab/2 // well inside the border's grab

	ed.hoverTracks(cx-ed.viewX, cy)
	if !ed.killHovOn || ed.killHov != 0 {
		t.Errorf("the pointer on the ✕ lit on=%v i=%d", ed.killHovOn, ed.killHov)
	}
	if ed.edgeHovOn {
		t.Errorf("the border lit under the ✕ too, at %.2f", ed.edgeHovT)
	}
	if c := ed.wantCursor(cx-ed.viewX, cy); c != "pointer" {
		t.Errorf("the pointer on the ✕ asked for %q, want a pointer", c)
	}

	// the same border, lower down the band: the ✕ is behind us and trimming
	// is back
	low := ed.picTop() + float64(ed.thumbHt) - 4
	ed.hoverTracks(near-ed.viewX, low)
	if ed.killHovOn {
		t.Error("the ✕ answered from the bottom of the band")
	}
	if !ed.edgeHovOn || ed.edgeHovT != 30 {
		t.Errorf("the border below the ✕ offered on=%v t=%.2f, want 30",
			ed.edgeHovOn, ed.edgeHovT)
	}
	if c := ed.wantCursor(near-ed.viewX, low); c != "ew-resize" {
		t.Errorf("the border below the ✕ asked for %q, want a resize arrow", c)
	}

	// and the pointer leaving puts the badge out
	ed.hoverTracks(cx-ed.viewX, cy)
	ed.hoverTracks(-1, -1)
	if ed.killHovOn {
		t.Error("the pointer left the page and the ✕ stayed lit")
	}
}

// A scene of a few seconds has no corner to spare: a plate on it would cover
// most of the clip and hang over into the next one. Those go with ⌦, like
// everything else too small to aim at.
func TestATinySceneCarriesNoKillBadge(t *testing.T) {
	ed := killEd(t)
	if _, _, ok := ed.segKillCentre(2); ok {
		t.Errorf("a %.0f px scene carries a ✕", ed.xOf(74)-ed.xOf(70))
	}
	// and nothing in the whole band answers for it
	for _, y := range []float64{ed.picTop() + 2, ed.picTop() + segKillTop} {
		for x := ed.xOf(70); x <= ed.xOf(74); x++ {
			if i := ed.segKillAt(x, y); i == 2 {
				t.Fatalf("the tiny scene answered a press at %.0f,%.0f", x, y)
			}
		}
	}
}

// Only footage. A violet card is not green and has no ✕; neither has the one
// insert that IS drawn green -- a sound laid over the picture, where the mark
// could not say whether it meant the sound or the frames under it. Both are
// removed by taking them in hand and pressing ⌦.
func TestOnlyFootageCarriesAKillBadge(t *testing.T) {
	ed := killEd(t)
	ed.segs = []cutSeg{
		{S: 0, E: 40},                           // footage
		{S: 40, E: 44, Ins: "card.png", Dur: 4}, // a spliced card
		{S: 44, E: 84, Ins: "room.wav", Ss: 0},  // a sound over the picture
		{S: 84, E: 124, Ins: "sting.mp4"},       // a video sting over it
	}
	for i, s := range ed.segs {
		_, _, ok := ed.segKillCentre(i)
		if ok != (i == 0) {
			t.Errorf("scene %d (%s) carries ✕=%v", i, insBase(s.Ins), ok)
		}
	}
}

// The wiring: drawn over everything else in the picture band, asked before the
// border it overlaps, and the toolbar button it replaces has not come back as
// something that guesses.
//
// A － Remove IS on the bar again, for the job a per-scene ✕ cannot do -- a hole
// in the middle of a scene, which leaves two scenes that did not exist to be
// pressed. What must never come back is the guessing: this one reads the
// selection and nothing else (cut_selrm.go), so the pin is on the handler it
// calls, not on the button being absent.
func TestTheKillBadgeIsWired(t *testing.T) {
	src, err := os.ReadFile("cut.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ed.drawSegKill(cr, vx0, vx1)",                  // over the tint, the dimming and the inserts
		"if i := ed.segKillAt(x+ed.viewX, y); i >= 0 {", // before pickAt
		"ed.killSeg(i)",
	} {
		if !strings.Contains(string(src), want) {
			t.Errorf("the cut page no longer contains %q", want)
		}
	}
	if !strings.Contains(string(src), "ed.remBtn.ConnectClicked(func() { a.removeSelRange() })") {
		t.Error("－ Remove is wired to something other than the selection's verb — " +
			"a toolbar remove that guesses is what the ✕ was built to end")
	}
	// the draw comes after the inserts and the speed wash, or a card would
	// paint over a control
	s := string(src)
	if i, j := strings.Index(s, "cr.SetSourceRGBA(0.55, 0.35, 0.9, 0.55)"), strings.Index(s, "ed.drawSegKill(cr"); i < 0 || j < 0 || j < i {
		t.Errorf("the ✕ is drawn at %d and the inserts at %d — it must come last", j, i)
	}
	// ⌦ still reaches the things that have no ✕
	if !strings.Contains(s, "a.removeSelClicked()") {
		t.Error("⌦ no longer removes anything")
	}
}

// The badge's target is bigger than its plate, and both are centred on the same
// point: a control you can see but not comfortably hit is worse than no control.
func TestTheKillBadgeIsEasierToHitThanToSee(t *testing.T) {
	if segKillHit <= segKillR+segKillPad {
		t.Errorf("the ✕'s target (%.1f) is no bigger than its plate (%.1f)",
			segKillHit, segKillR+segKillPad)
	}
	ed := killEd(t)
	cx, cy, _ := ed.segKillCentre(0)
	for _, d := range [][2]float64{{-1, -1}, {1, -1}, {-1, 1}, {1, 1}} {
		x, y := cx+d[0]*(segKillR+segKillPad), cy+d[1]*(segKillR+segKillPad)
		if ed.segKillAt(x, y) != 0 {
			t.Errorf("the plate's %+.0f,%+.0f corner is not on the target", d[0], d[1])
		}
	}
	// and it stops: the whole band is not a remove button
	if ed.segKillAt(cx, cy+3*segKillHit) >= 0 {
		t.Error("the ✕ answers from most of the way down the band")
	}
	if math.Abs(cy-ed.picTop()) < 1 {
		t.Error("the ✕ sits on the band's very top edge, where it has no plate to show")
	}
}
