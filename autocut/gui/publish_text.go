package main

// Words printed on the thumbnail.
//
// The image model is not asked to letter anything any more. It letters well
// enough, and that was the problem: the title was part of the instruction, so
// changing four words meant a GPU redraw of the whole picture, the model
// sometimes lettered them twice (see the history in publish_test.go), and
// there was no way to put a second line anywhere. The words are printed on
// AFTER the draw now, locally, the way the render prints a Text effect on the
// finished video: a box, the words fitted to it (fitText), white with a dark
// edge (drawFxText). Same layout code, so the thumbnail's words look like the
// video's.
//
// Two kinds of words land this way. The TITLE goes across the upper part of
// the picture in a fixed band (pubTitleBox) -- it is the YouTube title, typed
// in its own entry, and the model that draws the picture is told to keep that
// part of the frame calm (editInstruction). And any number of MARKED texts:
// drag a box on the result, type the words, and they are printed to fill it.
// Each box wears a ✎; pressing it reopens the words.
//
// thumbnail-plain.png is the picture as the model drew it, no words;
// thumbnail.png is the same picture with the words printed on. Rewording
// either kind re-prints from the plain copy (recomposite) -- no model, no
// GPU, no run.

import (
	"math"
	"os"
	"strings"

	"github.com/diamondburned/gotk4/pkg/cairo"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// pubText is one block of marked words: a box in fractions of the finished
// thumbnail, and the text fitted into it. The same four numbers a text
// effect's box is (fxBox), stored with names because this goes in the project
// file.
type pubText struct {
	Cx   float64 `json:"cx"`
	Cy   float64 `json:"cy"`
	Wf   float64 `json:"wf"`
	Hf   float64 `json:"hf"`
	Text string  `json:"text"`
}

// box is the pubText as the drawing code speaks it, kept on the frame.
func (t pubText) box() fxBox { return fxBox{cx: t.Cx, cy: t.Cy, wf: t.Wf, hf: t.Hf}.clamp() }

// pubTitleBox is where the title goes: a band across the upper part of the
// picture. Fixed rather than draggable because the title is not a marked text
// -- it is typed in its own entry, and the image model is told to keep this
// part of the frame calm for it, which only works if "this part" never moves.
var pubTitleBox = fxBox{cx: 0.5, cy: 0.14, wf: 0.94, hf: 0.18}

// drawPubTexts prints the words onto the plain picture and writes the result:
// every marked text in its box, then the title across the top, drawn exactly
// as the video's Text effects are (fitText + drawFxText, so the preview, the
// render and the thumbnail cannot disagree about how words look). With
// nothing to print the plain bytes are copied through untouched -- a decode
// and re-encode of a picture nothing was drawn on is a lossless way to make
// the two files differ.
func drawPubTexts(plain, out string, texts []pubText, title string) error {
	title = strings.TrimSpace(title)
	var on []pubText
	for _, t := range texts {
		if strings.TrimSpace(t.Text) != "" {
			on = append(on, t)
		}
	}
	if len(on) == 0 && title == "" {
		b, err := os.ReadFile(plain)
		if err != nil {
			return err
		}
		return os.WriteFile(out, b, 0o644)
	}
	surf, err := cairo.CreatePNGSurfaceFromPNG(plain)
	if err != nil {
		return err
	}
	cr := cairo.Create(surf)
	w, h := float64(surf.Width()), float64(surf.Height())
	for _, t := range on {
		x, y, bw, bh := t.box().px(w, h)
		drawFxText(cr, cutFx{Text: t.Text}, 1, x, y, bw, bh)
	}
	if title != "" {
		x, y, bw, bh := pubTitleBox.px(w, h)
		drawFxText(cr, cutFx{Text: title}, 1, x, y, bw, bh)
	}
	return surf.WriteToPNG(out)
}

// recomposite re-prints the words onto the last drawn picture and shows the
// result. GTK thread; cheap enough to run on a saved edit (one PNG decode and
// encode). Not during a run -- the runner is writing these very files, and it
// prints the words itself from its own snapshot when it gets there.
func (p *publisher) recomposite() {
	if p.a.running {
		return
	}
	dir := p.a.publishDir()
	plain := dir + "/thumbnail-plain.png"
	if !exists(plain) {
		return // nothing drawn yet; the run prints the words when it draws
	}
	title := strings.TrimSpace(p.title.Text())
	if err := drawPubTexts(plain, dir+"/thumbnail.png", p.texts, title); err != nil {
		p.a.logf("thumbnail words: %v", err)
		return
	}
	p.showShot()
}

// setTexts replaces the marked texts and re-prints. Every path that changes
// them lands here, the way setFrames owns the image row: the overlay's boxes,
// the picture and the project must not disagree about what is printed.
func (p *publisher) setTexts(ts []pubText) {
	p.texts = ts
	if p.shotOver != nil {
		p.shotOver.QueueDraw()
	}
	p.recomposite()
}

// pubIconPx is the ✎ chip's edge in widget pixels: big enough to press,
// small enough not to cover the words it edits.
const pubIconPx = 22.0

// textOverlay is the result picture with the marking layer on it: the boxes
// the words were given, a ✎ on each, and the drag that marks a new one.
// The words themselves are not drawn here -- they are IN thumbnail.png
// (drawPubTexts), and painting them twice would double the edge.
func (p *publisher) textOverlay(pic *gtk.Picture) gtk.Widgetter {
	area := gtk.NewDrawingArea()
	p.shotOver = area
	area.SetTooltipText("Drag a box over the picture to put words on it — they are printed " +
		"to fill the box, like a Text effect in Cut, and the box can be marked as often " +
		"as you like. Press a box's ✎ to reword or remove it. The title is printed " +
		"across the top on its own.")

	// the rubber band, while a new box is being dragged out: widget pixels,
	// shared by the drag and the draw
	var band *[4]float64

	// disp is where the shown picture sits in the widget, or ok=false when
	// there is nothing to mark on.
	disp := func(w, h float64) (ox, oy, dw, dh float64, ok bool) {
		if p.shotPath == "" || p.shotA <= 0 || w <= 0 || h <= 0 {
			return 0, 0, 0, 0, false
		}
		ox, oy, dw, dh = fxDisp(w, h, p.shotA)
		return ox, oy, dw, dh, dw > 0 && dh > 0
	}
	// iconPx is text i's ✎ chip, in widget pixels: the box's top-left corner,
	// held inside it.
	iconPx := func(i int, ox, oy, dw, dh float64) (x, y float64) {
		bx, by, _, _ := p.texts[i].box().px(dw, dh)
		return ox + bx + 2, oy + by + 2
	}

	area.SetDrawFunc(func(_ *gtk.DrawingArea, cr *cairo.Context, w, h int) {
		ox, oy, dw, dh, ok := disp(float64(w), float64(h))
		if !ok {
			return
		}
		for i, t := range p.texts {
			bx, by, bw, bh := t.box().px(dw, dh)
			// a hairline, same weight as the crop box's, so a box whose words
			// blend into the picture can still be found and re-worded
			cr.SetSourceRGBA(1, 1, 1, 0.55)
			cr.SetLineWidth(1)
			cr.Rectangle(ox+bx+0.5, oy+by+0.5, bw-1, bh-1)
			cr.Stroke()
			ix, iy := iconPx(i, ox, oy, dw, dh)
			cr.SetSourceRGBA(0, 0, 0, 0.7)
			cr.Rectangle(ix, iy, pubIconPx, pubIconPx)
			cr.Fill()
			cr.SetSourceRGBA(1, 1, 1, 0.95)
			cr.SelectFontFace("sans-serif", cairo.FontSlantNormal, cairo.FontWeightNormal)
			cr.SetFontSize(pubIconPx * 0.65)
			e := cr.TextExtents("✎")
			cr.MoveTo(ix+(pubIconPx-e.Width)/2-e.XBearing, iy+(pubIconPx-e.Height)/2-e.YBearing)
			cr.ShowText("✎")
		}
		if band != nil {
			cr.SetSourceRGBA(1, 1, 1, 0.9)
			cr.SetLineWidth(1.5)
			cr.Rectangle(band[0], band[1], band[2], band[3])
			cr.Stroke()
		}
	})

	g := gtk.NewGestureDrag()
	var x0, y0 float64
	g.ConnectDragBegin(func(x, y float64) { x0, y0 = x, y })
	g.ConnectDragUpdate(func(dx, dy float64) {
		band = &[4]float64{math.Min(x0, x0+dx), math.Min(y0, y0+dy), math.Abs(dx), math.Abs(dy)}
		area.QueueDraw()
	})
	g.ConnectDragEnd(func(dx, dy float64) {
		band = nil
		area.QueueDraw()
		ox, oy, dw, dh, ok := disp(float64(area.AllocatedWidth()), float64(area.AllocatedHeight()))
		if !ok {
			return
		}
		// a press that never travelled is a press, and the only thing here to
		// press is a ✎
		if math.Abs(dx) < 8 && math.Abs(dy) < 8 {
			for i := range p.texts {
				ix, iy := iconPx(i, ox, oy, dw, dh)
				if x0 >= ix && x0 <= ix+pubIconPx && y0 >= iy && y0 <= iy+pubIconPx {
					p.editText(i)
					return
				}
			}
			return
		}
		b := fxBox{
			cx: (x0 + dx/2 - ox) / dw,
			cy: (y0 + dy/2 - oy) / dh,
			wf: math.Abs(dx) / dw,
			hf: math.Abs(dy) / dh,
		}.clamp()
		p.a.askPubText("", func(s string) {
			if strings.TrimSpace(s) == "" {
				return // a box with no words would print nothing and still wear a ✎
			}
			p.setTexts(append(append([]pubText(nil), p.texts...),
				pubText{Cx: b.cx, Cy: b.cy, Wf: b.wf, Hf: b.hf, Text: s}))
		}, nil)
	})
	area.AddController(g)

	ov := gtk.NewOverlay()
	ov.SetChild(pic)
	ov.AddOverlay(area)
	return ov
}

// editText reopens text i's words. Saving empty removes it -- the words are
// the whole effect, so no words and Remove mean the same thing.
func (p *publisher) editText(i int) {
	if i < 0 || i >= len(p.texts) {
		return
	}
	rm := func() {
		p.setTexts(append(append([]pubText(nil), p.texts[:i]...), p.texts[i+1:]...))
	}
	p.a.askPubText(p.texts[i].Text, func(s string) {
		if strings.TrimSpace(s) == "" {
			rm()
			return
		}
		ts := append([]pubText(nil), p.texts...)
		ts[i].Text = s
		p.setTexts(ts)
	}, rm)
}

// askPubText is the words dialog: a multi-line box, because newlines are
// content here exactly as they are in a Text effect (they break a line, and
// nothing else does). rm is nil for a new box -- there is nothing to remove
// until the words exist.
func (a *App) askPubText(initial string, ok func(string), rm func()) {
	win := gtk.NewWindow()
	win.SetTransientFor(&a.win.Window)
	win.SetModal(true)
	win.SetTitle("Words on the thumbnail")
	win.SetDefaultSize(380, -1)

	d := gtk.NewLabel("Printed to fill the box you marked — a longer line comes out " +
		"smaller, and Enter starts a new line.")
	d.SetXAlign(0)
	d.SetWrap(true)
	d.AddCSSClass("dim-label")

	tv := gtk.NewTextView()
	tv.SetWrapMode(gtk.WrapWordChar)
	tv.SetAcceptsTab(false) // Tab moves to the next field, as everywhere else
	tv.Buffer().SetText(initial)
	sc := gtk.NewScrolledWindow()
	sc.SetChild(tv)
	sc.SetSizeRequest(-1, 90)
	sc.SetPolicy(gtk.PolicyNever, gtk.PolicyAutomatic)
	sc.SetHasFrame(true)

	save := gtk.NewButtonWithLabel("Save")
	save.AddCSSClass("suggested-action")
	save.ConnectClicked(func() {
		b := tv.Buffer()
		s := b.Text(b.StartIter(), b.EndIter(), false)
		win.Close()
		ok(s)
	})
	cancel := gtk.NewButtonWithLabel("Cancel")
	cancel.ConnectClicked(func() { win.Close() })

	btns := gtk.NewBox(gtk.OrientationHorizontal, 8)
	btns.SetHAlign(gtk.AlignEnd)
	btns.SetMarginTop(8)
	if rm != nil {
		del := gtk.NewButtonWithLabel("Remove")
		del.AddCSSClass("destructive-action")
		del.ConnectClicked(func() {
			win.Close()
			rm()
		})
		btns.Append(del)
	}
	btns.Append(cancel)
	btns.Append(save)

	box := gtk.NewBox(gtk.OrientationVertical, 8)
	box.SetMarginTop(16)
	box.SetMarginBottom(16)
	box.SetMarginStart(16)
	box.SetMarginEnd(16)
	box.Append(d)
	box.Append(sc)
	box.Append(btns)
	win.SetChild(box)
	tv.GrabFocus()
	win.SetVisible(true)
}
