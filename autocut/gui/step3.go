package main

// Step 3: Cut. Two thumbnail tracks over one shared session timeline -- the
// source on top, the cut below with removed parts as empty stretches. Mouse
// wheel zooms both around the cursor and the bar's + and − do it around the
// middle of the view, in both cases no further out than the whole session
// (minPps). Drag selects on either track, and a
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
// step3/cut.json {"segs":[{"s":..,"e":..}]}   session-time seconds, sorted

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
	"github.com/diamondburned/gotk4/pkg/pango"
)

const (
	rulerH   = 18  // tick zone on top of the source track
	gapPx    = 26  // display width of a between-recordings hole
	snapTol  = 5.0 // seconds the Add edges may move to find a better cut point
	minSegLn = 1.0 // segments shorter than this are dropped when editing
	undoDeep = 50  // how many edits back Undo reaches
)

// One paragraph or bullet per line, unwrapped: see describeSystem.
const suggestSystem = `You choose the moments for a highlight video of a gaming session, cut for YouTube. Someone who was not there should be able to watch it from start to finish and enjoy it.

You get the whole session as one timeline. Every line is [mm:ss], then a second bracket naming the recording it came from and either EVENT or a speaker, then the line itself. EVENT lines describe what was on screen at that moment and say whether it was hectic or calm. Speaker lines are what was said, possibly across several recordings of the same room. The minutes keep counting past 59, so [72:30] is 4350 seconds.

Return strict JSON, nothing else:
{"segments":[{"start":<sec>,"end":<sec>}]}

Timing.

- start and end are session seconds: mm*60+ss from the stamps.
- 6 to 20 segments, chronological, never overlapping. 8 to 45 seconds each as a rule, but a beat that the session notes ask for, or that needs its whole build up and payoff, runs as long as it needs. A story cut off in the middle is worse than a long segment.
- The total should land within about a tenth of the target length you are given. Well short or well over is rejected and you are asked again.
- Only times the timeline actually shows. Never invent one, and never round to a moment nothing happens at.
- Only stretches with footage. A span with no EVENT lines from any recording has nothing to show, and a segment there is thrown away, which leaves the video short.

What you have been told about this session.

- The request may open with a block headed ABOUT THIS SESSION: notes on what happened, who did what, what to look out for. They are written by someone who was there, they know things the timeline only hints at, and they outrank every general rule below.
- Whatever the notes name is a segment. Work out where it happens from the words spoken around it and from the EVENT lines, and take it. If the notes name four things, four of your segments are those things and the rest fill in around them.
- Take the whole thing, not the moment it is first mentioned. Something the notes single out usually runs setup, then the thing itself, then the reaction, and those can be minutes apart: someone is warned not to open the chest long before anyone opens it. Start where it is set up and end after the last reaction to it. Cutting at the first mention hands the viewer a setup with no payoff, which is the one way to make an important moment worse than leaving it out.
- Footage still decides. If no recording has EVENT lines over a named moment, it happened off camera: take the nearest stretch that IS on camera, where it is talked about, rather than a stretch with nothing to show.
- Being in the notes does not put it in the video. Never invent a moment because the notes led you to expect one.

What goes in.

- Open with an introduction. The first segment establishes what this session is: wherever the speakers say what they are doing, where they are, or what they are after. It is allowed to be quiet. Its job is to stop the viewer being lost, not to impress.
- Then vary the pace on purpose. The EVENT lines tell you which moments are hectic and which are calm. All peaks is as tiring to watch as no peaks, so set a loud stretch against a quiet one, and let the good fast sequences run long enough to breathe rather than clipping every one to the minimum.
- Keep the funny lines. A joke, a good insult, a scream, someone confidently wrong, someone breaking down laughing: this is why people watch other people play, and it beats a technically impressive moment nobody reacted to. When the joke lands in the speech and the picture is unremarkable, take it anyway.
- Take the action peaks too: wins, disasters, near misses, reveals, and anything that pays off something set up earlier in the session. A callback is worth more than a bigger explosion.
- Spread the picks over the whole session instead of mining one dense stretch, and finish on something that feels like an ending: the outro if there is one, otherwise the last win or the last good line.

Where to cut.

- Do not cut into a sentence. Use the stamps of the lines either side to start a beat before the first word you want and to end after the reaction to it, so no clip opens or closes mid-word.
- Give a joke its setup. A punchline with the line that set it up cut off is not funny, and neither is a reaction to something the viewer did not see.
- End on the payoff, never on the setup. Where the picture shows something being opened, decided, fought or discovered, the segment ends after the outcome and after what was said about it, however long that takes. Ending a beat just before the thing everyone was waiting for is the worst cut you can make.
- A segment that is only silence, or only walking around, is not a highlight however much the pacing wants a calm one. Calm means quieter action or a good quiet line, not nothing.`

type cutSeg struct {
	S float64 `json:"s"`
	E float64 `json:"e"`
}

type tlVideo struct {
	base     string
	path     string
	start    float64 // session time of this video's t=0
	wall     float64 // the same instant on the wall clock, for naming outputs
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
	// the preview has been started and not stopped, which is what makes the run
	// bar its transport. Same rule as narrate and produce: a recording merely
	// LOADED -- which clicking the timeline does, just to show the frame there
	// -- must not take ▶ away from Suggest cut.
	started bool

	markIn, markOut float64 // editor-style in/out points, session time
	hasIn, hasOut   bool

	// The tracks are NOT a wide widget in a scrolled window (see drawTrack):
	// they are exactly as wide as the space they have, and this adjustment is
	// the window onto the timeline that they draw.
	hadj             *gtk.Adjustment
	hbar             *gtk.Scrollbar
	viewX, viewW     float64 // scroll offset and width of that window, in timeline px
	srcArea, cutArea *gtk.DrawingArea
	total            *gtk.Label
	target           *gtk.Entry
	inputs           *gtk.Label // what this page reads, and what Suggest is sent
	out              *gtk.Label // what step3/ holds, same line as step 1 and 2 show

	thumbs map[string]*gdkpixbuf.Pixbuf
	scores map[string][]float64 // per video: visual change per frame
	gaps   map[string][]float64 // per video: session-time speech-gap points

	undo [][]cutSeg // one snapshot per edit; every edit is reversible
	base []cutSeg   // the cut at the last checkpoint; Revert returns to this

	undoBtn, revertBtn *gtk.Button
	playBtn            *gtk.Button // ▶/⏸ for the preview; drawn by syncPlayIcons
}

