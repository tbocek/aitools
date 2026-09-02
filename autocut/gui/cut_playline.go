package main

// The playhead's own layer.
//
// The red line used to be drawn by the bands it crosses, which made the 100ms
// playback tick repaint the whole timeline to move a 2px line: every thumbnail
// rescaled, every waveform column refilled, ten times a second, for as long as
// the preview ran. GTK4 has no partial invalidation -- QueueDraw repaints all
// of a widget or none of it -- so the cheap repaint has to be a cheap WIDGET:
// a transparent layer over both bands with nothing on it but the line. The
// bands underneath now repaint when their pixels change, which is on edits and
// scrolls (redrawTracks), not on the clock.

import (
	"github.com/diamondburned/gotk4/pkg/cairo"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// lineOver lays the line's layer over the two bands. Transparent to the
// pointer as well as the eye: every hover, click and drag on the timeline
// belongs to the bands underneath, and a layer that could be a target would
// swallow all of them.
func (ed *cutEditor) lineOver(band gtk.Widgetter) *gtk.Overlay {
	ed.lineArea = gtk.NewDrawingArea()
	ed.lineArea.SetCanTarget(false)
	ed.lineArea.SetDrawFunc(func(_ *gtk.DrawingArea, cr *cairo.Context, _, h int) {
		ed.drawPlayline(cr, h)
	})
	over := gtk.NewOverlay()
	over.SetChild(band)
	over.AddOverlay(ed.lineArea)
	return over
}

// drawPlayline is the line itself, through both bands and the gap between
// them. The bands draw translated by the scroll; this layer does not scroll,
// so the view offset is subtracted here instead.
func (ed *cutEditor) drawPlayline(cr *cairo.Context, h int) {
	if !ed.hasPlay {
		return
	}
	x := ed.xOf(ed.playhead) - ed.viewX
	cr.SetSourceRGB(0.9, 0.15, 0.15)
	cr.SetLineWidth(2)
	cr.MoveTo(x, 0)
	cr.LineTo(x, float64(h))
	cr.Stroke()
}

// redrawLine repaints what the running clock alone moves: the line's layer and
// the framing overlay on the preview, cursor and camera with it -- the same
// trio redrawTracks settles, minus the bands. This is the playback tick's
// repaint; redrawTracks stays the answer for anything that changes what the
// bands show.
func (ed *cutEditor) redrawLine() {
	if ed.lineArea != nil {
		ed.lineArea.QueueDraw()
	}
	if ed.fxArea != nil {
		ed.fxArea.QueueDraw()
		ed.syncFxCursor()
		ed.syncPreviewZoom()
	}
}
