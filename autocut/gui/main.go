package main

// Workflow console for the autocut pipeline. Six steps live in a sidebar, each
// gated on the previous one's output, with a shared run bar and log at the
// bottom: 1 inputs (sources, STT, frames), 2 the narration voice, 3 describe
// the frames and fix the transcripts into one session timeline, 4 cut,
// 5 narrate, 6 produce the upload.
//
// The voice has a step of its own because ▶ in the run bar starts the step on
// screen, and the voice starts nothing -- it plays a sample. Sharing a page
// with the sources made one button mean two things.
//
// The step numbers are the sidebar's, not the disk's: output folders keep the
// names they were written under (step2/ = the event logs, step3/ = the fixed
// transcripts even though one page now runs both, step5/ = voice + narration,
// step6/ = the render), so a project made under an older numbering still opens.
//
//   cd autocut && ./gui/autocut-gui
//
// Everything is written under the output folder (default: the autocut
// directory), one stepN/ per step, so a run can resume where it stopped.

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotk4/pkg/pango"
)

var mediaExt = map[string]bool{
	".flac": true, ".wav": true, ".mp3": true, ".m4a": true, ".aac": true,
	".ogg": true, ".opus": true, ".wma": true, ".mp4": true, ".mkv": true,
	".mov": true, ".webm": true, ".avi": true, ".ts": true,
}

type App struct {
	root     string // autocut directory (the scripts and project files live here)
	inDir    string // holds input_video/ and input_audio/; default root, project-settable
	outDir   string // where step outputs go; default root, project-settable
	voiceDir string // where the voice transcript lives
	videoDir string // where the video transcript / edl / clips live
	source   string // the input video file

	win         *gtk.ApplicationWindow
	stack       *gtk.Stack
	side        *gtk.ListBox  // custom sidebar: rows can gray out with a hint
	sideGuard   bool          // suppresses re-entrant selection while reverting
	step2Locked bool          // step 1 has not produced inputs for Describe yet
	step4Locked bool          // no session timeline yet -- nothing to cut
	narrLocked  bool          // no cut yet -- nothing to narrate
	prodLocked  bool          // no cut yet -- nothing to produce
	logExp      *gtk.Expander // collapsed until something actually runs
	player      *Player       // the step 6 preview
	log         *gtk.TextView
	status      *gtk.Label
	running     bool
	ttsNoted    string // the audio.cpp server already reported in the log
	ttsModel    string // the model id that server serves, asked for once

	// step 1 page
	vidList   *sourceList
	audList   *sourceList
	interval  *gtk.Scale
	scalePick *gtk.DropDown
	inLabel   *gtk.Label
	outLabel  *gtk.Label
	s1out     *gtk.Label
	und       *understander
	ed        *cutEditor
	voice5    *voicePicker
	voiceSel  string  // the chosen voice id, cached from step5/voice.txt
	pitchSel  float64 // semitones the reference is shifted by, from step5/pitch.txt
	pitchRead bool    // ...and whether that file has been read yet (0 is a real value)
	narr5     *narrator
	prod      *producer
	progress  *gtk.ProgressBar
	playBtn   *gtk.Button
	stopBtn   *gtk.Button

	// pipeline control: pause parks the runners at the next checkpoint, stop
	// kills the in-flight subprocesses; finished stages stay on disk either
	// way. STT and frame extraction run as parallel tracks (GPU vs CPU), so
	// several subprocesses can be live at once.
	ctlMu     sync.Mutex
	curCmds   map[*exec.Cmd]bool
	stopFlag  atomic.Bool
	pauseFlag atomic.Bool
	runCtx    context.Context // canceled by stop -- aborts in-flight LLM calls
	runCancel context.CancelFunc
	srcMu     sync.Mutex
	selVid    []string // snapshot of the checked sources, taken on the GUI
	selAud    []string // thread when a run starts

	// the editable system prompts. The views are the GUI thread's; promptTxt is
	// the copy a runner reads, kept current by the buffers' changed handler --
	// same rule as selVid/selAud, for the same reason.
	promptMu    sync.Mutex
	promptTxt   map[string]string
	promptViews map[string]*gtk.TextView

	// one progress bar fed by both tracks: summed fractions, joined texts
	progMu    sync.Mutex
	progParts [2]float64
	progTexts [2]string
}

