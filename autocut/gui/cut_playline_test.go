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
	// was would stand on the wrong second until the next tick
	body := funcBody(t, "cut.go", `func \(ed \*cutEditor\) redrawTracks\(\) \{`)
	if !strings.Contains(body, "ed.lineArea.QueueDraw()") {
		t.Error("redrawTracks no longer repaints the line's layer")
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
