package main

// Step 4: Cut. Two thumbnail tracks over one shared session timeline -- the
// source on top, the cut below with removed parts as empty stretches. Mouse
// wheel zooms both around the cursor, drag selects on either track, and a
// rough selection is fine: Add snaps its edges to a nearby scene change or
// speech gap, computed from data earlier steps already produced. "Suggest
// cut" (LLM) fills the cut to the target length; from then on the human owns
// it and the total simply is what it is. That suggestion (or, before any
// suggestion, whatever was on disk) is the checkpoint Revert returns to, so
// hand edits are always a separate, droppable layer.
//
// Gaps between recordings (wall-clock holes with no video) draw as a fixed
// narrow hatched band, not proportionally -- a 30 minute break should not be
// 30 minutes of scrollbar.
//
// step4/cut.json {"segs":[{"s":..,"e":..}]}   session-time seconds, sorted

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/diamondburned/gotk4/pkg/cairo"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gdkpixbuf/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

const (
	rulerH   = 18  // tick zone on top of the source track
	gapPx    = 26  // display width of a between-recordings hole
	snapTol  = 5.0 // seconds the Add edges may move to find a better cut point
	minSegLn = 1.0 // segments shorter than this are dropped when editing
	undoDeep = 50  // how many edits back Undo reaches
)

const suggestSystem = `You pick the moments for a highlight cut of a gaming session. You get the
full session timeline: visual event log entries and everything said, all with
[mm:ss] session timestamps, possibly across multiple recordings.
Return strict JSON, nothing else:
{"segments":[{"start":<sec>,"end":<sec>}]}
Rules:
- Segments use SESSION seconds (the [mm:ss] stamps: mm*60+ss).
- 6 to 20 segments, chronological, each 8-45 s, total close to the target.
- Pick the strongest material: action peaks, funny lines, reveals, the outro
  if there is one. Never invent times; only use moments the timeline shows.`

type cutSeg struct {
	S float64 `json:"s"`
	E float64 `json:"e"`
}

type tlVideo struct {
	base     string
	path     string
	start    float64 // session time of this video's t=0
	dur      float64
	interval float64
	fps      float64
	frames   []string
	pxOrigin float64 // display x of the video's left edge (at current zoom)
}

// ffprobeFPS reads the average frame rate; r_frame_rate lies on VFR captures.
func ffprobeFPS(path string) float64 {
	out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=avg_frame_rate", "-of", "csv=p=0", path).Output()
	if err != nil {
		return 30
	}
	var num, den float64
	fmt.Sscanf(strings.TrimSpace(string(out)), "%f/%f", &num, &den)
	if den > 0 && num/den >= 1 && num/den <= 240 {
		return num / den
	}
	return 30
}

type cutEditor struct {
	a    *App
	vids []tlVideo
	segs []cutSeg

	pps    float64 // pixels per second (zoom)
	lastX  float64 // cursor x, for zoom centering
	totalW float64

	sel struct {
		t0, t1 float64
		active bool
	}
	thumbHt   int // thumbnail height; the 🔍 buttons change it
	playhead  float64
	hasPlay   bool
	player    *Player
	playVideo *tlVideo // which recording the preview is playing

	markIn, markOut float64 // editor-style in/out points, session time
	hasIn, hasOut   bool

	scroll           *gtk.ScrolledWindow
	srcArea, cutArea *gtk.DrawingArea
	total            *gtk.Label
	target           *gtk.Entry
	hints            *gtk.TextView

	thumbs map[string]*gdkpixbuf.Pixbuf
	scores map[string][]float64 // per video: visual change per frame
	gaps   map[string][]float64 // per video: session-time speech-gap points

	undo [][]cutSeg // one snapshot per edit; every edit is reversible
	base []cutSeg   // the cut at the last checkpoint; Revert returns to this

	undoBtn, revertBtn *gtk.Button
}

// ---- data ------------------------------------------------------------------

func (a *App) cutPath() string { return filepath.Join(a.outDir, "step4", "cut.json") }

// reload rebuilds the timeline from the current selection + step outputs.
func (ed *cutEditor) reload() error {
	a := ed.a
	vids := a.vidList.selected()
	auds := a.audList.selected()
	if len(vids) == 0 {
		return fmt.Errorf("no videos selected")
	}
	// same zero convention as session.tsv: min start over ALL sources
	zero := math.MaxFloat64
	type st struct {
		path  string
		start float64
	}
	var all []st
	for _, r := range append(append([]string{}, vids...), auds...) {
		p := filepath.Join(a.root, r)
		s, err := sourceStart(p)
		if err != nil {
			return fmt.Errorf("cannot place %s in time: %w", baseName(p), err)
		}
		all = append(all, st{p, s})
		zero = math.Min(zero, s)
	}
	ed.vids = nil
	for _, s := range all[:len(vids)] {
		p, err := a.planVideo(s.path, filepath.Join(a.outDir, "step2"))
		if err != nil {
			return err
		}
		dur, _ := ffprobeDur(s.path)
		ed.vids = append(ed.vids, tlVideo{
			base: p.base, path: s.path, start: s.start - zero, dur: dur,
			interval: p.interval, fps: ffprobeFPS(s.path), frames: p.frames,
		})
	}
	sort.Slice(ed.vids, func(i, j int) bool { return ed.vids[i].start < ed.vids[j].start })

	// cut state; the undo history belongs to the cut that produced it
	ed.segs = nil
	ed.undo = nil
	ed.syncButtons()
	if b, err := os.ReadFile(a.cutPath()); err == nil {
		var c struct{ Segs []cutSeg }
		if json.Unmarshal(b, &c) == nil {
			ed.segs = c.Segs
		}
	}
	ed.setBase() // what is on disk now is the checkpoint this session edits from

	// speech-gap candidates: midpoints of silence between anything anyone
	// says, per video, in session time -- Add prefers cutting there
	ed.gaps = map[string][]float64{}
	var speech [][2]float64
	for _, s := range all {
		base := baseName(s.path)
		rows := loadSeg4(filepath.Join(a.outDir, "step3", base, "transcript.fixed.tsv"))
		if rows == nil {
			rows = loadSeg4(filepath.Join(a.outDir, "step3", base, "commentary.fixed.tsv"))
		}
		if rows == nil {
			rows = loadSeg4(filepath.Join(a.outDir, "step1", base, "transcript.tsv"))
		}
		for _, r := range rows {
			speech = append(speech, [2]float64{s.start - zero + r.s, s.start - zero + r.e})
		}
	}
	sort.Slice(speech, func(i, j int) bool { return speech[i][0] < speech[j][0] })
	for vi := range ed.vids {
		v := &ed.vids[vi]
		var pts []float64
		last := v.start
		for _, sp := range speech {
			if sp[0] > last && sp[0] < v.start+v.dur {
				pts = append(pts, (last+sp[0])/2) // silence midpoint
			}
			if sp[1] > last {
				last = sp[1]
			}
		}
		ed.gaps[v.base] = pts
	}

	// visual-change scores in the background; snapping works without them
	// (speech gaps only) until they land
	if ed.scores == nil {
		ed.scores = map[string][]float64{}
	}
	for _, v := range ed.vids {
		if _, ok := ed.scores[v.base]; ok {
			continue
		}
		v := v
		go func() {
			sc := frameChangeScores(v.frames)
			glib.IdleAdd(func() { ed.scores[v.base] = sc })
		}()
	}
	ed.relayout()
	return nil
}

