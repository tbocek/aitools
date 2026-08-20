package main

// The preview's framing overlay: the one place the camera effects are SEEN on
// the picture rather than on the timeline.
//
// A transparent drawing area sits over the preview (a GtkOverlay made in
// buildStep3). Passively it draws where the camera is at the playhead -- the
// cut's aspect as a bright rectangle, everything outside it dimmed, which is
// exactly what the finished video will and will not show. With an effect
// button armed, or a view/zoom held from the lane, it takes the pointer and a
// drag draws the camera rectangle by hand: locked to the cut's aspect,
// snapping to the full frame's width or height when it comes close, free to be
// smaller (a zoom in) or larger (a pull back past the frame's edge). A press
// that lands INSIDE a held rectangle slides it whole instead, its edges
// snapping to the frame's own edges.
//
// The overlay only ever DRAWS; what it produces is normalized numbers handed
// to the same cutFx everything else reads. It does not try to crop the live
// preview -- GStreamer plays the frame as filmed, the rectangle says what the
// render will make of it, and the render (step5_fx.go) reads the same numbers.

import (
	"math"
	"strings"

	"github.com/diamondburned/gotk4/pkg/cairo"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gsk/v4"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// srcSize is the pixel size of the recording under the preview, for turning
// normalized camera rectangles into places on the screen. Falls back to the
// first recording, then to plain HD, so the overlay never divides by zero.
func (ed *cutEditor) srcSize() (float64, float64) {
	v := ed.playVideo
	if v == nil && len(ed.vids) > 0 {
		v = &ed.vids[0]
	}
	if v != nil && v.w > 0 && v.h > 0 {
		return float64(v.w), float64(v.h)
	}
	return 1920, 1080
}

// outAspect is the shape of the finished video: the chosen aspect, or the
// footage's own when none is chosen.
func (ed *cutEditor) outAspect() float64 {
	if a := parseAspect(ed.aspect); a > 0 {
		return a
	}
	sw, sh := ed.srcSize()
	return sw / sh
}

// fxRectHeld is the held effect if it is one with a camera rectangle.
func (ed *cutEditor) fxRectHeld() *cutFx {
	if f := ed.heldFx(); f != nil && (f.Kind == "view" || f.Kind == "zoom") {
		return f
	}
	return nil
}

// viewInForce is the view the picture is framed by at session time t: the
// last one at or before t, or -- before any view at all -- the earliest one,
// because the first view frames the video from the very start (viewRectAt).
// nil when the cut has no views.
func (ed *cutEditor) viewInForce(t float64) *cutFx {
	var best *cutFx
	for i := range ed.fx {
		f := &ed.fx[i]
		if f.Kind != "view" {
			continue
		}
		switch {
		case best == nil:
			best = f
		case f.T <= t+1e-9:
			// a view at or before t beats anything later, and the latest such wins
			if best.T > t+1e-9 || f.T > best.T {
				best = f
			}
		case best.T > t+1e-9 && f.T < best.T:
			best = f // both are still ahead: the earliest is the one in force
		}
	}
	return best
}

// fxZoomCovers is whether a zoom's arc is over t. While one is, the rectangle
// on the picture is the zoom mid-flight rather than any view's own frame, so
// it is not something a hand may grab and call a view.
func fxZoomCovers(fx []cutFx, t float64) bool {
	for _, f := range fx {
		if f.Kind == "zoom" && f.Dur > 0 && t >= f.T && t < f.T+f.Dur {
			return true
		}
	}
	return false
}

// fxCamSettled is whether the camera is STANDING STILL at t -- parked on one
// view's own rectangle, with no zoom in flight and no glide half done.
//
// It is the condition for offering the camera rectangle to the hand. While the
// camera is moving, the rectangle on the picture belongs to no single effect:
// it is somewhere between two of them, and dragging it would have to snap to
// one end of the journey the moment the hand touched it. Nothing to grab is
// the honest answer, and the lane is still there for picking either end up.
func fxCamSettled(fx []cutFx, t float64) bool {
	if fxZoomCovers(fx, t) {
		return false
	}
	views := viewsOf(fx)
	if len(views) == 0 {
		return false
	}
	for _, v := range views {
		// a glide that is under way at t, ending after it: still travelling.
		// The earliest view is excluded -- its glide has nowhere to travel
		// from and viewRectAt makes it a no-op (the first-view rule).
		if v.T != views[0].T && v.Trans > 0 && t >= v.T && t < v.T+v.Trans {
			return false
		}
	}
	return true
}

// fxDragTarget is the effect a drag on the picture edits when nothing has been
// picked up: the view the picture is framed by right now. This is what makes
// the overlay direct -- the rectangle you can see is the rectangle you can
// take hold of, without going down to the lane to pick its effect up first.
//
// It is deliberately narrow. Nothing is offered while the preview is playing
// (the picture on screen is then the camera's own window, drawn by a different
// mapping -- see syncPreviewZoom -- so a drag would move the box somewhere
// other than where the hand went), nothing while the camera is in motion, and
// nothing when no view has been placed at all.
func (ed *cutEditor) fxDragTarget() *cutFx {
	if f := ed.fxRectHeld(); f != nil {
		return f
	}
	if !ed.hasPlay || ed.livePreview() || !fxCamSettled(ed.fx, ed.playhead) {
		return nil
	}
	return ed.viewInForce(ed.playhead)
}

// livePreview is whether the zoomed live layer is the thing on screen. While
// it is, the overlay draws only the black around the output box and offers
// nothing to grab: framing is done on a paused picture, where the outline
// shows everything the camera COULD see.
func (ed *cutEditor) livePreview() bool {
	return ed.fxZoom != nil && ed.fxZoom.Visible()
}

// fxHeldText is the held effect if it is a text -- the other kind of rectangle
// the picture offers, and the only one that is not bound to the cut's aspect.
func (ed *cutEditor) fxHeldText() *cutFx {
	if f := ed.heldFx(); f != nil && f.Kind == "text" {
		return f
	}
	return nil
}

// fxEdges says what a point at (x, y) has hold of on the rectangle r: which
// borders it is within reach of, and whether it is inside at all. Both edges
// at once is a corner, which is why these come back as two independent axes.
func fxEdges(x, y, rx, ry, rw, rh float64) (horiz, vert, left, top, inside bool) {
	inX := x >= rx-fxGrab && x <= rx+rw+fxGrab
	inY := y >= ry-fxGrab && y <= ry+rh+fxGrab
	nearL, nearR := math.Abs(x-rx) <= fxGrab, math.Abs(x-(rx+rw)) <= fxGrab
	nearT, nearB := math.Abs(y-ry) <= fxGrab, math.Abs(y-(ry+rh)) <= fxGrab
	horiz, vert = (nearL || nearR) && inY, (nearT || nearB) && inX
	inside = x >= rx && x <= rx+rw && y >= ry && y <= ry+rh
	return horiz, vert, nearL, nearT, inside
}

// fxCursorName is the pointer over a rectangle: the resize arrows on the
// borders and the corners, the move hand inside. Naming what a border does
// before it is pressed is the whole of how anyone finds out it resizes.
func fxCursorName(horiz, vert, left, top, inside bool) string {
	switch {
	case horiz && vert:
		if left == top {
			return "nwse-resize"
		}
		return "nesw-resize"
	case horiz:
		return "ew-resize"
	case vert:
		return "ns-resize"
	case inside:
		return "move"
	}
	return "default"
}

// resizeRect is a border drag, in widget pixels. The hand is at (x, y), the
// far edge or corner the drag keeps still is (ax, ay), and pxA is the cut's
// aspect expressed in those same pixels. horiz/vert say which borders were
// grabbed and left/top which side of the rectangle they were on.
//
// ONE number is really dragged -- the height -- and the width follows from the
// cut's ratio, because a camera window that is not the shape of the finished
// video is not something the render can honour. A corner takes whichever axis
// the hand moved further, so the rectangle keeps up with the pointer instead
// of lagging behind the shorter one. Returns the new centre and height.
func resizeRect(x, y, ax, ay, pxA float64, horiz, vert, left, top bool) (cx, cy, h float64) {
	switch {
	case horiz && vert:
		h = math.Max(math.Abs(x-ax)/pxA, math.Abs(y-ay))
	case horiz:
		h = math.Abs(x-ax) / pxA
	default:
		h = math.Abs(y - ay)
	}
	h = math.Max(h, 12) // a rectangle smaller than this is a slip, not an edit
	w := h * pxA
	cx, cy = ax, ay
	if horiz {
		cx = ax + w/2
		if left {
			cx = ax - w/2
		}
	}
	if vert {
		cy = ay + h/2
		if top {
			cy = ay - h/2
		}
	}
	return cx, cy, h
}

// resizeFree is a border drag on a rectangle that owes nothing to the cut's
// aspect -- a text box, which is whatever shape the words want. The two axes
// are independent: a side moves one edge, a corner moves two, and an edge that
// was not grabbed does not move at all. x0,y0,w0,h0 is the rectangle as it was;
// ax,ay the edges being kept still. min is the smallest it may become, so a
// box cannot be collapsed into something the hand can no longer find.
func resizeFree(x, y, ax, ay, x0, y0, w0, h0, min float64,
	horiz, vert, left, top bool) (nx, ny, nw, nh float64) {
	nx, ny, nw, nh = x0, y0, w0, h0
	if horiz {
		if left {
			nx = math.Min(x, ax-min)
			nw = ax - nx
		} else {
			nw = math.Max(x-ax, min)
			nx = ax
		}
	}
	if vert {
		if top {
			ny = math.Min(y, ay-min)
			nh = ay - ny
		} else {
			nh = math.Max(y-ay, min)
			ny = ay
		}
	}
	return nx, ny, nw, nh
}

// fxGrab is how near the rectangle's edge a press counts as taking hold of
// that edge rather than of the whole rectangle. A 1.5 px line is not a mouse
// target; this is, on either side of it.
const fxGrab = 9.0

// syncFxCursor decides whether the overlay owns the pointer. Only while a
// drag would mean something: armed, or holding a view/zoom to re-frame. The
// rest of the time it must not take the click that toggles playback.
func (ed *cutEditor) syncFxCursor() {
	if ed.fxArea == nil {
		return
	}
	armed := ed.fxArm != ""
	held := ed.fxRectHeld() != nil || ed.fxHeldText() != nil
	// the overlay also takes the pointer when there is simply something on the
	// picture to grab -- a view framing it, a text showing on it -- so that a
	// box can be moved and resized where it is seen. That swallows the click
	// that toggles playback, so every press that turns out not to be a drag is
	// handed back to it (see the drag's end).
	ed.fxArea.SetCanTarget(armed || held || ed.fxDragTarget() != nil ||
		len(textsAt(ed.fx, ed.playhead)) > 0)
	if armed {
		// nothing is under the pointer yet; the motion controller takes over
		// as soon as it moves. Its cache is set here too, or the first move
		// after arming would decide the crosshair is already up and leave the
		// resize arrow the last hover put there.
		ed.fxCursor = "crosshair"
		ed.fxArea.SetCursor(gdk.NewCursorFromName("crosshair", nil))
	} else if ed.fxCursor == "crosshair" {
		ed.fxCursor = "default"
		ed.fxArea.SetCursor(gdk.NewCursorFromName("default", nil))
	}
}

// fxDisp is where the video's picture actually is inside the overlay widget:
// GtkPicture keeps the frame's aspect (contain), so there are bars either
// side or above, and the maths here has to match or the rectangle is drawn on
// the bars.
func fxDisp(w, h, srcA float64) (ox, oy, dw, dh float64) {
	if w <= 0 || h <= 0 || srcA <= 0 {
		return 0, 0, w, h
	}
	dw = w
	dh = w / srcA
	if dh > h {
		dh = h
		dw = h * srcA
	}
	return (w - dw) / 2, (h - dh) / 2, dw, dh
}

// snapRectPos pulls a sliding camera rectangle onto the frame's edges when it
// comes close: cx,cy is the centre, wf,hf the rectangle's size, all fractions
// of the source frame; tx,ty how close counts, in the same units. The axes
// snap independently, so corners come for free.
func snapRectPos(cx, cy, wf, hf, tx, ty float64) (float64, float64) {
	switch {
	case math.Abs(cx-wf/2) < tx:
		cx = wf / 2 // left edge on the frame's left
	case math.Abs(cx-(1-wf/2)) < tx:
		cx = 1 - wf/2 // right edge on the frame's right
	}
	switch {
	case math.Abs(cy-hf/2) < ty:
		cy = hf / 2 // top on top
	case math.Abs(cy-(1-hf/2)) < ty:
		cy = 1 - hf/2 // bottom on bottom
	}
	return cx, cy
}

// fxZoomDrag reports that the drag in flight belongs to a zoom -- armed, or
// re-framing a held one. A zoom's rectangle is not bound by the cut's aspect.
func (ed *cutEditor) fxZoomDrag() bool {
	if ed.fxArm != "" {
		return ed.fxArm == "zoom"
	}
	f := ed.fxRectHeld()
	return f != nil && f.Kind == "zoom"
}

// fxFreeDrag reports that the rectangle being drawn is a free shape rather
// than one locked to the cut's aspect: a zoom's window (which the render fits
// the output box into) and a text's box (which is a shape on the finished
// frame, not a frame of its own).
func (ed *cutEditor) fxFreeDrag() bool {
	return ed.fxArm == "text" || ed.fxZoomDrag()
}

// fxClampRect keeps a camera rectangle somewhere a person can find it again.
// The limits are deliberately loose -- a pull-back well past the frame's edge
// is a real shot, and so is a tight crop -- they only rule out the rectangles
// that are no longer a picture: a hair-thin sliver, something a thousand
// frames wide, a centre dragged into the void off the side of the recording.
func fxClampRect(f *cutFx) {
	if f == nil {
		return
	}
	f.Hf = math.Min(math.Max(f.Hf, 0.02), 12)
	f.Cx = math.Min(math.Max(f.Cx, -2), 3)
	f.Cy = math.Min(math.Max(f.Cy, -2), 3)
}

// liveSize is the size the zoom layer's picture is actually drawn at: the
// paintable's own pixels when it knows them, the probe's otherwise.
func (ed *cutEditor) liveSize() (float64, float64) {
	if ed.player != nil {
		if pw, ph := ed.player.video.IntrinsicWidth(), ed.player.video.IntrinsicHeight(); pw > 0 && ph > 0 {
			return float64(pw), float64(ph)
		}
	}
	return ed.srcSize()
}

// liveZoom is the transform that puts the camera on the playing preview: the
// scale s and offset tx,ty that map the picture, drawn 1:1 at sw×sh, so the
// camera rect r fills the output-shaped box centred in a W×H widget.
func liveZoom(W, H, sw, sh, outA float64, r fxRect) (s, tx, ty float64) {
	bx, by, bw, bh := fxDisp(W, H, outA)
	s = bh / (r.hf * sh)
	tx = bx + bw/2 - r.cx*sw*s
	ty = by + bh/2 - r.cy*sh*s
	return
}

// syncPreviewZoom puts the real camera on the playing preview. While the
// stream runs and the cut has a camera -- and nobody is framing by hand --
// the layer shows the same video again, transformed so the camera's window
// fills the output box: the preview actually zooms, glides included. Paused,
// or with an effect armed or held, the layer goes away and the outline comes
// back, because framing is done against everything the camera could see.
func (ed *cutEditor) syncPreviewZoom() {
	if ed.fxZoom == nil {
		return
	}
	on := ed.player != nil && ed.player.playing && !ed.player.still && ed.hasPlay &&
		fxHasCamera(ed.aspect, ed.fx) && ed.fxArm == "" && ed.fxRectHeld() == nil
	if !on {
		ed.fxZoom.SetVisible(false)
		return
	}
	W := float64(ed.fxArea.AllocatedWidth())
	H := float64(ed.fxArea.AllocatedHeight())
	sw, sh := ed.liveSize()
	if W <= 0 || H <= 0 || sw <= 0 || sh <= 0 {
		ed.fxZoom.SetVisible(false)
		return
	}
	// the picture is pinned to its own pixel size, so the transform below is
	// the whole mapping (GtkFixed hands children their natural size)
	if rw, rh := ed.fxZoomPic.SizeRequest(); rw != int(sw) || rh != int(sh) {
		ed.fxZoomPic.SetSizeRequest(int(sw), int(sh))
	}
	r := fxRectAt(ed.fx, ed.playhead, sw/sh, ed.outAspect())
	s, tx, ty := liveZoom(W, H, sw, sh, ed.outAspect(), r)
	if s <= 0 || math.IsNaN(s) || math.IsInf(s, 0) {
		ed.fxZoom.SetVisible(false)
		return
	}
	ed.fxZoom.SetChildTransform(ed.fxZoomPic, zoomTransform(s, tx, ty))
	ed.fxZoom.SetVisible(true)
}

// zoomTransform is translate(tx,ty) then scale(s), as one call on no transform
// at all.
//
// It is written this way on purpose, and the shape matters more than it looks.
// Every gsk_transform_* call CONSUMES the transform it is chained onto -- gotk4
// says so in its own generated comment ("This function consumes next") -- but
// the Go value it consumed still carries a finalizer that will unref it again.
// So the natural spelling,
//
//	gsk.NewTransform().Translate(pt).Scale(s, s)
//
// leaves two unrefs owing on references the C calls already took, and this runs
// on every tick of a playing preview. Go's finalizer goroutine collects them
// seconds later and frees memory GTK is still holding, which surfaces as GLib
// shouting about ref counts and boxes it no longer recognises and then a
// segfault somewhere entirely unrelated. A nil transform is the identity, so
// starting from nil consumes nothing, and one matrix does both steps: the 2D
// matrix [xx yx x0; xy yy y0] sends a point to (s*x + tx, s*y + ty), which is
// what the chain meant. The one transform that comes back is handed straight
// to SetChildTransform, which is transfer-none (GtkFixed refs it), so the
// finalizer it does carry is the only one owing and it balances.
func zoomTransform(s, tx, ty float64) *gsk.Transform {
	var identity *gsk.Transform
	return identity.Matrix2D(float32(s), 0, 0, float32(s), float32(tx), float32(ty))
}

// buildFxOverlay wraps the preview picture with the framing overlay and wires
// its gestures. Everything the drag knows lives in the closure; what it emits
// goes through the same addFx/updateHeldFx as every other edit.
func (ed *cutEditor) buildFxOverlay() *gtk.Overlay {
	over := gtk.NewOverlay()
	over.SetChild(ed.player.Picture)
	// the wheel over the picture steps frames -- the preview is where you look
	// while hunting for one, so it answers the scrub gesture itself
	over.AddController(ed.wheelFrames())

	// the live-zoom layer: a second picture of the same paintable, hidden
	// until syncPreviewZoom has a playing camera to show. It sits under the
	// drawing area, which paints the black outside the output box over it.
	zfix := gtk.NewFixed()
	zfix.SetOverflow(gtk.OverflowHidden)
	zfix.SetCanTarget(false)
	zpic := gtk.NewPicture()
	zpic.SetPaintable(ed.player.video)
	zfix.Put(zpic, 0, 0)
	zfix.SetVisible(false)
	ed.fxZoom, ed.fxZoomPic = zfix, zpic
	over.AddOverlay(zfix)

	area := gtk.NewDrawingArea()
	ed.fxArea = area
	over.AddOverlay(area)
	area.SetCanTarget(false) // passive until armFx or a held view/zoom

	// ---- geometry ---------------------------------------------------------
	//
	// Three rectangles nest inside each other, and everything below is said in
	// terms of them. dispPx is where the video's picture actually is in the
	// widget (GtkPicture keeps the frame's aspect, so there are bars). camPx
	// is the camera's window on that picture -- which is exactly what the
	// finished video shows. And a text box is a fraction of THAT, because text
	// is placed on the output frame and has to stay put while the camera moves
	// under it (fxtext.go).

	dispPx := func() (ox, oy, dw, dh float64) {
		aw, ah := float64(area.AllocatedWidth()), float64(area.AllocatedHeight())
		sw, sh := ed.srcSize()
		return fxDisp(aw, ah, sw/sh)
	}

	// pxAspect is the cut's aspect measured in widget pixels: dh widget px are
	// sh source px, so a rectangle's width is its height times this.
	pxAspect := func() float64 {
		sw, sh := ed.srcSize()
		_, _, dw, dh := dispPx()
		if dw <= 0 || dh <= 0 || sw <= 0 || sh <= 0 {
			return ed.outAspect()
		}
		return ed.outAspect() * (dw / sw) * (sh / dh)
	}

	// rectPx puts a normalized camera rectangle on the widget.
	rectPx := func(r fxRect) (rx, ry, rw, rh float64) {
		ox, oy, dw, dh := dispPx()
		rh = r.hf * dh
		rw = rh * pxAspect()
		return ox + r.cx*dw - rw/2, oy + r.cy*dh - rh/2, rw, rh
	}

	// camPx is the camera rectangle the overlay is showing: the held view or
	// zoom's own, or -- with nothing held -- where the camera is at the
	// playhead. Not ok when nothing frames the picture at all.
	camPx := func() (rx, ry, rw, rh float64, ok bool) {
		if f := ed.fxRectHeld(); f != nil {
			rx, ry, rw, rh = rectPx(fxRect{f.Cx, f.Cy, f.Hf})
			return rx, ry, rw, rh, true
		}
		if !ed.hasPlay || !fxHasCamera(ed.aspect, ed.fx) {
			return
		}
		sw, sh := ed.srcSize()
		rx, ry, rw, rh = rectPx(fxRectAt(ed.fx, ed.playhead, sw/sh, ed.outAspect()))
		return rx, ry, rw, rh, true
	}

	// outFramePx is the finished frame on screen -- the rectangle a text box
	// is a fraction of. That is the camera's window when there is a camera,
	// the whole picture when there is not, and while the preview is playing
	// the output box the live layer is drawn into.
	outFramePx := func() (x, y, w, h float64) {
		if ed.livePreview() {
			aw, ah := float64(area.AllocatedWidth()), float64(area.AllocatedHeight())
			return fxDisp(aw, ah, ed.outAspect())
		}
		if rx, ry, rw, rh, ok := camPx(); ok {
			return rx, ry, rw, rh
		}
		return dispPx()
	}

	textPx := func(f cutFx) (x, y, w, h float64) {
		ox, oy, ow, oh := outFramePx()
		bx, by, bw, bh := f.textBox().px(ow, oh)
		return ox + bx, oy + by, bw, bh
	}

	// setTextPx is the way back: a rectangle on the widget written onto the
	// effect as fractions of the finished frame, clamped so a box can never be
	// dragged off the video and lost.
	setTextPx := func(f *cutFx, x, y, w, h float64) {
		ox, oy, ow, oh := outFramePx()
		if ow <= 0 || oh <= 0 {
			return
		}
		b := fxBox{cx: (x + w/2 - ox) / ow, cy: (y + h/2 - oy) / oh, wf: w / ow, hf: h / oh}.clamp()
		f.Cx, f.Cy, f.Wf, f.Hf = b.cx, b.cy, b.wf, b.hf
	}

	// ---- what a press takes hold of ----------------------------------------

	// fxGrabbed is one rectangle offered to the hand: the effect behind it,
	// where it is on the widget, and which of its borders the pointer has.
	type fxGrabbed struct {
		f                              *cutFx
		text                           bool
		x, y, w, h                     float64
		horiz, vert, left, top, inside bool
	}
	// grabAt is the whole hit test, and its ORDER is the rule the picture
	// follows: whatever is held answers first (you are working on it), then
	// the texts on screen from the top down (they sit inside the camera
	// rectangle, so they have to be asked before it), and last the view that
	// frames the picture. Nothing while an effect is armed -- that drag is
	// drawing a new box -- and nothing while the preview plays.
	grabAt := func(x, y float64) *fxGrabbed {
		try := func(f *cutFx, text bool, rx, ry, rw, rh float64) *fxGrabbed {
			g := &fxGrabbed{f: f, text: text, x: rx, y: ry, w: rw, h: rh}
			g.horiz, g.vert, g.left, g.top, g.inside = fxEdges(x, y, rx, ry, rw, rh)
			if !g.horiz && !g.vert && !g.inside {
				return nil
			}
			return g
		}
		if ed.fxArm != "" || ed.livePreview() {
			return nil
		}
		if f := ed.fxHeldText(); f != nil {
			tx, ty, tw, th := textPx(*f)
			return try(f, true, tx, ty, tw, th)
		}
		if f := ed.fxRectHeld(); f != nil {
			if rx, ry, rw, rh, ok := camPx(); ok {
				return try(f, false, rx, ry, rw, rh)
			}
			return nil
		}
		vis := textsAt(ed.fx, ed.playhead)
		for i := len(vis) - 1; i >= 0; i-- {
			f := &ed.fx[vis[i]]
			tx, ty, tw, th := textPx(*f)
			if g := try(f, true, tx, ty, tw, th); g != nil {
				return g
			}
		}
		if f := ed.fxDragTarget(); f != nil {
			if rx, ry, rw, rh, ok := camPx(); ok {
				return try(f, false, rx, ry, rw, rh)
			}
		}
		return nil
	}

	// The drag in flight, in widget px. Its kind is settled at the press and
	// never changes under the hand:
	//
	//	""     grabbed nothing -- the press is the preview's own click
	//	draw   a fresh rectangle, dragged corner to corner
	//	move   slides what was grabbed
	//	size   scales it about the border opposite the one taken hold of
	var drag struct {
		on             bool
		kind           string
		x0, y0, x1, y1 float64
		moved          bool
		g              fxGrabbed
		cx0, cy0       float64 // a camera rectangle's centre when the press landed
		ax, ay         float64 // the point a resize keeps still
	}

	// dragRect is the rectangle the current drag means, in widget px:
	// anchored at the press, following the pointer, locked to the cut's aspect
	// and snapped to the frame's full width or height when it comes within
	// reach -- unless it is a free one (a zoom's window, a text's box), which
	// is simply the two corners.
	dragRect := func() (x, y, w, h float64) {
		_, _, dw, dh := dispPx()
		pxA := pxAspect()
		dx, dy := drag.x1-drag.x0, drag.y1-drag.y0
		if ed.fxFreeDrag() {
			return math.Min(drag.x0, drag.x1), math.Min(drag.y0, drag.y1),
				math.Abs(dx), math.Abs(dy)
		}
		h = math.Max(math.Abs(dy), math.Abs(dx)/pxA)
		// snap: close to the whole frame in either direction is the whole frame
		if math.Abs(h*pxA-dw) < dw*0.05 {
			h = dw / pxA
		} else if math.Abs(h-dh) < dh*0.05 {
			h = dh
		}
		w = h * pxA
		x, y = drag.x0, drag.y0
		if dx < 0 {
			x -= w
		}
		if dy < 0 {
			y -= h
		}
		return x, y, w, h
	}

	// drawText paints one text effect where the render will put it: the same
	// font size and the same line breaks (fitText answers for both), each line
	// centred on its own measured width -- which is what SVG's text-anchor
	// does too. The dark edge is eight offset copies rather than the SVG's
	// stroke, because cairo's Go binding has no text path to stroke; it is the
	// same idea at preview resolution.
	drawText := func(cr *cairo.Context, f cutFx, alpha float64) {
		tx, ty, tw, th := textPx(f)
		size, lines := fitText(f.Text, tw, th)
		if size <= 0 || alpha <= 0.01 {
			return
		}
		cr.SelectFontFace("sans-serif", cairo.FontSlantNormal, cairo.FontWeightBold)
		cr.SetFontSize(size)
		base := textBaselines(ty, th, size, len(lines))
		mid := tx + tw/2
		d := math.Max(1, size*0.08)
		for i, ln := range lines {
			if strings.TrimSpace(ln) == "" {
				continue
			}
			x := mid - cr.TextExtents(ln).XAdvance/2
			cr.SetSourceRGBA(0, 0, 0, 0.85*alpha)
			for _, o := range [8][2]float64{{-d, 0}, {d, 0}, {0, -d}, {0, d},
				{-d, -d}, {d, -d}, {-d, d}, {d, d}} {
				cr.MoveTo(x+o[0], base[i]+o[1])
				cr.ShowText(ln)
			}
			cr.SetSourceRGBA(1, 1, 1, alpha)
			cr.MoveTo(x, base[i])
			cr.ShowText(ln)
		}
	}

	// boxOutline is the dashed violet frame around a text box, drawn only for
	// the one being worked on: a title with a rectangle permanently round it
	// would not look like the title the render makes.
	boxOutline := func(cr *cairo.Context, x, y, w, h float64) {
		cr.SetSourceRGBA(0.6, 0.55, 0.95, 0.9)
		cr.SetLineWidth(1.5)
		cr.SetDash([]float64{4, 3}, 0)
		cr.Rectangle(x, y, w, h)
		cr.Stroke()
		cr.SetDash(nil, 0)
	}

	area.SetDrawFunc(func(_ *gtk.DrawingArea, cr *cairo.Context, w, h int) {
		if ed.player == nil || ed.player.still {
			return // a card is on screen; the camera talks about footage
		}
		if ed.livePreview() {
			// the camera is live on the picture underneath (syncPreviewZoom):
			// paint everything the render will not have -- the picture
			// outside the output box, and any box a pulled-back frame leaves
			// bare -- and nothing else; the outline waits for the pause
			lw, lh := ed.liveSize()
			outA := ed.outAspect()
			bx, by, bw, bh := fxDisp(float64(w), float64(h), outA)
			s, tx, ty := liveZoom(float64(w), float64(h), lw, lh, outA,
				fxRectAt(ed.fx, ed.playhead, lw/lh, outA))
			x0, y0 := math.Max(bx, tx), math.Max(by, ty)
			x1, y1 := math.Min(bx+bw, tx+lw*s), math.Min(by+bh, ty+lh*s)
			cr.SetSourceRGB(0, 0, 0)
			cr.Rectangle(0, 0, float64(w), float64(h))
			if x1 > x0 && y1 > y0 {
				cr.NewSubPath()
				cr.Rectangle(x0, y0, x1-x0, y1-y0)
			}
			cr.SetFillRule(cairo.FillRuleEvenOdd)
			cr.Fill()
			cr.SetFillRule(cairo.FillRuleWinding)
			// the words go on while it plays, faded exactly as they will be:
			// a title is a thing you watch, not a thing you inspect paused
			for _, i := range textsAt(ed.fx, ed.playhead) {
				drawText(cr, ed.fx[i], textAlpha(ed.fx[i], ed.playhead))
			}
			return
		}
		// 1. the camera rectangle, or the one being drawn in its place
		var rx, ry, rw, rh float64
		haveCam := false
		label := ed.aspect
		drawingCam := drag.on && drag.kind == "draw" && ed.fxArm != "text"
		switch {
		case drawingCam:
			rx, ry, rw, rh = dragRect()
			haveCam = true
		default:
			if x, y, ww, hh, ok := camPx(); ok {
				rx, ry, rw, rh, haveCam = x, y, ww, hh, true
				if f := ed.fxRectHeld(); f != nil {
					label = f.fxLabel()
				}
			}
		}
		if haveCam {
			// dim what the finished video does not show
			cr.SetSourceRGBA(0, 0, 0, 0.45)
			cr.Rectangle(0, 0, float64(w), float64(h))
			cr.NewSubPath()
			cr.Rectangle(rx, ry, rw, rh)
			cr.SetFillRule(cairo.FillRuleEvenOdd)
			cr.Fill()
			cr.SetFillRule(cairo.FillRuleWinding)
			col := [3]float64{1, 1, 1}
			if drawingCam || ed.fxRectHeld() != nil {
				col = [3]float64{0.95, 0.62, 0.15}
				if f := ed.fxRectHeld(); (f != nil && f.Kind == "zoom") || ed.fxArm == "zoom" {
					col = [3]float64{0.25, 0.72, 0.82}
				}
			}
			cr.SetSourceRGB(col[0], col[1], col[2])
			cr.SetLineWidth(1.5)
			cr.Rectangle(rx, ry, rw, rh)
			cr.Stroke()
			if label != "" {
				cr.SetFontSize(10)
				plateText(cr, rx+4, ry+13, label)
			}
		}
		// 2. the words on the finished frame, faded as the render will fade
		// them -- and the held one always, wherever the playhead is, because
		// something being edited has to be visible to be edited
		held := ed.fxHeldText()
		for _, i := range textsAt(ed.fx, ed.playhead) {
			if f := &ed.fx[i]; f != held {
				drawText(cr, *f, textAlpha(*f, ed.playhead))
			}
		}
		if held != nil {
			drawText(cr, *held, 1)
			tx, ty, tw, th := textPx(*held)
			boxOutline(cr, tx, ty, tw, th)
		}
		// 3. a text box being drawn by hand, or one under the pointer that a
		// press would take hold of
		switch {
		case drag.on && drag.kind == "draw" && ed.fxArm == "text":
			x, y, ww, hh := dragRect()
			boxOutline(cr, x, y, ww, hh)
		case drag.on && drag.g.text && drag.g.f != nil && drag.g.f != held:
			tx, ty, tw, th := textPx(*drag.g.f)
			boxOutline(cr, tx, ty, tw, th)
		}
	})

	g := gtk.NewGestureDrag()
	g.ConnectDragBegin(func(x, y float64) {
		drag.kind, drag.moved, drag.g = "", false, fxGrabbed{}
		drag.on = true
		drag.x0, drag.y0, drag.x1, drag.y1 = x, y, x, y
		defer area.QueueDraw()
		if ed.fxArm != "" {
			drag.kind = "draw" // an armed button always means a fresh box
			return
		}
		gr := grabAt(x, y)
		if gr == nil {
			// clear of everything: with a camera rectangle held, a drag draws
			// a new one for it; otherwise the press belongs to the picture
			if ed.fxRectHeld() != nil {
				drag.kind = "draw"
			}
			return
		}
		drag.g = *gr
		drag.cx0, drag.cy0 = gr.f.Cx, gr.f.Cy
		if gr.horiz || gr.vert {
			// an edge grabbed square-on keeps the middle of the opposite one,
			// so the rectangle grows along that axis alone; a corner keeps the
			// far corner, which is what every resize handle everywhere does
			drag.kind = "size"
			drag.ax, drag.ay = gr.x+gr.w/2, gr.y+gr.h/2
			if gr.horiz {
				drag.ax = gr.x
				if gr.left {
					drag.ax = gr.x + gr.w
				}
			}
			if gr.vert {
				drag.ay = gr.y
				if gr.top {
					drag.ay = gr.y + gr.h
				}
			}
			return
		}
		drag.kind = "move"
	})
	g.ConnectDragUpdate(func(dx, dy float64) {
		if !drag.on {
			return
		}
		drag.x1, drag.y1 = drag.x0+dx, drag.y0+dy
		editing := drag.kind == "move" || drag.kind == "size"
		if !drag.moved && math.Hypot(dx, dy) > 2 {
			drag.moved = true
			if editing {
				ed.pushUndo() // once, before anything shifts: one Undo per drag
			}
		}
		if editing && drag.moved {
			f := drag.g.f
			ox, oy, dw, dh := dispPx()
			if f == nil || dw <= 0 || dh <= 0 {
				drag.on = false
				return
			}
			switch {
			case drag.g.text && drag.kind == "move":
				setTextPx(f, drag.g.x+dx, drag.g.y+dy, drag.g.w, drag.g.h)
			case drag.g.text:
				// a text box owes nothing to the cut's aspect: the two axes
				// move independently, and an edge not grabbed does not move
				nx, ny, nw, nh := resizeFree(drag.x1, drag.y1, drag.ax, drag.ay,
					drag.g.x, drag.g.y, drag.g.w, drag.g.h, 16,
					drag.g.horiz, drag.g.vert, drag.g.left, drag.g.top)
				setTextPx(f, nx, ny, nw, nh)
			case drag.kind == "move":
				// the rectangle's width as a fraction of the frame's width:
				// hf frame-heights tall, and the cut's aspect wide
				sw, sh := ed.srcSize()
				wf := f.Hf * (sh / sw) * ed.outAspect()
				cx, cy := drag.cx0+dx/dw, drag.cy0+dy/dh
				f.Cx, f.Cy = snapRectPos(cx, cy, wf, f.Hf, 10/dw, 10/dh)
				fxClampRect(f)
			default:
				cx, cy, h := resizeRect(drag.x1, drag.y1, drag.ax, drag.ay,
					pxAspect(), drag.g.horiz, drag.g.vert, drag.g.left, drag.g.top)
				f.Hf = h / dh
				f.Cx, f.Cy = (cx-ox)/dw, (cy-oy)/dh
				fxClampRect(f)
			}
			ed.syncPreviewZoom() // the live layer may be showing this camera
		}
		area.QueueDraw()
	})
	g.ConnectDragEnd(func(dx, dy float64) {
		if !drag.on {
			return
		}
		drag.on = false
		area.QueueDraw()
		zoomFree := ed.fxZoomDrag() // before the arm is cleared below
		switch drag.kind {
		case "", "move", "size":
			// A press that never moved is a CLICK, and a click on the picture
			// plays or pauses it -- wherever it landed. The overlay owns the
			// pointer over most of the frame now, and a picture that stops
			// answering the click that starts it because there happens to be a
			// view under the finger is a worse trade than any gesture is worth.
			if !drag.moved {
				ed.toggle()
				return
			}
			if f := drag.g.f; f != nil && drag.kind != "" {
				ed.persist()
				what := " moved"
				if drag.kind == "size" {
					what = " resized"
				}
				ed.a.setStatus(f.fxLabel() + what + " — ↶ Undo takes it back")
			}
			return
		}
		rx, ry, rw, rh := dragRect()
		ox, oy, dw, dh := dispPx()
		tiny := math.Max(rw, rh) < 12 || dw <= 0 || dh <= 0
		if ed.fxArm == "text" {
			// a click is enough for a text: the default box across the lower
			// third is where a caption goes, and having to draw one before
			// being allowed to type is a toll on the common case
			ed.fxArm = ""
			ed.syncFxCursor()
			f := cutFx{Kind: "text", T: ed.playhead, Dur: 3, Trans: 0.3, Tout: 0.3}
			b := fxTextDefault
			if !tiny {
				fx0, fy0, fw0, fh0 := outFramePx()
				if fw0 > 0 && fh0 > 0 {
					b = fxBox{cx: (rx + rw/2 - fx0) / fw0, cy: (ry + rh/2 - fy0) / fh0,
						wf: rw / fw0, hf: rh / fh0}.clamp()
				}
			}
			f.Cx, f.Cy, f.Wf, f.Hf = b.cx, b.cy, b.wf, b.hf
			ed.a.askTextParams(f, true, func(nf cutFx) {
				if strings.TrimSpace(nf.Text) == "" {
					ed.a.setStatus("no words were typed — nothing was placed")
					return
				}
				ed.addFx(nf)
				ed.a.setStatus(nf.fxLabel() + " — the words are on the picture for those " +
					"seconds; ↶ Undo takes it back")
			})
			return
		}
		if tiny {
			return // a click, not a framing -- the arm stays up for another try
		}
		cx := (rx + rw/2 - ox) / dw
		cy := (ry + rh/2 - oy) / dh
		hf := rh / dh
		if zoomFree {
			// the smallest output-shaped window that holds the free drawing
			hf = math.Max(rh, rw/pxAspect()) / dh
		}
		switch {
		case ed.fxArm == "view":
			ed.fxArm = ""
			ed.syncFxCursor()
			f := cutFx{Kind: "view", T: ed.playhead, Cx: cx, Cy: cy, Hf: hf}
			fxClampRect(&f)
			first := len(viewsOf(ed.fx)) == 0
			ed.a.askViewParams(f, true, func(nf cutFx) {
				ed.addFx(nf)
				msg := " — the video shows this region from here on; ↶ Undo takes it back"
				if first {
					msg = " — the first view: the video is framed like this from the very start; " +
						"↶ Undo takes it back"
				}
				ed.a.setStatus(nf.fxLabel() + msg)
			})
		case ed.fxArm == "zoom":
			ed.fxArm = ""
			ed.syncFxCursor()
			f := cutFx{Kind: "zoom", T: ed.playhead, Cx: cx, Cy: cy, Hf: hf, Trans: 1, Tout: 1, Dur: 3}
			fxClampRect(&f)
			ed.a.askZoomParams(f, true, func(nf cutFx) {
				ed.addFx(nf)
				ed.a.setStatus(nf.fxLabel() + " — ↶ Undo takes it back")
			})
		default:
			if f := ed.fxRectHeld(); f != nil {
				ed.pushUndo()
				f.Cx, f.Cy, f.Hf = cx, cy, hf
				fxClampRect(f)
				ed.persist()
				ed.a.setStatus(f.fxLabel() + " re-framed — ↶ Undo takes it back")
			}
		}
	})
	area.AddController(g)

	// the pointer says what a press would do before it is pressed: the resize
	// arrows on a border, the move hand inside a box. Without this the border
	// handle is invisible -- there is nothing on a 1.5 px line to suggest that
	// pulling it is different from pulling the middle.
	motion := gtk.NewEventControllerMotion()
	motion.ConnectMotion(func(x, y float64) {
		name := "default"
		if ed.fxArm != "" {
			name = "crosshair"
		} else if gr := grabAt(x, y); gr != nil {
			name = fxCursorName(gr.horiz, gr.vert, gr.left, gr.top, gr.inside)
		}
		if name != ed.fxCursor {
			ed.fxCursor = name
			area.SetCursor(gdk.NewCursorFromName(name, nil))
		}
	})
	area.AddController(motion)

	// the right button on the picture is "never mind" -- disarm, or put the
	// held effect down, the meaning it has clear of things on the lane --
	// EXCEPT on a box, where it asks that box's numbers instead: the words and
	// seconds of a text, the transition of a view, exactly what ✎ Edit opens.
	// It works on a box that is merely under the pointer as well as on the
	// held one, because the picture offers those to the hand too and a
	// rectangle that can be dragged should be able to answer for itself. The
	// first view is the exception's exception: it frames the video from the
	// very start, so there is no transition to choose and the click says why.
	esc := gtk.NewGestureClick()
	esc.SetButton(gdk.BUTTON_SECONDARY)
	esc.ConnectPressed(func(n int, x, y float64) {
		if ed.fxArm != "" {
			ed.fxArm = ""
			ed.a.setStatus("cancelled")
			ed.dropFx()
			ed.syncFxCursor()
			return
		}
		if gr := grabAt(x, y); gr != nil && gr.f != nil {
			if gr.f.Kind == "view" && ed.firstView(*gr.f) {
				ed.a.setStatus("the first view has no transition — the video is framed " +
					"on it from the very start")
				return
			}
			ed.a.editFxAt(gr.f)
			return
		}
		ed.dropFx()
		ed.syncFxCursor()
	})
	area.AddController(esc)

	return over
}
