package main

// The scope strip, and what it changes.
//
// A selection has meant footage or sound since the lanes learnt the verbs, but
// the meaning was decided entirely by which band the hand landed on, written
// nowhere, and unchangeable: a selection dragged on the thumbnails that turned
// out to be about sound had to be thrown away and dragged again in a lane, by
// eye, at the same seconds. The strip on the seam between the two bands is
// where that choice is now said and changed. Three rungs: the picture alone,
// picture and sound together, and one recording's sound. ▲ climbs towards the
// picture, ▼ walks down through the lanes and round.
//
// These tests hold the handle's geography, the walk through the lanes, what the
// two halves are painted on each of the three rungs, and the consequence: the
// verbs downstream read the choice, and the two that only make sense for
// footage refuse a sound.

import (
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/diamondburned/gotk4/pkg/cairo"
)

func renderScope(t *testing.T, ed *cutEditor, w, h int) func(x, y int) (r, g, b uint8) {
	t.Helper()
	surf := cairo.CreateImageSurface(cairo.FormatARGB32, w, h)
	cr := cairo.Create(surf)
	ed.drawScope(cr, w, h)
	surf.Flush()
	data, stride := surf.Data(), surf.Stride()
	pix := make([]byte, len(data))
	copy(pix, data) // off the C heap before the surface is collected
	runtime.KeepAlive(surf)
	return func(x, y int) (uint8, uint8, uint8) {
		i := y*stride + x*4
		return pix[i+2], pix[i+1], pix[i]
	}
}

// scopeSel is selEd with twenty seconds selected on the pictures: at 4 px a
// second the handle is 80 px wide, from x 20 to x 100, well clear of the
// minimum width so that the tests below see the seconds and not the floor.
func scopeSel(t *testing.T) (*App, *cutEditor) {
	t.Helper()
	a, ed := selEd(t)
	ed.sel.t0, ed.sel.t1, ed.sel.active, ed.sel.aud = 5, 25, true, ""
	return a, ed
}

// The handle stands on the seconds it is about, and it is never smaller than
// two stacked arrows: a selection of half a second is exactly when saying what
// it is about matters most, and it would otherwise be two pixels wide.
func TestTheHandleStandsOnTheSelection(t *testing.T) {
	_, ed := scopeSel(t)
	if x0, x1, ok := ed.scopeBoxPx(); !ok || x0 != 20 || x1 != 100 {
		t.Errorf("the handle is %.0f – %.0f (ok %v), want 20 – 100", x0, x1, ok)
	}
	ed.sel.t0, ed.sel.t1 = 10, 11 // one second: 4 px, under the minimum
	x0, x1, ok := ed.scopeBoxPx()
	if !ok || x1-x0 != scopeMinW {
		t.Errorf("a one-second selection got a handle %.0f px wide, want %.0f", x1-x0, scopeMinW)
	}
	if mid := (x0 + x1) / 2; mid != 42 {
		t.Errorf("the handle is centred at %.0f, want 42 — the middle of the seconds it is about", mid)
	}
	// nothing selected: no handle. The handle IS the selection's, and there is
	// no scope to state when there are no seconds to state it about.
	ed.sel.active = false
	if _, _, ok := ed.scopeBoxPx(); ok {
		t.Error("a handle with no selection under it")
	}
	// ...but a session with nothing recorded anywhere still gets one. See
	// TestASilentCaptureStillReachesThePictureAlone below.
	ed.sel.active, ed.auds = true, nil
	if _, _, ok := ed.scopeBoxPx(); !ok {
		t.Error("no handle in a session with no recording — ▲▲ is out of reach there")
	}
}

// A screen capture with no sound track at all has no lanes, so for a long time
// it had no strip either -- the strip appeared with the second recording, on
// the argument that the handle's job was to name WHICH sound.
//
// The third rung ended that argument and nobody moved the strip. The picture
// alone is not about which sound: it is about an insert bringing none of its
// own, and a silent capture is the session most likely to want that, since a
// sting dropped into one has nothing to be heard over and would blare. Two
// rungs, both reachable, and ▼ with nothing to walk to comes back to the
// middle rather than stranding the selection.
func TestASilentCaptureStillReachesThePictureAlone(t *testing.T) {
	_, ed := scopeSel(t)
	ed.auds = nil // no sound anywhere in the session

	up, down := func() { ed.scopeClicked(60, 4) }, func() { ed.scopeClicked(60, 18) }
	for i, c := range []struct {
		press func()
		name  string
	}{
		{up, "picture alone"},
		{down, "picture + sound"}, // ▼ with no lane to walk to comes back
		{up, "picture alone"},
		{up, "picture + sound"}, // ▲ toggles the two rather than sticking on top
		{down, "picture + sound"},
	} {
		c.press()
		if ed.sel.aud != "" {
			t.Fatalf("press %d pointed the selection at %q, and there are no lanes", i, ed.sel.aud)
		}
		if got := ed.scopeName(); got != c.name {
			t.Errorf("press %d left the selection on %q, want %q", i, got, c.name)
		}
	}
	// ...and both halves of the handle are there to be pressed, which is the
	// thing the missing strip took away
	if _, _, ok := ed.scopeBoxPx(); !ok {
		t.Error("the handle went away with the lanes")
	}
	if got := ed.scopePartAt(60, 4); got != scopeUp {
		t.Errorf("the top half of the handle answers %d, want scopeUp", got)
	}
}

