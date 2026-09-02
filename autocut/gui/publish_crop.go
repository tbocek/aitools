package main

// The thumbnail is the video's own shape, and the crop box says which part of
// the frame survives being made that shape.
//
// It was 1280x720 whatever the cut was. That is YouTube's size and it is the
// right answer for a widescreen video, but a session cut to 9:16 got a
// thumbnail nothing like the video it was for: the picture the model was
// handed was widescreen, the frame it drew into was widescreen, and the short
// it advertised was not. The frame follows the cut now, on the same rule
// produce uses -- parseAspect, and nothing else.
//
// Which leaves the question a portrait frame asks and a widescreen one never
// did: a 16:9 recording has three times more width than a 9:16 thumbnail can
// hold, so SOMETHING gets thrown away, and only the person who filmed it knows
// what. So the base image wears a box of the finished shape, and that box
// slides. It cannot be resized -- it is the biggest rectangle of the output's
// shape that fits in the frame (fullFill, the same rule the camera's default
// framing uses), so its size is not a choice anybody has to make. Where it
// sits is the whole question, and it is answered by dragging it.
//
// Only the base. The references are there to be named in a sentence ("the ship
// from the second image"), not to be composed, and a crop handle on each of
// them would be four controls asking about one picture.

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"math"
	"os"

	"github.com/diamondburned/gotk4/pkg/cairo"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// pubLongSide is the thumbnail's longer edge in pixels. 1280 because that is
// what YouTube asks for, and because a 16:9 cut then comes out at exactly the
// 1280x720 this page always drew.
const pubLongSide = 1280

// pubBox is the frame the thumbnail is drawn at for a cut of this aspect.
// "source" or "" -- no aspect chosen -- keeps the widescreen default: the
// footage's own shape is not known here, and 16:9 is what a recording is.
// Both sides are even, because half the image models quietly round and the
// half that do not refuse.
func pubBox(aspect string) (int, int) {
	a := parseAspect(aspect)
	if a <= 0 {
		a = 16.0 / 9
	}
	w, h := float64(pubLongSide), float64(pubLongSide)
	if a >= 1 {
		h = w / a
	} else {
		w = h * a
	}
	return int(math.Round(w/2)) * 2, int(math.Round(h/2)) * 2
}

// pubCropAt is the crop box on a source frame of aspect srcA for a thumbnail
// of aspect outA, centred on cx,cy. The size is fullFill's -- the biggest
// rectangle of the output's shape that fits -- and the centre is pulled back
// inside the frame, so a box can never be dragged off the picture and lost.
//
// hf and the width it implies are fractions of the SOURCE frame, exactly like
// a camera rectangle (cutFx), so the two speak the same units and the drawing
// code is the same arithmetic.
func pubCropAt(srcA, outA, cx, cy float64) fxRect {
	r := fullFill(srcA, outA)
	wf := pubCropW(r.hf, srcA, outA)
	// An axis with no slack needs no separate case: fullFill never returns
	// more than the whole frame, so when the box fills one the clamp's two
	// bounds meet at the middle and the drag on that axis simply does nothing.
	r.cx = math.Min(math.Max(cx, wf/2), 1-wf/2)
	r.cy = math.Min(math.Max(cy, r.hf/2), 1-r.hf/2)
	return r
}

// pubCropW is the crop box's width as a fraction of the source frame: hf frame
// heights tall and outA of those wide, said in widths.
func pubCropW(hf, srcA, outA float64) float64 {
	if srcA <= 0 || outA <= 0 {
		return 1
	}
	return math.Min(1, hf*outA/srcA)
}

// pubWholeFrame is whether a crop takes the whole picture -- the case where
// the recording and the thumbnail are already the same shape. Worth knowing:
// re-encoding a frame that is not being cropped would cost a JPEG its quality
// for nothing.
func pubWholeFrame(r fxRect, srcA, outA float64) bool {
	return r.hf >= 0.999 && pubCropW(r.hf, srcA, outA) >= 0.999
}

// imageAspect is an image file's width over height, or 0 if it cannot be read.
// DecodeConfig rather than Decode: the header carries the size and a thumbnail
// row of eight 4K frames is not worth eight full decodes.
func imageAspect(path string) float64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	c, _, err := image.DecodeConfig(f)
	if err != nil || c.Width <= 0 || c.Height <= 0 {
		return 0
	}
	return float64(c.Width) / float64(c.Height)
}

