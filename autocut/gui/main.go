package main

// Workflow console for the autocut pipeline. Five steps, one per tab in the
// header bar, each gated on the previous one's output, with a shared run bar
// and log at the bottom: 1 inputs (sources, STT, frames), 2 describe the frames
// and fix the transcripts into one session timeline, 3 cut, 4 narrate -- the
// voice on top and its lines below -- and 5 produce the upload.
//
// The voice had a step of its own, before the narration and two rows from it,
// so the one thing you cannot judge about a voice -- how it reads THIS
// narration -- came up before there was any. It is the top third of the narrate
// page now, with its own ▶ and ⏹ for the sample so the run bar keeps meaning
// "run the step on screen".
//
// One folder per step, stepN/, numbered by the tab it belongs to. Step 2 runs
// two jobs and splits its folder between them by name, since a number would say
// nothing: step2/describe/ = the event logs, step2/transcript/ = the fixed
// transcripts, the subtitles and the session timeline.
//
//   cd autocut && ./gui/autocut-gui
//
// Everything is written under the output folder (default: the autocut
// directory), one folder per job, so a run can resume where it stopped.

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotk4/pkg/pango"
)

// steps is the pipeline in order, and the tab row is this table. The labels are
// short because five of them sit side by side in the header bar, where the
// width they ask for is a floor under the whole window -- what each step
// actually does is on hover, which is where the sidebar's longer titles went.
// wait is what the tab says instead while the step cannot be entered: it names
// the step to finish rather than its number, since the numbering has moved
// twice already and a hint pointing at the wrong tab is worse than none.
//
// help is the long form of tip, and it is the last of the paragraphs that used
// to sit at the top of each page. A page explains itself once and is then
// shorter by three lines forever: the paragraph is behind the ⓘ in the header
// bar, which shows the open page's and only that.
var steps = []struct{ name, label, tip, wait, help string }{
	{"step1", "Inputs", "The sources, their transcripts and the frames", "",
		"Add this session's files — footage, voice recordings, or one screen capture " +
			"that is both. Each row says what its file is for: the camera means frames " +
			"come out of it and it can be cut, and the microphone tags who is speaking, " +
			"1 being the voice the narration is spoken in. Running the step transcribes " +
			"everything in the list and pulls a frame out of the footage every few " +
			"seconds — that is what the later steps read instead of the video.\n\n" +
			"Language is what this session is spoken in, told to the speech-to-text " +
			"model: it belongs to the footage, so it is here and in the project rather " +
			"than in Settings, and each project carries its own."},
	{"step2", "Describe", "Describe the frames and fix the transcripts into one session timeline",
		"Finish Inputs first — this step needs its transcripts and frames",
		"Two model jobs over what Inputs produced: the frames are described, and the raw " +
			"transcripts are cleaned up and merged into one timeline covering the whole " +
			"session. Both prompts are on the page — they are what the models are told, " +
			"in full, and an edited one is kept in the project.\n\nThis is the long one, " +
			"so the two run buttons mean different things here. ⏸ parks it between " +
			"requests and ▶ carries on from the same frame; ⏹ ends it, and the next ▶ " +
			"describes the session from the beginning. Edit a prompt and it is ⏹ you " +
			"want — a resumed run would describe the rest of the footage under the new " +
			"wording and leave the first half under the old."},
	{"step3", "Cut", "Choose the clips the video is made of",
		"Finish Describe first — the cut works on the session timeline",
		"Two tracks over the session: the source above, the cut below. Suggest cut " +
			"asks the model to fill the target length from what Describe found; from then " +
			"on the cut is yours — drag to select, add, remove, and Revert goes back to " +
			"the suggestion. The scroll wheel zooms around the cursor.\n\nA left click " +
			"drops the red playhead; the clock beside the transport buttons is where it " +
			"landed, in mm:ss.d of session time, and hovering it says which recording " +
			"that is, which frame, and whether the cut keeps it.\n\nA clip's own " +
			"edges are trimmed rather than re-selected: right-click a green border to " +
			"pick it up — it turns white — and drag it, or move it a frame at a time " +
			"with ‹f and f› (the arrow keys do the same, Shift for five). The picture " +
			"follows the edge, so what you are trimming to is on screen, and ▶ plays " +
			"from the edge rather than from the playhead. Right-click " +
			"clear of a border, or any left click, puts it down; the whole trim is one " +
			"Undo."},
	{"step4", "Narrate", "The narration, and the voice it is spoken in",
		"Finish Cut first — narration is written for the cut's clips",
		"▶ below is the initial fill: it writes the narration once (again only if the " +
			"cut moves) and speaks every line not cached yet. After that the lines are " +
			"yours: each box is \"[emotion] words\" with its start time beside it — edit " +
			"the words, the delivery, or when the line begins — a time inside another " +
			"clip moves the line to that clip, and one outside the cut is refused with " +
			"the line left where it was. \"+ Line at playhead\" " +
			"adds a line at the paused second, 🗑 removes one, and an edited line is " +
			"simply re-spoken the first time it plays. The voice is cloned from a " +
			"sample above; the slider and the picture play the cut, speaking each " +
			"line where it was placed, with the blue row following along. The bar is " +
			"the cut end to end — what the edit removed takes up no room on it, so " +
			"every point on it is video you keep, and the time beside it reads " +
			"\"session clock · how far into the finished video\". The cut is " +
			"the master: once it is rolling it keeps rolling, clip after clip, and " +
			"nothing moves the picture but you. Picking a line — clicking its row, or " +
			"its ▶ — starts three seconds before that line so you can watch it land, " +
			"and then the preview simply carries on down the cut. A clip the narration " +
			"left alone keeps its row and its ▶ too: that one plays the clip from the " +
			"top on its own audio, which is how you decide whether it wants a line. " +
			"A line whose time you retype into another clip leaves that row behind it, " +
			"empty. ⏪ and ⏩ " +
			"jump three seconds; stopped, the wheel over the slider steps one frame " +
			"at a time, which is the resolution a line's placement is judged at. The " +
			"playhead only lands on material the cut kept: dropped into a gap it " +
			"carries on the way it was going, to the head of the next clip or back " +
			"to the tail of the one behind.\n\nA take " +
			"is stable: the same words, delivery and voice always come back as the " +
			"same performance, so nothing you have approved changes behind you. " +
			"When a delivery is wrong rather than the words, the row's ↻ re-rolls " +
			"it — same line, a new draw — and plays the result.\n\nThe TTS blends eight base emotions — " +
			"happy, angry, sad, afraid, disgusted, melancholic, surprised, calm — " +
			"and maps whatever is in the [brackets] onto them. One or two of those " +
			"words work best; \"loud\" or \"fast\" is not an emotion and only dilutes " +
			"the match (anger already shouts, calm is already slow). Length lives in " +
			"the words themselves: more short exclamations, not stretched letters, " +
			"which get spelled out.\n\nPlain words are read by a small judge model, " +
			"which is forgiving but approximate. Write a weight and it is skipped, " +
			"the mix going to the engine exactly as asked: \"[angry=1]\" is pure " +
			"anger at full force, \"[happy=0.8, surprised=0.4]\" a blend you chose. " +
			"Weights run 0 to 1, and the difference is audible: \"[surprised]\" " +
			"is the judge's reading of the word, \"[surprised=1]\" the axis " +
			"itself at full force.\n\nBeside the eight there are named mixes of " +
			"them, which take a weight the same way: excited, ecstatic, playful, " +
			"proud, relieved, hopeful, tender, nostalgic, solemn, awed, alarmed, " +
			"horrified, desperate, confused, frustrated, bitter, contemptuous, " +
			"dismayed, heartbroken, ominous, tense. \"[excited=1]\" is happiness " +
			"with surprise mixed into it, which is what excitement sounds like " +
			"and what plain happiness does not. Any name none of these knows sends the " +
			"line back to the judge rather than guessing at an axis."},
	{"step5", "Produce", "Render the video and write the upload text",
		"Finish Cut first — there is no cut to produce a video from",
		"▶ below renders the final video: every clip is cut from its own recording, " +
			"the narration is laid over ducked game audio, and the whole thing is " +
			"loudness-normalized to -14 LUFS for YouTube. Lines that have not been " +
			"synthesized yet are spoken first.\n\nWhen it finishes, the result is " +
			"waiting in the picture below: ▶ then plays it rather than producing " +
			"again, and ⏹ hands ▶ back to producing. The row at the top says what " +
			"is going in — the cut, the lines, and how many of them still have to " +
			"be spoken — and the row at the foot says what is on disk."},
	{"step6", "Publish", "Draw the thumbnail and write the upload text",
		"Finish Cut first — there is nothing to make a thumbnail of yet",
		"The two things a finished video still needs: a thumbnail, and the text " +
			"under it on the upload page. The page is split the same way — the " +
			"picture and everything that makes it on the left, the words on the " +
			"right.\n\nThe first ▶ writes the title and the description, and then " +
			"draws. Every ▶ after that only draws — the model is not asked again, " +
			"however much you edit the boxes, so rewording the instruction or " +
			"changing the images costs GPU time and no thinking. Deleting the step6 " +
			"folder is what starts the text over; Suggest again, beside the title, " +
			"rewrites both without redrawing.\n\nThe edit instruction is yours to " +
			"write — nothing suggests one. There was a second model job that picked " +
			"a frame and wrote an instruction for it, and it was removed because it " +
			"did neither well.\n\nThe picture is usually not made from nothing — " +
			"it is an edit of one of your own frames, which is what keeps it " +
			"recognizably this video rather than a stock illustration of the " +
			"genre. Two are taken from the cut on the first run, and the row is " +
			"yours after that: Add image… puts another one in, − takes one out, " +
			"Change… swaps one in place. The first in the row is the picture being " +
			"edited; the others are references the instruction can name by " +
			"position — \"put the ship from the second image behind them\" — and " +
			"Make base promotes one of them to the front. Empty the row and the " +
			"thumbnail is drawn from the instruction alone.\n\nWrite the instruction as instructions, not as a description " +
			"of a picture — say what to change, and everything you do not mention " +
			"stays as it is. That is the whole reason this is an edit model rather " +
			"than the image-to-image one it used to be: there, a single strength " +
			"dial renoised the entire frame, so asking for one change altered every " +
			"other thing too.\n\nThe title is lettered by the image model as part " +
			"of the instruction. Keep it to four or five words — that is what " +
			"survives being shrunk to a phone's sidebar, and it is also what a model " +
			"spells reliably. Leave it empty and the picture comes back without any."},
}