// Which half a press lands on, and that a press beside the handle lands on
// neither: the strip runs the width of the page and only the handle is a
// control.
func TestTheHandleHasTwoHalvesAndNothingElse(t *testing.T) {
	_, ed := scopeSel(t)
	for _, c := range []struct {
		px, y float64
		want  int
	}{
		{60, 1, scopeUp}, {60, 11, scopeUp},
		{60, 12, scopeDown}, {60, 23, scopeDown},
		{20, 4, scopeUp}, {100, 18, scopeDown}, // the very edges are still it
		{19, 4, scopeNone}, {101, 18, scopeNone},
		{200, 4, scopeNone},
	} {
		if got := ed.scopePartAt(c.px, c.y); got != c.want {
			t.Errorf("scopePartAt(%.0f, %.0f) = %d, want %d", c.px, c.y, got, c.want)
		}
	}
}

// The handle is pressed as often as anything in the toolbar and it is the only
// control on the page with no button around it, so it has to be big enough to
// hit without aiming. Two rungs share the strip's height, so half of it is what
// the hand actually gets: at the sixteen the strip opened with that was eight
// pixels tall, and thirty across on a short selection.
func TestTheHandleIsBigEnoughToHit(t *testing.T) {
	if half := scopeH / 2; half < 10 {
		t.Errorf("a rung is %.0f px tall, which is a target you aim at", half)
	}
	if scopeMinW < 40 {
		t.Errorf("the smallest handle is %.0f px across, which is not a press", scopeMinW)
	}
	// and the arrow grew with the rung: a rung with room for a bigger arrow
	// drawn with the old small one is a big target that still LOOKS small, and
	// the arrow is the whole of what the handle says. Read down the arrow's own
	// column -- the handle stands from x 20, the arrow 13 px into it -- and
	// count the white in the top rung.
	_, ed := scopeSel(t)
	at := renderScope(t, ed, 400, int(scopeH))
	var tall int
	for y := 0; y < int(scopeH)/2; y++ {
		if r, g, b := at(33, y); r > 200 && g > 200 && b > 200 {
			tall++
		}
	}
	if tall < 6 {
		t.Errorf("the arrow is %d px tall in a %.0f px rung", tall, scopeH/2)
	}
}

// ▼ walks the recordings, which is the only way on the page to say WHICH sound
// a selection dragged on the thumbnails is about, and past the last one it
// comes back up to footage rather than round to the first: a walk that only
// ever cycled the lanes would have no way out of them.
func TestPressingDownWalksTheRecordings(t *testing.T) {
	_, ed := scopeSel(t)
	down := func() { ed.scopeClicked(60, 18) }

	for _, want := range []string{"mic", "cam", "room", "", "mic"} { // and round again
		down()
		if ed.sel.aud != want {
			t.Fatalf("▼ pointed the selection at %q, want %q", ed.sel.aud, want)
		}
	}
	// the rung it wraps onto is footage in one piece, not the picture alone:
	// walking off the end of the lanes is not a way to ask for silent frames
	for i := 0; i < 3; i++ {
		down()
	}
	if ed.sel.aud != "" || ed.sel.pic {
		t.Errorf("▼ past the last recording gave %q, picture alone %v — want footage in one piece",
			ed.sel.aud, ed.sel.pic)
	}
	// a press clear of the handle changes nothing at all
	ed.sel.aud = "cam"
	ed.scopeClicked(300, 12)
	if ed.sel.aud != "cam" {
		t.Errorf("a press beside the handle moved the scope to %q", ed.sel.aud)
	}
}

// ▲ is always towards the picture: back out of a lane to footage, on from
// footage to the frames without the sound filmed with them, and back again --
// so the top rung is reachable and escapable with the one arrow.
func TestPressingUpClimbsTowardsThePicture(t *testing.T) {
	_, ed := scopeSel(t)
	up := func() { ed.scopeClicked(60, 4) }

	ed.setSelScope("cam", false)
	up()
	if ed.selSnd() || ed.selPic() {
		t.Fatalf("▲ from a lane gave %q, picture alone %v — want footage in one piece",
			ed.sel.aud, ed.sel.pic)
	}
	up()
	if !ed.selPic() {
		t.Fatal("▲ from footage did not go on to the picture alone")
	}
	up()
	if ed.selPic() {
		t.Error("▲ from the picture alone did not bring the sound back with it")
	}
}