// pubCropRefImage is sdRefImage with the box applied: the base frame cut down
// to what the crop keeps, handed over as a PNG data URL. A crop that keeps the
// whole picture goes through sdRefImage untouched.
func pubCropRefImage(path string, r fxRect, srcA, outA float64) (string, error) {
	if srcA <= 0 || outA <= 0 || pubWholeFrame(r, srcA, outA) {
		return sdRefImage(path)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	src, _, err := image.Decode(f)
	if err != nil {
		return "", err
	}
	b := src.Bounds()
	wf := pubCropW(r.hf, srcA, outA)
	x0 := b.Min.X + int(math.Round((r.cx-wf/2)*float64(b.Dx())))
	y0 := b.Min.Y + int(math.Round((r.cy-r.hf/2)*float64(b.Dy())))
	x1 := x0 + int(math.Round(wf*float64(b.Dx())))
	y1 := y0 + int(math.Round(r.hf*float64(b.Dy())))
	cut := image.Rect(x0, y0, x1, y1).Intersect(b)
	if cut.Empty() {
		return "", fmt.Errorf("the crop box keeps none of %s", path)
	}
	// SubImage is what every std image type offers and none of them promise;
	// the type assertion is the check, and a decoder that does not have it
	// falls back to the whole frame rather than to an error nobody can act on.
	sub, ok := src.(interface {
		SubImage(image.Rectangle) image.Image
	})
	if !ok {
		return sdRefImage(path)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, sub.SubImage(cut)); err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

// ---- the box on the picture -------------------------------------------------

// cropOverlay is the base image with its crop box laid over it, ready to be
// dragged. Anything else -- a reference, or a base whose shape already matches
// the cut -- comes back as the plain picture: a control that cannot change
// anything is worse than no control, because it invites the question of what
// it does.
//
// The box is drawn by dimming everything OUTSIDE it rather than by outlining
// it. An outline on a photograph is a line among the lines already there; a
// darkened surround is the thumbnail, lit, and the rest of the frame explained
// as the part that goes away.
func (s *pubSlot) cropOverlay(pic *gtk.Picture) gtk.Widgetter {
	p := s.p
	outA := 0.0
	if w, h := pubBox(p.aspect); h > 0 {
		outA = float64(w) / float64(h)
	}
	// read once per rebuild, not once per frame: this opens the file
	srcA := imageAspect(s.path)
	if s.i != 0 || srcA <= 0 || outA <= 0 || pubWholeFrame(pubCropAt(srcA, outA, 0.5, 0.5), srcA, outA) {
		return pic
	}
	// through pubSettings so the "never dragged means the middle" default is
	// written down once, in the place the project file reads it from
	rect := func() fxRect { return pubSettings{Crop: p.crop}.cropRect(srcA, outA) }

	area := gtk.NewDrawingArea()
	area.SetTooltipText("The part of this frame the thumbnail keeps — the video's own shape. " +
		"Drag it over what matters; everything dimmed is cut away before the image model " +
		"is given the picture.")
	area.SetDrawFunc(func(_ *gtk.DrawingArea, cr *cairo.Context, w, h int) {
		ox, oy, dw, dh := fxDisp(float64(w), float64(h), srcA)
		if dw <= 0 || dh <= 0 {
			return
		}
		r := rect()
		wf := pubCropW(r.hf, srcA, outA)
		bx, by := ox+(r.cx-wf/2)*dw, oy+(r.cy-r.hf/2)*dh
		bw, bh := wf*dw, r.hf*dh
		// the surround, as one path with the box cut out of it: filled even-odd
		// so the hole is a hole and not a second dark rectangle over the first
		cr.SetSourceRGBA(0, 0, 0, 0.55)
		cr.SetFillRule(cairo.FillRuleEvenOdd)
		cr.Rectangle(ox, oy, dw, dh)
		cr.Rectangle(bx, by, bw, bh)
		cr.Fill()
		cr.SetFillRule(cairo.FillRuleWinding)
		// and a hairline on the box itself, because a dimmed surround alone
		// disappears over footage that is already dark
		cr.SetSourceRGBA(1, 1, 1, 0.9)
		cr.SetLineWidth(1.5)
		cr.Rectangle(bx+0.75, by+0.75, bw-1.5, bh-1.5)
		cr.Stroke()
	})

	// the drag. The centre is taken at the press and moved by the pointer's
	// travel, not set to where the pointer is: grabbing the box near its edge
	// and having it jump so its middle is under the finger is the one thing
	// that makes a box like this feel broken.
	g := gtk.NewGestureDrag()
	var cx0, cy0 float64
	g.ConnectDragBegin(func(float64, float64) { r := rect(); cx0, cy0 = r.cx, r.cy })
	g.ConnectDragUpdate(func(dx, dy float64) {
		_, _, dw, dh := fxDisp(float64(area.AllocatedWidth()), float64(area.AllocatedHeight()), srcA)
		if dw <= 0 || dh <= 0 {
			return
		}
		r := pubCropAt(srcA, outA, cx0+dx/dw, cy0+dy/dh)
		p.crop = &pubPoint{X: r.cx, Y: r.cy}
		area.QueueDraw()
	})
	area.AddController(g)

	ov := gtk.NewOverlay()
	ov.SetChild(pic)
	ov.AddOverlay(area)
	return ov
}
