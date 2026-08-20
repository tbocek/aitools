package main

// The overlay's slide-and-snap: the maths that pulls a moved camera rectangle
// onto the frame's edges, and the wiring that makes a press inside the held
// rectangle a slide rather than a fresh drawing.

import (
	"math"
	"os"
	"strings"
	"testing"
)

// A moved region snaps to the frame's top, left, right and bottom -- each
// axis on its own, so a corner is just both at once -- and stays put
// everywhere else. All in fractions of the source frame, exactly as the
// overlay calls it: a 9:16 crop of a 16:9 frame is wf ~0.316 wide at full
// height, so its centre pins to wf/2 on the left and 1-wf/2 on the right.
func TestAMovedRegionSnapsToTheFrameEdges(t *testing.T) {
	wf, hf := 0.316, 1.0/2 // half-height 9:16-ish window
	tx, ty := 0.02, 0.02
	for _, c := range []struct {
		name         string
		cx, cy       float64
		wantX, wantY float64
	}{
		{"left edge", wf/2 + 0.01, 0.5, wf / 2, 0.5},
		{"right edge", 1 - wf/2 - 0.01, 0.5, 1 - wf/2, 0.5},
		{"top edge", 0.5, hf/2 + 0.01, 0.5, hf / 2},
		{"bottom edge", 0.5, 1 - hf/2 - 0.01, 0.5, 1 - hf/2},
		{"corner", wf/2 + 0.01, hf/2 + 0.01, wf / 2, hf / 2},
		{"free in the middle", 0.4, 0.37, 0.4, 0.37},
		{"just out of reach", wf/2 + 0.03, 0.5, wf/2 + 0.03, 0.5},
	} {
		gx, gy := snapRectPos(c.cx, c.cy, wf, hf, tx, ty)
		if math.Abs(gx-c.wantX) > 1e-9 || math.Abs(gy-c.wantY) > 1e-9 {
			t.Errorf("%s: (%.4f, %.4f) snapped to (%.4f, %.4f), want (%.4f, %.4f)",
				c.name, c.cx, c.cy, gx, gy, c.wantX, c.wantY)
		}
	}
}

// The gesture itself, pinned in the source: a press on the rectangle's border
// resizes it, one inside it slides it (one undo for the whole of either,
// snapped through snapRectPos, persisted at the end), a press that grabbed
// nothing is still the play/pause click, and the first-view rule lives in
// viewRectAt where preview and render both read it.
func TestTheSlideGestureIsWired(t *testing.T) {
	b, err := os.ReadFile("step3_fxview.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{
		// one hit test answers every press, and the rectangle it offers when
		// nothing is held is the view actually framing the picture
		"gr := grabAt(x, y)",
		"if f := ed.fxDragTarget(); f != nil {",
		// border, then inside: resize, then move
		"g.horiz, g.vert, g.left, g.top, g.inside = fxEdges(x, y, rx, ry, rw, rh)",
		`drag.kind = "size"`,
		`drag.kind = "move"`,
		// the width follows the cut's ratio, always
		"cx, cy, h := resizeRect(drag.x1, drag.y1, drag.ax, drag.ay,",
		"f.Hf = h / dh",
		// the live zoom layer follows the hand
		"ed.syncPreviewZoom()",
		// a press that never moved is a click, and a click is play/pause --
		// wherever on the picture it landed
		"if !drag.moved {",
		"ed.toggle()",
		// the right button on a rectangle opens its numbers, except on the
		// first view, which has none to open
		"ed.a.editFxAt(gr.f)",
		"if gr.f.Kind == \"view\" && ed.firstView(*gr.f) {",
		// one snapshot before anything shifts, so one Undo undoes the drag
		"ed.pushUndo() // once, before anything shifts: one Undo per drag",
		// every movement runs through the edge snap
		"f.Cx, f.Cy = snapRectPos(cx, cy, wf, f.Hf, 10/dw, 10/dh)",
		// a slide that went somewhere is saved when the button lifts
		"ed.persist()",
		// and can never leave a rectangle nobody can find again
		"fxClampRect(f)",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("the overlay no longer contains %q", want)
		}
	}
	b, err = os.ReadFile("step3_fx.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "first := fxRect{views[0].Cx, views[0].Cy, views[0].Hf}") {
		t.Error("viewRectAt no longer starts the camera on the first view's rectangle")
	}
	// the lane answers the same way: the right button on the effect already
	// in hand opens its numbers instead of dropping it
	if !strings.Contains(string(b), "if ed.fxOn && ed.fxSel == best {") {
		t.Error("pickFxAt no longer opens the held effect's numbers on a second right-click")
	}
	// and picking one up puts the line on it. Without this the overlay draws
	// the held view's rectangle over a frame from somewhere else entirely,
	// which reads as the other views never being shown at all.
	if !strings.Contains(string(b), "ed.setPlayhead(ed.fx[i].T)") {
		t.Error("holdFx no longer moves the playhead onto the effect it picks up")
	}
	// the zoom dialog asks all three of its times
	for _, want := range []string{
		`fxNumRow("Glide in seconds"`,
		`fxNumRow("Glide out seconds"`,
		`fxNumRow("Length seconds"`,
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("the zoom dialog no longer contains %s", want)
		}
	}
}