// The frame slider snaps to these stops; a linear 0.1..5 s scale would cram
// all the useful low end into the first pixel. Index 0 keeps every frame.
var frameStops = []float64{0, 0.1, 0.2, 0.5, 1, 2, 3, 4, 5}
var frameStopLabels = []string{"each", "0.1", "0.2", "0.5", "1s", "2s", "3s", "4s", "5s"}

// Frame size presets, no-resize first because that is the default and picking a
// size is the exception. Name is the identity and Label is only what the drop
// down shows: the name goes into the project file AND into the stamp that
// decides whether frames must be extracted again, so it has to stay put even
// when the wording moves. 896w is the width the vision model gets fed.
var scalePresets = []struct{ Name, Label, VF string }{
	{"original", "Resize", ""},
	{"896w (LLM)", "896w (LLM)", "scale=896:-2"},
	{"480p", "480p", "scale=-2:480"},
	{"720p", "720p", "scale=-2:720"},
	{"1080p", "1080p", "scale=-2:1080"},
}

func (a *App) frameScale() (name, vf string) {
	i := int(a.scalePick.Selected())
	if i < 0 || i >= len(scalePresets) {
		i = 0
	}
	return scalePresets[i].Name, scalePresets[i].VF
}

func (a *App) setFrameScale(name string) {
	for i, p := range scalePresets {
		if p.Name == name || p.Label == name {
			a.scalePick.SetSelected(uint(i))
			return
		}
	}
}

func (a *App) frameInterval() float64 {
	i := int(math.Round(a.interval.Value()))
	if i < 0 {
		i = 0
	}
	if i >= len(frameStops) {
		i = len(frameStops) - 1
	}
	return frameStops[i]
}

func (a *App) setFrameInterval(v float64) {
	best, bd := 4, math.MaxFloat64 // default 1 s
	for i, s := range frameStops {
		if d := math.Abs(s - v); d < bd {
			best, bd = i, d
		}
	}
	a.interval.SetValue(float64(best))
}

// srcPath resolves a source path -- always stored as input_video/x.mkv, i.e.
// relative to the INPUT folder, which is the root only until someone moves it.
func (a *App) srcPath(rel string) string { return filepath.Join(a.inDir, rel) }

// setInDir repoints both source lists at a new folder. The lists start empty
// rather than merged: a checkmark carried over to a same-named file in another
// folder would be a guess about which recording is meant, and a wrong guess
// there is silent -- the wrong video renders.
func (a *App) setInDir(dir string) {
	a.inDir = dir
	if a.inLabel != nil {
		a.inLabel.SetText(dir)
	}
	if a.vidList != nil {
		a.vidList.setRoot(dir)
	}
	if a.audList != nil {
		a.audList.setRoot(dir)
	}
	a.updateStep1Info()
	a.updateGates()
}

func (a *App) setOutDir(dir string) {
	a.outDir = dir
	if a.outLabel != nil {
		a.outLabel.SetText(dir)
	}
	a.loadMeta()
	a.followOutDir()
	// the voice belongs to the project, not to the session: drop the cached id
	// and pitch so both are re-read from the folder we just moved to
	a.voiceSel = ""
	a.pitchRead = false
	if a.voice5 != nil {
		a.voice5.syncSelection()
	}
	// The narration lives in the output folder, and this is the only thing that
	// re-reads it: the page is built once at startup, when outDir is still the
	// root, so without this a saved narration would only ever load for a project
	// whose output IS the root -- it looked like narration was never saved.
	if a.narr5 != nil {
		a.narr5.load()
		a.narr5.rebuildRows()
	}
	// every page shows state derived from the output folder -- refresh all
	a.updateStep1Info()
	a.und.refresh()
	a.updateStep6Info()
	a.updateGates()
}