// frameChangeScores diffs consecutive frames at postage-stamp size; local
// maxima are scene-change candidates.
func frameChangeScores(frames []string) []float64 {
	out := make([]float64, len(frames))
	var prev []byte
	for i, f := range frames {
		pb, err := gdkpixbuf.NewPixbufFromFileAtScale(f, 24, 14, false)
		if err != nil {
			continue
		}
		px := pb.Pixels()
		if prev != nil && len(prev) == len(px) {
			sum := 0
			for j := 0; j < len(px); j++ {
				d := int(px[j]) - int(prev[j])
				if d < 0 {
					d = -d
				}
				sum += d
			}
			out[i] = float64(sum) / float64(len(px))
		}
		prev = append(prev[:0], px...)
	}
	return out
}

// ---- geometry --------------------------------------------------------------

func (ed *cutEditor) relayout() {
	x := 0.0
	for i := range ed.vids {
		if i > 0 {
			x += gapPx
		}
		ed.vids[i].pxOrigin = x
		x += ed.vids[i].dur * ed.pps
	}
	ed.totalW = x
	w := int(x) + 1
	if ed.srcArea != nil {
		ed.srcArea.SetSizeRequest(w, rulerH+ed.thumbHt+8)
		ed.cutArea.SetSizeRequest(w, ed.thumbHt+8)
		ed.srcArea.QueueDraw()
		ed.cutArea.QueueDraw()
	}
	ed.updateTotal()
}

func (ed *cutEditor) setThumbH(h int) {
	ed.thumbHt = max(40, min(160, h))
	ed.thumbs = map[string]*gdkpixbuf.Pixbuf{} // cached at the old height
	ed.relayout()
}

// setPlayhead drops the red line and cues the preview there. Whatever the
// player was doing continues: paused stays paused (showing the new frame),
// playing keeps playing from the new spot.
func (ed *cutEditor) setPlayhead(t float64) {
	ed.playhead = t
	ed.hasPlay = true
	if v := ed.videoAt(t); v != nil && ed.player != nil {
		wasPlaying := ed.player.playing
		if ed.playVideo == v {
			ed.player.SeekTo(t - v.start) // same file: cheap in-place seek
		} else {
			ed.playVideo = v
			ed.player.PlaySegment(v.path, t-v.start, -1, wasPlaying)
		}
	}
	if ed.srcArea != nil {
		ed.srcArea.QueueDraw()
		ed.cutArea.QueueDraw()
	}
}

// frameStep pauses and nudges the preview by whole frames.
func (ed *cutEditor) frameStep(n int) {
	if ed.playVideo == nil || ed.player == nil {
		ed.a.setStatus("click a track first to place the playhead")
		return
	}
	v := ed.playVideo
	ed.player.Pause()
	local := math.Max(0, math.Min(v.dur, ed.playhead-v.start+float64(n)/v.fps))
	ed.playhead = v.start + local
	ed.player.SeekTo(local)
	ed.srcArea.QueueDraw()
	ed.cutArea.QueueDraw()
}

// setMark places the in or out point at the playhead; once both exist they
// become the active selection, ready for the add/remove strips.
func (ed *cutEditor) setMark(out bool) {
	if !ed.hasPlay {
		ed.a.setStatus("place the playhead first (click a track)")
		return
	}
	if out {
		ed.markOut, ed.hasOut = ed.playhead, true
	} else {
		ed.markIn, ed.hasIn = ed.playhead, true
	}
	if ed.hasIn && ed.hasOut && ed.markOut > ed.markIn {
		ed.sel.t0, ed.sel.t1 = ed.markIn, ed.markOut
		ed.sel.active = true
	}
	ed.srcArea.QueueDraw()
	ed.cutArea.QueueDraw()
}

// followPlayback keeps the red line on the player's clock while it runs;
// on pause the queries stop and the line simply stays put.
func (ed *cutEditor) followPlayback() bool {
	if ed.player == nil || !ed.player.playing || ed.playVideo == nil {
		return true
	}
	if pos, ok := ed.player.Position(); ok {
		ed.playhead = ed.playVideo.start + pos
		if ed.srcArea != nil {
			ed.srcArea.QueueDraw()
			ed.cutArea.QueueDraw()
		}
	}
	return true // keep the timer alive
}