// stepLocked reports whether a tab's prerequisites are missing. Step 1 never
// is, and an unknown name (there is none, but the lookup can fail) counts as
// open rather than as locked -- refusing to show a page is the worse mistake.
func (a *App) stepLocked(i int) bool {
	if i < 0 || i >= len(steps) {
		return false
	}
	switch steps[i].name {
	case "step2":
		return a.step2Locked
	case "step3":
		return a.step3Locked
	case "step4":
		// choosing a voice lives on this tab and therefore waits for a cut,
		// which it does not need. That is deliberate: it belongs beside the lines
		// it will speak, and hearing a sample before there is anything to narrate
		// decides nothing you cannot decide again afterwards.
		return a.step4Locked
	case "step5":
		return a.step5Locked
	case "step6":
		// the thumbnail is painted over a frame the cut kept and its text is
		// written from the clips, so this waits on the same thing Produce does --
		// but not on the video itself, which is a long render you should be able
		// to start the upload text without waiting for
		return a.step5Locked
	}
	return false
}

func stepIndex(name string) int {
	for i, s := range steps {
		if s.name == name {
			return i
		}
	}
	return -1
}

// showStep moves to a page without going through the tab's click handler --
// for the bounce off a locked tab, and for sending a page whose prerequisites
// vanished back to the start.
func (a *App) showStep(name string) {
	a.tabGuard = true
	for i, s := range steps {
		a.tabs[i].SetActive(s.name == name)
	}
	a.tabGuard = false
	a.stack.SetVisibleChildName(name)
	a.updateRunControls() // ▶ ⏹ belong to the new page's playback now
	a.syncHelp()          // and so does the ⓘ
	// Cut's Inputs row lists what Suggest will be sent, and one of those things
	// is the context box on Describe -- the page you have usually just come
	// from. Refreshed on arrival rather than on every keystroke over there,
	// which would re-read the session timeline as you type.
	// ...and the tracks themselves, when the last run (or another project) moved
	// what they are drawn from. Only then: a rebuild probes every recording and
	// drops the undo history, which is not what a tab click should cost.
	if name == "step3" && a.ed != nil {
		if a.ed.stale || len(a.ed.vids) == 0 {
			a.updateStep3Info() // which does updateInputs itself
		} else {
			a.ed.updateInputs()
		}
	}
	// Narrate's row says the same thing about the narration, and one of the
	// things it counts is the cut -- which is the page you have just come from
	// and the one thing most likely to have changed under it.
	if name == "step4" && a.narr5 != nil {
		a.narr5.refit() // a clip dragged wider over there moves its line's slot here
		a.narr5.updateInputs()
		a.narr5.updateOut()
	}
	// ...and Produce reads both of those, plus the synthesis cache and the file
	// it wrote last time. It is the end of the chain, so everything upstream is
	// something that may have moved since it was last looked at.
	if name == "step5" {
		a.updateStep5Info()
	}
	// Publish reads all of that AND the folder it wrote into last time, since the
	// thumbnail on the page is the file on disk rather than something remembered
	if name == "step6" && a.pub != nil {
		a.pub.refresh()
	}
}

