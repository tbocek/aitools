package main

// The playhead's own layer (cut_playline.go): the red line moved off the bands
// onto a transparent overlay so the 100ms playback tick repaints a near-empty
// widget instead of every thumbnail and waveform on the page.

import (
	"runtime"
	"strings"
	"testing"

	"github.com/diamondburned/gotk4/pkg/cairo"
)

// lineInk paints the layer headless and answers with per-pixel alpha.
func lineInk(t *testing.T, ed *cutEditor, w, h int) func(x, y int) uint8 {
	t.Helper()
	surf := cairo.CreateImageSurface(cairo.FormatARGB32, w, h)
	cr := cairo.Create(surf)
	ed.drawPlayline(cr, h)
	surf.Flush()
	data, stride := surf.Data(), surf.Stride()
	pix := make([]byte, len(data))
	copy(pix, data) // off the C heap before the surface is collected
	runtime.KeepAlive(surf)
	return func(x, y int) uint8 { return pix[y*stride+x*4+3] }
}

func TestTheLineLayerDrawsTheLineAndNothingElse(t *testing.T) {
	// one filmed run, 10px a second, scrolled 40px along: the playhead at 12s
	// stands at 120 on the timeline and 80 on the glass
	ed := &cutEditor{
		hasPlay:  true,
		playhead: 12,
		viewX:    40,
		pps:      10,
		spans:    []tlSpan{{t0: 0, t1: 60, px: 0}},
	}
	at := lineInk(t, ed, 200, 50)
	for _, y := range []int{0, 25, 49} {
		if at(80, y) == 0 {
			t.Errorf("no ink at (80,%d) — the line is not where the playhead is", y)
		}
	}
	// top to bottom: the layer spans both bands and the gap between them,
	// which the per-band lines never crossed
	for _, x := range []int{60, 100, 160} {
		if at(x, 25) != 0 {
			t.Errorf("ink at (%d,25) — the layer draws more than the line", x)
		}
	}

	// before the first click there is no playhead, and the layer says nothing
	ed.hasPlay = false
	at = lineInk(t, ed, 200, 50)
	for x := 0; x < 200; x += 5 {
		if at(x, 25) != 0 {
			t.Fatalf("ink at (%d,25) with no playhead set", x)
		}
	}
}

// The point of the layer: the tick stops repainting the bands. The tick and
// the widgets cannot run headless, so the wiring is the fact.
func TestThePlaybackTickRepaintsTheLineNotTheBands(t *testing.T) {
	src := readSrc(t, "cut.go")
	for _, want := range []string{
		// the tick's choice: full bands only when the green bar walked on
		"if ed.bandClipIdx() != ed.lineIdx {",
		"ed.redrawLine()",
		// a full repaint remembers which scene the bar was painted on, and
		// takes the line's layer with it -- scroll and zoom move the line too
		"ed.lineIdx = ed.bandClipIdx()",
		// the layer rides over the two bands and swallows no events
		"tracks.Append(ed.lineOver(band))",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("cut.go no longer contains %q", want)
		}
	}
	for _, want := range []string{
		"ed.lineArea.SetCanTarget(false)", // hovers and drags belong to the bands
		"over.AddOverlay(ed.lineArea)",
		"x := ed.xOf(ed.playhead) - ed.viewX", // the layer does not scroll; the bands do
	} {
		if !strings.Contains(readSrc(t, "cut_playline.go"), want) {
			t.Errorf("cut_playline.go no longer contains %q", want)
		}
	}
	// a full repaint moves the line's layer too: the scrollbar under a paused
	// player repaints the bands at the new offset, and a line left where it
	// was would stand on the wrong second until the next tick. The draws live
	// in queueTracks -- the half a pan and a zoom use on their own, without the
	// preview sync -- and redrawTracks is that plus the syncs.
	body := funcBody(t, "cut.go", `func \(ed \*cutEditor\) queueTracks\(\) \{`)
	if !strings.Contains(body, "ed.lineArea.QueueDraw()") {
		t.Error("queueTracks no longer repaints the line's layer")
	}
	if body := funcBody(t, "cut.go", `func \(ed \*cutEditor\) redrawTracks\(\) \{`); !strings.Contains(body, "ed.queueTracks()") {
		t.Error("redrawTracks no longer queues the tracks, so an edit repaints nothing")
	}
	// and the framing overlay still follows the clock on the cheap path
	body = funcBody(t, "cut_playline.go", `func \(ed \*cutEditor\) redrawLine\(\) \{`)
	for _, want := range []string{"ed.fxArea.QueueDraw()", "ed.syncFxCursor()", "ed.syncPreviewZoom()"} {
		if !strings.Contains(body, want) {
			t.Errorf("redrawLine no longer settles %q with the line", want)
		}
	}

	// the bands themselves are out of the red-line business: the only red
	// vertical left in their draw funcs is the layer's
	for _, f := range []struct{ file, head string }{
		{"cut.go", `func \(ed \*cutEditor\) drawTrack\(cr \*cairo\.Context, w, h int\) \{`},
		{"cut_audio.go", `func \(ed \*cutEditor\) drawAudio\(cr \*cairo\.Context, w, h int\) \{`},
	} {
		if strings.Contains(funcBody(t, f.file, f.head), "0.9, 0.15, 0.15") {
			t.Errorf("%s still paints the playhead line — the tick repaints it all again", f.file)
		}
	}
}

// ⏸ then ▶ carries on from where it stopped.
//
// ▶ has starts that are not the line: with a clip edge held it plays from the
// edge, with a clip held from the clip's own start -- both there so that
// pressing play after a trim shows the trim rather than whatever second the
// line was last left on. Resuming is not one of those. Pause halfway through
// the clip you are working on, press ▶, and the old code read it as a fresh
// start and took you back to the clip's first frame: the same seconds again,
// every time, exactly where you had asked it to stop.
func TestPauseThenPlayCarriesOnFromWhereItStopped(t *testing.T) {
	ed := newTestEd(t)
	ed.playhead = 42

	if ed.resumingHere() {
		t.Error("a page that has never played reads as resuming")
	}
	ed.markPause()
	if !ed.resumingHere() {
		t.Error("the line has not moved since the pause and it does not read as resuming")
	}
	// a hand on the line -- a click on a track, a frame step with nothing held
	// -- is the hand choosing a new place, and ▶ after that is a start
	ed.playhead = 42.5
	if ed.resumingHere() {
		t.Error("the line moved after the pause and ▶ still reads as a resume")
	}
	// ...and back on the paused second it resumes again: the test is where the
	// line IS, not what has happened to it
	ed.playhead = 42
	if !ed.resumingHere() {
		t.Error("the line is back where the pause left it and ▶ does not resume")
	}
	// ⏹ is not ⏸: it hands the transport back, and there is no position left
	// to carry on from
	ed.stop()
	if ed.resumingHere() {
		t.Error("⏹ left a resume point behind")
	}

	// the wiring: the resume is decided before the press is spent, it is one
	// case of the same switch the held-edge and held-clip starts are in --
	// ahead of both, or the hold would win the press it is meant to lose --
	// and ⏸ is what writes the point
	src := readSrc(t, "cut.go")
	for _, want := range []string{
		"resume := ed.resumingHere()",
		"ed.markPause() // this press is the ⏸",
		"case resume:",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("cut.go no longer contains %q", want)
		}
	}
	i := strings.Index(src, "case resume:")
	for _, later := range []string{"case ed.edgeOn:", "case s != nil:"} {
		if j := strings.Index(src, later); j < 0 || j < i {
			t.Errorf("%q is asked before the resume, so a held thing wins a press that meant carry on", later)
		}
	}
}