// ---- data ------------------------------------------------------------------

// mmss is how this page says a duration. It was three identical closures in
// three functions before something outside them needed it too.
func mmss(t float64) string { return fmt.Sprintf("%d:%02d", int(t)/60, int(t)%60) }

func (a *App) cutDir() string  { return filepath.Join(a.outDir, "step3") }
func (a *App) cutPath() string { return filepath.Join(a.cutDir(), "cut.json") }

// reload rebuilds the timeline from the current selection + step outputs.
func (ed *cutEditor) reload() error {
	a := ed.a
	vids, auds := a.snapSources()
	if len(vids) == 0 {
		return fmt.Errorf("nothing to cut — no source on the Inputs step is marked as footage")
	}
	// same zero convention as session.tsv: min start over ALL sources
	zero := math.MaxFloat64
	type st struct {
		path  string
		start float64
	}
	var all []st
	for _, p := range append(append([]string{}, vids...), auds...) {
		s, err := sourceStart(p)
		if err != nil {
			return fmt.Errorf("cannot place %s in time: %w", baseName(p), err)
		}
		all = append(all, st{p, s})
		zero = math.Min(zero, s)
	}
	ed.vids = nil
	for _, s := range all[:len(vids)] {
		p, err := a.planVideo(s.path, a.describeDir())
		if err != nil {
			return err
		}
		dur, _ := ffprobeDur(s.path)
		ed.vids = append(ed.vids, tlVideo{
			base: p.base, path: s.path, start: s.start - zero, wall: s.start, dur: dur,
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
		rows := loadSeg4(filepath.Join(a.transcriptDir(), base, "transcript.fixed.tsv"))
		if rows == nil {
			rows = loadSeg4(filepath.Join(a.transcriptDir(), base, "commentary.fixed.tsv"))
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
	ed.updateInputs() // the recordings just changed, and so did their lengths
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
	if ed.srcArea != nil {
		// height only: the width is whatever the page gives us
		ed.srcArea.SetSizeRequest(-1, rulerH+ed.thumbHt+8)
		ed.cutArea.SetSizeRequest(-1, ed.thumbHt+8)
		ed.syncScroll()
		ed.srcArea.QueueDraw()
		ed.cutArea.QueueDraw()
	}
	ed.updateTotal()
}

// syncScroll points the scrollbar at the timeline as it now is. It is also
// where the bar disappears: a bar that cannot move is a bar that says there is
// something off to the right, and at the zoom floor there is not.
func (ed *cutEditor) syncScroll() {
	if ed.hadj == nil {
		return
	}
	ed.hadj.SetUpper(ed.totalW)
	ed.hadj.SetPageSize(ed.viewW)
	ed.hadj.SetStepIncrement(ed.viewW / 8)
	ed.hadj.SetPageIncrement(ed.viewW * 0.9)
	ed.hadj.SetValue(ed.hadj.Value()) // re-clamps against the new upper
	ed.viewX = ed.hadj.Value()
	ed.hbar.SetVisible(ed.totalW > ed.viewW+0.5)
}

// setOff scrolls to a timeline x; the adjustment does the clamping.
func (ed *cutEditor) setOff(x float64) {
	if ed.hadj == nil {
		ed.viewX = math.Max(0, x)
		return
	}
	ed.hadj.SetValue(x)
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

// tAtView is the same for an x on the widget, which is a window onto the
// timeline scrolled viewX px along it.
func (ed *cutEditor) tAtView(x float64) float64 { return ed.tAt(x + ed.viewX) }

// frameRange is which of a recording's frames drawTrack has to paint for the
// px window x0..x1: a half-open range walked in strides of step.
//
// The first index is snapped DOWN to a multiple of the stride, which is the
// point of doing this here rather than inline. Every frame is a candidate but
// only every step'th is drawn, so an unsnapped start would pick a different
// set of frames for every scroll position and the thumbnails would visibly
// reshuffle as the timeline moved under them.
func (v *tlVideo) frameRange(pps, x0, x1 float64, step int) (first, last int) {
	perFrame := pps * v.interval // px of timeline per frame
	first = max(0, int((x0-v.pxOrigin)/perFrame))
	first -= first % step
	last = min(len(v.frames), int((x1-v.pxOrigin)/perFrame)+1)
	if last <= first {
		return 0, 0 // the window is off one end of this recording
	}
	return first, last
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
	ed.updateOut() // the file on disk just changed size
	// Narrate and Produce are gated on cut.json existing, and this is the only
	// place it comes into existence. Without this the tabs stayed grey after a
	// perfectly good cut and woke up only on a rescan or a restart, which looks
	// exactly like the cut not having worked.
	ed.a.updateGates()
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
	ed.total.SetText(fmt.Sprintf("cut %s  ·  source %s  ·  %d segment(s)",
		mmss(sum), mmss(src), len(ed.segs)))
}

// updateInputs says what this page is working from: the recordings on the
// tracks, and the session timeline that Suggest is sent.
//
// The timeline is the part worth spelling out. It is not "the videos" -- it is
// every line anyone said, cleaned, merged with the event log of every
// recording, and the whole of it goes into the request. That is a thing you
// would otherwise have to read the code to know, and it is the difference
// between a suggestion that can hear a joke and one that can only see.
func (ed *cutEditor) updateInputs() {
	if ed == nil || ed.inputs == nil {
		return
	}
	src := 0.0
	var names []string
	for _, v := range ed.vids {
		src += v.dur
		names = append(names, fmt.Sprintf("%s  %s", mmss(v.dur), v.base))
	}
	line := fmt.Sprintf("%d recording(s) · %s of footage", len(ed.vids), mmss(src))
	detail := strings.Join(names, "\n")
	if len(names) == 0 {
		line, detail = "nothing to cut — no source on Inputs is marked as footage", ""
	}

	b, err := os.ReadFile(filepath.Join(ed.a.transcriptDir(), "session.txt"))
	switch {
	case err != nil:
		line += " · no session timeline — run Describe"
	default:
		speech, events := 0, 0
		for _, ln := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
			if ln == "" {
				continue
			}
			if strings.Contains(ln, " EVENT] ") {
				events++
			} else {
				speech++
			}
		}
		line += fmt.Sprintf(" · timeline %d lines (%d spoken, %d on screen) → all of it goes to Suggest",
			speech+events, speech, events)
		detail += fmt.Sprintf("\n\nstep2/transcript/session.txt — %d kB, sent whole with the cut prompt",
			(len(b)+512)/1024)
	}
	// the context box on Describe rides along with every request this page
	// makes, so this row -- which is the list of what Suggest is sent -- is
	// where it has to appear. Silent extra input is how a cut ends up obeying
	// something the user forgot they wrote.
	if c := ed.a.sessionCtx(); c != "" {
		line += " · session context"
		detail += "\n\nSession context (Describe), sent with Suggest and the audit:\n" + c
	}
	ed.inputs.SetText(line)
	ed.inputs.SetTooltipText(strings.TrimSpace(detail))
}

// updateOut says what is on disk, which is not what ed.total says: the total is
// the cut in the editor, and until it is persisted the two differ.
func (ed *cutEditor) updateOut() {
	if ed == nil || ed.out == nil {
		return
	}
	ed.out.SetText(summarizeOutputs(ed.a.cutDir()))
}

// ---- drawing ---------------------------------------------------------------

// plateText draws a label on its own dark ground, at the given baseline.
//
// The video name sits ON the thumbnails, and no single ink is readable there:
// white vanishes into a bright frame, black into a dark one. Inverting what is
// underneath (cairo's DIFFERENCE operator) sounds like the answer and is not --
// mid-gray inverts to mid-gray, and a gameplay frame is mostly mid-gray. The
// plate is what subtitles do, and it works on every frame.
func plateText(cr *cairo.Context, x, y float64, s string) {
	e := cr.TextExtents(s)
	cr.SetSourceRGBA(0, 0, 0, 0.66)
	cr.Rectangle(x-3, y-11, e.Width+6, 14)
	cr.Fill()
	cr.SetSourceRGB(1, 1, 1)
	cr.MoveTo(x, y)
	cr.ShowText(s)
}

// drawTrack paints one track. The widget is the size of the window onto the
// timeline, never the size of the timeline: an hour at the top zoom is 432,000
// px wide, which is thirteen times what a cairo surface can even be, and every
// redraw -- ten a second while the preview runs -- was walking all of it. So
// everything below is in timeline coordinates with the view scrolled under it
// (the Translate), and every loop is cut down to what is actually on screen
// first. The work per frame is then the same whether the session is a minute or
// an afternoon.
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

	// what is on screen, in timeline px. The margin is for the things that
	// start left of the edge and reach into view: a thumbnail, a tick's label.
	const margin = 80
	vx0, vx1 := ed.viewX-margin, ed.viewX+float64(w)
	cr.Save()
	cr.Translate(-ed.viewX, 0)
	defer cr.Restore()

	for vi, v := range ed.vids {
		if v.pxOrigin > vx1 || v.pxOrigin+v.dur*ed.pps < vx0-gapPx {
			continue // this recording is off screen entirely
		}
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
		// thumbnails, only the ones in view
		step := max(1, int(th*1.78/(ed.pps*v.interval)+0.5))
		first, last := v.frameRange(ed.pps, vx0, vx1, step)
		for i := first; i < last; i += step {
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
		// (a fill of the whole recording at alpha 0.001 stood here to "dim" what
		// the cut drops: a quarter of one step in 255, i.e. nothing. The red
		// overlay further down is what actually marks those stretches.)

		// file boundary + name
		cr.SetSourceRGB(0.9, 0.7, 0.2)
		cr.SetLineWidth(2)
		cr.MoveTo(v.pxOrigin, top)
		cr.LineTo(v.pxOrigin, top+th+4)
		cr.Stroke()
		if !isCut {
			cr.SetFontSize(10)
			plateText(cr, v.pxOrigin+4, top+12, v.base)
		}
	}

	// ruler on the source track
	if !isCut {
		stepS := tickStep(ed.pps)
		cr.SetFontSize(9)
		for _, v := range ed.vids {
			if v.pxOrigin > vx1 || v.pxOrigin+v.dur*ed.pps < vx0 {
				continue
			}
			from := math.Max(v.start, v.start+(vx0-v.pxOrigin)/ed.pps)
			to := math.Min(v.start+v.dur, v.start+(vx1-v.pxOrigin)/ed.pps)
			t0 := math.Ceil(from/stepS) * stepS
			for t := t0; t < to; t += stepS {
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
			if x1 < vx0 || x0 > vx1 {
				continue
			}
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
		fill := func(a, b float64) {
			x0, x1 := ed.xOf(a), ed.xOf(b)
			if x1 < vx0 || x0 > vx1 {
				return
			}
			cr.Rectangle(x0, top, x1-x0, th+4)
			cr.Fill()
		}
		for _, v := range ed.vids {
			if v.pxOrigin > vx1 || v.pxOrigin+v.dur*ed.pps < vx0 {
				continue
			}
			cur := v.start
			for _, s := range ed.segs {
				if s.E <= v.start || s.S >= v.start+v.dur {
					continue
				}
				if s.S > cur {
					fill(cur, s.S)
				}
				cur = math.Max(cur, s.E)
			}
			if cur < v.start+v.dur {
				fill(cur, v.start+v.dur)
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

// The run bar drives the preview through these; see transport in pipeline.go.
func (ed *cutEditor) playing() bool { return ed.player != nil && ed.player.Playing() }
func (ed *cutEditor) cued() bool    { return ed.player != nil && ed.player.Cued() }

func (ed *cutEditor) toggle() {
	if ed.player != nil {
		ed.player.Toggle()
		ed.started = ed.started || ed.player.Playing()
		ed.a.updateRunControls()
	}
}

func (ed *cutEditor) stop() {
	if ed.player != nil {
		ed.player.Stop()
	}
	ed.started = false // ⏹ hands ▶ back to Suggest cut
}

// ---- page ------------------------------------------------------------------

func (a *App) buildStep3() gtk.Widgetter {
	ed := &cutEditor{a: a, pps: 4, thumbHt: 64, thumbs: map[string]*gdkpixbuf.Pixbuf{}}
	a.ed = ed
	if p, err := NewPlayer(); err == nil {
		ed.player = p // the preview above the tracks; independent of Review's
		p.OnState = a.updateRunControls
		p.OnError = a.playerErr("the cut preview")
		glib.TimeoutAdd(100, ed.followPlayback)
	} else {
		a.logf("cut preview player: %v", err)
	}

	suggest := gtk.NewButtonWithLabel("Suggest cut")
	suggest.ConnectClicked(func() { a.suggestClicked() })
	ed.target = gtk.NewEntry()
	ed.target.SetText("300")
	ed.target.SetMaxWidthChars(4)
	ed.target.SetWidthChars(4) // it holds a number of seconds, not a sentence
	ed.target.SetInputPurpose(gtk.InputPurposeDigits)
	ed.target.SetTooltipText("target seconds for the FIRST suggested cut; your edits are never limited")
	// "target s:" spelled out cost more width than the field it labelled. Joined
	// to the button it belongs to, the row reads "Suggest cut [300] s" and needs
	// no caption; the tooltip on the field says the rest.
	secs := gtk.NewLabel("s")
	secs.AddCSSClass("dim-label")
	secs.SetTooltipText("seconds")
	add := gtk.NewButtonWithLabel("＋ Add")
	add.AddCSSClass("suggested-action")
	add.ConnectClicked(func() { a.addSelClicked() })
	add.SetTooltipText("keep the selected region (Undo takes it back)")
	rem := gtk.NewButtonWithLabel("－ Remove")
	rem.SetTooltipText("drop the selected region — or, with nothing selected, " +
		"the one scene under the playhead (Del)")
	rem.ConnectClicked(func() { a.removeSelClicked() })
	// Undo and Revert are icons, not words. They were the two widest buttons in
	// the bar and they are both the kind of control you reach for by shape --
	// Undo has a keyboard shortcut people already know, and Revert is a rare
	// deliberate act, not something scanned for. The glyphs they used to carry
	// (↶ and ↺) were nearly the same picture; the theme's undo arrow and
	// revert-to-saved icon are not.
	ed.revertBtn = gtk.NewButtonFromIconName("document-revert-symbolic")
	ed.revertBtn.SetTooltipText("Revert edits — drop everything you added or removed by hand and go back to " +
		"the last suggestion — or, if you have not suggested yet, to the cut this page opened with")
	ed.revertBtn.SetSensitive(false)
	ed.revertBtn.ConnectClicked(func() { a.revertClicked() })
	ed.undoBtn = gtk.NewButtonFromIconName("edit-undo-symbolic")
	ed.undoBtn.SetTooltipText("Undo — take back the last Add, Remove or Suggest (Ctrl+Z)")
	ed.undoBtn.SetSensitive(false)
	ed.undoBtn.ConnectClicked(func() { ed.undoLast() })
	ed.total = gtk.NewLabel("")
	ed.total.SetHExpand(true)
	ed.total.SetXAlign(1)
	// a label with no ellipsis reports its whole text as a minimum, and this bar
	// is a plain box, so that minimum was a floor under the window itself:
	// measured, the bar could not be narrower than 1527px, of which this line
	// was 272. Ellipsized it still shows in full wherever there is room -- the
	// natural width does not move -- and the window is free to be narrower than
	// the sentence.
	ed.total.SetEllipsize(pango.EllipsizeEnd)

	// Two pairs that both step something up and down, so they must not look
	// alike: one zooms the timeline, the other sizes the thumbnails drawn on it.
	// They used to be a bare +/− and a pair of magnifiers, which is backwards --
	// a magnifier IS the zoom icon, and the thing being made bigger in the other
	// pair is a picture. So: the theme's zoom icons for the timeline, and a
	// picture with a sign for the thumbnails. Different nouns, not two spellings
	// of the same one.
	sized := func(sign, tip string, click func()) *gtk.Button {
		row := gtk.NewBox(gtk.OrientationHorizontal, 1)
		row.Append(gtk.NewImageFromIconName("image-x-generic-symbolic"))
		row.Append(gtk.NewLabel(sign))
		b := gtk.NewButton()
		b.SetChild(row)
		b.SetTooltipText(tip)
		b.ConnectClicked(click)
		return b
	}
	thumbMinus := sized("−", "smaller thumbnails on the tracks", func() { ed.setThumbH(ed.thumbHt * 3 / 4) })
	thumbPlus := sized("+", "larger thumbnails on the tracks", func() { ed.setThumbH(ed.thumbHt * 4 / 3) })

	zoomOut := gtk.NewButtonFromIconName("zoom-out-symbolic")
	zoomOut.SetTooltipText("zoom the timeline out — it stops where the whole session is on screen " +
		"(the scroll wheel does the same, around the cursor)")
	zoomOut.ConnectClicked(func() { ed.zoomStep(1 / 1.25) })
	zoomIn := gtk.NewButtonFromIconName("zoom-in-symbolic")
	zoomIn.SetTooltipText("zoom the timeline in, around the middle of what is on screen " +
		"(the scroll wheel does the same, around the cursor)")
	zoomIn.ConnectClicked(func() { ed.zoomStep(1.25) })

	// one button that is ▶ or ⏸ depending on the preview, like every other play
	// button in the app (syncPlayIcons in pipeline.go keeps it drawn)
	ed.playBtn = gtk.NewButtonFromIconName("media-playback-start-symbolic")
	ed.playBtn.SetTooltipText("play or pause the preview at the playhead")
	ed.playBtn.ConnectClicked(ed.toggle)
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

	// One box, not two. What Suggest is told is the rules plus what this session
	// was, and splitting that into a "system prompt" and a "context" box only
	// asked the user to guess which half a sentence belonged in -- the runner
	// glued them together anyway. Pasted TEXT reaches the model; a bare URL is
	// not fetched.
	prompt := a.promptEditor("cut", "Cut prompt",
		"The rules, plus what this session was and what matters in it")
	// The audit is the second half of what Suggest does, so its wording lives on
	// the same page, under the prompt it checks against. Below and not beside:
	// this one is read after the first, and it is the one you leave alone.
	audit := a.promptEditor("audit", "Audit prompt",
		"How the suggestion is read back: what counts as ending too early, and how readily a segment is dropped")
	promptBox := gtk.NewBox(gtk.OrientationVertical, 4)
	promptBox.SetMarginStart(12)
	promptBox.SetMarginEnd(12)
	promptBox.Append(prompt)
	promptBox.Append(audit)
	// one scrollbar for the prompt column: the editor inside is given its full
	// height by this viewport, so it never scrolls against this one
	promptPane := gtk.NewScrolledWindow()
	promptPane.SetChild(promptBox)
	promptPane.SetPolicy(gtk.PolicyNever, gtk.PolicyAutomatic)

	// The bar in groups rather than as one row of twenty equal buttons. Twenty
	// things spaced identically is twenty things to read every time, and the
	// eight pixels between each of them added up to a bar that would not fit a
	// laptop screen. Buttons that do one job together are linked into a single
	// segmented control -- no gaps inside, so the group reads as one object and
	// the eye lands on four groups instead of twenty buttons.
	//
	// Left to right is also the order of the work: move the playhead, mark what
	// you found, change the cut. Then a rule, and past it the view controls,
	// which change what you SEE and never what is saved.
	linked := func(ws ...gtk.Widgetter) *gtk.Box {
		b := gtk.NewBox(gtk.OrientationHorizontal, 0)
		b.AddCSSClass("linked")
		for _, w := range ws {
			b.Append(w)
		}
		return b
	}
	rule := func() *gtk.Separator {
		s := gtk.NewSeparator(gtk.OrientationVertical)
		s.SetMarginTop(2)
		s.SetMarginBottom(2)
		return s
	}

	bar := gtk.NewBox(gtk.OrientationHorizontal, 6)
	bar.Append(linked(ed.playBtn, prev5, prevF, nextF, next5))
	bar.Append(linked(markIn, markOut, clearBtn))
	bar.Append(rule())
	bar.Append(linked(suggest, ed.target))
	bar.Append(secs)
	bar.Append(linked(add, rem))
	bar.Append(linked(ed.undoBtn, ed.revertBtn))
	bar.Append(rule()) // past here nothing changes the cut
	bar.Append(linked(zoomOut, zoomIn))
	bar.Append(linked(thumbMinus, thumbPlus))
	bar.Append(ed.total)

	ed.srcArea = gtk.NewDrawingArea()
	ed.cutArea = gtk.NewDrawingArea()
	ed.srcArea.SetDrawFunc(func(_ *gtk.DrawingArea, cr *cairo.Context, w, h int) {
		ed.drawTrack(cr, w, h, false)
	})
	ed.cutArea.SetDrawFunc(func(_ *gtk.DrawingArea, cr *cairo.Context, w, h int) {
		ed.drawTrack(cr, w, h, true)
	})
	ed.srcArea.SetHExpand(true)
	ed.cutArea.SetHExpand(true)
	// the tracks are as wide as the page, so their width IS the view width
	ed.srcArea.ConnectResize(func(w, h int) {
		ed.viewW = float64(w)
		ed.syncScroll()
		// the fit-the-window zoom moves with the window: widen the page while
		// fully zoomed out and the timeline has to grow with it, or a scrollbar
		// comes back for the empty strip beside it
		if m := ed.minPps(); ed.pps < m {
			ed.pps = m
			glib.IdleAdd(ed.relayout) // never resize from inside an allocation
		}
	})

	for _, area := range []*gtk.DrawingArea{ed.srcArea, ed.cutArea} {
		area := area
		area.SetFocusable(true) // so Del/Ctrl+Z reach the page after a click
		// wheel zooms around the cursor; Shift+wheel (and a trackpad's sideways
		// swipe) pans, which used to be the scrolled window's job
		motion := gtk.NewEventControllerMotion()
		motion.ConnectMotion(func(x, y float64) { ed.lastX = x })
		area.AddController(motion)
		scroll := gtk.NewEventControllerScroll(gtk.EventControllerScrollBothAxes)
		scroll.ConnectScroll(func(dx, dy float64) bool {
			if scroll.CurrentEventState()&gdk.ShiftMask != 0 {
				dx, dy = dy, 0
			}
			if dx != 0 {
				ed.setOff(ed.viewX + dx*ed.viewW/8)
			}
			if dy != 0 {
				ed.zoomAt(ed.lastX, math.Pow(1.25, -dy))
			}
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
			ed.sel.t0 = ed.tAtView(x)
			ed.sel.t1 = ed.sel.t0
			ed.sel.active = true
		})
		drag.ConnectDragUpdate(func(ox, oy float64) {
			ed.sel.t1 = ed.tAtView(dragStartX + ox)
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
			ed.setPlayhead(ed.tAtView(dragStartX))
		})
		area.AddController(drag)
	}

	// The scrollbar is ours rather than a scrolled window's, because a scrolled
	// window would want a child as wide as the whole timeline (see drawTrack).
	// Hidden when it cannot move: a bar at the zoom floor is a bar that says
	// there is more session off to the right when there is not.
	ed.hadj = gtk.NewAdjustment(0, 0, 0, 1, 1, 0)
	ed.hadj.ConnectValueChanged(func() {
		ed.viewX = ed.hadj.Value()
		ed.srcArea.QueueDraw()
		ed.cutArea.QueueDraw()
	})
	ed.hbar = gtk.NewScrollbar(gtk.OrientationHorizontal, ed.hadj)
	ed.hbar.SetVisible(false)

	tracks := gtk.NewBox(gtk.OrientationVertical, 4)
	tracks.Append(ed.srcArea)
	tracks.Append(ed.cutArea)
	tracks.Append(ed.hbar)
	tracks.SetVExpand(true)
	tracks.SetVAlign(gtk.AlignStart) // the tracks are their own height; the rest is air

	bottom := gtk.NewBox(gtk.OrientationVertical, 8)
	bottom.SetMarginTop(6)
	bottom.SetMarginStart(12)
	bottom.SetMarginEnd(12)
	bottom.SetMarginBottom(8)
	bottom.Append(bar)
	bottom.Append(tracks)

	// Same line steps 1 and 2 end on, in the same place: what this step wrote,
	// and a way into the folder. ed.total above says what the editor holds;
	// this says what is actually saved, which is the thing the next step reads.
	openOut := gtk.NewButtonFromIconName("folder-open-symbolic")
	openOut.SetTooltipText("step3/ — the cut, as cut.json")
	openOut.ConnectClicked(func() { a.openFolder(a.cutDir()) })
	ed.out = gtk.NewLabel("")
	outLbl := gtk.NewLabel("Outputs:")
	outLbl.AddCSSClass("heading")
	outRow := gtk.NewBox(gtk.OrientationHorizontal, 8)
	outRow.SetHAlign(gtk.AlignEnd)
	outRow.Append(outLbl)
	outRow.Append(openOut)
	outRow.Append(ed.out)
	bottom.Append(outRow)
	ed.updateOut()

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

	// Video and prompt side by side on top, timeline across the full width
	// below. The picture is 16:9 and the prompt is a column of text, so they
	// want opposite shapes -- stacked, the prompt ate the height the tracks
	// needed and the space beside the video stayed empty. The tracks are the one
	// thing that wants the whole width, so they get it.
	top := gtk.NewPaned(gtk.OrientationHorizontal)
	top.SetEndChild(promptPane)
	top.SetShrinkEndChild(false)
	if ed.player != nil {
		ed.player.Picture.SetVExpand(true)
		ed.player.Picture.SetSizeRequest(-1, 160)
		// clicking the video itself also toggles; the ▶/⏸ button lives in the bar
		click := gtk.NewGestureClick()
		click.ConnectReleased(func(n int, x, y float64) { ed.toggle() })
		ed.player.Picture.AddController(click)
		// a frame + breathing room, so the video is not glued to its neighbors
		vframe := videoFrame(ed.player.Picture)
		vframe.SetMarginTop(10)
		vframe.SetMarginStart(12)
		vframe.SetMarginEnd(12)
		vframe.SetMarginBottom(6)
		top.SetStartChild(vframe)
	} else {
		top.SetStartChild(gtk.NewBox(gtk.OrientationVertical, 0)) // no preview: the prompt has the row
	}
	top.SetPosition(660)

	// What this page reads, at the top, where Inputs and Describe put theirs.
	// The question it answers is the one asked just before pressing Suggest --
	// is everything in here, and does the model get to hear as well as see --
	// and it was answerable only by opening session.txt.
	ed.inputs = gtk.NewLabel("")
	ed.inputs.SetXAlign(0)
	ed.inputs.SetHExpand(true)
	ed.inputs.SetEllipsize(pango.EllipsizeEnd) // never a floor under the window
	inLbl := gtk.NewLabel("Inputs:")
	inLbl.AddCSSClass("heading")
	inRow := gtk.NewBox(gtk.OrientationHorizontal, 8)
	inRow.SetMarginStart(12)
	inRow.SetMarginEnd(12)
	inRow.SetMarginTop(6)
	inRow.Append(inLbl)
	inRow.Append(ed.inputs)
	ed.updateInputs()

	// which half of the page matters depends on whether you are cutting or
	// tuning what Suggest is told, so the divider is the user's
	pane := gtk.NewPaned(gtk.OrientationVertical)
	pane.SetStartChild(top)
	pane.SetEndChild(bottom)
	pane.SetPosition(380)
	pane.SetVExpand(true)

	page := gtk.NewBox(gtk.OrientationVertical, 4)
	page.Append(inRow)
	page.Append(pane)
	return page
}

// zoomStep is what the + and − in the bar do: the wheel's own step, but around
// the middle of what is on screen rather than around a cursor that is up on the
// button and not over the timeline at all. Zooming about the view's center is
// also what keeps a click-click-click on + heading somewhere: whatever you
// centered stays centered.
func (ed *cutEditor) zoomStep(factor float64) { ed.zoomAt(ed.viewW/2, factor) }

// zoomAt zooms about a point of the VIEW (a cursor position, or its middle),
// keeping whatever is under that point under it afterwards.
func (ed *cutEditor) zoomAt(viewX, factor float64) {
	t := ed.tAtView(viewX)
	ed.pps = math.Max(ed.minPps(), math.Min(120, ed.pps*factor))
	ed.relayout()
	ed.setOff(ed.xOf(t) - viewX)
}

// minPps is the zoom at which the whole session fits across the window, and
// therefore the floor: below it the timeline would be smaller than the space it
// has and scrolling would move nothing.
//
// The gaps between recordings are drawn at a fixed width and do not shrink with
// the zoom, so they come off the width the footage may use. Dividing the window
// by the duration alone -- which is what this did -- left the fully zoomed-out
// timeline wider than its window by every gap in it, so the scrollbar stayed
// and it still slid, which reads as a timeline hiding something off to the
// right when there is nothing out there at all.
func (ed *cutEditor) minPps() float64 {
	return fitPps(ed.viewW, ed.totalDur(), len(ed.vids))
}

// fitPps is that floor without a widget in the way: the zoom at which dur
// seconds spread over n recordings come to exactly view pixels, gaps and the
// rounding in relayout included.
func fitPps(view, dur float64, n int) float64 {
	if view <= 0 || dur <= 0 {
		return 0 // no allocation yet, or nothing loaded: no width to fit into
	}
	gaps := float64(max(0, n-1)) * gapPx
	return math.Max(0, (view-gaps-1)/dur) // -1: relayout rounds the width up
}

func (ed *cutEditor) totalDur() float64 {
	d := 0.0
	for _, v := range ed.vids {
		d += v.dur
	}
	return d
}

// updateStep3Info (re)loads the editor when its inputs exist.
func (a *App) updateStep3Info() {
	if a.ed == nil {
		return
	}
	a.ed.updateOut()    // true even with no timeline to load: the folder is the folder
	a.ed.updateInputs() // and so is what is missing, which is the useful part here
	if !exists(filepath.Join(a.transcriptDir(), "session.tsv")) {
		return
	}
	if err := a.ed.reload(); err != nil {
		a.logf("cut editor: %v", err)
	}
}

func (ed *cutEditor) clearMarks() {
	ed.hasIn, ed.hasOut = false, false
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
	session, err := os.ReadFile(filepath.Join(a.transcriptDir(), "session.txt"))
	if err != nil {
		a.setStatus("run Transcript first — no session timeline")
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
	a.logf(">>> suggest: target %.0f s, thinking over the session timeline — two long LLM calls "+
		"(choose, then audit what was chosen), expect a few minutes", target)
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
		a.logCtx("suggest")
		segs, err := a.suggestCut(string(session), target)
		if err == nil {
			glib.IdleAdd(func() { a.progress.SetText("auditing the cut…") })
			a.logfIdle(">>> audit: reading the %d proposed segments back against the brief — a second long call", len(segs))
			segs = a.auditCut(string(session), target, segs)
		}
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

// One paragraph or bullet per line, unwrapped: see describeSystem.
//
// The session belongs in the cut prompt, not here: this one is read the same
// way for every project, and it is handed the cut prompt as the brief, so notes
// about what mattered in a session reach the audit anyway. What is worth
// editing here is how suspicious the audit is -- how readily it drops, how far
// it will extend an end.
const auditSystem = `You are checking a proposed highlight cut against the brief it was made from, before anyone watches it. You did not choose these moments. Your job is to find where they are wrong.

You get the brief, the target length, the session timeline and the proposed segments. Timeline lines are [mm:ss], then a bracket naming the recording and either EVENT or a speaker, then the line itself. EVENT lines are what was on screen. The minutes keep counting past 59, so [72:30] is 4350 seconds.

Return strict JSON, nothing else:
{"checks":[{"i":<number>,"verdict":"ok","start":<sec>,"end":<sec>,"why":"<short>"}],"add":[{"start":<sec>,"end":<sec>,"why":"<short>"}]}

- One check per proposed segment, all of them, in order, under the numbers you were given.
- verdict "ok" leaves it exactly as it is: repeat the start and end you were given and leave why empty.
- verdict "fix" keeps the moment and corrects its boundaries. Give the new start and end, and say in a few words what was wrong.
- verdict "drop" takes it out of the video. Use it sparingly, for a stretch where nothing happens or one that shows what another segment already showed.
- add is for what is missing. This is where most of your value is. Say why in a few words.

What you are checking, hardest first.

- Does every moment the editor names appear in the cut? The request may open with a block headed ABOUT THIS SESSION, written by someone who was there, and anything it singles out has to be in the video. If it is missing, add it. If a segment covers it but stops short, fix it.
- Does each segment run to its payoff? Read the timeline past the end of the segment. If the thing it is about is still being argued about, still being opened, still being decided, or gets its reaction after the end, then the end is too early. Extend it past the last line that belongs to the moment. This is the commonest fault and the one worth looking hardest for.
- Does each segment start early enough to make sense on its own? A reaction whose cause was cut off, or a punchline without its setup, needs the start moved back to where the setup begins.
- Is a boundary in the middle of a sentence? Move it into the gap between two lines.
- Would someone who was not there follow the video from these segments alone? The first one has to establish what the session is.

Rules you may not break.

- Only times the timeline actually shows. Never invent one.
- Only stretches with footage. A span with no EVENT lines from any recording has nothing to show and will be thrown away.
- After your corrections the segments must still be in order and must not overlap. If extending one would run into the next, extend it anyway and drop the next, saying so.
- Keep the total near the target. If your fixes and additions make it much longer, pay for them by dropping the weakest segments.
- When a segment is right, say ok. A check that changes something for the sake of changing it is worse than no check at all.`

// auditCut is the second read of a suggestion, against the brief that produced
// it. The first call chooses moments from thousands of timeline lines at once;
// this one has far less to do -- it has the moments in front of it and only has
// to ask whether each one is right -- and that is what makes it worth a second
// long call. It is also the only check with any judgement in it: everything the
// code validates is arithmetic (does the JSON parse, is there footage, does the
// total land near the target), and none of that can see a segment that ends
// forty seconds before the chest is opened.
//
// It never fails the run. Anything wrong with the audit -- a refusal, bad JSON,
// a corrected cut that no longer passes the arithmetic -- leaves the original
// suggestion standing and says so in the log. A second opinion that can lose
// you the first one is not worth having.
func (a *App) auditCut(session string, target float64, segs []cutSeg) []cutSeg {
	var props strings.Builder
	for i, s := range segs {
		fmt.Fprintf(&props, "#%d  [%s] to [%s]  (%.0f s)\n", i+1, mmss(s.S), mmss(s.E), s.E-s.S)
	}
	user := a.ctxBlock() + fmt.Sprintf("THE BRIEF THE CUT WAS MADE FROM:\n%s\n\nTARGET LENGTH: %.0f seconds.\n\n"+
		"PROPOSED SEGMENTS:\n%s\nSESSION TIMELINE:\n%s", a.prompt("cut"), target, props.String(), session)
	msgs := []map[string]any{msg("system", a.prompt("audit")), msg("user", user)}

	if err := a.checkpoint(); err != nil {
		return segs
	}
	reply, err := a.llmChatRetry(msgs, true)
	if err != nil {
		a.logfIdle(">>> audit skipped: %v — keeping the suggestion as it is", err)
		return segs
	}
	clean := strings.TrimSpace(reply)
	if i := strings.Index(clean, "{"); i >= 0 {
		clean = clean[i:]
	}
	clean = strings.TrimSuffix(strings.TrimSpace(clean), "```")
	var out struct {
		Checks []struct {
			I          int
			Verdict    string
			Start, End float64
			Why        string
		} `json:"checks"`
		Add []struct {
			Start, End float64
			Why        string
		} `json:"add"`
	}
	if err := json.Unmarshal([]byte(clean), &out); err != nil {
		a.logfIdle(">>> audit answered with something that is not JSON — keeping the suggestion as it is")
		return segs
	}

	keep := make([]cutSeg, len(segs))
	copy(keep, segs)
	drop := make([]bool, len(segs))
	fixed, dropped := 0, 0
	for _, c := range out.Checks {
		i := c.I - 1
		if i < 0 || i >= len(segs) {
			continue // a number for a segment that was never proposed
		}
		switch strings.ToLower(strings.TrimSpace(c.Verdict)) {
		case "drop":
			drop[i] = true
			dropped++
			a.logfIdle("    audit − #%d [%s]–[%s]: %s", c.I, mmss(segs[i].S), mmss(segs[i].E), c.Why)
		case "fix":
			if c.End <= c.Start {
				continue
			}
			if c.Start == segs[i].S && c.End == segs[i].E {
				continue // "fix" with nothing changed is an ok
			}
			a.logfIdle("    audit ~ #%d [%s]–[%s] → [%s]–[%s] (%+.0f s): %s", c.I,
				mmss(segs[i].S), mmss(segs[i].E), mmss(c.Start), mmss(c.End),
				(c.End-c.Start)-(segs[i].E-segs[i].S), c.Why)
			keep[i] = cutSeg{c.Start, c.End}
			fixed++
		}
	}
	var res []cutSeg
	for i, s := range keep {
		if !drop[i] {
			res = append(res, s)
		}
	}
	for _, ad := range out.Add {
		if ad.End <= ad.Start {
			continue
		}
		a.logfIdle("    audit + [%s]–[%s] (%.0f s): %s", mmss(ad.Start), mmss(ad.End), ad.End-ad.Start, ad.Why)
		res = append(res, cutSeg{ad.Start, ad.End})
	}
	if fixed+dropped+len(out.Add) == 0 {
		a.logfIdle(">>> audit: all %d segments pass, nothing changed", len(segs))
		return segs
	}

	res = a.keepFilmed(res)
	sort.Slice(res, func(i, j int) bool { return res[i].S < res[j].S })
	// the audit is told not to overlap and mostly does not; where extending one
	// segment reached into the next, one longer segment is what was meant
	var merged []cutSeg
	for _, s := range res {
		if n := len(merged); n > 0 && s.S <= merged[n-1].E {
			if s.E > merged[n-1].E {
				merged[n-1].E = s.E
			}
			continue
		}
		merged = append(merged, s)
	}
	total := 0.0
	for _, s := range merged {
		total += s.E - s.S
	}
	if len(merged) < 4 || total < target*0.6 || total > target*1.5 {
		a.logfIdle(">>> audit result rejected (%d segments, %.0f s against a %.0f s target) — "+
			"keeping the suggestion as it is", len(merged), total, target)
		return segs
	}
	a.logfIdle(">>> audit: %d fixed, %d dropped, %d added — %d segments, %d:%02d total",
		fixed, dropped, len(out.Add), len(merged), int(total)/60, int(total)%60)
	return merged
}

// keepFilmed drops what cannot be shown: a segment with no recording at either
// end is time nobody has footage of. Both the suggestion and the audit go
// through it, so neither can put a hole in the video.
func (a *App) keepFilmed(segs []cutSeg) []cutSeg {
	var out []cutSeg
	for _, s := range segs {
		if a.ed.videoAt(s.S) == nil && a.ed.videoAt(s.E) == nil {
			continue
		}
		out = append(out, s)
	}
	return out
}

func (a *App) suggestCut(session string, target float64) ([]cutSeg, error) {
	system := a.prompt("cut")
	user := a.ctxBlock() + fmt.Sprintf("TARGET LENGTH: %.0f seconds.\n\nSESSION TIMELINE:\n%s", target, session)
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
			var segs []cutSeg
			for _, s := range out.Segments {
				if s.End <= s.Start {
					problem = "segment with end before start"
					break
				}
				segs = append(segs, cutSeg{s.Start, s.End})
			}
			// only video-backed time counts, and it is counted after the drop:
			// a suggestion that spent half its length on stretches nobody
			// filmed is short, and being told the number it actually landed on
			// is what makes the next attempt aim elsewhere
			asked := len(segs)
			segs = a.keepFilmed(segs)
			if n := asked - len(segs); n > 0 {
				a.logfIdle(">>> suggest attempt %d: %d segment(s) dropped for having no footage", try+1, n)
			}
			total := 0.0
			for _, s := range segs {
				total += s.E - s.S
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