// helpPopover is what the ⓘ in the header bar drops down: what the open page
// does, and only that. It listed all five steps at once to begin with, which
// made the paragraph you actually wanted something to go looking for in a
// scrolling column of four you did not.
//
// The two labels are kept because the popover now follows the tabs; the table
// they are filled from is a compile-time constant, so syncHelp is all there is.
func (a *App) helpPopover() *gtk.Popover {
	a.helpTitle = gtk.NewLabel("")
	a.helpTitle.SetXAlign(0)
	a.helpTitle.AddCSSClass("heading")
	a.helpBody = gtk.NewLabel("")
	a.helpBody.SetXAlign(0)
	a.helpBody.SetWrap(true)
	a.helpBody.SetMaxWidthChars(64) // wrapping needs a width to wrap at, or it never does
	a.helpBody.AddCSSClass("dim-label")

	box := gtk.NewBox(gtk.OrientationVertical, 4)
	box.SetMarginTop(4)
	box.SetMarginBottom(4)
	box.SetMarginStart(4)
	box.SetMarginEnd(4)
	box.Append(a.helpTitle)
	box.Append(a.helpBody)

	p := gtk.NewPopover()
	p.SetChild(box)
	return p
}

// syncHelp points the ⓘ at the page that is open. Called from showStep, which
// every page change goes through -- including the bounce off a locked tab,
// where the help must stay on the page you did not leave.
func (a *App) syncHelp() {
	if a.helpTitle == nil {
		return
	}
	i := stepIndex(a.stack.VisibleChildName())
	if i < 0 {
		return
	}
	a.helpTitle.SetText(steps[i].label)
	a.helpBody.SetText(steps[i].help)
}

type App struct {
	root   string // autocut directory (the scripts and project files live here)
	vidDir string // where the last video came from; where a chooser opens
	audDir string // same, for a recording -- neither is what the list is made of
	outDir string // where step outputs go; default root, project-settable

	win   *gtk.ApplicationWindow
	stack *gtk.Stack
	// one tab per entry in steps, in that order; they gray out with a hint
	tabs        []*gtk.ToggleButton
	tabGuard    bool          // suppresses re-entrant toggles while reverting
	step2Locked bool          // step 1 has not produced inputs for Describe yet
	step3Locked bool          // no session timeline yet -- nothing to cut
	step4Locked bool          // no cut yet -- nothing to narrate
	step5Locked bool          // no cut yet -- nothing to produce
	logExp      *gtk.Expander // collapsed until something actually runs
	helpTitle   *gtk.Label    // the ⓘ popover, refilled per page by syncHelp
	helpBody    *gtk.Label
	player      *Player // the step 5 preview
	log         *gtk.TextView
	status      *gtk.Label
	running     bool
	audioNoted  string // the audio.cpp server already reported in the log
	ttsModel    string // the model id that server serves, asked for once

	// step 1 page
	srcList   *sourceList // the session's files, and what each one is for
	interval  *freqPick
	scalePick *gtk.DropDown
	langEntry *gtk.Entry // what the ASR is told this session is spoken in
	outLabel  *gtk.Label
	s1out     *gtk.Label
	und       *understander
	ed        *cutEditor
	voice5    *voicePicker
	// the chosen voice id, cached from step4/voice.txt. Guarded: since the
	// narrator's name became part of every step's input (tlLabel), this is read
	// by the describe and transcript workers as well as by the GUI thread.
	voiceMu   sync.Mutex
	voiceSel  string
	pitchSel  float64 // semitones the reference is shifted by, from step4/pitch.txt
	pitchRead bool    // ...and whether that file has been read yet (0 is a real value)
	narr5     *narrator
	prod      *producer
	pub       *publisher
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
	// ⏸ parks a run, ⏹ abandons one, and the difference has to be visible on
	// the next ▶: resume where it stopped, or start over. Only the describer
	// can tell them apart at all -- it is the one job that resumes per chunk --
	// so a stopped Describe + Transcript sets this, and the next run of that
	// step throws its half-written event logs away first. See resetDescribe.
	undRestart bool

	srcMu   sync.Mutex
	selVid  []string              // snapshot of the session's sources, taken on the GUI
	selAud  []string              // thread when a run starts: the footage, then the rest
	selNarr [narratorSlots]string // ...and who was tagged as which narrator

	// the editable system prompts. The views are the GUI thread's; promptTxt is
	// the copy a runner reads, kept current by the buffers' changed handler --
	// same rule as selVid/selAud, for the same reason.
	promptMu    sync.Mutex
	promptTxt   map[string]string
	promptViews map[string]*gtk.TextView
	// every text box's heading row, so a row with a Reset button and a row with
	// a bare label are the same height and the boxes under them line up
	// (editorBody). GUI thread only, like the views.
	headGroup *gtk.SizeGroup
	// what langEntry says, for the runner to read; guarded like ctxTxt below and
	// for the same reason
	langTxt string
	// what the editor says about THIS session, typed on Describe and read by
	// every step (context.go). Under promptMu for the same reason as the
	// prompts: the box belongs to the GUI thread, the string is what a runner
	// reads. Not in promptTxt -- it is not a prompt and it is stored in full,
	// whereas a prompt is stored only when it differs from the built-in.
	ctxTxt  string
	ctxView *gtk.TextView

	// the project file, and what was last written to it. projPath is the named
	// file Save/Load last used -- the working copy at root/project.json is
	// written whatever it says. Both are the GUI thread's (project.go).
	// projLabel is projPath's name in the header bar; nil under the tests,
	// which build an App without a window.
	projPath  string
	projSaved []byte
	projLabel *gtk.Label

	// one progress bar fed by both tracks: summed fractions, joined texts
	progMu    sync.Mutex
	progParts [2]float64
	progTexts [2]string
}