func (ed *cutEditor) xOf(t float64) float64 {
	for _, v := range ed.vids {
		if t <= v.start+v.dur {
			if t < v.start {
				return v.pxOrigin
			}
			return v.pxOrigin + (t-v.start)*ed.pps
		}
	}
	return ed.totalW
}

func (ed *cutEditor) tAt(x float64) float64 {
	for i, v := range ed.vids {
		end := v.pxOrigin + v.dur*ed.pps
		if x < v.pxOrigin {
			if i == 0 {
				return v.start
			}
			return v.start // inside a gap: clamp to the next video's start
		}
		if x <= end {
			return v.start + (x-v.pxOrigin)/ed.pps
		}
	}
	last := ed.vids[len(ed.vids)-1]
	return last.start + last.dur
}

func (ed *cutEditor) videoAt(t float64) *tlVideo {
	for i := range ed.vids {
		v := &ed.vids[i]
		if t >= v.start && t <= v.start+v.dur {
			return v
		}
	}
	return nil
}

// ---- editing ---------------------------------------------------------------

// snapEdge moves a rough edge to the best nearby cut point: strongest visual
// change or a speech gap, with a bias outward so sloppy selections keep the
// action whole instead of clipping it.
func (ed *cutEditor) snapEdge(t float64, isStart bool) float64 {
	v := ed.videoAt(t)
	if v == nil {
		return t
	}
	best, bestScore := t, 0.35 // a candidate must beat "just leave it"
	try := func(c, score float64) {
		d := math.Abs(c - t)
		if d > snapTol || c < v.start || c > v.start+v.dur {
			return
		}
		score -= 0.4 * d / snapTol
		if (isStart && c <= t) || (!isStart && c >= t) {
			score += 0.3
		}
		if score > bestScore {
			best, bestScore = c, score
		}
	}
	for _, g := range ed.gaps[v.base] {
		try(g, 0.8)
	}
	if sc := ed.scores[v.base]; sc != nil {
		mean := 0.0
		for _, s := range sc {
			mean += s
		}
		mean /= float64(len(sc) + 1)
		i0 := int((t - snapTol - v.start) / v.interval)
		i1 := int((t + snapTol - v.start) / v.interval)
		for i := max(1, i0); i <= min(len(sc)-1, i1); i++ {
			if sc[i] > 2*mean && sc[i] >= sc[i-1] {
				try(v.start+float64(i)*v.interval, math.Min(1, sc[i]/(4*mean)))
			}
		}
	}
	return best
}

func (ed *cutEditor) addRange(t0, t1 float64) {
	if t1 < t0 {
		t0, t1 = t1, t0
	}
	t0 = ed.snapEdge(t0, true)
	t1 = ed.snapEdge(t1, false)
	// a selection may span several recordings and the hole between them:
	// split into per-video pieces, drop slivers
	for _, v := range ed.vids {
		s := math.Max(t0, v.start)
		e := math.Min(t1, v.start+v.dur)
		if e-s >= minSegLn {
			ed.segs = append(ed.segs, cutSeg{s, e})
		}
	}
	ed.coalesce()
	ed.persist()
}

func (ed *cutEditor) removeRange(t0, t1 float64) {
	if t1 < t0 {
		t0, t1 = t1, t0
	}
	var out []cutSeg
	for _, s := range ed.segs {
		if s.E <= t0 || s.S >= t1 { // untouched
			out = append(out, s)
			continue
		}
		if s.S < t0 && t0-s.S >= minSegLn {
			out = append(out, cutSeg{s.S, t0})
		}
		if s.E > t1 && s.E-t1 >= minSegLn {
			out = append(out, cutSeg{t1, s.E})
		}
	}
	ed.segs = out
	ed.coalesce()
	ed.persist()
}

// pushUndo snapshots the cut before an edit. Every path that changes segs goes
// through here first, so Add, Remove and Suggest are all reversible -- pressing
// Add is a try, not a commitment.
func (ed *cutEditor) pushUndo() {
	ed.undo = append(ed.undo, append([]cutSeg(nil), ed.segs...))
	if len(ed.undo) > undoDeep {
		ed.undo = ed.undo[len(ed.undo)-undoDeep:]
	}
	ed.syncButtons()
}

func (ed *cutEditor) undoLast() {
	if len(ed.undo) == 0 {
		ed.a.setStatus("nothing to undo")
		return
	}
	ed.segs = ed.undo[len(ed.undo)-1]
	ed.undo = ed.undo[:len(ed.undo)-1]
	ed.sel.active = false
	ed.clearMarks()
	ed.persist()
	ed.syncButtons()
	ed.a.setStatus(fmt.Sprintf("undone — %d segment(s) left", len(ed.segs)))
}

// setBase marks the current cut as what Revert returns to: whatever was on disk
// when the page loaded, or whatever Suggest produced. Everything after that is
// the user's own delta.
func (ed *cutEditor) setBase() {
	ed.base = append([]cutSeg(nil), ed.segs...)
	ed.syncButtons()
}

func sameCut(a, b []cutSeg) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (ed *cutEditor) syncButtons() {
	if ed.undoBtn != nil {
		ed.undoBtn.SetSensitive(len(ed.undo) > 0)
	}
	if ed.revertBtn != nil {
		ed.revertBtn.SetSensitive(!sameCut(ed.segs, ed.base))
	}
}

// segAt returns the index of the kept scene covering t, or -1.
func (ed *cutEditor) segAt(t float64) int {
	for i, s := range ed.segs {
		if t >= s.S && t < s.E {
			return i
		}
	}
	return -1
}

