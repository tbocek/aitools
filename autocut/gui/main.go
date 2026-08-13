package main

// Workflow console for the autocut pipeline. Seven steps live in a sidebar,
// each gated on the previous one's output, with a shared run bar and log at the
// bottom: 1 inputs + STT + frames, 2 describe the frames, 3 fix the transcripts
// into one session timeline, 4 cut, 5 pick the voice, 6 narrate, 7 produce the
// upload.
//
// The step numbers are the sidebar's, not the disk's: output folders keep the
// names they were written under (step5/ = voice + narration, step6/ = the
// render), so a project made before the voice step existed still opens.
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
	root     string // autocut directory (inputs and scripts live here)
	outDir   string // where step outputs go; default root, project-settable
	voiceDir string // where the voice transcript lives
	videoDir string // where the video transcript / edl / clips live
	source   string // the input video file

	win         *gtk.ApplicationWindow
	stack       *gtk.Stack
	side        *gtk.ListBox  // custom sidebar: rows can gray out with a hint
	sideGuard   bool          // suppresses re-entrant selection while reverting
	step2Locked bool          // step 1 has not produced inputs for step 2 yet
	step3Locked bool          // no event logs yet -- nothing to fix against
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
	outLabel  *gtk.Label
	s1out     *gtk.Label
	s2info    *gtk.Label
	s2out     *gtk.Label
	s2hints   *gtk.TextView
	s3info    *gtk.Label
	s3out     *gtk.Label
	s3hints   *gtk.TextView
	ed        *cutEditor
	voice5    *voicePicker
	voiceSel  string  // the chosen voice id, cached from step5/voice.txt
	pitchSel  float64 // semitones the reference is shifted by, from step5/pitch.txt
	pitchRead bool    // ...and whether that file has been read yet (0 is a real value)
	narr5     *narrator
	prod      *producer
	progress  *gtk.ProgressBar
	playBtn   *gtk.Button
	pauseBtn  *gtk.Button
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
	pitchNow  atomic.Uint64 // float64 bits: the pitch slider, readable off-thread
	srcMu     sync.Mutex
	selVid    []string // snapshot of the checked sources, taken on the GUI
	selAud    []string // thread when a run starts

	// one progress bar fed by both tracks: summed fractions, joined texts
	progMu    sync.Mutex
	progParts [2]float64
	progTexts [2]string
}

// The frame slider snaps to these stops; a linear 0.1..5 s scale would cram
// all the useful low end into the first pixel. Index 0 keeps every frame.
var frameStops = []float64{0, 0.1, 0.2, 0.5, 1, 2, 3, 4, 5}
var frameStopLabels = []string{"frame", "0.1", "0.2", "0.5", "1s", "2s", "3s", "4s", "5s"}

