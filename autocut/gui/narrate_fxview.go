package main

// The Narrate preview shows the finished picture.
//
// This page exists to judge a narration line against the moment it is spoken
// over: does "and there it is" land while the thing is on screen, does the
// title already say it, is there room in the sound. Every one of those is a
// question about the FINISHED video, and until this existed the page answered
// them over raw footage -- uncropped, unzoomed, with no titles on it. A line
// written for a close-up was checked against the wide shot it was cropped out
// of, and a line explaining what the title already says looked necessary.
//
// There is almost nothing in this file, and that is the point. The layers, the
// geometry, the clock and the painting are the Cut page's, which have been
// right for a long time (cut_fxscreen.go, cut_fxpaint.go); all this page adds
// is the four answers fxPage asks for and a drawing area that never has to
// draw a grab. The sound's half is narrate.go's syncFxSound.

import (
	"github.com/diamondburned/gotk4/pkg/cairo"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// ---- what this page answers for its screen (fxPage) --------------------------

func (n *narrator) fxCut() *cutEditor { return n.a.ed }
func (n *narrator) fxPlayer() *Player { return n.player }
func (n *narrator) fxAt() float64     { return n.pos }

// fxSrcSize is asked only before the first frame arrives, so it hands the
// question to the page that keeps the recordings: this preview plays whichever
// one the scene names, and once it is playing the paintable answers instead.
func (n *narrator) fxSrcSize() (float64, float64) { return n.a.ed.fxSrcSize() }

// fxCamOK is yes whenever there is footage. Nothing is ever aimed by hand
// here -- effects are placed on the Cut page and only watched on this one --
// so the one reason Cut takes the camera layer down cannot arise.
func (n *narrator) fxCamOK() bool { return n.player != nil && n.player.Loaded() }

// buildNarrFx wraps the preview picture in the same three layers Cut's preview
// has and returns what to hang in the frame.
func (n *narrator) buildNarrFx() gtk.Widgetter {
	n.fx = &fxScreen{}
	over := n.fx.buildLayers(n, n.player.Picture, n.player.video)
	n.fx.fxArea.SetDrawFunc(n.drawFx)
	// the display's clock, not the page's: the 100ms tick is right for a red
	// line and wrong for a glide, and this layer costs nothing while it is down
	n.fx.fxArea.AddTickCallback(func(_ gtk.Widgetter, _ gdk.FrameClocker) bool {
		if n.fx.livePreview() && n.player != nil && n.player.playing {
			n.fx.syncPreviewZoom()
			n.fx.fxArea.QueueDraw() // the mask and the titles move with the camera
		}
		return true
	})
	return over
}

// syncFx settles all three layers for wherever the playhead is now. Hung on
// setPlayhead, which every way this preview moves goes through, so a seek into
// a zoom shows the zoom and a seek into a stop shows the frozen frame.
func (n *narrator) syncFx(t float64) {
	if n.fx == nil {
		return
	}
	n.fx.reLive(t)
	n.fx.syncPreviewZoom()
	n.fx.syncFxStill()
	n.fx.fxArea.QueueDraw()
}

// drawFx paints what the render will have that the layers underneath do not:
// the black around the output frame, and the titles.
//
// The whole of the difference from Cut's overlay is what is missing. There is
// no camera outline, no text-box handles, no label, no drag -- an effect is
// placed on the Cut page, and here it is only watched.
func (n *narrator) drawFx(_ *gtk.DrawingArea, cr *cairo.Context, w, h int) {
	if n.player == nil || n.player.still {
		return // a card is on screen; the camera talks about footage
	}
	if n.fx.livePreview() {
		n.fx.paintLive(cr, w, h)
		return
	}
	n.fx.paintFlat(cr, w, h)
}