// The frame slider snaps to these stops; a linear 0.1..5 s scale would cram
// all the useful low end into the first pixel. Index 0 keeps every frame.
var frameStops = []float64{0, 0.1, 0.2, 0.5, 1, 2, 3, 4, 5}
var frameStopLabels = []string{"each", "0.1", "0.2", "0.5", "1s", "2s", "3s", "4s", "5s"}

// where a session with nobody's opinion on it starts: one frame a second. Named
// rather than typed twice, since a new project has to land on the same stop the
// stepper builds itself at -- and index 0 is not it (that is every frame, which
// is gigabytes).
const defFrameStop = 4

// Frame size presets, no-resize first because that is the default and picking a
// size is the exception. Name is the identity and Label is only what the drop
// down shows: the name goes into the project file AND into the stamp that
// decides whether frames must be extracted again, so it has to stay put even
// when the wording moves. 896w is the width the vision model gets fed.
var scalePresets = []struct{ Name, Label, VF string }{
	// the label said "Resize", which on the closed button read as the
	// control's caption and in the open list as a nonsense first choice
	{"original", "Original", ""},
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

// defLanguage is what a session is assumed to be in when nobody says. It was a
// setting on this machine (llm.conf) and is now a field on the project, for the
// reason setup.go's header gives: the stack is the same every night, the
// footage is not.
const defLanguage = "en"

// projectLanguage is the box's text as the project file stores it: what was
// typed, trimmed, and "" when nothing was -- an empty box means the default,
// and writing "en" into every project would freeze today's default into files
// that never asked for it (same rule as an unedited prompt).
func (a *App) projectLanguage() string {
	a.promptMu.Lock()
	defer a.promptMu.Unlock()
	return strings.TrimSpace(a.langTxt)
}

// asrLanguage is the same value with the default filled in: what step 1 puts in
// the request. Callable from a runner's goroutine, which is the whole reason
// the string is cached beside the widget rather than read off it -- the box is
// the GUI thread's (same rule as sessionCtx).
func (a *App) asrLanguage() string {
	if s := a.projectLanguage(); s != "" {
		return s
	}
	return defLanguage
}

func (a *App) setLanguage(s string) {
	a.promptMu.Lock()
	a.langTxt = s
	a.promptMu.Unlock()
}

// applyLanguage loads a project's language into the box as well as the cache.
// GUI thread only, like applySessionCtx and for the same reason.
func (a *App) applyLanguage(s string) {
	a.setLanguage(s)
	if a.langEntry != nil {
		a.langEntry.SetText(s)
	}
}

func (a *App) frameInterval() float64 { return frameStops[a.interval.i] }

func (a *App) setFrameInterval(v float64) { a.interval.set(nearestStop(v)) }

// nearestStop maps seconds onto the stop table -- a project may store a value
// from a build whose table was different, and typing is free-form.
func nearestStop(v float64) int {
	best, bd := 4, math.MaxFloat64 // default 1 s
	for i, s := range frameStops {
		if d := math.Abs(s - v); d < bd {
			best, bd = i, d
		}
	}
	return best
}

// freqPick is the frame-frequency stepper: one stop visible at a time, - / +
// walking the table, and the text editable by hand. Hand-rolled rather than a
// GtkSpinButton because the spin button re-reads its own display as a plain
// number before every step -- and "each" and "1s" are not numbers, so every
// click first corrupted the value it was about to step.
type freqPick struct {
	box   *gtk.Box
	entry *gtk.Entry
	i     int
}

func newFreqPick() *freqPick {
	f := &freqPick{}
	f.entry = gtk.NewEntry()
	f.entry.SetWidthChars(4)
	f.entry.SetMaxWidthChars(4)
	f.entry.SetAlignment(0.5)
	f.entry.SetTooltipText("Seconds between frames — type 0.1 to 5, or 'each' for every frame")
	f.entry.ConnectActivate(f.parse)
	// clicking elsewhere commits like Enter does: a half-typed value silently
	// kept on screen would not be the value a run then uses
	fc := gtk.NewEventControllerFocus()
	fc.ConnectLeave(f.parse)
	f.entry.AddController(fc)

	minus := gtk.NewButtonFromIconName("list-remove-symbolic")
	minus.ConnectClicked(func() { f.set(f.i - 1) })
	plus := gtk.NewButtonFromIconName("list-add-symbolic")
	plus.ConnectClicked(func() { f.set(f.i + 1) })

	f.box = gtk.NewBox(gtk.OrientationHorizontal, 0)
	f.box.AddCSSClass("linked") // one control, three parts
	f.box.Append(f.entry)
	f.box.Append(minus)
	f.box.Append(plus)
	f.set(defFrameStop)
	return f
}

// set clamps to the table -- past either end the buttons simply stop moving.
func (f *freqPick) set(i int) {
	if i < 0 {
		i = 0
	}
	if i >= len(frameStops) {
		i = len(frameStops) - 1
	}
	f.i = i
	f.entry.SetText(frameStopLabels[i])
}

// parse commits a typed value: seconds with or without the s, or "each"/"0"
// for every frame, landing on the nearest stop. Anything unreadable snaps the
// display back to the value still in force.
func (f *freqPick) parse() {
	t := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(f.entry.Text())), "s")
	if t == "each" {
		f.set(0)
		return
	}
	v, err := strconv.ParseFloat(t, 64)
	if err != nil || v < 0 {
		f.set(f.i)
		return
	}
	f.set(nearestStop(v))
}