// The live zoom maps the camera onto the output box: whatever rectangle
// fxRectAt answers at the playhead, the transformed picture puts it exactly
// over the output-shaped box centred in the widget -- its height filling the
// box, its centre on the box's centre. The letterboxed full fit is the
// render's own letterbox: the frame's width fills the box's width.
func TestTheLiveZoomMapsTheCameraOntoTheOutputBox(t *testing.T) {
	W, H, sw, sh, outA := 800.0, 450.0, 1920.0, 1080.0, 9.0/16
	bx, by, bw, bh := fxDisp(W, H, outA)
	for _, r := range []fxRect{
		fullFit(sw/sh, outA),
		{0.4, 0.6, 0.5},
		{0.25, 0.3, 0.2},
	} {
		s, tx, ty := liveZoom(W, H, sw, sh, outA, r)
		if got := r.hf * sh * s; math.Abs(got-bh) > 1e-6 {
			t.Errorf("rect %+v: camera height maps to %.3f px, want the box's %.3f", r, got, bh)
		}
		if gx, gy := r.cx*sw*s+tx, r.cy*sh*s+ty; math.Abs(gx-(bx+bw/2)) > 1e-6 || math.Abs(gy-(by+bh/2)) > 1e-6 {
			t.Errorf("rect %+v: centre maps to (%.2f, %.2f), want the box centre (%.2f, %.2f)",
				r, gx, gy, bx+bw/2, by+bh/2)
		}
	}
	s, tx, _ := liveZoom(W, H, sw, sh, outA, fullFit(sw/sh, outA))
	if math.Abs(sw*s-bw) > 1e-6 || math.Abs(tx-bx) > 1e-6 {
		t.Errorf("full fit draws the frame %.2f px wide at x=%.2f, want %.2f wide at x=%.2f",
			sw*s, tx, bw, bx)
	}
}

// The zoom's freedom and the playing preview, pinned in the source: a zoom
// drag is the free rectangle (the camera then takes the smallest
// output-shaped window that holds it), and while the stream runs a second
// picture of the same paintable is transformed to the camera and synced from
// every place the clock moves.
func TestTheFreeZoomAndTheLivePreviewAreWired(t *testing.T) {
	b, err := os.ReadFile("step3_fxview.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{
		"if zoomFree {",
		"hf = math.Max(rh, rw/pxAspect()) / dh",
		"over.AddOverlay(zfix)",
		"ed.fxZoom.SetChildTransform(ed.fxZoomPic, zoomTransform(s, tx, ty))",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("the overlay no longer contains %q", want)
		}
	}
	b, err = os.ReadFile("step3.go")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(b), "ed.syncPreviewZoom()"); n < 2 {
		t.Errorf("the live zoom is synced from %d places in step3.go, want the redraw and the pause", n)
	}
}

// ---- the rectangle under the hand -------------------------------------

