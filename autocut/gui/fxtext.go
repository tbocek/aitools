package main

// Text on the picture: the layout, and the SVG the render draws it from.
//
// A text effect is words, a stretch of time, and a box to put them in. The box
// is the interesting part, and it is why this file exists apart from the other
// two halves of the effects (cut_fx.go edits them, produce_fx.go renders them).
//
// WHERE THE BOX IS. A view or a zoom points a camera at part of the SOURCE, so
// its rectangle is a fraction of the source frame. Text is the opposite: it is
// put on the FINISHED video, and it has to stay where it was put while the
// camera glides underneath it. So a text box is a fraction of the OUTPUT frame
// -- Cx, Cy, Wf, Hf against the frame the video comes out at -- and the render
// composites it after the camera chain, where the picture is already the
// output's size and shape. Wf is the one field views and zooms deliberately do
// not have: a camera window is always exactly the cut's aspect, and a text box
// is whatever shape the words want.
//
// HOW BIG THE WORDS ARE. Neither the preview nor the render may measure text:
// cairo could, librsvg will not tell us, and if they measured separately they
// would disagree and the preview would stop being a preview. So fitText below
// is the only measurement either of them makes -- a plain estimate over an
// average advance, the same one the cards use (fitFont in svgcards.go) -- and
// both sides lay the words out from the numbers it returns. The estimate errs
// small, so a title that is a size too modest still reads and one that runs out
// of its box does not happen.

import (
	"fmt"
	"math"
	"strings"
)

const (
	// textAdvance is the average width of a character as a fraction of the
	// font size. Sans-serif faces sit a little over half an em; this is the
	// number svgcards.go has always fitted chip labels with.
	textAdvance = 0.58
	// textLine is the distance between baselines, font sizes.
	textLine = 1.25
	// textAscent is how far the first baseline sits below the top of the
	// block, font sizes.
	textAscent = 0.95
	// textMinPt is as small as fitting is allowed to go. Past this the words
	// are not readable at video sizes, so the box overflows instead -- which
	// is visible, and a visible mistake can be fixed.
	textMinPt = 7.0
	// textMaxLines is the most lines a box is broken into. A box asked to hold
	// a paragraph would otherwise shrink until it was a grey smudge; this
	// stops at a size that still reads and lets the rest run out of the box.
	textMaxLines = 12
)

// fxTextDefault is the box a text effect gets when it is placed without one
// being drawn: the lower third, most of the width, which is where a caption
// goes unless somebody says otherwise.
var fxTextDefault = fxBox{cx: 0.5, cy: 0.78, wf: 0.8, hf: 0.16}

// fxBox is a rectangle on the output frame, all four numbers fractions of it.
// The text effect's own box; view and zoom use fxRect, which is a fraction of
// the SOURCE and carries no width.
type fxBox struct{ cx, cy, wf, hf float64 }

// textBox is f's box, with anything unset filled in. A text effect written by
// an older build -- or by hand -- has no box at all, and no box means the
// default one rather than a zero-sized rectangle nobody can see or grab. A
// drawing gets its own default, in the middle rather than the lower third
// (fxsvg.go): the two are put on the picture for different reasons.
func (f cutFx) textBox() fxBox {
	b := fxBox{f.Cx, f.Cy, f.Wf, f.Hf}
	if b.wf <= 0 || b.hf <= 0 {
		if f.Kind == "svg" {
			return fxSvgDefault
		}
		return fxTextDefault
	}
	return b.clamp()
}

// clamp keeps a text box on the frame: big enough to hold a word and to be
// grabbed, and never dragged so far that it cannot be found again. Both edges
// are allowed to sit exactly on the frame's, and no further.
func (b fxBox) clamp() fxBox {
	b.wf = math.Min(math.Max(b.wf, 0.04), 1)
	b.hf = math.Min(math.Max(b.hf, 0.03), 1)
	b.cx = math.Min(math.Max(b.cx, b.wf/2), 1-b.wf/2)
	b.cy = math.Min(math.Max(b.cy, b.hf/2), 1-b.hf/2)
	return b
}