func main() {
	a := &App{curCmds: map[*exec.Cmd]bool{}}
	wd, _ := os.Getwd()
	if _, err := os.Stat(filepath.Join(wd, "input_video")); err != nil {
		// started elsewhere: the binary lives in <root>/gui
		exe, _ := os.Executable()
		wd = filepath.Dir(filepath.Dir(exe))
	}
	a.root = wd
	a.inDir = wd
	a.outDir = wd
	a.loadMeta() // adopts step1/ dirs when step 1 has run before

	app := gtk.NewApplication("li.jos.autocut", gio.ApplicationFlagsNone)
	app.ConnectActivate(func() { a.build(app) })
	os.Exit(app.Run(nil))
}

// ---- state -----------------------------------------------------------------

// loadMeta reads step1/meta.env and points the later stages at step1/'s dirs.
func (a *App) loadMeta() map[string]string {
	m := map[string]string{}
	b, err := os.ReadFile(filepath.Join(a.outDir, "step1", "meta.env"))
	if err != nil {
		return m
	}
	for _, line := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			m[k] = v
		}
	}
	if m["AUDIO_BASE"] != "" && m["VIDEO_BASE"] != "" {
		a.voiceDir = filepath.Join(a.outDir, "step1", m["AUDIO_BASE"])
		a.videoDir = filepath.Join(a.outDir, "step1", m["VIDEO_BASE"])
		a.source = m["VIDEO_FILE"]
	}
	return m
}

// snapSources caches the checked sources. Background runners must never touch
// the list widgets -- GTK objects belong to the GUI thread -- so every run
// takes this snapshot first and works from it.
func (a *App) snapSources() (vids, auds []string) {
	if a.vidList == nil || a.audList == nil {
		return a.snappedSources()
	}
	vids, auds = a.vidList.selected(), a.audList.selected()
	a.srcMu.Lock()
	a.selVid, a.selAud = vids, auds
	a.srcMu.Unlock()
	return
}

func (a *App) snappedSources() (vids, auds []string) {
	a.srcMu.Lock()
	defer a.srcMu.Unlock()
	return a.selVid, a.selAud
}

