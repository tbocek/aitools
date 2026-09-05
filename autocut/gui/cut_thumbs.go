package main

// The pictures on the video rows, and getting them there without stalling the
// page.
//
// A row is painted by drawing one frame every thumbStep frames, and the frames
// are files on disk -- whatever the Prepare step extracted, at whatever size it
// was told to (scalePresets: "Original" by default, which on a modern capture
// is a 4K JPEG per frame).
//
// They used to be decoded where they were needed: a miss in the cache called
// gdk_pixbuf_new_from_file_at_scale from inside drawTrack, on the GTK thread,
// in the middle of painting. That is bearable while the view is still, because
// a file is decoded once and the cache is never emptied -- and it is exactly
// wrong for the one gesture that changes which files are needed.
//
// Zooming is that gesture. The step is a thumbnail's width in frames
// (th*aspect / (pps*interval)), so a zoom does not merely move the pictures
// along: at 4 px/s with frames every 5 s it draws every fifth frame, and at
// 20 px/s every one of them. One notch of the wheel therefore replaces four
// out of every five thumbnails on screen with files that have never been read,
// and the draw that follows it stops for a screenful of JPEG decodes. The zoom
// arithmetic was already coalesced onto one idle (zoomWheel); what the next
// notch waited for was this.
//
// So nothing is decoded in a draw any more. A miss asks for the file and paints
// nothing there; a worker goroutine reads the batch and hands the pictures back
// on the GTK thread, which stores them and asks for one more draw. The band
// fills in a frame or two later -- which is what the waveform lanes have always
// done ("still being decoded; the ground already says it is here") -- and the
// wheel keeps up because a draw is now only drawing.
//
// The cache holds cairo SURFACES rather than pixbufs, which is the second half
// of the same story: gdk_cairo_set_source_pixbuf converts a pixbuf into a
// surface every time it is called, so the old cache paid that conversion once
// per thumbnail per paint -- on every scroll, every zoom and ten times a second
// while the preview runs. Converted once on arrival, a paint is a memcpy.

import (
	"github.com/diamondburned/gotk4/pkg/cairo"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gdkpixbuf/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
)

// thumbPic is one frame, ready to paint: the surface and its width, which is
// whatever shape the source is at the row's height.
//
// A nil surface is a file that could not be read. It is kept in the cache
// exactly so it is not read again -- every draw would otherwise ask for it
// again, which is a decode attempt per frame per missing file.
type thumbPic struct {
	surf *cairo.Surface
	w    float64
}

// thumb is what drawTrack asks for. It never reads a file: the answer is
// either a picture that is ready or nothing at all, and asking for one that is
// not ready puts it on the list the loader drains.
func (ed *cutEditor) thumb(path string) *thumbPic {
	if p, ok := ed.thumbs[path]; ok {
		return p
	}
	if ed.thumbWant == nil {
		ed.thumbWant = map[string]bool{}
	}
	if !ed.thumbWant[path] {
		ed.thumbWant[path] = true
		ed.loadThumbs()
	}
	return nil
}

// thumbBatch is how many files one round of the loader hands back at a time.
// The whole point is that the page keeps moving, so a long list arrives in
// pieces: a screenful of pictures appears while the rest are still being read.
const thumbBatch = 6

// loadThumbs arms the worker, on an idle rather than here.
//
// On an idle because this is called from inside a draw: starting a goroutine
// is cheap, but the map it reads is the GTK thread's and the draw is not
// finished with it. One armed loader at a time -- what arrives while it runs
// is asked for again by the next draw, which is the same list one frame later.
func (ed *cutEditor) loadThumbs() {
	if ed.thumbBusy {
		return
	}
	ed.thumbBusy = true
	glib.IdleAdd(func() { ed.runThumbs() })
}

// runThumbs takes the list, reads the files off the GTK thread, and hands the
// pictures back to it.
//
// gen is the row height the batch was asked at. It travels with the work
// because setThumbH empties the cache and asks for everything again at another
// size, and a picture decoded for the old height would otherwise land in the
// new cache and be drawn at the wrong scale.
func (ed *cutEditor) runThumbs() {
	want := make([]string, 0, len(ed.thumbWant))
	for p := range ed.thumbWant {
		want = append(want, p)
	}
	ed.thumbWant = map[string]bool{}
	if len(want) == 0 {
		ed.thumbBusy = false
		return
	}
	h, gen := ed.thumbHt, ed.thumbGen
	go func() {
		done := make(map[string]*gdkpixbuf.Pixbuf, thumbBatch)
		flush := func() {
			if len(done) == 0 {
				return
			}
			batch := done
			done = make(map[string]*gdkpixbuf.Pixbuf, thumbBatch)
			glib.IdleAdd(func() { ed.tookThumbs(batch, gen) })
		}
		for _, p := range want {
			// the read, the decode and the scale, all of it here: this is the
			// work that used to happen inside drawTrack
			pb, err := gdkpixbuf.NewPixbufFromFileAtScale(p, -1, h, true)
			if err != nil {
				pb = nil // remembered as unreadable rather than tried again
			}
			done[p] = pb
			if len(done) >= thumbBatch {
				flush()
			}
		}
		flush()
		glib.IdleAdd(func() {
			ed.thumbBusy = false
			if len(ed.thumbWant) > 0 {
				ed.loadThumbs() // more was asked for while this ran
			}
		})
	}()
}

// tookThumbs puts a batch in the cache and asks for one draw for the lot.
//
// The pixbuf becomes a cairo surface here, on the GTK thread with the rest of
// the drawing machinery, and is not kept: what the band paints from is the
// surface, and holding both would be two copies of every frame in memory for
// no reader.
func (ed *cutEditor) tookThumbs(batch map[string]*gdkpixbuf.Pixbuf, gen int) {
	if gen != ed.thumbGen {
		return // asked for at a row height nobody is drawing any more
	}
	if ed.thumbs == nil {
		ed.thumbs = map[string]*thumbPic{}
	}
	for p, pb := range batch {
		ed.thumbs[p] = thumbSurface(pb)
	}
	ed.queueTracks()
}

// thumbSurface paints a pixbuf onto an image surface once, so that every draw
// after it is a copy rather than a conversion. A pixbuf that could not be read
// comes back as a picture with no surface, which is how the cache remembers
// that the file is not worth asking about again.
func thumbSurface(pb *gdkpixbuf.Pixbuf) *thumbPic {
	if pb == nil {
		return &thumbPic{}
	}
	w, h := pb.Width(), pb.Height()
	if w <= 0 || h <= 0 {
		return &thumbPic{}
	}
	surf := cairo.CreateImageSurface(cairo.FormatARGB32, w, h)
	cr := cairo.Create(surf)
	gdk.CairoSetSourcePixbuf(cr, pb, 0, 0)
	cr.Paint()
	surf.Flush()
	return &thumbPic{surf: surf, w: float64(w)}
}