func (ed *cutEditor) coalesce() {
	sort.Slice(ed.segs, func(i, j int) bool { return ed.segs[i].S < ed.segs[j].S })
	var out []cutSeg
	for _, s := range ed.segs {
		if n := len(out); n > 0 && s.S <= out[n-1].E+0.25 {
			if s.E > out[n-1].E {
				out[n-1].E = s.E
			}
			continue
		}
		out = append(out, s)
	}
	ed.segs = out
}

func (ed *cutEditor) persist() {
	b, _ := json.MarshalIndent(struct {
		Segs []cutSeg `json:"segs"`
	}{ed.segs}, "", "  ")
	os.MkdirAll(filepath.Dir(ed.a.cutPath()), 0o755)
	if err := os.WriteFile(ed.a.cutPath(), append(b, '\n'), 0o644); err != nil {
		ed.a.logf("save cut: %v", err)
	}
	ed.updateTotal()
	ed.syncButtons() // every edit changes whether there is a delta to revert
	if ed.srcArea != nil {
		ed.srcArea.QueueDraw()
		ed.cutArea.QueueDraw()
	}
}

func (ed *cutEditor) updateTotal() {
	if ed.total == nil {
		return
	}
	sum, src := 0.0, 0.0
	for _, s := range ed.segs {
		sum += s.E - s.S
	}
	for _, v := range ed.vids {
		src += v.dur
	}
	mmss := func(t float64) string { return fmt.Sprintf("%d:%02d", int(t)/60, int(t)%60) }
	ed.total.SetText(fmt.Sprintf("cut %s  ·  source %s  ·  %d segment(s)",
		mmss(sum), mmss(src), len(ed.segs)))
}

// ---- drawing ---------------------------------------------------------------

func (ed *cutEditor) drawTrack(cr *cairo.Context, w, h int, isCut bool) {
	th := float64(ed.thumbHt)
	top := 0.0
	if !isCut {
		top = rulerH
	}
	// background
	cr.SetSourceRGB(0.13, 0.13, 0.13)
	cr.Rectangle(0, 0, float64(w), float64(h))
	cr.Fill()

	for vi, v := range ed.vids {
		// hatched hole before this video
		if vi > 0 {
			gx := v.pxOrigin - gapPx
			cr.SetSourceRGB(0.22, 0.2, 0.16)
			cr.Rectangle(gx, top, gapPx, th+4)
			cr.Fill()
			cr.SetSourceRGB(0.45, 0.4, 0.3)
			cr.SetLineWidth(1)
			for x := gx - th; x < gx+gapPx; x += 6 {
				cr.MoveTo(x, top+th+4)
				cr.LineTo(x+th+4, top)
				cr.Stroke()
			}
		}
		// thumbnails
		step := max(1, int(th*1.78/(ed.pps*v.interval)+0.5))
		for i := 0; i < len(v.frames); i += step {
			t := v.start + float64(i)*v.interval
			x := ed.xOf(t)
			if isCut && !ed.inCut(t) {
				continue
			}
			pb := ed.thumb(v.frames[i])
			if pb != nil {
				gdk.CairoSetSourcePixbuf(cr, pb, x, top+2)
				cr.Rectangle(x, top+2, math.Min(float64(pb.Width()), float64(step)*v.interval*ed.pps), th)
				cr.Fill()
			}
		}
		// on the cut track, dim the removed stretches lightly so the scale
		// stays readable (the "empty areas")
		if isCut {
			cr.SetSourceRGBA(0.1, 0.1, 0.1, 0.001)
			cr.Rectangle(v.pxOrigin, top, v.dur*ed.pps, th+4)
			cr.Fill()
		}
		// file boundary + name
		cr.SetSourceRGB(0.9, 0.7, 0.2)
		cr.SetLineWidth(2)
		cr.MoveTo(v.pxOrigin, top)
		cr.LineTo(v.pxOrigin, top+th+4)
		cr.Stroke()
		if !isCut {
			cr.SetSourceRGB(0.9, 0.9, 0.9)
			cr.SetFontSize(10)
			cr.MoveTo(v.pxOrigin+4, top+12)
			cr.ShowText(v.base)
		}
	}

	// ruler on the source track
	if !isCut {
		stepS := tickStep(ed.pps)
		cr.SetFontSize(9)
		for _, v := range ed.vids {
			t0 := math.Ceil(v.start/stepS) * stepS
			for t := t0; t < v.start+v.dur; t += stepS {
				x := ed.xOf(t)
				cr.SetSourceRGB(0.6, 0.6, 0.6)
				cr.MoveTo(x, float64(rulerH))
				cr.LineTo(x, float64(rulerH)-5)
				cr.Stroke()
				cr.MoveTo(x+2, float64(rulerH)-7)
				cr.ShowText(fmt.Sprintf("%d:%02d", int(t)/60, int(t)%60))
			}
		}
	}

	// in/out markers: solid lines with flag triangles, same visual weight as
	// the yellow file boundaries so they are actually findable
	if ed.hasIn {
		x := ed.xOf(ed.markIn)
		cr.SetSourceRGB(0.15, 0.85, 0.25)
		cr.SetLineWidth(3)
		cr.MoveTo(x, top)
		cr.LineTo(x, top+th+4)
		cr.Stroke()
		cr.MoveTo(x, top)
		cr.LineTo(x+9, top)
		cr.LineTo(x, top+9)
		cr.ClosePath()
		cr.Fill()
	}
	if ed.hasOut {
		x := ed.xOf(ed.markOut)
		cr.SetSourceRGB(0.92, 0.12, 0.12)
		cr.SetLineWidth(3)
		cr.MoveTo(x, top)
		cr.LineTo(x, top+th+4)
		cr.Stroke()
		cr.MoveTo(x, top)
		cr.LineTo(x-9, top)
		cr.LineTo(x, top+9)
		cr.ClosePath()
		cr.Fill()
	}

	// state overlays: the SOURCE stream tints everything the cut keeps in
	// green; the CUT stream tints everything removed in red
	if !isCut {
		for _, s := range ed.segs {
			x0, x1 := ed.xOf(s.S), ed.xOf(s.E)
			cr.SetSourceRGBA(0.2, 0.8, 0.3, 0.30)
			cr.Rectangle(x0, top, x1-x0, th+4)
			cr.Fill()
			// hard green edges, boundary-marker style
			cr.SetSourceRGB(0.15, 0.85, 0.25)
			cr.SetLineWidth(2)
			for _, x := range []float64{x0, x1} {
				cr.MoveTo(x, top)
				cr.LineTo(x, top+th+4)
				cr.Stroke()
			}
		}
	} else {
		cr.SetSourceRGBA(0.85, 0.2, 0.2, 0.30)
		for _, v := range ed.vids {
			cur := v.start
			for _, s := range ed.segs {
				if s.E <= v.start || s.S >= v.start+v.dur {
					continue
				}
				if s.S > cur {
					x0, x1 := ed.xOf(cur), ed.xOf(s.S)
					cr.Rectangle(x0, top, x1-x0, th+4)
					cr.Fill()
				}
				cur = math.Max(cur, s.E)
			}
			if cur < v.start+v.dur {
				x0, x1 := ed.xOf(cur), ed.xOf(v.start+v.dur)
				cr.Rectangle(x0, top, x1-x0, th+4)
				cr.Fill()
			}
		}
	}

	// selection rubber band
	if ed.sel.active {
		a, b := ed.sel.t0, ed.sel.t1
		if b < a {
			a, b = b, a
		}
		x0, x1 := ed.xOf(a), ed.xOf(b)
		cr.SetSourceRGBA(0.3, 0.55, 0.9, 0.45)
		cr.Rectangle(x0, top, x1-x0, th+4)
		cr.Fill()
	}

	// the red select point / playhead
	if ed.hasPlay {
		x := ed.xOf(ed.playhead)
		cr.SetSourceRGB(0.9, 0.15, 0.15)
		cr.SetLineWidth(2)
		cr.MoveTo(x, 0)
		cr.LineTo(x, float64(h))
		cr.Stroke()
	}
}