// Frame size presets; the first is the width the vision model gets fed.
var scalePresets = []struct{ Name, VF string }{
	{"896w (LLM)", "scale=896:-2"},
	{"480p", "scale=-2:480"},
	{"720p", "scale=-2:720"},
	{"1080p", "scale=-2:1080"},
	{"original", ""},
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
		if p.Name == name {
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
	// every page shows state derived from the output folder -- refresh all
	a.updateStep1Info()
	a.updateStep2Info()
	a.updateStep3Info()
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

	a.win = gtk.NewApplicationWindow(app)
	a.win.SetTitle("autocut")
	a.win.SetDefaultSize(1500, 950)

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
	a.playBtn = gtk.NewButtonFromIconName("media-playback-start-symbolic")
	a.playBtn.AddCSSClass("suggested-action")
	a.playBtn.SetTooltipText("Run the current step, or resume when paused")
	a.playBtn.ConnectClicked(a.playClicked)
	a.pauseBtn = gtk.NewButtonFromIconName("media-playback-pause-symbolic")
	a.pauseBtn.SetTooltipText("Pause after the current stage")
	a.pauseBtn.SetSensitive(false)
	a.pauseBtn.ConnectClicked(a.pauseClicked)
	a.stopBtn = gtk.NewButtonFromIconName("media-playback-stop-symbolic")
	a.stopBtn.SetTooltipText("Stop the run — finished work is kept")
	a.stopBtn.SetSensitive(false)
	a.stopBtn.ConnectClicked(a.stopClicked)
	a.progress = gtk.NewProgressBar()
	a.progress.SetShowText(true)
	a.progress.SetText("nothing running")
	a.progress.SetHExpand(true)
	a.progress.SetVAlign(gtk.AlignCenter)

	a.stack = gtk.NewStack()
	a.stack.SetTransitionType(gtk.StackTransitionTypeCrossfade)
	a.stack.AddNamed(a.buildStep1(), "step1")
	a.stack.AddNamed(a.buildStep2(), "step2")
	a.stack.AddNamed(a.buildStep3(), "step3")
	a.stack.AddNamed(a.buildStep4(), "step4")
	a.stack.AddNamed(a.buildStep5Voice(), "voice")
	a.stack.AddNamed(a.buildStep5(), "narrate")
	a.stack.AddNamed(a.buildStep6(), "produce")

	// custom sidebar instead of GtkStackSidebar: its rows can gray out and
	// carry a hover hint, which the stock widget cannot do
	a.side = gtk.NewListBox()
	a.side.AddCSSClass("navigation-sidebar")
	a.side.SetSelectionMode(gtk.SelectionSingle)
	for _, title := range []string{"1 · Inputs & STT", "2 · Describe", "3 · Transcript", "4 · Cut", "5 · Voice", "6 · Narrate", "7 · Produce"} {
		l := gtk.NewLabel(title)
		l.SetXAlign(0)
		l.SetMarginTop(8)
		l.SetMarginBottom(8)
		l.SetMarginStart(6)
		a.side.Append(l)
	}
	pageNames := []string{"step1", "step2", "step3", "step4", "voice", "narrate", "produce"}
	a.side.ConnectRowSelected(func(row *gtk.ListBoxRow) {
		if row == nil || a.sideGuard {
			return
		}
		// the voice step (4) is never gated: picking and hearing a reference
		// voice needs nothing from the earlier steps except for "my own voice",
		// which says so itself when the diarization is missing
		locked := map[int]bool{1: a.step2Locked, 2: a.step3Locked, 3: a.step4Locked,
			5: a.narrLocked, 6: a.prodLocked}
		if locked[row.Index()] {
			a.sideGuard = true
			a.side.SelectRow(a.side.RowAtIndex(0))
			a.sideGuard = false
			a.setStatus("finish the earlier steps first")
			return
		}
		a.stack.SetVisibleChildName(pageNames[row.Index()])
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
	ctlRow.Append(a.pauseBtn)
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
	a.step3Locked = true
	if a.vidList != nil {
		for _, v := range a.vidList.selected() {
			if exists(filepath.Join(a.outDir, "step2", baseName(v), "events.tsv")) {
				a.step3Locked = false
				break
			}
		}
	}
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
	setRow(1, a.step2Locked, "Finish step 1 first — this step needs its transcripts and frames")
	setRow(2, a.step3Locked, "Finish step 2 first — the fixer grounds lines in the event logs")
	setRow(3, a.step4Locked, "Finish step 3 first — the cut works on the session timeline")
	setRow(5, a.narrLocked, "Finish step 4 first — narration is written for the cut's clips")
	setRow(6, a.prodLocked, "Finish step 4 first — there is no cut to produce a video from")
	name := a.stack.VisibleChildName()
	if (name == "step2" && a.step2Locked) || (name == "step3" && a.step3Locked) ||
		(name == "step4" && a.step4Locked) || (name == "narrate" && a.narrLocked) ||
		(name == "produce" && a.prodLocked) {
		a.sideGuard = true
		a.side.SelectRow(a.side.RowAtIndex(0))
		a.sideGuard = false
		a.stack.SetVisibleChildName("step1")
	}
}

func (a *App) buildStep1() gtk.Widgetter {
	a.vidList = newSourceList(a.root, "input_video", nil)
	a.audList = newSourceList(a.root, "input_audio", nil)

	// output folder row, first: it applies to every step. Buttons lead, the
	// path follows -- an expanding path in the middle would push Choose to
	// the far edge, away from what it changes.
	choose := gtk.NewButtonWithLabel("Choose…")
	choose.ConnectClicked(a.chooseOutDirDialog)
	openBtn := gtk.NewButtonFromIconName("folder-open-symbolic")
	openBtn.SetTooltipText("Open the output folder")
	openBtn.ConnectClicked(func() { a.openFolder(a.outDir) })
	a.outLabel = gtk.NewLabel(a.outDir)
	a.outLabel.SetXAlign(0)
	a.outLabel.SetHExpand(true)
	a.outLabel.SetEllipsize(pango.EllipsizeStart)
	outRow := gtk.NewBox(gtk.OrientationHorizontal, 8)
	outRow.Append(choose)
	outRow.Append(openBtn)
	outRow.Append(gtk.NewLabel("Output folder:"))
	outRow.Append(a.outLabel)

	srcPane := func(title string, s *sourceList, under gtk.Widgetter) gtk.Widgetter {
		l := gtk.NewLabel(title)
		l.SetXAlign(0)
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

	// the frame slider belongs to the videos, so it sits under their box;
	// discrete stops -- a linear scale would bury 0.1..0.5 in one pixel
	a.interval = gtk.NewScaleWithRange(gtk.OrientationHorizontal,
		0, float64(len(frameStops)-1), 1)
	a.interval.SetDrawValue(false)
	a.interval.SetHExpand(true)
	for i, l := range frameStopLabels {
		a.interval.AddMark(float64(i), gtk.PosBottom, l)
	}
	a.setFrameInterval(1)
	ivRow := gtk.NewBox(gtk.OrientationHorizontal, 8)
	ivRow.Append(gtk.NewLabel("Frame every:"))
	ivRow.Append(a.interval)

	names := make([]string, len(scalePresets))
	for i, p := range scalePresets {
		names[i] = p.Name
	}
	a.scalePick = gtk.NewDropDownFromStrings(names)
	a.setFrameScale("original")
	sizeRow := gtk.NewBox(gtk.OrientationHorizontal, 8)
	sizeRow.Append(gtk.NewLabel("Frame size:"))
	sizeRow.Append(a.scalePick)
	vidOpts := gtk.NewBox(gtk.OrientationVertical, 6)
	vidOpts.Append(ivRow)
	vidOpts.Append(sizeRow)

	sources := gtk.NewBox(gtk.OrientationHorizontal, 12)
	sources.SetVExpand(true)
	sources.SetHomogeneous(true)
	sources.Append(srcPane("Input videos — checked play top to bottom", a.vidList, vidOpts))
	sources.Append(srcPane("Voice recordings — checked, in order", a.audList, nil))

	a.s1out = gtk.NewLabel("")
	a.s1out.SetXAlign(0)
	a.s1out.SetYAlign(0)
	a.s1out.AddCSSClass("monospace")
	// sized to its content up to a cap, so the source lists keep the page's
	// spare height instead of splitting it with a mostly-empty listing
	outScroll := gtk.NewScrolledWindow()
	outScroll.SetChild(a.s1out)
	outScroll.SetPropagateNaturalHeight(true)
	outScroll.SetMaxContentHeight(200)
	outLbl := gtk.NewLabel("Outputs")
	outLbl.SetXAlign(0)
	outLbl.AddCSSClass("heading")

	a.updateStep1Info()

	box := gtk.NewBox(gtk.OrientationVertical, 12)
	box.SetMarginTop(16)
	box.SetMarginStart(16)
	box.SetMarginEnd(16)
	box.SetMarginBottom(8)
	box.Append(outRow)
	box.Append(sources)
	box.Append(outLbl)
	box.Append(outScroll)
	return box
}

func (a *App) rescanAll() {
	a.vidList.rescan()
	a.audList.rescan()
	a.loadMeta()
	a.updateGates()
	a.updateStep1Info()
	a.updateStep2Info()
	a.updateStep3Info()
	a.updateStep4Info()
	a.updateStep6Info()
	a.setStatus("rescanned")
}

func (a *App) updateStep1Info() {
	if a.s1out == nil || a.progress == nil {
		return // page not built yet
	}
	s1 := filepath.Join(a.outDir, "step1")
	a.s1out.SetText(describeOutputs(s1))
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
			out[i] = filepath.Join(a.root, r)
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