// Which view a drag on the picture edits. The rule has to match what the
// overlay draws, or the hand grabs one rectangle and moves another: the latest
// view at or before the playhead, and before the first view, the first one --
// which is exactly viewRectAt's own first-view rule, said again.
func TestTheViewInForceIsTheOneOnScreen(t *testing.T) {
	ed := &cutEditor{fx: []cutFx{
		{Kind: "view", T: 55, Cx: 0.7, Cy: 0.5, Hf: 0.5},
		{Kind: "zoom", T: 10, Dur: 4},
		{Kind: "view", T: 20, Cx: 0.3, Cy: 0.5, Hf: 0.8},
	}}
	for _, c := range []struct {
		t    float64
		want float64 // the view's Cx, or -1 for none
	}{
		{0, 0.3}, // before every view: the earliest one is in force
		{19.9, 0.3},
		{20, 0.3}, // its own moment counts as in force
		{40, 0.3},
		{55, 0.7},
		{999, 0.7},
	} {
		f := ed.viewInForce(c.t)
		if f == nil {
			t.Errorf("at %gs no view is in force, wanted Cx %g", c.t, c.want)
			continue
		}
		if f.Cx != c.want {
			t.Errorf("at %gs the view in force has Cx %g, wanted %g", c.t, f.Cx, c.want)
		}
	}
	// with no views at all there is nothing to frame and nothing to grab
	if f := (&cutEditor{fx: []cutFx{{Kind: "zoom", T: 0, Dur: 3}}}).viewInForce(1); f != nil {
		t.Errorf("a cut with no views still offered %+v to drag", *f)
	}
}

// A zoom mid-flight owns the rectangle -- it is neither view's frame, it is
// somewhere between them -- so the picture offers nothing to take hold of
// while one is running. Its last moment is already free again.
func TestAZoomInFlightIsNotSomethingToGrab(t *testing.T) {
	fx := []cutFx{{Kind: "zoom", T: 10, Dur: 4}}
	for _, c := range []struct {
		t    float64
		want bool
	}{{9.9, false}, {10, true}, {13.9, true}, {14, false}} {
		if got := fxZoomCovers(fx, c.t); got != c.want {
			t.Errorf("at %gs a zoom covering the playhead reads %v, wanted %v", c.t, got, c.want)
		}
	}
	// a zoom with no length covers nothing; it is a jump, not an arc
	if fxZoomCovers([]cutFx{{Kind: "zoom", T: 10}}, 10) {
		t.Error("a zero-length zoom claims to cover its own moment")
	}
	// and the whole rule: held beats everything, otherwise the view on screen
	ed := &cutEditor{hasPlay: true, playhead: 12, fx: []cutFx{
		{Kind: "view", T: 0, Cx: 0.25}, {Kind: "zoom", T: 10, Dur: 4},
	}}
	if f := ed.fxDragTarget(); f != nil {
		t.Errorf("the zoom in flight still offered %+v to drag", *f)
	}
	ed.playhead = 20
	if f := ed.fxDragTarget(); f == nil || f.Cx != 0.25 {
		t.Errorf("past the zoom the drag target is %v, wanted the view at 0", f)
	}
	ed.hasPlay = false
	if f := ed.fxDragTarget(); f != nil {
		t.Error("with no video loaded the picture still offered something to drag")
	}
}

// ---- dragging a border ------------------------------------------------

// The border drag. The anchor -- the far edge, or the far corner -- stays
// exactly where it was, the height follows the hand, and the width is always
// the height times the cut's aspect, whichever border was taken hold of.
func TestResizeRectKeepsTheAnchorAndTheRatio(t *testing.T) {
	const pxA = 16.0 / 9.0
	for _, c := range []struct {
		name                   string
		x, y, ax, ay           float64
		horiz, vert, left, top bool
		wantCx, wantCy, wantH  float64
	}{
		// right border pulled out to x=500, left edge anchored at 100
		{"right border", 500, 0, 100, 250, true, false, false, false, 300, 250, 400 / pxA},
		// left border pulled to x=100 with the right edge anchored at 500
		{"left border", 100, 0, 500, 250, true, false, true, false, 300, 250, 400 / pxA},
		// bottom border: the height is the hand, the width follows
		{"bottom border", 0, 400, 300, 100, false, true, false, false, 300, 250, 300},
		{"top border", 0, 100, 300, 400, false, true, false, true, 300, 250, 300},
		// a corner takes the longer axis: 400 px across is 225 tall, less
		// than the 300 the hand moved down, so the height wins
		{"corner, height wins", 500, 400, 100, 100, true, true, false, false,
			100 + 300*pxA/2, 250, 300},
		// and the other way round: 800 across is 450 tall, more than 300
		{"corner, width wins", 900, 400, 100, 100, true, true, false, false,
			500, 100 + 800/pxA/2, 800 / pxA},
	} {
		cx, cy, h := resizeRect(c.x, c.y, c.ax, c.ay, pxA, c.horiz, c.vert, c.left, c.top)
		if math.Abs(cx-c.wantCx) > 1e-6 || math.Abs(cy-c.wantCy) > 1e-6 || math.Abs(h-c.wantH) > 1e-6 {
			t.Errorf("%s: got centre (%g, %g) height %g, wanted (%g, %g) %g",
				c.name, cx, cy, h, c.wantCx, c.wantCy, c.wantH)
			continue
		}
		// the anchor really did not move
		w := h * pxA
		l, r, top0, bot := cx-w/2, cx+w/2, cy-h/2, cy+h/2
		if c.horiz {
			held := l // the right border was taken, so the left one is anchored
			if c.left {
				held = r
			}
			if math.Abs(held-c.ax) > 1e-6 {
				t.Errorf("%s: the anchored edge moved from %g to %g", c.name, c.ax, held)
			}
		}
		if c.vert {
			held := top0
			if c.top {
				held = bot
			}
			if math.Abs(held-c.ay) > 1e-6 {
				t.Errorf("%s: the anchored edge moved from %g to %g", c.name, c.ay, held)
			}
		}
	}
	// a rectangle dragged to nothing stops at a size a hand can still find
	if _, _, h := resizeRect(100, 100, 100, 100, pxA, true, true, false, false); h != 12 {
		t.Errorf("a drag back onto the anchor left a %g px rectangle", h)
	}
}