func (ed *cutEditor) inCut(t float64) bool {
	for _, s := range ed.segs {
		if t >= s.S && t < s.E {
			return true
		}
	}
	return false
}

func tickStep(pps float64) float64 {
	for _, s := range []float64{1, 2, 5, 10, 30, 60, 120, 300, 600} {
		if s*pps >= 70 {
			return s
		}
	}
	return 1200
}

func (ed *cutEditor) thumb(path string) *gdkpixbuf.Pixbuf {
	if pb, ok := ed.thumbs[path]; ok {
		return pb
	}
	pb, err := gdkpixbuf.NewPixbufFromFileAtScale(path, -1, ed.thumbHt, true)
	if err != nil {
		pb = nil
	}
	ed.thumbs[path] = pb
	return pb
}

// ---- page ------------------------------------------------------------------

func (a *App) buildStep4() gtk.Widgetter {
	ed := &cutEditor{a: a, pps: 4, thumbHt: 64, thumbs: map[string]*gdkpixbuf.Pixbuf{}}
	a.ed = ed
	if p, err := NewPlayer(); err == nil {
		ed.player = p // the preview above the tracks; independent of Review's
		glib.TimeoutAdd(100, ed.followPlayback)
	} else {
		a.logf("cut preview player: %v", err)
	}

	suggest := gtk.NewButtonWithLabel("Suggest cut")
	suggest.ConnectClicked(func() { a.suggestClicked() })
	ed.target = gtk.NewEntry()
	ed.target.SetText("300")
	ed.target.SetMaxWidthChars(5)
	ed.target.SetTooltipText("target seconds for the FIRST suggested cut; your edits are never limited")
	add := gtk.NewButtonWithLabel("＋ Add")
	add.AddCSSClass("suggested-action")
	add.ConnectClicked(func() { a.addSelClicked() })
	add.SetTooltipText("keep the selected region (Undo takes it back)")
	rem := gtk.NewButtonWithLabel("－ Remove")
	rem.SetTooltipText("drop the selected region — or, with nothing selected, " +
		"the one scene under the playhead (Del)")
	rem.ConnectClicked(func() { a.removeSelClicked() })
	ed.revertBtn = gtk.NewButtonWithLabel("↺ Revert edits")
	ed.revertBtn.SetTooltipText("drop everything you added or removed by hand and go back to " +
		"the last suggestion — or, if you have not suggested yet, to the cut this page opened with")
	ed.revertBtn.SetSensitive(false)
	ed.revertBtn.ConnectClicked(func() { a.revertClicked() })
	ed.undoBtn = gtk.NewButtonWithLabel("↶ Undo")
	ed.undoBtn.SetTooltipText("take back the last Add, Remove or Suggest (Ctrl+Z)")
	ed.undoBtn.SetSensitive(false)
	ed.undoBtn.ConnectClicked(func() { ed.undoLast() })
	ed.total = gtk.NewLabel("")
	ed.total.SetHExpand(true)
	ed.total.SetXAlign(1)

	thumbMinus := gtk.NewButtonFromIconName("zoom-out-symbolic")
	thumbMinus.SetTooltipText("smaller thumbnails")
	thumbMinus.ConnectClicked(func() { ed.setThumbH(ed.thumbHt * 3 / 4) })
	thumbPlus := gtk.NewButtonFromIconName("zoom-in-symbolic")
	thumbPlus.SetTooltipText("larger thumbnails")
	thumbPlus.ConnectClicked(func() { ed.setThumbH(ed.thumbHt * 4 / 3) })

	toggle := gtk.NewButtonWithLabel("⏯")
	toggle.SetTooltipText("play / pause the preview")
	toggle.ConnectClicked(func() {
		if ed.player != nil {
			ed.player.Toggle()
		}
	})
	prev5 := gtk.NewButtonWithLabel("‹‹f")
	prev5.SetTooltipText("back 5 frames (pauses)")
	prev5.ConnectClicked(func() { ed.frameStep(-5) })
	prevF := gtk.NewButtonWithLabel("‹f")
	prevF.SetTooltipText("previous frame (pauses)")
	prevF.ConnectClicked(func() { ed.frameStep(-1) })
	nextF := gtk.NewButtonWithLabel("f›")
	nextF.SetTooltipText("next frame (pauses)")
	nextF.ConnectClicked(func() { ed.frameStep(+1) })
	next5 := gtk.NewButtonWithLabel("f››")
	next5.SetTooltipText("forward 5 frames (pauses)")
	next5.ConnectClicked(func() { ed.frameStep(+5) })
	markIn := gtk.NewButtonWithLabel("⟦ in")
	markIn.SetTooltipText("set the start marker at the playhead")
	markIn.ConnectClicked(func() { ed.setMark(false) })
	markOut := gtk.NewButtonWithLabel("out ⟧")
	markOut.SetTooltipText("set the end marker at the playhead")
	markOut.ConnectClicked(func() { ed.setMark(true) })
	clearBtn := gtk.NewButtonWithLabel("✕")
	clearBtn.SetTooltipText("clear the selection and the in/out marks")
	clearBtn.ConnectClicked(func() {
		ed.sel.active = false
		ed.clearMarks()
		ed.srcArea.QueueDraw()
		ed.cutArea.QueueDraw()
	})

	// footage context for the Suggest LLM, tucked behind a toggle so the
	// toolbar stays lean; persists with the project
	ed.hints = gtk.NewTextView()
	ed.hints.SetWrapMode(gtk.WrapWord)
	ed.hints.SetTopMargin(4)
	ed.hints.SetLeftMargin(6)
	hintScroll := gtk.NewScrolledWindow()
	hintScroll.SetChild(ed.hints)
	hintScroll.SetSizeRequest(-1, 60)
	hintScroll.AddCSSClass("frame")
	hintLbl := gtk.NewLabel("Context for the cut (pasted TEXT reaches the model — bare URLs are not fetched): " +
		"what was this session, what matters, what to focus on")
	hintLbl.SetXAlign(0)
	hintLbl.SetWrap(true)
	hintLbl.AddCSSClass("dim-label")
	hintBox := gtk.NewBox(gtk.OrientationVertical, 4)
	hintBox.Append(hintLbl)
	hintBox.Append(hintScroll)

	bar := gtk.NewBox(gtk.OrientationHorizontal, 8)
	bar.Append(toggle)
	bar.Append(prev5)
	bar.Append(prevF)
	bar.Append(nextF)
	bar.Append(next5)
	bar.Append(markIn)
	bar.Append(markOut)
	bar.Append(clearBtn)
	bar.Append(suggest)
	bar.Append(gtk.NewLabel("target s:"))
	bar.Append(ed.target)
	bar.Append(add)
	bar.Append(rem)
	bar.Append(ed.revertBtn)
	bar.Append(ed.undoBtn)
	bar.Append(thumbMinus)
	bar.Append(thumbPlus)
	bar.Append(ed.total)

	ed.srcArea = gtk.NewDrawingArea()
	ed.cutArea = gtk.NewDrawingArea()
	ed.srcArea.SetDrawFunc(func(_ *gtk.DrawingArea, cr *cairo.Context, w, h int) {
		ed.drawTrack(cr, w, h, false)
	})
	ed.cutArea.SetDrawFunc(func(_ *gtk.DrawingArea, cr *cairo.Context, w, h int) {
		ed.drawTrack(cr, w, h, true)
	})

	for _, area := range []*gtk.DrawingArea{ed.srcArea, ed.cutArea} {
		area := area
		area.SetFocusable(true) // so Del/Ctrl+Z reach the page after a click
		// wheel zoom around the cursor; Shift+wheel stays horizontal scroll
		motion := gtk.NewEventControllerMotion()
		motion.ConnectMotion(func(x, y float64) { ed.lastX = x })
		area.AddController(motion)
		scroll := gtk.NewEventControllerScroll(gtk.EventControllerScrollVertical)
		scroll.ConnectScroll(func(dx, dy float64) bool {
			if scroll.CurrentEventState()&gdk.ShiftMask != 0 {
				return false
			}
			ed.zoomAt(ed.lastX, math.Pow(1.25, -dy))
			return true
		})
		area.AddController(scroll)
		drag := gtk.NewGestureDrag()
		var dragStartX, dragStartY float64
		var hadSel bool
		var selT0, selT1 float64
		drag.ConnectDragBegin(func(x, y float64) {
			area.GrabFocus()
			dragStartX, dragStartY = x, y
			hadSel, selT0, selT1 = ed.sel.active, ed.sel.t0, ed.sel.t1
			ed.sel.t0 = ed.tAt(x)
			ed.sel.t1 = ed.sel.t0
			ed.sel.active = true
		})
		drag.ConnectDragUpdate(func(ox, oy float64) {
			ed.sel.t1 = ed.tAt(dragStartX + ox)
			ed.srcArea.QueueDraw()
			ed.cutArea.QueueDraw()
		})
		drag.ConnectDragEnd(func(ox, oy float64) {
			_ = dragStartY
			_, _, _ = hadSel, selT0, selT1
			if math.Abs(ox) >= 5 || math.Abs(oy) >= 5 {
				return // a real drag: the new selection stands
			}
			// a press without movement is a CLICK: cue the playhead
			ed.sel.active = false
			ed.setPlayhead(ed.tAt(dragStartX))
		})
		area.AddController(drag)
	}

	tracks := gtk.NewBox(gtk.OrientationVertical, 4)
	tracks.Append(ed.srcArea)
	tracks.Append(ed.cutArea)
	ed.scroll = gtk.NewScrolledWindow()
	ed.scroll.SetChild(tracks)
	ed.scroll.SetPolicy(gtk.PolicyAlways, gtk.PolicyNever)
	ed.scroll.SetVExpand(true)

	bottom := gtk.NewBox(gtk.OrientationVertical, 8)
	bottom.SetMarginTop(6)
	bottom.SetMarginStart(12)
	bottom.SetMarginEnd(12)
	bottom.SetMarginBottom(8)
	bottom.Append(bar)
	bottom.Append(hintBox)
	bottom.Append(ed.scroll)

	// Ctrl+Z and Del on the page. Bubble phase on purpose: the notes box and
	// the target entry see the key first and keep their own editing behaviour.
	keys := gtk.NewEventControllerKey()
	keys.ConnectKeyPressed(func(keyval, keycode uint, state gdk.ModifierType) bool {
		switch {
		case keyval == gdk.KEY_z && state&gdk.ControlMask != 0:
			ed.undoLast()
		case keyval == gdk.KEY_Delete || keyval == gdk.KEY_BackSpace:
			a.removeSelClicked()
		default:
			return false
		}
		return true
	})
	bottom.AddController(keys)

	// video above, streams below, with a draggable divider between them
	if ed.player != nil {
		ed.player.Picture.SetVExpand(true)
		ed.player.Picture.SetSizeRequest(-1, 160)
		// clicking the video itself also toggles; the ⏯ button lives in the bar
		click := gtk.NewGestureClick()
		click.ConnectReleased(func(n int, x, y float64) { ed.player.Toggle() })
		ed.player.Picture.AddController(click)
		// a frame + breathing room, so the video is not glued to its neighbors
		vframe := gtk.NewFrame("")
		vframe.SetChild(ed.player.Picture)
		vframe.SetMarginTop(10)
		vframe.SetMarginStart(12)
		vframe.SetMarginEnd(12)
		vframe.SetMarginBottom(6)
		pane := gtk.NewPaned(gtk.OrientationVertical)
		pane.SetStartChild(vframe)
		pane.SetEndChild(bottom)
		pane.SetPosition(380)
		return pane
	}
	return bottom
}