func listMedia(dir string) []string {
	ents, _ := os.ReadDir(dir)
	var out []string
	for _, e := range ents {
		if !e.IsDir() && mediaExt[strings.ToLower(filepath.Ext(e.Name()))] {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// ---- UI --------------------------------------------------------------------

func (a *App) build(app *gtk.Application) {
	// activate fires again when a second launch forwards to this instance;
	// building twice would spawn extra windows that start playing on their own
	if a.win != nil {
		a.win.Present()
		return
	}
	var err error
	a.player, err = NewPlayer()
	if err != nil {
		fmt.Fprintln(os.Stderr, "player:", err)
		os.Exit(1)
	}
	a.player.OnState = a.updateRunControls

	a.win = gtk.NewApplicationWindow(app)
	a.win.SetTitle("autocut")
	// fits a 1366x768 laptop with room for the panel: every page is either
	// scrolled or split by a divider, so a bigger screen is worth more space
	// but no page depends on having it
	a.win.SetDefaultSize(1240, 740)

	head := gtk.NewHeaderBar()
	loadP := gtk.NewButtonWithLabel("Load Project")
	loadP.ConnectClicked(a.loadProjectDialog)
	saveP := gtk.NewButtonWithLabel("Save Project")
	saveP.ConnectClicked(a.saveProjectDialog)
	head.PackStart(loadP)
	head.PackStart(saveP)
	// every step needs a rescan after files change on disk, so it lives here
	rescan := gtk.NewButtonFromIconName("view-refresh-symbolic")
	rescan.SetTooltipText("Rescan inputs and outputs")
	rescan.ConnectClicked(a.rescanAll)
	head.PackEnd(rescan)
	setup := gtk.NewButtonFromIconName("preferences-system-symbolic")
	setup.SetTooltipText("Settings — the LLM and audio.cpp endpoints")
	setup.ConnectClicked(a.setupDialog)
	head.PackEnd(setup)
	a.win.SetTitlebar(head)

	// run controls exist BEFORE the pages: page builders refresh their info
	// texts during construction, and those touch the shared progress bar
	// ▶ is play and pause both -- it draws itself from what is under way, and
	// what is under way may be a run or this page's playback. See transport.
	a.playBtn = gtk.NewButtonFromIconName("media-playback-start-symbolic")
	a.playBtn.AddCSSClass("suggested-action")
	a.playBtn.SetTooltipText("Run this step — or resume what is paused")
	a.playBtn.ConnectClicked(a.playClicked)
	a.stopBtn = gtk.NewButtonFromIconName("media-playback-stop-symbolic")
	a.stopBtn.SetTooltipText("Stop the run or the playback — finished work is kept")
	a.stopBtn.SetSensitive(false)
	a.stopBtn.ConnectClicked(a.stopClicked)
	a.progress = gtk.NewProgressBar()
	a.progress.SetShowText(true)
	a.progress.SetText("nothing running")
	a.progress.SetHExpand(true)
	a.progress.SetVAlign(gtk.AlignCenter)

	a.stack = gtk.NewStack()
	a.stack.SetTransitionType(gtk.StackTransitionTypeCrossfade)
	// each page asks for its own size, not for the largest page's. Homogeneous
	// (the GTK default) means the timeline on the cut step sets the floor for
	// every other page too, and the whole window inherits it -- so a page that
	// would fit a small screen on its own is clipped because a page you are not
	// looking at would not.
	a.stack.SetHhomogeneous(false)
	a.stack.SetVhomogeneous(false)
	a.stack.AddNamed(a.buildStep1(), "step1")
	a.stack.AddNamed(a.buildStep5Voice(), "voice")
	a.stack.AddNamed(a.buildUnderstand(), "understand")
	a.stack.AddNamed(a.buildStep4(), "step4")
	a.stack.AddNamed(a.buildStep5(), "narrate")
	a.stack.AddNamed(a.buildStep6(), "produce")

	// custom sidebar instead of GtkStackSidebar: its rows can gray out and
	// carry a hover hint, which the stock widget cannot do
	a.side = gtk.NewListBox()
	a.side.AddCSSClass("navigation-sidebar")
	a.side.SetSelectionMode(gtk.SelectionSingle)
	for _, title := range []string{"1) Inputs / STT / Frames", "2) Narration Voice",
		"3) Describe + Transcript", "4) Cut", "5) Narrate", "6) Produce"} {
		l := gtk.NewLabel(title)
		l.SetXAlign(0)
		l.SetMarginTop(8)
		l.SetMarginBottom(8)
		l.SetMarginStart(6)
		a.side.Append(l)
	}
	pageNames := []string{"step1", "voice", "understand", "step4", "narrate", "produce"}
	a.side.ConnectRowSelected(func(row *gtk.ListBoxRow) {
		if row == nil || a.sideGuard {
			return
		}
		// the voice step (1) is never gated: picking and hearing a reference
		// voice needs nothing from the earlier steps except for "my own voice",
		// which says so itself when the diarization is missing
		locked := map[int]bool{2: a.step2Locked, 3: a.step4Locked,
			4: a.narrLocked, 5: a.prodLocked}
		if locked[row.Index()] {
			a.sideGuard = true
			a.side.SelectRow(a.side.RowAtIndex(0))
			a.sideGuard = false
			a.setStatus("finish the earlier steps first")
			return
		}
		a.stack.SetVisibleChildName(pageNames[row.Index()])
		a.updateRunControls() // ▶ ⏹ belong to the new page's playback now
	})
	a.side.SelectRow(a.side.RowAtIndex(0))
	a.side.SetSizeRequest(170, -1)

	// shared log + status across all pages: one bottom row, the status text
	// living in the expander header so nothing reserves empty space
	a.log = gtk.NewTextView()
	a.log.SetEditable(false)
	a.log.SetMonospace(true)
	logScroll := gtk.NewScrolledWindow()
	logScroll.SetChild(a.log)
	logScroll.SetSizeRequest(-1, 220)
	a.status = gtk.NewLabel("")
	a.status.SetXAlign(1) // status lives right-aligned in the free header space
	a.status.SetHExpand(true)
	a.status.SetEllipsize(pango.EllipsizeEnd)
	a.status.AddCSSClass("dim-label")
	a.status.SetMarginEnd(8)
	logLbl := gtk.NewLabel("Log")
	head2 := gtk.NewBox(gtk.OrientationHorizontal, 12)
	head2.SetHExpand(true)
	head2.Append(logLbl)
	head2.Append(a.status)
	a.logExp = gtk.NewExpander("")
	a.logExp.SetLabelWidget(head2)
	a.logExp.SetChild(logScroll)
	a.logExp.SetMarginStart(8)
	a.logExp.SetMarginEnd(8)
	a.logExp.SetMarginTop(2)
	a.logExp.SetMarginBottom(2)

	// the shared bottom bar: run controls act on the visible step
	ctlRow := gtk.NewBox(gtk.OrientationHorizontal, 8)
	ctlRow.SetMarginStart(8)
	ctlRow.SetMarginEnd(8)
	ctlRow.SetMarginTop(4)
	ctlRow.SetMarginBottom(2)
	ctlRow.Append(a.playBtn)
	ctlRow.Append(a.stopBtn)
	ctlRow.Append(a.progress)

	body := gtk.NewPaned(gtk.OrientationHorizontal)
	body.SetStartChild(a.side)
	body.SetEndChild(a.stack)
	body.SetPosition(170)
	body.SetVExpand(true)

	outer := gtk.NewBox(gtk.OrientationVertical, 0)
	outer.Append(body)
	// a hard edge above the control/log rows, so the sidebar and page ending
	// there reads as a status bar rather than as widgets stopping mid-air
	outer.Append(gtk.NewSeparator(gtk.OrientationHorizontal))
	outer.Append(ctlRow)
	outer.Append(a.logExp)
	a.win.SetChild(outer)

	// pick up where the last session left off
	if pj := filepath.Join(a.root, "project.json"); exists(pj) {
		a.loadProjectFrom(pj)
	}

	a.updateGates()
	a.updateStep4Info()
	a.updateStep6Info()
	a.win.SetVisible(true)
}

// updateGates grays sidebar rows whose prerequisites are missing, with
// the reason on hover; clicking a locked row just bounces back.
func (a *App) updateGates() {
	if a.side == nil {
		return
	}
	a.step2Locked = a.loadMeta()["VIDEO_BASE"] == ""
	setRow := func(idx int, locked bool, hint string) {
		row := a.side.RowAtIndex(idx)
		w := gtk.BaseWidget(row.Child())
		if locked {
			w.AddCSSClass("dim-label")
			row.SetTooltipText(hint)
		} else {
			w.RemoveCSSClass("dim-label")
			row.SetTooltipText("")
		}
	}
	a.step4Locked = !exists(filepath.Join(a.outDir, "step3", "session.tsv"))
	a.narrLocked = !exists(a.cutPath())
	a.prodLocked = !exists(a.cutPath())
	// the hints name the step rather than its number: the numbering has moved
	// twice already, and a tooltip pointing at the wrong row is worse than none
	setRow(2, a.step2Locked, "Finish Inputs first — this step needs its transcripts and frames")
	setRow(3, a.step4Locked, "Finish Describe + Transcript first — the cut works on the session timeline")
	setRow(4, a.narrLocked, "Finish Cut first — narration is written for the cut's clips")
	setRow(5, a.prodLocked, "Finish Cut first — there is no cut to produce a video from")
	// Describe used to gate Transcript from the sidebar. They share a page now,
	// so the ordering between them is the page's business, not a locked row's.
	name := a.stack.VisibleChildName()
	if (name == "understand" && a.step2Locked) ||
		(name == "step4" && a.step4Locked) || (name == "narrate" && a.narrLocked) ||
		(name == "produce" && a.prodLocked) {
		a.sideGuard = true
		a.side.SelectRow(a.side.RowAtIndex(0))
		a.sideGuard = false
		a.stack.SetVisibleChildName("step1")
	}
}

func (a *App) buildStep1() gtk.Widgetter {
	a.vidList = newSourceList(a.inDir, "input_video", nil)
	// the voice step's "my own voice" row names the recording it would be cut
	// from, so it has to be told when that stops being the same file
	a.audList = newSourceList(a.inDir, "input_audio", func() {
		if a.voice5 != nil {
			a.voice5.refreshOwn()
		}
	})

	// the two folder rows, first: they apply to every step. Buttons lead, the
	// path follows -- an expanding path in the middle would push Choose to
	// the far edge, away from what it changes.
	dirRow := func(caption, tip, dir string, choose func(), open func()) (*gtk.Box, *gtk.Label) {
		btn := gtk.NewButtonWithLabel("Choose…")
		btn.ConnectClicked(choose)
		openBtn := gtk.NewButtonFromIconName("folder-open-symbolic")
		openBtn.SetTooltipText(tip)
		openBtn.ConnectClicked(open)
		lbl := gtk.NewLabel(dir)
		lbl.SetXAlign(0)
		lbl.SetHExpand(true)
		lbl.SetEllipsize(pango.EllipsizeStart)
		row := gtk.NewBox(gtk.OrientationHorizontal, 8)
		row.Append(btn)
		row.Append(openBtn)
		row.Append(gtk.NewLabel(caption))
		row.Append(lbl)
		return row, lbl
	}
	var inRow, outRow *gtk.Box
	inRow, a.inLabel = dirRow("Input folder:", "Open the input folder", a.inDir,
		a.chooseInDirDialog, func() { a.openFolder(a.inDir) })
	outRow, a.outLabel = dirRow("Output folder:", "Open the output folder", a.outDir,
		a.chooseOutDirDialog, func() { a.openFolder(a.outDir) })
	// stacked, not abreast: side by side, the pair demanded twice the width of
	// the widest row before anything could shrink, and a narrow window spent it
	// on the one thing that cannot be ellipsized away -- the buttons -- leaving
	// the path cut off at both ends
	dirs := gtk.NewBox(gtk.OrientationVertical, 6)
	dirs.Append(inRow)
	dirs.Append(outRow)

	srcPane := func(title string, s *sourceList, under gtk.Widgetter) gtk.Widgetter {
		l := gtk.NewLabel(title)
		l.SetXAlign(0)
		// the heading is a caption, not a constraint: unellipsized it sets a
		// floor under the whole pane, and the pane is the part meant to shrink
		l.SetEllipsize(pango.EllipsizeEnd)
		l.SetTooltipText(title)
		l.AddCSSClass("heading")
		sc := gtk.NewScrolledWindow()
		sc.SetChild(s.box)
		sc.SetVExpand(true)
		b := gtk.NewBox(gtk.OrientationVertical, 4)
		b.SetHExpand(true)
		b.Append(l)
		b.Append(sc)
		if under != nil {
			b.Append(under)
		}
		return b
	}

	// the frame controls belong to the videos, so they sit under their box, on
	// one row: how often, then how big. Discrete stops -- a linear scale would
	// bury 0.1..0.5 in one pixel.
	a.interval = gtk.NewScaleWithRange(gtk.OrientationHorizontal,
		0, float64(len(frameStops)-1), 1)
	a.interval.SetDrawValue(false)
	a.interval.SetHExpand(true)
	for i, l := range frameStopLabels {
		a.interval.AddMark(float64(i), gtk.PosBottom, l)
	}
	a.setFrameInterval(1)

	labels := make([]string, len(scalePresets))
	for i, p := range scalePresets {
		labels[i] = p.Label
	}
	a.scalePick = gtk.NewDropDownFromStrings(labels)
	a.scalePick.SetTooltipText("Left on Resize the frames keep the video's own size")
	a.setFrameScale("original")
	a.scalePick.SetVAlign(gtk.AlignCenter)

	vidOpts := gtk.NewBox(gtk.OrientationHorizontal, 8)
	vidOpts.Append(gtk.NewLabel("Freq:"))
	vidOpts.Append(a.interval)
	vidOpts.Append(a.scalePick)

	// a divider, not a fixed split: which list needs the width depends on how
	// long the filenames are, and that is the recorder's decision, not ours
	vidsPane := srcPane("Input videos — checked play top to bottom", a.vidList, vidOpts)
	audsPane := srcPane("Voice recordings — checked, in order", a.audList, nil)
	gtk.BaseWidget(vidsPane).SetMarginEnd(8) // air either side of the handle
	gtk.BaseWidget(audsPane).SetMarginStart(8)
	sources := gtk.NewPaned(gtk.OrientationHorizontal)
	sources.SetVExpand(true)
	sources.SetStartChild(vidsPane)
	sources.SetEndChild(audsPane)
	// shrink off, so the divider stops at what a pane actually needs. Left on
	// (the GTK default) a pane is allocated below its minimum and simply
	// clipped -- which is how the right-hand list came to run off the window
	// and take the page's margin with it.
	sources.SetShrinkStartChild(false)
	sources.SetShrinkEndChild(false)
	sources.SetPosition(620)

	// one line, not a listing: on a finished step 1 the listing is thousands of
	// frames, and the only questions it has to answer are how much is there and
	// whether it is from this run or last week's
	a.s1out = gtk.NewLabel("")
	a.s1out.SetXAlign(0)
	outLbl := gtk.NewLabel("Outputs:")
	outLbl.AddCSSClass("heading")
	outRow2 := gtk.NewBox(gtk.OrientationHorizontal, 8)
	outRow2.Append(outLbl)
	outRow2.Append(a.s1out)

	a.updateStep1Info()

	box := gtk.NewBox(gtk.OrientationVertical, 12)
	box.SetMarginTop(16)
	box.SetMarginStart(16)
	box.SetMarginEnd(16)
	box.SetMarginBottom(8)
	box.Append(dirs)
	box.Append(sources)
	box.Append(outRow2)
	return box
}

func (a *App) rescanAll() {
	a.vidList.rescan()
	a.audList.rescan()
	a.loadMeta()
	a.updateGates()
	a.updateStep1Info()
	a.und.refresh()
	a.updateStep4Info()
	a.updateStep6Info()
	a.setStatus("rescanned")
}

func (a *App) updateStep1Info() {
	if a.s1out == nil || a.progress == nil {
		return // page not built yet
	}
	s1 := filepath.Join(a.outDir, "step1")
	a.s1out.SetText(summarizeOutputs(s1))
	if a.running {
		return // the runner owns the progress text while active
	}
	frames, _ := os.ReadDir(filepath.Join(s1, "frames"))
	if len(frames) > 0 {
		a.progress.SetText(fmt.Sprintf("step1 ready (%d frame set(s))", len(frames)))
	} else {
		a.progress.SetText("step 1 has not run yet")
	}
}

func (a *App) step1Clicked() {
	if a.running {
		return
	}
	vids := a.vidList.selected()
	auds := a.audList.selected()
	if len(vids) == 0 || len(auds) == 0 {
		a.setStatus("select at least one video and one voice recording")
		return
	}
	// the working copy always reflects what actually ran
	a.saveProjectTo(filepath.Join(a.root, "project.json"))
	abs := func(rels []string) []string {
		out := make([]string, len(rels))
		for i, r := range rels {
			out[i] = a.srcPath(r)
		}
		return out
	}
	scaleName, scaleVF := a.frameScale()
	a.startStep1(abs(vids), abs(auds), a.frameInterval(), scaleName, scaleVF)
}

// ---- helpers ---------------------------------------------------------------

func (a *App) setStatus(s string) { a.status.SetText(s) }

func (a *App) logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...) // mirror to the launching terminal
	buf := a.log.Buffer()
	end := buf.EndIter()
	buf.Insert(end, fmt.Sprintf(format, args...)+"\n")
	mark := buf.CreateMark("", buf.EndIter(), false)
	a.log.ScrollToMark(mark, 0, false, 0, 1)
	buf.DeleteMark(mark)
}

func (a *App) logfIdle(format string, args ...any) {
	glib.IdleAdd(func() { a.logf(format, args...) })
}