// ---- snapping to the cuts ---------------------------------------------

// Dragging an effect along the lane snaps it to the clip borders -- the green
// edges -- because that is where a view almost always belongs: on the cut, not
// four frames after it. Nudging with ‹f f› is deliberately left unsnapped.
func TestSnapFxTimeTakesTheNearestCutBorder(t *testing.T) {
	segs := []cutSeg{{S: 10, E: 20}, {S: 55, E: 90}}
	for _, c := range []struct{ in, want float64 }{
		{9.8, 10},  // just before a border
		{20.4, 20}, // just after one
		{54.6, 55},
		{30, 30}, // nowhere near one: left alone
		{20.9, 20.9},
	} {
		if got := snapFxTime(segs, c.in, 0.5); got != c.want {
			t.Errorf("%gs snapped to %gs, wanted %gs", c.in, got, c.want)
		}
	}
	// between two borders inside the snap, the nearer one wins
	if got := snapFxTime([]cutSeg{{S: 10, E: 11}}, 10.6, 2); got != 11 {
		t.Errorf("between two borders it snapped to %gs, wanted 11s", got)
	}
	// and the pixel snap is a pixel distance turned into seconds by the zoom
	ed := &cutEditor{pps: 40, segs: segs}
	if got := ed.snapFx(20 + 7/40.0); got != 20 {
		t.Errorf("7 px from a border it snapped to %gs, wanted 20s", got)
	}
	if got := ed.snapFx(20 + 9/40.0); got == 20 {
		t.Error("9 px from a border it still snapped, which is further than snapPx")
	}
}

// What a point on a rectangle has hold of: the borders within reach, and
// whether it is inside at all. The reach is the same on both sides of the
// line, because a 1.5 px border is not a mouse target.
func TestTheBorderUnderThePointerIsKnown(t *testing.T) {
	const rx, ry, rw, rh = 100.0, 50.0, 200.0, 100.0
	for _, c := range []struct {
		name                           string
		x, y                           float64
		horiz, vert, left, top, inside bool
	}{
		{"the middle", 200, 100, false, false, false, false, true},
		{"the left border", 100, 100, true, false, true, false, true},
		{"just outside the left border", 95, 100, true, false, true, false, false},
		{"the right border", 300, 100, true, false, false, false, true},
		{"the top border", 200, 50, false, true, false, true, true},
		{"the bottom border", 200, 150, false, true, false, false, true},
		{"the top-left corner", 100, 50, true, true, true, true, true},
		{"the bottom-right corner", 300, 150, true, true, false, false, true},
		{"nowhere near it", 500, 400, false, false, false, false, false},
	} {
		h, v, l, tp, in := fxEdges(c.x, c.y, rx, ry, rw, rh)
		if h != c.horiz || v != c.vert || in != c.inside ||
			(h && l != c.left) || (v && tp != c.top) {
			t.Errorf("%s: horiz=%v vert=%v left=%v top=%v inside=%v, want %v %v %v %v %v",
				c.name, h, v, l, tp, in, c.horiz, c.vert, c.left, c.top, c.inside)
		}
	}
	// and the pointer says so before anything is pressed
	for _, c := range []struct {
		horiz, vert, left, top, inside bool
		want                           string
	}{
		{true, true, true, true, true, "nwse-resize"},   // top-left
		{true, true, false, false, true, "nwse-resize"}, // bottom-right
		{true, true, false, true, true, "nesw-resize"},  // top-right
		{true, true, true, false, true, "nesw-resize"},  // bottom-left
		{true, false, true, false, true, "ew-resize"},
		{false, true, false, true, true, "ns-resize"},
		{false, false, false, false, true, "move"},
		{false, false, false, false, false, "default"},
	} {
		if got := fxCursorName(c.horiz, c.vert, c.left, c.top, c.inside); got != c.want {
			t.Errorf("%+v gave the %q cursor, want %q", c, got, c.want)
		}
	}
}