// The Describe page is one step and owns one folder, step2/. Inside it the two
// jobs get a folder each, named after the job rather than after the order it
// runs in -- there is nothing left on the tab bar to match a number to. They
// stay two folders because the describer resumes per chunk and the fixer does
// not.
func (a *App) narrateDir() string    { return filepath.Join(a.outDir, "step4") }
func (a *App) produceDir() string    { return filepath.Join(a.outDir, "step5") }
func (a *App) understandDir() string { return filepath.Join(a.outDir, "step2") }
func (a *App) describeDir() string   { return filepath.Join(a.understandDir(), "describe") }
func (a *App) transcriptDir() string { return filepath.Join(a.understandDir(), "transcript") }

func (a *App) setOutDir(dir string) {
	a.outDir = dir
	if a.outLabel != nil {
		a.outLabel.SetText(dir)
	}
	a.followOutDir()
	// the voice belongs to the project, not to the session: drop the cached id
	// and pitch so both are re-read from the folder we just moved to
	a.voiceMu.Lock()
	a.voiceSel = ""
	a.voiceMu.Unlock()
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
	a.updateStep5Info()
	a.pub.refresh()
	a.updateGates()
}

// outDirRow builds the output-folder row: buttons lead, the path follows -- an
// expanding path in the middle would push Choose to the far edge, away from
// what it changes. Returns the row and the path label, which the caller owns
// and has to keep in step with a.outDir (setOutDir does that for step 1's, and
// understander.refresh for the Describe page's).
//
// One folder, several pages, but exactly one place it is set and shown: here.
// The later pages open it and say how much is in their part of it; a path
// repeated on every page that writes into it is a line of chrome per page for
// something that changes once a project.
func (a *App) outDirRow(caption string) (*gtk.Box, *gtk.Label) {
	const tip = "Where every step writes"
	btn := gtk.NewButtonWithLabel("Choose…")
	btn.SetTooltipText(tip)
	btn.ConnectClicked(a.chooseOutDirDialog)
	openBtn := gtk.NewButtonFromIconName("folder-open-symbolic")
	openBtn.SetTooltipText(tip)
	openBtn.ConnectClicked(func() { a.openFolder(a.outDir) })
	// Right-aligned, and the row it goes in is too: the Describe page reads its
	// outputs off the right-hand end and so does the row this sits next to, and
	// a reading you have to hunt for in a different place on each page is one
	// you stop trusting. It no longer expands -- an expanding path stretched
	// this row across the window and left the count marooned at the far edge.
	lbl := gtk.NewLabel(a.outDir)
	lbl.SetXAlign(1)
	lbl.SetEllipsize(pango.EllipsizeStart) // the tail names the folder; the head is /home/…
	lbl.SetSelectable(true)                // it is a path: it gets pasted into a terminal
	row := gtk.NewBox(gtk.OrientationHorizontal, 8)
	row.Append(btn)
	row.Append(openBtn)
	if caption != "" {
		row.Append(gtk.NewLabel(caption))
	}
	row.Append(lbl)
	return row, lbl
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
	a.vidDir = filepath.Join(wd, "input_video")
	a.audDir = filepath.Join(wd, "input_audio")
	a.outDir = wd

	app := gtk.NewApplication(appID, gio.ApplicationFlagsNone)
	app.ConnectActivate(func() { a.build(app) })
	os.Exit(app.Run(nil))
}

// ---- state -----------------------------------------------------------------

// loadMeta reads step1/meta.env: the primary video and recording of the last
// run, and the frame settings it ran with. Its existence is also the marker
// that step 1 has run at all -- either of the two files may be missing from a
// legitimate session, so the keys are not that marker.
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
	return m
}

// snapSources caches the session's sources. Background runners must never touch
// the list widget -- GTK objects belong to the GUI thread -- so every run takes
// this snapshot first and works from it.
//
// The pair it hands back is the list split by role: the footage, then
// everything else. Every source is in exactly one of the two, which is what
// makes appending them the whole session -- the reason a video that is also a
// voice is one row here and not one row in each of two lists.
func (a *App) snapSources() (vids, auds []string) {
	if a.srcList == nil {
		return a.snappedSources()
	}
	vids, auds = a.srcList.split()
	var narr [narratorSlots]string
	for n := 1; n <= narratorSlots; n++ {
		narr[n-1] = a.srcList.narratorPath(n)
	}
	a.srcMu.Lock()
	a.selVid, a.selAud, a.selNarr = vids, auds, narr
	a.srcMu.Unlock()
	return
}

func (a *App) snappedSources() (vids, auds []string) {
	a.srcMu.Lock()
	defer a.srcMu.Unlock()
	return a.selVid, a.selAud
}

// narratorPath is the recording tagged with slot n, from the snapshot -- so a
// runner can ask who narrator 2 is without touching a widget.
func (a *App) narratorPath(n int) string {
	if n < 1 || n > narratorSlots {
		return ""
	}
	a.srcMu.Lock()
	defer a.srcMu.Unlock()
	return a.selNarr[n-1]
}