func (ed *cutEditor) zoomAt(x, factor float64) {
	t := ed.tAt(x)
	adj := ed.scroll.HAdjustment()
	viewX := x - adj.Value()
	minPps := adj.PageSize() / math.Max(1, ed.totalDur())
	ed.pps = math.Max(minPps, math.Min(120, ed.pps*factor))
	ed.relayout()
	glib.IdleAdd(func() { // after the size change lands
		adj := ed.scroll.HAdjustment()
		adj.SetValue(ed.xOf(t) - viewX)
	})
}

func (ed *cutEditor) totalDur() float64 {
	d := 0.0
	for _, v := range ed.vids {
		d += v.dur
	}
	return d
}

// updateStep4Info (re)loads the editor when its inputs exist.
func (a *App) updateStep4Info() {
	if a.ed == nil {
		return
	}
	if !exists(filepath.Join(a.outDir, "step3", "session.tsv")) {
		return
	}
	if err := a.ed.reload(); err != nil {
		a.logf("cut editor: %v", err)
	}
}

func (ed *cutEditor) clearMarks() {
	ed.hasIn, ed.hasOut = false, false
}

func (a *App) cutHints() string {
	if a.ed == nil || a.ed.hints == nil {
		return ""
	}
	buf := a.ed.hints.Buffer()
	return strings.TrimSpace(buf.Text(buf.StartIter(), buf.EndIter(), false))
}