// A text box's resize: the two axes independent (it owes nothing to the cut's
// aspect), the edge opposite the one grabbed standing still, an edge that was
// not grabbed not moving at all, and never collapsing below the minimum.
func TestResizeFreeMovesOnlyTheEdgesGrabbed(t *testing.T) {
	const x0, y0, w0, h0, min = 100.0, 50.0, 200.0, 100.0, 16.0
	// the right border pulled out: the left edge stays, the height is untouched
	nx, ny, nw, nh := resizeFree(400, 999, x0, y0, x0, y0, w0, h0, min, true, false, false, false)
	if nx != x0 || ny != y0 || nh != h0 || nw != 300 {
		t.Errorf("the right border gave %.0f,%.0f %.0fx%.0f, want 100,50 300x100", nx, ny, nw, nh)
	}
	// the left border pulled out: the right edge (x0+w0 = 300) stays put
	nx, _, nw, _ = resizeFree(40, 0, x0+w0, y0+h0, x0, y0, w0, h0, min, true, false, true, false)
	if nx != 40 || nx+nw != 300 {
		t.Errorf("the left border gave x=%.0f w=%.0f; the right edge moved to %.0f, want 300",
			nx, nw, nx+nw)
	}
	// the top-left corner: both axes move, the far corner stands still
	nx, ny, nw, nh = resizeFree(40, 20, x0+w0, y0+h0, x0, y0, w0, h0, min, true, true, true, true)
	if nx+nw != 300 || ny+nh != 150 {
		t.Errorf("the corner drag moved the far corner to %.0f,%.0f, want 300,150", nx+nw, ny+nh)
	}
	// dragged inside out: it stops at the minimum instead of inverting
	_, _, nw, nh = resizeFree(x0+500, y0+500, x0, y0, x0, y0, w0, h0, min, true, true, true, true)
	if nw < min || nh < min {
		t.Errorf("an inverted drag left a %.0fx%.0f box, want nothing below %.0f", nw, nh, min)
	}
	_, _, nw, nh = resizeFree(x0-500, y0-500, x0, y0, x0, y0, w0, h0, min, true, true, false, false)
	if nw < min || nh < min {
		t.Errorf("the other inversion left a %.0fx%.0f box, want nothing below %.0f", nw, nh, min)
	}
}

// A camera rectangle is left somewhere a person can find it again: a pull-back
// well past the frame and a tight crop both survive, a sliver and a rectangle
// dragged into the void do not.
func TestACameraRectangleCannotBeLost(t *testing.T) {
	for _, c := range []cutFx{
		{Kind: "view", Hf: 0, Cx: 0.5, Cy: 0.5},
		{Kind: "view", Hf: -3, Cx: -99, Cy: 99},
		{Kind: "zoom", Hf: 1e6, Cx: 1e6, Cy: -1e6},
	} {
		f := c
		fxClampRect(&f)
		if f.Hf < 0.02 || f.Hf > 12 || f.Cx < -2 || f.Cx > 3 || f.Cy < -2 || f.Cy > 3 {
			t.Errorf("%+v clamped to %+v, which is still off the map", c, f)
		}
	}
	// the ordinary ones are left exactly alone
	for _, c := range []cutFx{
		{Kind: "view", Hf: 1, Cx: 0.5, Cy: 0.5},
		{Kind: "zoom", Hf: 0.2, Cx: 0.1, Cy: 0.9},
		{Kind: "view", Hf: 2.5, Cx: 0.5, Cy: 0.5}, // a pull back past the frame
	} {
		f := c
		fxClampRect(&f)
		if f != c {
			t.Errorf("%+v was changed to %+v", c, f)
		}
	}
	fxClampRect(nil) // and nothing is not a crash
}