// An arrow is lit when its half is IN the selection, so the middle rung lights
// both of them: the strip has to say what the selection IS as well as offer the
// other answers, and "picture and sound together" drawn with two dark arrows is
// the one rung that could not be told from a handle that had lost its state.
// Each narrow rung has an arrow dark, which is what makes it narrow.
func TestTheLiveHalfIsLit(t *testing.T) {
	_, ed := scopeSel(t)
	blue := func(at func(int, int) (uint8, uint8, uint8), y int) bool {
		r, g, b := at(60, y)
		return b > 150 && int(b)-int(r) > 80 && g > r && g < b
	}
	for _, c := range []struct {
		what     string
		aud      string
		pic      bool
		up, down bool
	}{
		{"picture and sound", "", false, true, true},
		{"the picture alone", "", true, true, false},
		{"mic's sound", "mic", false, false, true},
	} {
		ed.sel.aud, ed.sel.pic = c.aud, c.pic
		at := renderScope(t, ed, 400, int(scopeH))
		if up, down := blue(at, 6), blue(at, 18); up != c.up || down != c.down {
			t.Errorf("on %s the handle is lit ▲%v ▼%v, want ▲%v ▼%v",
				c.what, up, down, c.up, c.down)
		}
	}
}

// The payoff: the handle is not decoration, it is what ⧉ Copy reads. A
// selection dragged on the pictures and then pointed down takes sound.
func TestTheHandleChangesWhatCopyTakes(t *testing.T) {
	a, ed := scopeSel(t)
	a.copyClicked()
	if ed.copyAud != "" {
		t.Errorf("a ▲ selection copied %q, want footage", ed.copyAud)
	}
	ed.scopeClicked(60, 18) // ▼ mic
	a.copyClicked()
	if ed.copyAud != "mic" {
		t.Errorf("a ▼ selection copied %q, want mic's sound", ed.copyAud)
	}
	if ed.copyFrom != 5 || ed.copyLen != 20 {
		t.Errorf("the seconds changed with the scope: %.0f for %.0f", ed.copyFrom, ed.copyLen)
	}
}

// ＋ Add and ⌦ choose which FOOTAGE the cut keeps, and footage here is picture
// and the sound filmed with it in one piece. On a ▼ selection they have nothing
// they could honestly do, so ＋ Add is greyed -- and both refuse from the
// keyboard too, because ⌦ is a key and keys do not grey.
func TestTheFootageVerbsRefuseASoundSelection(t *testing.T) {
	a, ed := scopeSel(t)
	ed.segs = []cutSeg{{S: 0, E: 30}, {S: 100, E: 200}}
	ed.scopeClicked(60, 18) // ▼ mic
	was := append([]cutSeg(nil), ed.segs...)

	a.removeSelClicked()
	segsEqual(t, ed.segs, was)
	a.addSelClicked()
	segsEqual(t, ed.segs, was)

	// and ▲ hands them back: the refusal is about the scope, not about the
	// selection being unusable
	ed.scopeClicked(60, 4)
	a.removeSelClicked()
	if len(ed.segs) == len(was) && ed.segs[0].E == was[0].E {
		t.Errorf("⌦ did nothing on a ▲ selection either: %+v", ed.segs)
	}
}

// A recording can go away between one reload and the next, and a selection
// pointing at one nobody has is a selection whose every verb would miss.
func TestASelectionLosesASoundThatIsNoLongerThere(t *testing.T) {
	_, ed := scopeSel(t)
	ed.sel.aud = "mic"
	ed.fitScope()
	if ed.sel.aud != "mic" {
		t.Fatalf("a live recording was dropped from the selection: %q", ed.sel.aud)
	}
	ed.auds = ed.auds[1:] // mic is gone
	ed.fitScope()
	if ed.sel.aud != "" {
		t.Errorf("the selection still points at %q, which is not in the session", ed.sel.aud)
	}
}

// The seams: the strip's own row in the box, the redraw that keeps it in step
// with the bands either side, the visibility that follows the lanes, and the
// two verbs it greys.
func TestTheScopeStripIsWired(t *testing.T) {
	pins := map[string][]string{
		"cut.go": {
			"ed.scopeArea = ed.newScopeArea()",
			"tracks.Append(ed.scopeArea) // the seam, and the handle that names it",
			"if ed.scopeArea != nil {\n\t\ted.scopeArea.QueueDraw()",
			"ed.fitScope() // the seam sits under the pictures, lanes or no lanes",
			"func (ed *cutEditor) syncSelBtns() {",
			"ed.addBtn.SetSensitive(!snd)",
			`case ed.sel.active && ed.sel.aud != "":`, // ⌦ refuses too
		},
		"cut_scope.go": {
			"func (ed *cutEditor) scopePartAt(px, y float64) int {",
			"return ed.auds[i+1].base",                             // ▼ walks
			"lit := (up && !ed.selSnd()) || (!up && !ed.selPic())", // three rungs, two arrows
			"cr.SetSourceRGBA(0.3, 0.55, 0.9, 0.85)",               // the selection's own blue
			"ed.scopeArea.SetVisible(len(ed.vids) > 0)",
		},
		"cut_selband.go": {"case ed.scopeArea:"}, // its own cursor slot
	}
	for file, want := range pins {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range want {
			if !strings.Contains(string(src), w) {
				t.Errorf("%s no longer contains %q", file, w)
			}
		}
	}
}