// voiceSource is the recording the narration is cloned from: narrator 1, or --
// for a session nobody has tagged -- the same guess the pipeline made before
// the tags existed, the first recording, and failing that the first source at
// all, which is the single-video session.
func (a *App) voiceSource() string {
	if p := a.narratorPath(1); p != "" {
		return p
	}
	vids, auds := a.snappedSources()
	if len(auds) > 0 {
		return auds[0]
	}
	if len(vids) > 0 {
		return vids[0]
	}
	return ""
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
	a.player.OnError = a.playerErr("the preview")

	a.win = gtk.NewApplicationWindow(app)
	a.win.SetTitle("autocut")
	// the few styles of our own: the no-timestamp flag on a source row, and the
	// settings dialog's test verdicts. Plain GTK only promises the semantic
	// warning/success classes on a handful of widgets, so the colors are stated
	// here rather than hoped for from the theme.
	css := gtk.NewCSSProvider()
	css.LoadFromData(".stamp-warn { color: #e5a50a; } " +
		".test-ok { color: #26a269; } .test-bad { color: #c01c28; } " +
		// the bars a preview's aspect leaves over, in the color every other
		// player puts there instead of the page background (see videoFrame)
		".videoframe { background-color: #101010; }")
	gtk.StyleContextAddProviderForDisplay(gtk.BaseWidget(a.win).Display(), css,
		gtk.STYLE_PROVIDER_PRIORITY_APPLICATION)
	// fits a 1366x768 laptop with room for the panel: every page is either
	// scrolled or split by a divider, so a bigger screen is worth more space
	// but no page depends on having it
	a.win.SetDefaultSize(1240, 740)

	head := gtk.NewHeaderBar()
	// Symbols, like everything else in this bar. Two spelled-out labels took
	// more of the title bar than the four icon buttons at the other end put
	// together, for the two things pressed least often in the app -- and they
	// were the only words up there, so they read as the important ones. The
	// tooltip says what the icon means, which is the deal every other button
	// here already makes.
	// New, then Open, then Save: the order every application puts them in, and
	// the order they happen in
	newP := gtk.NewButtonFromIconName("document-new-symbolic")
	newP.SetTooltipText("New project — empty the session and start over")
	newP.ConnectClicked(a.newProjectDialog)
	loadP := gtk.NewButtonFromIconName("document-open-symbolic")
	loadP.SetTooltipText("Load a project — sources, prompts and settings")
	loadP.ConnectClicked(a.loadProjectDialog)
	saveP := gtk.NewButtonFromIconName("document-save-symbolic")
	saveP.SetTooltipText("Save this project to a file")
	saveP.ConnectClicked(a.saveProjectDialog)
	head.PackStart(newP)
	head.PackStart(loadP)
	head.PackStart(saveP)
	// Which file this session is being written to. Projects are files in a
	// folder and several sit side by side (a recut, a variant, last week's), and
	// until now the window said nothing about which one Save was following --
	// the title bar was the five tabs and nothing else, so the only way to find
	// out was to open the Save dialog and read the name it proposed.
	//
	// Ellipsized rather than wrapped or left to grow: the tabs are the title
	// widget and sit centered, so a project with a long name must not push them
	// off center or shove the buttons at the other end off the bar. The label
	// therefore asks for at most projNameChars characters and ends in "…" when
	// the name is longer -- the CSS text-overflow: ellipsis of the web app --
	// and the tooltip carries the whole path for when the tail is the part you
	// needed to read.
	a.projLabel = gtk.NewLabel("")
	a.projLabel.SetEllipsize(pango.EllipsizeEnd)
	a.projLabel.SetMaxWidthChars(projNameChars)
	a.projLabel.AddCSSClass("dim-label")
	a.projLabel.SetMarginStart(6)
	head.PackStart(a.projLabel)
	a.showProject()
	// every step needs a rescan after files change on disk, so it lives here
	rescan := gtk.NewButtonFromIconName("view-refresh-symbolic")
	rescan.SetTooltipText("Rescan inputs and outputs")
	rescan.ConnectClicked(a.rescanAll)
	head.PackEnd(rescan)
	setup := gtk.NewButtonFromIconName("preferences-system-symbolic")
	setup.SetTooltipText("Settings — the LLM and audio.cpp endpoints")
	setup.ConnectClicked(a.setupDialog)
	head.PackEnd(setup)
	info := gtk.NewMenuButton()
	info.SetIconName("help-about-symbolic")
	info.SetTooltipText("What this step does")
	info.SetPopover(a.helpPopover())
	head.PackEnd(info)
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
	a.stopBtn.SetTooltipText("Stop the run or the playback — ⏸ is what parks one to carry on later")
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
	a.stack.SetVExpand(true)
	a.stack.AddNamed(a.buildStep1(), "step1")
	a.stack.AddNamed(a.buildUnderstand(), "step2")
	a.stack.AddNamed(a.buildStep3(), "step3")
	a.stack.AddNamed(a.buildStep4(), "step4")
	a.stack.AddNamed(a.buildStep5(), "step5")
	a.stack.AddNamed(a.buildStep6(), "step6")

	// Tabs, not a sidebar down the left. The steps are a fixed five, so a list
	// spent 170px of width on five rows and a column of empty space under them;
	// as a group of toggle buttons they fit the header bar, which is already
	// there -- the page gets the width back and gives up no height for it.
	// Hand-rolled rather than a GtkStackSwitcher, because a tab has to be able
	// to gray out and say what is missing, which the stock widget cannot do.
	tabRow := gtk.NewBox(gtk.OrientationHorizontal, 0)
	tabRow.AddCSSClass("linked") // one segmented control, not five loose buttons
	for i, s := range steps {
		i, b := i, gtk.NewToggleButtonWithLabel(s.label)
		b.SetTooltipText(s.tip)
		if i > 0 {
			b.SetGroup(a.tabs[0]) // radio behaviour: some page is always current
		}
		b.ConnectToggled(func() {
			// the other half of every switch is a tab going inactive, and the
			// bounce below re-toggles two more -- only the one being entered acts
			if a.tabGuard || !b.Active() {
				return
			}
			if a.stepLocked(i) {
				a.showStep(a.stack.VisibleChildName()) // bounce: the page did not move
				// the same sentence the tooltip carries: a click is how you find
				// out the tab is closed, so it must not answer with less than
				// hovering it would have
				a.setStatus(steps[i].wait)
				return
			}
			a.showStep(steps[i].name)
		})
		a.tabs = append(a.tabs, b)
		tabRow.Append(b)
	}
	head.SetTitleWidget(tabRow)
	a.showStep("step1")

	// shared log + status across all pages: one bottom row, the status text
	// living in the expander header so nothing reserves empty space
	var logScroll *gtk.ScrolledWindow
	a.log, logScroll = newLogPane(220)
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

	bottom := gtk.NewBox(gtk.OrientationVertical, 0)
	// a hard edge above the control/log rows, so the page ending there reads as
	// a status bar rather than as widgets stopping mid-air
	bottom.Append(gtk.NewSeparator(gtk.OrientationHorizontal))
	bottom.Append(ctlRow)
	bottom.Append(a.logExp)

	// The log against the page above it, on a divider. How much log you want is
	// a per-moment question -- all of it while a run talks, none of it while
	// cutting -- and it was a fixed 220 px that the page had to live around.
	// Only the page takes the window's extra height; the log keeps whatever it
	// was dragged to, and cannot be dragged over the run controls.
	outer := gtk.NewPaned(gtk.OrientationVertical)
	outer.SetStartChild(a.stack)
	outer.SetEndChild(bottom)
	outer.SetResizeStartChild(true)
	outer.SetResizeEndChild(false)
	outer.SetShrinkEndChild(false)
	// Two halves, and both are needed. The expander and its scroller have to be
	// told to fill what the divider gives them, or the log sits at its natural
	// height under an empty stretch of window (same trick as the settings
	// dialog). And collapsing it hands the height back rather than leaving the
	// divider parked where a log nobody can see used to be -- position(-1) is
	// how a GtkPaned is told to forget a dragged position.
	logGrow := func() {
		on := a.logExp.Expanded()
		logScroll.SetVExpand(on)
		a.logExp.SetVExpand(on)
		if !on {
			outer.SetPosition(-1)
		}
	}
	a.logExp.NotifyProperty("expanded", logGrow)
	logGrow()
	a.win.SetChild(outer)
	// after the log exists, so that a theme that cannot find the icon says so
	// somewhere the user will look rather than only on stderr
	a.setupIcons()

	// Pick up where the last session left off: the named project that was open
	// when it ended, and only failing that the working copy. Opening
	// project.json unconditionally was the old behaviour, and it silently
	// undid the Save that named a variant -- you saved jan-video.json, quit,
	// and came back to the session you had saved it to get away from.
	if pj := a.lastProject(); pj != "" {
		a.loadProjectFrom(pj)
	} else if pj := filepath.Join(a.root, "project.json"); exists(pj) {
		a.loadProjectFrom(pj)
	}

	a.updateGates()
	a.updateStep3Info()
	a.updateStep5Info()
	a.startAutosave() // from here on the project file follows the window
	a.win.SetVisible(true)
}