func (a *App) addSelClicked() {
	ed := a.ed
	if !ed.sel.active || len(ed.vids) == 0 {
		a.setStatus("drag a region on a track first")
		return
	}
	ed.pushUndo()
	ed.addRange(ed.sel.t0, ed.sel.t1)
	ed.sel.active = false
	ed.clearMarks()
	a.setStatus("added — ↶ Undo (Ctrl+Z) takes it back")
}

// revertClicked throws away the hand-made delta and nothing else. Undoing ten
// Adds one at a time is not a workflow, but neither is nuking a suggestion you
// wanted to keep: this returns to the checkpoint -- the last suggestion, or the
// cut the page opened with -- and is itself one ↶ Undo away from coming back.
func (a *App) revertClicked() {
	ed := a.ed
	if sameCut(ed.segs, ed.base) {
		a.setStatus("nothing to revert — the cut is as it was")
		return
	}
	was := len(ed.segs)
	ed.pushUndo()
	ed.segs = append([]cutSeg(nil), ed.base...)
	ed.sel.active = false
	ed.clearMarks()
	ed.persist()
	switch {
	case len(ed.base) == 0:
		a.setStatus(fmt.Sprintf("reverted — your %d hand-made segment(s) are gone, "+
			"the cut is empty again (↶ Undo brings them back)", was))
	default:
		a.setStatus(fmt.Sprintf("reverted to the %d segment(s) of the last suggestion "+
			"(↶ Undo brings your edits back)", len(ed.base)))
	}
}