// px is the box in the pixels of a w×h frame: corner, width and height.
func (b fxBox) px(w, h float64) (x, y, bw, bh float64) {
	bw, bh = b.wf*w, b.hf*h
	return b.cx*w - bw/2, b.cy*h - bh/2, bw, bh
}

// textLines is the words as the layout will break them: explicit newlines
// always break, and anything longer than the width is wrapped between words.
// A single word too long for the box is split rather than left to run out of
// it -- one over-long word is a URL or a hashtag, not a mistake to preserve.
func textLines(text string, width, size float64) []string {
	adv := math.Max(size*textAdvance, 1e-6)
	max := int(width / adv) // characters that fit on a line
	if max < 1 {
		max = 1
	}
	var out []string
	for _, para := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		words := strings.Fields(para)
		if len(words) == 0 {
			out = append(out, "") // a blank line was typed on purpose
			continue
		}
		line := ""
		for _, w := range words {
			for len([]rune(w)) > max { // an over-long word, split at the edge
				if line != "" {
					out = append(out, line)
					line = ""
				}
				r := []rune(w)
				out = append(out, string(r[:max]))
				w = string(r[max:])
			}
			switch {
			case line == "":
				line = w
			case len([]rune(line))+1+len([]rune(w)) <= max:
				line += " " + w
			default:
				out = append(out, line)
				line = w
			}
		}
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// fitText is the whole of the layout: the largest font size at which the words
// fit the box, and the lines they break into at that size. Sizes are in the
// same units as the box, so the caller decides whether that is output pixels
// (the render) or widget pixels (the preview) and gets a consistent answer
// either way.
//
// Binary search rather than stepping down, because the answer has to be the
// same on both sides to the last decimal: the preview and the render must not
// disagree about where line two starts.
func fitText(text string, boxW, boxH float64) (size float64, lines []string) {
	if strings.TrimSpace(text) == "" || boxW <= 0 || boxH <= 0 {
		return 0, nil
	}
	fits := func(s float64) ([]string, bool) {
		ls := textLines(text, boxW, s)
		if len(ls) > textMaxLines {
			return ls, false
		}
		return ls, float64(len(ls))*s*textLine <= boxH
	}
	// one line as tall as the box is the ceiling; nothing bigger can fit
	hi := boxH / textLine
	if ls, ok := fits(hi); ok {
		return hi, ls
	}
	lo := textMinPt
	if hi <= lo {
		ls, _ := fits(lo)
		return lo, ls
	}
	for i := 0; i < 40; i++ {
		mid := (lo + hi) / 2
		if _, ok := fits(mid); ok {
			lo = mid
		} else {
			hi = mid
		}
	}
	lines, _ = fits(lo)
	return lo, lines
}

// textBaselines is where each line's baseline sits, given the box in pixels
// and the size fitText chose. The block is centred in the box vertically; each
// line is centred horizontally, so the x is the box's middle for all of them.
func textBaselines(y, boxH, size float64, n int) []float64 {
	if n <= 0 {
		return nil
	}
	block := float64(n) * size * textLine
	top := y + math.Max(0, (boxH-block)/2)
	out := make([]float64, n)
	for i := range out {
		out[i] = top + float64(i)*size*textLine + size*textAscent
	}
	return out
}

// ---- the SVG the render draws ------------------------------------------------

// textSVG is one text effect as a transparent document exactly the size of the
// output frame, for the render to composite over the picture (see textChain).
// The whole frame rather than just the box, so the overlay is a plain 0,0
// composite and there is no second place for the geometry to be got wrong.
//
// White words with a dark outline around them, because footage is any colour
// and text with no outline disappears into half of it. The outline is a second
// copy of every line drawn underneath in stroke only, which is the one way of
// doing it that needs nothing from the renderer beyond stroke and fill.
func textSVG(f cutFx, outW, outH int) []byte {
	w, h := float64(outW), float64(outH)
	x, y, bw, bh := f.textBox().px(w, h)
	size, lines := fitText(f.Text, bw, bh)
	var b strings.Builder
	fmt.Fprintf(&b, `<?xml version="1.0" encoding="UTF-8"?>`+"\n"+
		`<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" `+
		`viewBox="0 0 %d %d">`+"\n", outW, outH, outW, outH)
	if size > 0 {
		mid := x + bw/2
		base := textBaselines(y, bh, size, len(lines))
		fmt.Fprintf(&b, `<g font-family="sans-serif" font-weight="bold" font-size="%s" `+
			`text-anchor="middle">`+"\n", trimNum(size))
		// the outline first, the fill over it: one pass each, so a descender
		// of one line is never drawn over the outline of the next
		for _, pass := range [2]string{
			fmt.Sprintf(`fill="none" stroke="#000" stroke-width="%s" stroke-linejoin="round" opacity="0.85"`,
				trimNum(size*0.16)),
			`fill="#ffffff"`,
		} {
			for i, ln := range lines {
				if strings.TrimSpace(ln) == "" {
					continue
				}
				// attrEscaper, not svgEscape: svgEscape is the percent
				// escaping an insert's "?" parameters need, and the words
				// here are XML content -- an unescaped & or < is a document
				// librsvg refuses, which would lose the whole overlay
				fmt.Fprintf(&b, `<text x="%s" y="%s" %s>%s</text>`+"\n",
					trimNum(mid), trimNum(base[i]), pass, attrEscaper.Replace(ln))
			}
		}
		b.WriteString("</g>\n")
	}
	b.WriteString("</svg>\n")
	return []byte(b.String())
}

// ---- when a text is on screen ------------------------------------------------

// textCue is one text effect inside one clip, in that clip's own output
// seconds: when it comes up, when it goes, and how long each fade takes.
type textCue struct {
	fx        cutFx
	s, e      float64
	fin, fout float64
	// idx is the effect's place in the cut's list, so the file written for it
	// can be named after something stable across a re-render.
	idx int
}

// overFx is whether an effect is one of the two laid OVER the running picture
// rather than done to it: words (text) or a drawing (svg, fxsvg.go). Both sit
// in a box on the finished frame, both fade on and off inside their bar, and
// both are composited after the camera -- so the cues, the filter graph and
// the preview treat them as one thing, and only the last step, what is
// actually drawn, tells them apart.
//
// An empty one is not an effect: a title with no words and a drawing with no
// file have nothing to put on the picture, and carrying them any further only
// makes an input ffmpeg cannot open.
func overFx(f cutFx) bool {
	switch f.Kind {
	case "text":
		return strings.TrimSpace(f.Text) != ""
	case "svg":
		return strings.TrimSpace(f.Src) != ""
	}
	return false
}

// textsOf is the overlay effects of a cut, earliest first, skipping the ones
// that would show nothing: nothing to draw, or no time to draw it in.
func textsOf(fx []cutFx) []cutFx {
	var out []cutFx
	for _, f := range fx {
		if overFx(f) && f.Dur > 0 {
			out = append(out, f)
		}
	}
	return out
}

// textsAt is the places in fx of the text effects visible at session time t,
// in the order they were placed -- so the last one is the one drawn on top and
// the first one a press should find. Indices rather than copies because the
// preview both draws these and lets them be dragged, and a drag needs the
// effect itself. The render walks textCues instead: it needs the clip's own
// clock rather than the session's.
func textsAt(fx []cutFx, t float64) []int {
	var out []int
	for i, f := range fx {
		if !overFx(f) || f.Dur <= 0 {
			continue
		}
		if t >= f.T && t < f.T+f.Dur {
			out = append(out, i)
		}
	}
	return out
}

// textAlpha is how opaque a text is at session time t: the fades, evaluated
// the same way the render's fade filters will. The preview draws with it so a
// half-faded title looks half-faded.
func textAlpha(f cutFx, t float64) float64 {
	if t < f.T || t >= f.T+f.Dur {
		return 0
	}
	fin, fout := f.textFades()
	a := 1.0
	if fin > 0 && t < f.T+fin {
		a = (t - f.T) / fin
	}
	if fout > 0 && t > f.T+f.Dur-fout {
		a = math.Min(a, (f.T+f.Dur-t)/fout)
	}
	return math.Min(1, math.Max(0, a))
}

// textFades is the fades a text actually gets: each at least 0, together no
// longer than the text is on screen, the way in winning any overlap. Exactly
// the bargain zoomGlides strikes for a zoom's two glides.
func (f cutFx) textFades() (fin, fout float64) {
	fin = math.Min(math.Max(f.Trans, 0), f.Dur)
	fout = math.Min(math.Max(f.Tout, 0), f.Dur-fin)
	return
}

// textCues maps the cut's text effects onto one clip. sessS is the session
// time the clip starts at, span the session seconds it covers (0 for a freeze
// or a spliced card, which are one session moment held for a while), rate its
// playback rate and length its output seconds -- the same five numbers
// buildCam is given, and mapped the same way, so a text and a camera move
// together under a speed effect instead of drifting apart.
func textCues(fx []cutFx, sessS, span, rate, length float64) []textCue {
	if length <= 0 {
		return nil
	}
	var out []textCue
	for i, f := range fx {
		if !overFx(f) || f.Dur <= 0 {
			continue
		}
		fin, fout := f.textFades()
		var s, e float64
		if span <= 0 {
			// one session moment stretched over the whole clip: either the
			// text covers that moment or it has nothing to appear over
			if sessS < f.T || sessS >= f.T+f.Dur {
				continue
			}
			s, e = 0, length
			fin, fout = 0, 0 // there is no time inside a held frame to fade over
		} else {
			var ok bool
			s, e, fin, fout, ok = cueClip(f.T, f.Dur, fin, fout, sessS, rate, length)
			if !ok {
				continue
			}
		}
		// the two fades cannot together outlast the appearance
		if fin+fout > e-s {
			fin = math.Min(fin, e-s)
			fout = math.Max(0, e-s-fin)
		}
		out = append(out, textCue{fx: f, s: s, e: e, fin: fin, fout: fout, idx: i})
	}
	return out
}

// cueClip maps a session stretch [T, T+dur] with its fades onto one clip that
// starts at sessS, plays at rate and runs length output seconds: the piece
// inside the clip, in the clip's own seconds. An overlay that starts before
// the clip is already fully up when the clip begins, so the fade it keeps is
// the part still owing -- and the same at the far end. ok is false when
// nothing of it worth a frame lands in the clip.
func cueClip(T, dur, fin, fout, sessS, rate, length float64) (s, e, cin, cout float64, ok bool) {
	if rate <= 0 {
		rate = 1
	}
	s = (T - sessS) / rate
	e = (T + dur - sessS) / rate
	if e <= 0 || s >= length {
		return 0, 0, 0, 0, false
	}
	if s < 0 {
		cin = math.Max(0, fin/rate+s)
		s = 0
	} else {
		cin = fin / rate
	}
	if e > length {
		cout = math.Max(0, fout/rate-(e-length))
		e = length
	} else {
		cout = fout / rate
	}
	if e-s < 0.04 { // shorter than a frame: nothing to see
		return 0, 0, 0, 0, false
	}
	return s, e, cin, cout, true
}

// freezeCues maps the cut's stop effects onto one clip, on exactly the terms
// textCues maps its titles -- the same five numbers, the same arithmetic --
// so a still and a title placed at the same second come and go together. A
// held clip (span 0: a spliced card, an audio insert's held frame) gets none;
// there is no footage running under it for a still to stand over.
//
// A stop stands over the seconds frozenSpans gives it rather than over its
// whole bar, which are the same seconds except where another speed effect
// crosses it and dilutes its ×0 (cut_speedmix.go). The fades belong to the
// bar's own ends: an edge made by a crossing effect is a hard one, because the
// picture there does not fade into motion, it simply starts moving.
func freezeCues(fx []cutFx, sessS, span, rate, length float64) []textCue {
	if length <= 0 || span <= 0 {
		return nil
	}
	var out []textCue
	for i, f := range fx {
		fin, fout := f.textFades()
		for _, fs := range frozenSpans(fx, f) {
			in, off := 0.0, 0.0
			if math.Abs(fs[0]-f.T) < 1e-9 {
				in = fin
			}
			if math.Abs(fs[1]-(f.T+f.Dur)) < 1e-9 {
				off = fout
			}
			s, e, cin, cout, ok := cueClip(fs[0], fs[1]-fs[0], in, off, sessS, rate, length)
			if !ok {
				continue
			}
			if cin+cout > e-s {
				cin = math.Min(cin, e-s)
				cout = math.Max(0, e-s-cin)
			}
			out = append(out, textCue{fx: f, s: s, e: e, fin: cin, fout: cout, idx: i})
		}
	}
	return out
}

// textChain is the filter graph that puts the cues on the picture: each one
// its own input, faded on its own alpha and composited over whatever came
// before, switched on for exactly its seconds. in is the label the picture
// arrives on, base the input index of the first overlay file, boxW and boxH
// the finished frame's size, and the label the picture leaves on comes back.
//
// The two kinds of overlay differ in one line each. A title's file is drawn
// the whole size of the frame already (textSVG), so it goes on at the origin
// and the box is inside the drawing. A drawing is the user's own file at
// whatever size it happens to be, so it is fitted into its box here -- shrunk
// to sit inside, its own shape kept, centred on whichever axis the fit left
// short (w and h in the overlay expression are the overlay's own size, so the
// centring is done with the numbers ffmpeg ends up with rather than the ones
// we predicted).
//
// Commas are backslash-escaped inside enable=, because the filtergraph parser
// splits filters on bare commas -- the same escaping pieceExpr does for
// zoompan's expressions.
func textChain(cues []textCue, in string, base, boxW, boxH int) (string, string) {
	var b strings.Builder
	cur := in
	for k, c := range cues {
		b.WriteString(fmt.Sprintf("[%d:v]format=rgba", base+k))
		pos := "x=0:y=0"
		if c.fx.Kind == "svg" {
			if x, y, bw, bh, ok := svgFitPx(c.fx, boxW, boxH); ok {
				b.WriteString(fmt.Sprintf(",scale=%d:%d:force_original_aspect_ratio=decrease", bw, bh))
				pos = fmt.Sprintf("x=%d+(%d-w)/2:y=%d+(%d-h)/2", x, bw, y, bh)
			} else {
				pos = "x=(W-w)/2:y=(H-h)/2" // no frame size to fit against
			}
		}
		if c.fin > 0 {
			b.WriteString(fmt.Sprintf(",fade=t=in:st=%.3f:d=%.3f:alpha=1", c.s, c.fin))
		}
		if c.fout > 0 {
			b.WriteString(fmt.Sprintf(",fade=t=out:st=%.3f:d=%.3f:alpha=1", c.e-c.fout, c.fout))
		}
		b.WriteString(fmt.Sprintf("[tx%d];", k))
		out := fmt.Sprintf("vtx%d", k)
		b.WriteString(fmt.Sprintf("[%s][tx%d]overlay=%s:eof_action=pass:"+
			"enable=between(t\\,%.3f\\,%.3f)[%s];", cur, k, pos, c.s, c.e, out))
		cur = out
	}
	return b.String(), cur
}