// updateGates grays the tabs whose prerequisites are missing and puts the
// reason where the description usually is; clicking a locked tab bounces back.
// Graying rather than desensitizing is the point: an insensitive button gets no
// hover, so the one place that says what is missing would be the one place you
// cannot reach.
//
// Describe used to gate Transcript from the sidebar. They share a page now, so
// the ordering between them is the page's business, not a locked tab's.
func (a *App) updateGates() {
	if len(a.tabs) == 0 {
		return
	}
	// step 1 left its record, so there are transcripts to describe and fix. Not
	// "there is a video in it": a session of recordings has nothing to see and
	// still has everything to read.
	a.step2Locked = len(a.loadMeta()) == 0
	a.step3Locked = !exists(filepath.Join(a.transcriptDir(), "session.tsv"))
	a.step4Locked = !exists(a.cutPath())
	a.step5Locked = !exists(a.cutPath())
	for i, s := range steps {
		w := gtk.BaseWidget(a.tabs[i].Child()) // the label, so the button keeps its frame
		if a.stepLocked(i) {
			w.AddCSSClass("dim-label")
			a.tabs[i].SetTooltipText(s.wait)
		} else {
			w.RemoveCSSClass("dim-label")
			a.tabs[i].SetTooltipText(s.tip)
		}
	}
	if a.stepLocked(stepIndex(a.stack.VisibleChildName())) {
		a.showStep("step1")
	}
}

func (a *App) buildStep1() gtk.Widgetter {
	// every edit to the list changes what a run would see and who the narration
	// would be spoken by, so the snapshot and the voice picker are refreshed
	// from here rather than from each of the six things that can edit a row
	a.srcList = newSourceList(func() {
		a.snapSources()
		if a.voice5 != nil {
			a.voice5.refreshNarrators()
		}
		a.updateStep1Info()
		if a.und != nil {
			a.und.refresh()
		}
		a.refreshCut() // the tracks ARE this list: a row added or unmarked changes them
	})

	var outRow *gtk.Box
	outRow, a.outLabel = a.outDirRow("Output folder:")

	// the frame controls: how often, then how big. A stepper over the same
	// discrete stops the slider had, each..5s, typable by hand.
	a.interval = newFreqPick()

	labels := make([]string, len(scalePresets))
	for i, p := range scalePresets {
		labels[i] = p.Label
	}
	a.scalePick = gtk.NewDropDownFromStrings(labels)
	a.scalePick.SetTooltipText("Frame size — Original keeps the video's own size")
	a.setFrameScale("original")
	a.scalePick.SetVAlign(gtk.AlignCenter)

	// The language, on the page whose run is the one that listens. It used to be
	// in Settings, next to the model ids -- which made it a property of the
	// machine, so a session in the other language was transcribed into gibberish
	// by a box nobody thought to open, three tabs away from the sources it was
	// wrong about.
	//
	// Free text, not a list: the code is the server's to interpret, and a drop
	// down would have to guess which of its models take what -- being able to
	// type what the server documents beats a menu that is right for one model.
	a.langEntry = gtk.NewEntry()
	a.langEntry.SetWidthChars(4)
	a.langEntry.SetMaxWidthChars(6)
	a.langEntry.SetPlaceholderText(defLanguage)
	a.langEntry.SetTooltipText("Language of this session's speech, as the ASR model spells it (en, de, …) — " +
		"the wrong one transcribes into gibberish. Empty means " + defLanguage)
	a.langEntry.SetVAlign(gtk.AlignCenter)
	// the cache follows every keystroke rather than a commit: unlike the frame
	// stepper there is nothing to parse, and a value that only counts once you
	// tab away is a value the next run silently disagrees with
	a.langEntry.ConnectChanged(func() { a.setLanguage(a.langEntry.Text()) })

	// One list, the width of the page. Two lists split by folder was the older
	// idea and it could not say the thing this page is mostly about: a screen
	// recording is the footage AND a voice, and the voice on it is everyone
	// else. Each row says what its file is for instead.
	addBtn := gtk.NewButtonWithLabel("Add files…")
	addBtn.SetTooltipText("Add recordings or footage — several at once")
	addBtn.ConnectClicked(a.addFilesDialog)
	addDirBtn := gtk.NewButtonWithLabel("Add folder…")
	addDirBtn.SetTooltipText("Add everything playable in a folder")
	addDirBtn.ConnectClicked(a.addFolderDialog)
	// what the symbols on a row mean, once, above the rows -- a tooltip answers
	// that only after you already suspect the answer. The same icons the rows
	// use, because a legend drawn in anything else is a second thing to learn.
	// The warning is NOT here: a permanent yellow triangle in the legend read
	// as an active warning about the files below it. It explains itself on the
	// row it appears on.
	legend := gtk.NewBox(gtk.OrientationHorizontal, 4)
	legend.SetHExpand(true)
	legend.SetHAlign(gtk.AlignEnd)
	for _, l := range []struct{ icon, text string }{
		{"camera-video-symbolic", "footage"},
		{"audio-input-microphone-symbolic", "narrator"},
		{"user-trash-symbolic", "remove"},
	} {
		lbl := gtk.NewLabel(l.text)
		lbl.AddCSSClass("dim-label")
		lbl.SetMarginEnd(6)
		legend.Append(gtk.NewImageFromIconName(l.icon))
		legend.Append(lbl)
	}
	addRow := gtk.NewBox(gtk.OrientationHorizontal, 8)
	addRow.Append(addBtn)
	addRow.Append(addDirBtn)
	addRow.Append(legend)

	head := gtk.NewLabel("Sources")
	head.SetXAlign(0)
	head.SetEllipsize(pango.EllipsizeEnd)
	head.AddCSSClass("heading")
	head.SetTooltipText("Every file here is transcribed, and placed on the session clock by the timestamp in its name")
	listScroll := gtk.NewScrolledWindow()
	listScroll.SetChild(a.srcList.box)
	listScroll.SetVExpand(true)
	sources := gtk.NewBox(gtk.OrientationVertical, 4)
	sources.SetVExpand(true)
	sources.Append(head)
	sources.Append(addRow)
	sources.Append(listScroll)

	// The bottom line, one line: how the frames are taken, then where
	// everything lands and what is already there -- the two ends of the same
	// run. A count, not a listing: on a finished step 1 the listing is
	// thousands of frames, and the names of those are in the log. What this has
	// to answer is how much is there and whether it is from this run or last
	// week's.
	a.s1out = gtk.NewLabel("")
	a.s1out.SetXAlign(0)
	outLbl := gtk.NewLabel("Outputs:")
	outLbl.AddCSSClass("heading")
	outRow.Append(outLbl)
	outRow.Append(a.s1out)
	outRow.SetHExpand(true)
	outRow.SetHAlign(gtk.AlignEnd) // the whole group sits at the right edge, as on Describe
	bottom := gtk.NewBox(gtk.OrientationHorizontal, 8)
	bottom.Append(gtk.NewLabel("Freq:"))
	bottom.Append(a.interval.box)
	bottom.Append(a.scalePick)
	bottom.Append(gtk.NewLabel("Language:"))
	bottom.Append(a.langEntry)
	bottom.Append(outRow)

	a.updateStep1Info()

	box := gtk.NewBox(gtk.OrientationVertical, 12)
	box.SetMarginTop(16)
	box.SetMarginStart(16)
	box.SetMarginEnd(16)
	box.SetMarginBottom(8)
	box.Append(sources)
	box.Append(bottom) // frames and outputs, under the list both are made from
	return box
}