// removeSelClicked drops the selected stretch or, when nothing is selected, the
// single scene under the playhead: clicking a green scene and pressing Remove
// should work without having to rubber-band it first. It never fails silently --
// a button that does nothing and says nothing reads as a missing button.
func (a *App) removeSelClicked() {
	ed := a.ed
	mmss := func(t float64) string { return fmt.Sprintf("%d:%02d", int(t)/60, int(t)%60) }
	switch i := -1; {
	case ed.sel.active:
		before := len(ed.segs)
		ed.pushUndo()
		ed.removeRange(ed.sel.t0, ed.sel.t1)
		ed.sel.active = false
		ed.clearMarks()
		a.setStatus(fmt.Sprintf("removed — %d segment(s), was %d", len(ed.segs), before))
	case ed.hasPlay:
		if i = ed.segAt(ed.playhead); i < 0 {
			a.setStatus("the playhead is not on a kept scene — click a green one, or drag a region")
			return
		}
		s := ed.segs[i]
		ed.pushUndo()
		ed.segs = append(ed.segs[:i], ed.segs[i+1:]...)
		ed.persist()
		a.setStatus(fmt.Sprintf("removed the scene at %s (%.0f s) — ↶ Undo takes it back",
			mmss(s.S), s.E-s.S))
	default:
		a.setStatus("nothing selected — click a kept scene, or drag a region on a track")
	}
}

// ---- suggest ----------------------------------------------------------------

func (a *App) suggestClicked() {
	if a.running {
		a.setStatus("a run is already active — stop it first (⏹)")
		return
	}
	// re-suggesting over an untouched suggestion is fine -- there is no human
	// work in it to lose. Over hand edits it is not, so say what to press.
	if len(a.ed.segs) > 0 && !sameCut(a.ed.segs, a.ed.base) {
		a.setStatus("you have hand edits — press ↺ Revert edits first if you want a fresh suggestion")
		return
	}
	session, err := os.ReadFile(filepath.Join(a.outDir, "step3", "session.txt"))
	if err != nil {
		a.setStatus("run step 3 first — no session timeline")
		return
	}
	target := 300.0
	fmt.Sscanf(a.ed.target.Text(), "%f", &target)

	a.running = true
	a.stopFlag.Store(false)
	a.pauseFlag.Store(false)
	a.runCtx, a.runCancel = context.WithCancel(context.Background())
	a.updateRunControls()
	a.setStatus("suggesting a cut…")
	a.logExp.SetExpanded(true)
	a.logf(">>> suggest: target %.0f s, thinking over the session timeline — this is one long LLM call, expect a minute or two", target)
	// a single call has no measurable fraction; pulse so it visibly lives
	a.progress.SetText("suggesting a cut…")
	glib.TimeoutAdd(150, func() bool {
		if !a.running {
			return false
		}
		a.progress.Pulse()
		return true
	})
	go func() {
		segs, err := a.suggestCut(string(session), target)
		glib.IdleAdd(func() {
			a.running = false
			a.updateRunControls()
			if err != nil {
				if !errors.Is(err, errStopped) {
					a.logf("suggest FAILED: %v", err)
				}
				a.progress.SetFraction(0)
				a.progress.SetText("suggest failed — see log")
				a.setStatus("suggest failed — see log")
				return
			}
			total := 0.0
			a.ed.pushUndo() // a suggestion is a proposal; Undo clears it again
			a.ed.segs = nil // a re-suggest replaces the old one, never stacks on it
			for _, s := range segs {
				a.ed.segs = append(a.ed.segs, cutSeg{
					a.ed.snapEdge(s.S, true), a.ed.snapEdge(s.E, false)})
				total += s.E - s.S
			}
			a.ed.coalesce()
			a.ed.persist()
			a.ed.setBase() // from here on, Revert comes back to this suggestion
			a.progress.SetFraction(1)
			a.progress.SetText("cut suggested")
			a.logf(">>> suggested %d segments, %d:%02d total — edit away",
				len(segs), int(total)/60, int(total)%60)
			a.setStatus(fmt.Sprintf("suggested %d segments", len(segs)))
		})
	}()
}

func (a *App) suggestCut(session string, target float64) ([]cutSeg, error) {
	system := suggestSystem
	if h := a.cutHints(); h != "" {
		system += "\nEditor's notes about this session -- trust them and let them guide what matters:\n" + h
	}
	user := fmt.Sprintf("TARGET LENGTH: %.0f seconds.\n\nSESSION TIMELINE:\n%s", target, session)
	msgs := []map[string]any{msg("system", system), msg("user", user)}
	for try := 0; try < 3; try++ {
		if err := a.checkpoint(); err != nil {
			return nil, err
		}
		reply, err := a.llmChatRetry(msgs, true)
		if err != nil {
			return nil, err
		}
		clean := strings.TrimSpace(reply)
		if i := strings.Index(clean, "{"); i >= 0 {
			clean = clean[i:]
		}
		clean = strings.TrimSuffix(strings.TrimSpace(clean), "```")
		var out struct {
			Segments []struct{ Start, End float64 } `json:"segments"`
		}
		problem := ""
		if err := json.Unmarshal([]byte(clean), &out); err != nil {
			problem = "not valid JSON: " + err.Error()
		} else if len(out.Segments) < 4 {
			problem = "fewer than 4 segments"
		} else {
			total := 0.0
			var segs []cutSeg
			for _, s := range out.Segments {
				if s.End <= s.Start {
					problem = "segment with end before start"
					break
				}
				// only video-backed time counts
				if a.ed.videoAt(s.Start) == nil && a.ed.videoAt(s.End) == nil {
					continue
				}
				segs = append(segs, cutSeg{s.Start, s.End})
				total += s.End - s.Start
			}
			if problem == "" {
				if total < target*0.6 || total > target*1.5 {
					problem = fmt.Sprintf("total %.0fs, target %.0fs", total, target)
				} else {
					return segs, nil
				}
			}
		}
		a.logfIdle(">>> suggest attempt %d rejected: %s", try+1, problem)
		msgs = append(msgs, msg("assistant", reply),
			msg("user", "Your answer failed validation: "+problem+". Return corrected strict JSON only."))
	}
	return nil, fmt.Errorf("no valid cut after 3 attempts")
}
