package main

// The little marks on the timeline: which kind of effect a band is, which
// track a card sits on, whether a stop takes its seconds of sound with it.
//
// They are DRAWN, not written, and that is the whole point of this file.
// Everything on the timeline is painted with cairo's toy text API -- one
// SelectFontFace, one ShowText -- which picks a single font face and does no
// fallback at all. A glyph that face happens not to have comes out as a hollow
// box, and no font on a stock Linux desktop has all of ⏸ ⧉ ⊕ ▭ ❝ ♪. So the
// lane, which is the one place the kind of an effect is meant to be readable
// without reading anything, showed a row of boxes.
//
// A path has no such problem. Every mark below is a handful of lines and arcs
// in a small square, drawn in whatever colour the caller has already set.

import (
	"fmt"
	"math"

	"github.com/diamondburned/gotk4/pkg/cairo"
)

// fxMarkW is the square a mark is drawn in. Nine pixels beside a ten pixel
// font: the mark reads as a sibling of the words rather than as a picture
// stuck in front of them.
const fxMarkW = 9.0

// markPlate is plateText with a mark in front of the words -- the dark plate
// covering both, so a band's label stays legible over whatever the band's own
// fill is doing under it.
func markPlate(cr *cairo.Context, x, y float64, kind, s string) {
	e := cr.TextExtents(s)
	cr.SetSourceRGBA(0, 0, 0, 0.66)
	cr.Rectangle(x-3, y-11, fxMarkW+4+e.Width+6, 14)
	cr.Fill()
	cr.SetSourceRGB(1, 1, 1)
	drawMark(cr, kind, x, y-10)
	cr.MoveTo(x+fxMarkW+4, y)
	cr.ShowText(s)
}

// drawMark paints one mark in the fxMarkW square whose top left corner is
// (x, y), in the colour and on the path state the caller has set up. An
// unknown kind draws nothing rather than a placeholder: a mark nobody
// recognises is worse than a label with no mark.
func drawMark(cr *cairo.Context, kind string, x, y float64) {
	m := fxMarkW
	cx, cy := x+m/2, y+m/2
	cr.SetLineWidth(1.2)
	switch kind {
	case "zoom":
		// a circle closing on a point: the camera moving in and back out
		cr.NewSubPath()
		cr.Arc(cx, cy, m/2-0.7, 0, 2*math.Pi)
		cr.Stroke()
		cr.MoveTo(cx-m/4, cy)
		cr.LineTo(cx+m/4, cy)
		cr.MoveTo(cx, cy-m/4)
		cr.LineTo(cx, cy+m/4)
		cr.Stroke()
	case "stay":
		// a frame that is put somewhere and left there
		cr.Rectangle(x+0.7, y+1.7, m-1.4, m-3.4)
		cr.Stroke()
	case "stop":
		// the two bars every pause button in the world wears
		cr.Rectangle(x+1.2, y+1, 2.3, m-2)
		cr.Rectangle(x+m-3.5, y+1, 2.3, m-2)
		cr.Fill()
	case "hush":
		// a stop that takes its sound with it: the same two bars, struck out
		drawMark(cr, "stop", x, y)
		cr.SetLineWidth(1.2)
		cr.MoveTo(x-0.4, y+m+0.4)
		cr.LineTo(x+m+0.4, y-0.4)
		cr.Stroke()
	case "speed":
		// two chevrons, the way every fast-forward points
		for _, dx := range []float64{0.6, 4.2} {
			cr.MoveTo(x+dx, y+1.4)
			cr.LineTo(x+dx+3, cy)
			cr.LineTo(x+dx, y+m-1.4)
		}
		cr.Stroke()
	case "text":
		// a T, which is what a title is
		cr.MoveTo(x+0.8, y+1.7)
		cr.LineTo(x+m-0.8, y+1.7)
		cr.MoveTo(cx, y+1.7)
		cr.LineTo(cx, y+m-1)
		cr.Stroke()
	case "svg":
		// a picture: a frame with a hill in it
		cr.Rectangle(x+0.7, y+1.2, m-1.4, m-2.4)
		cr.Stroke()
		cr.MoveTo(x+1.6, y+m-2)
		cr.LineTo(cx, cy-0.4)
		cr.LineTo(x+m-1.6, y+m-2)
		cr.ClosePath()
		cr.Fill()
	case "card":
		// two sheets, one laid over the other: something spliced in
		cr.Rectangle(x+0.7, y+2.6, m-3.3, m-3.9)
		cr.Stroke()
		cr.Rectangle(x+2.6, y+0.7, m-3.3, m-3.9)
		cr.Stroke()
	case "vol":
		// a speaker: a cone opening to the right, with a wave off it
		cr.MoveTo(x+0.8, y+2.4)
		cr.LineTo(x+0.8, y+m-2.4)
		cr.LineTo(x+2.6, y+m-2.4)
		cr.LineTo(x+4.6, y+m-0.8)
		cr.LineTo(x+4.6, y+0.8)
		cr.LineTo(x+2.6, y+2.4)
		cr.ClosePath()
		cr.Fill()
		cr.NewSubPath()
		cr.Arc(x+4.6, cy, 2.2, -1.0, 1.0)
		cr.Stroke()
	case "sound":
		// a note
		cr.NewSubPath()
		cr.Arc(x+2.4, y+m-2.2, 1.7, 0, 2*math.Pi)
		cr.Fill()
		cr.MoveTo(x+4.1, y+m-2.2)
		cr.LineTo(x+4.1, y+0.9)
		cr.LineTo(x+m-0.7, y+2.1)
		cr.Stroke()
	}
}

// laneLabel is what one band on the effect lane says: a mark naming the kind
// and a few words of detail. It is a function rather than three inline
// branches so the rule below can be checked -- the words go through cairo's
// toy text API, which has exactly one font face and no fallback, so a label
// may only use characters a font face is certain to have. Anything a mark can
// say instead, a mark says.
//
// chars is the room the words have, in characters; only a title uses it.
func laneLabel(f cutFx, chars int) (mark, label string) {
	switch f.Kind {
	case "zoom":
		if f.Stay {
			return "stay", fmt.Sprintf("%.1fs", f.Dur)
		}
		return "zoom", fmt.Sprintf("%.1fs", f.Dur)
	case "speed":
		if f.frozenFx() {
			if f.Mute {
				return "hush", fmt.Sprintf("%.1fs", f.Dur) // silent seconds
			}
			return "stop", fmt.Sprintf("%.1fs", f.Dur)
		}
		return "speed", "×" + fxNum(f.Rate)
	case "text":
		return "text", laneWords(f.Text, chars)
	case "svg":
		return "svg", laneWords(svgName(f), chars)
	case "volume":
		// the percent, which is the whole of what this one does. No "%" in the
		// words: the mark beside them is a speaker, so the number can only be
		// a loudness, and every character costs room on a narrow band
		return "vol", gainPct(f.Gain)
	}
	return "", ""
}