func (a *App) rescanAll() {
	// the list is the session, so a rescan cannot re-read it from a folder --
	// what it can do is notice that a source is no longer on disk. Say which:
	// a row that quietly disappeared is how a render comes out missing an angle.
	for _, gone := range a.srcList.prune() {
		a.logf("!!! dropped %s -- it is no longer there", gone)
	}
	a.updateGates() // which re-reads step 1's record
	a.updateStep1Info()
	a.und.refresh()
	a.updateStep3Info()
	a.updateStep4Info()
	a.updateStep5Info()
	a.setStatus("rescanned")
}

// updateStep4Info re-reads the narration from disk. It was the one step a
// rescan skipped: delete step4/ and the page went on showing the narration it
// had in memory and an Outputs line counting files that were no longer there.
// Nothing is lost by re-reading -- every edit on that page is written as it is
// typed -- and after a rescan the folder is the answer, including when the
// answer is that there is nothing in it.
func (a *App) updateStep4Info() {
	n := a.narr5
	if n == nil || n.list == nil {
		return // page not built yet
	}
	n.load()
	n.rebuildRows()
	n.updateInputs()
	n.updateOut()
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
	// one source is enough: a session can be a single screen recording that is
	// both the footage and every voice on it, and it can equally be a recording
	// with no footage at all
	vids, auds := a.snapSources()
	if len(vids)+len(auds) == 0 {
		a.setStatus("add at least one source on the Inputs step")
		return
	}
	if x, y := a.srcList.clash(); x != "" {
		a.logf("!!! %s and %s are both step1/%s -- rename one, or the second overwrites the first",
			x, y, baseName(x))
		a.setStatus(fmt.Sprintf("%s and %s have the same name — rename one",
			filepath.Base(x), filepath.Base(y)))
		return
	}
	// The working copy always reflects what actually ran -- and so does the
	// named project, since saveProjectNow writes every target. It used to be
	// saveProjectTo(project.json), which also RENAMED the open project to the
	// working copy: running step 1 quietly stopped the autosave following the
	// file you had opened, and the header bar changed under you to say so.
	a.saveProjectNow()
	scaleName, scaleVF := a.frameScale()
	a.startStep1(vids, auds, a.frameInterval(), scaleName, scaleVF)
}

// ---- helpers ---------------------------------------------------------------

func (a *App) setStatus(s string) {
	if a.status == nil {
		return // headless (tests): the status line is the window's
	}
	a.status.SetText(s)
}

// newLogPane is what a log looks like, everywhere one appears: a read-only
// monospace view that wraps rather than scrolls sideways, in a scroller with a
// border around it.
//
// There are two -- the run log at the bottom of the window and the settings
// dialog's test log -- and they were built separately, a dozen lines apart in
// two files, which is how they drifted: same font and same expander, but only
// one of them had the frame, so the run log's text sat loose on the window
// background with nothing to say where it began. One builder, one design.
//
// minHeight is the only thing the two disagree on, and legitimately: the run
// log is a page of a session, the dialog's is a handful of verdicts.
func newLogPane(minHeight int) (*gtk.TextView, *gtk.ScrolledWindow) {
	tv := gtk.NewTextView()
	tv.SetEditable(false)
	tv.SetCursorVisible(false) // read-only: a blinking caret in it is a lie
	tv.SetMonospace(true)
	tv.SetWrapMode(gtk.WrapWordChar) // paths and ffmpeg lines are long
	sw := gtk.NewScrolledWindow()
	sw.SetChild(tv)
	sw.SetMinContentHeight(minHeight)
	sw.AddCSSClass("frame")
	return tv, sw
}

func (a *App) logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...) // mirror to the launching terminal
	if a.log == nil {
		return // called before the window exists: stderr is the whole log
	}
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
