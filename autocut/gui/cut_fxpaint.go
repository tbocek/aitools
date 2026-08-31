package main

// Painting the finished frame on a widget that is showing the footage.
//
// Two pages do this. Cut's preview does it inside its editing overlay
// (cut_fxview.go), where the same rectangles also have to answer where a press
// landed and what a drag is resizing. Narrate's preview does it with nothing
// else on it (narrate_fxview.go): that page judges a narration line against the
// moment it is spoken over, and until this existed it judged it against raw
// uncropped footage with no titles on it -- a line written for a close-up,
// checked against the wide shot it was cropped out of.
//
// What lives here is the part that must not drift between the two, because it
// is a claim about what the RENDER will make: where the output frame sits in
// the widget, how the camera maps the picture into it, what is painted over,
// and where an overlay's box lands on the finished frame. What stays in
// cut_fxview.go is everything only an editor has -- outlines, labels, grabs.

import (
	"math"
	"strings"

	"github.com/diamondburned/gotk4/pkg/cairo"
)

// fxLiveFit is the mapping the finished frame is on at session time t: the
// camera rectangle there, put through liveZoom. ok is false for the cases a
// gsk transform cannot be built from -- a widget with no size yet, an unknown
// source size, a scale that came back NaN -- so the callers stop asking each
// in their own way and stop disagreeing about which ones matter.
func fxLiveFit(W, H, sw, sh, outA float64, fx []cutFx, t float64) (s, tx, ty float64, ok bool) {
	if W <= 0 || H <= 0 || sw <= 0 || sh <= 0 {
		return 0, 0, 0, false
	}
	s, tx, ty = liveZoom(W, H, sw, sh, outA, fxRectAt(fx, t, sw/sh, outA))
	if s <= 0 || math.IsNaN(s) || math.IsInf(s, 0) {
		return 0, 0, 0, false
	}
	return s, tx, ty, true
}

// fxMaskLive paints black over everything the finished video will not have: the
// widget outside the output box, and any part of that box the blown-up frame
// does not reach -- which is what a pull-back past the frame's edge leaves, and
// what the render fills with its blurred backdrop.
//
// The frame underneath is drawn at its own pixel size and transformed (s,tx,ty
// from fxLiveFit), so the lit rectangle is the intersection of the output box
// with where that transform put the picture.
func fxMaskLive(cr *cairo.Context, w, h, sw, sh, outA, s, tx, ty float64) {
	bx, by, bw, bh := fxDisp(w, h, outA)
	x0, y0 := math.Max(bx, tx), math.Max(by, ty)
	x1, y1 := math.Min(bx+bw, tx+sw*s), math.Min(by+bh, ty+sh*s)
	cr.SetSourceRGB(0, 0, 0)
	cr.Rectangle(0, 0, w, h)
	if x1 > x0 && y1 > y0 {
		cr.NewSubPath()
		cr.Rectangle(x0, y0, x1-x0, y1-y0)
	}
	cr.SetFillRule(cairo.FillRuleEvenOdd)
	cr.Fill()
	cr.SetFillRule(cairo.FillRuleWinding)
}

// fxOverPx is where an overlay's box lands, given the finished frame's own
// rectangle in the widget. An overlay is a fraction of the OUTPUT frame and not
// of the picture, which is what lets a title stay put while the camera moves
// under it.
func fxOverPx(f cutFx, ox, oy, ow, oh float64) (x, y, w, h float64) {
	bx, by, bw, bh := f.textBox().px(ow, oh)
	return ox + bx, oy + by, bw, bh
}

// drawFxText paints one text effect into the box it was given, the way the
// render will put it: the same font size and the same line breaks (fitText
// answers for both), each line centred on its own measured width -- which is
// what SVG's text-anchor does too. The dark edge is eight offset copies rather
// than the SVG's stroke, because cairo's Go binding has no text path to
// stroke; it is the same idea at preview resolution.
func drawFxText(cr *cairo.Context, f cutFx, alpha, tx, ty, tw, th float64) {
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

// drawFxOver is either kind of overlay put on the finished frame the way the
// render will put it: the words drawn, or the drawing painted into its box. On
// the editor because the SVG cache is (svgSurface) -- one parse per drawing,
// shared by both previews.
func (ed *cutEditor) drawFxOver(cr *cairo.Context, f cutFx, alpha, ox, oy, ow, oh float64) {
	x, y, w, h := fxOverPx(f, ox, oy, ow, oh)
	if f.Kind == "svg" {
		ed.drawSVG(cr, f, x, y, w, h, alpha)
		return
	}
	drawFxText(cr, f, alpha, x, y, w, h)
}

// drawFxOverlaysAt is every overlay owed to session time t, faded exactly as
// the render fades it. The titles are the half of "show me the effects" that
// has nothing to do with the camera, and a preview without them is a preview of
// a video that has not been finished.
func (ed *cutEditor) drawFxOverlaysAt(cr *cairo.Context, fx []cutFx, t, ox, oy, ow, oh float64) {
	for _, i := range textsAt(fx, t) {
		ed.drawFxOver(cr, fx[i], textAlpha(fx[i], t), ox, oy, ow, oh)
	}
}
