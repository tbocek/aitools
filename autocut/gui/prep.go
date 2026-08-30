package main

// Prepare: everything that has to happen before there is anything to cut.
//
// It was two pages. Inputs held the list of files, ran the speech-to-text over
// all of them and pulled a frame out of the footage every few seconds; Describe
// held two system prompts and ran the two model jobs that turn that output into
// something the Cut page can read. Nobody ever ran one without the other -- the
// second refused to start until the first had finished, and said so in a status
// line -- so they were one step with a tab in the middle of it. Now they are one
// tab and one ▶: the transcripts and the frames, then the describing and the
// fixing, in the order they have to happen anyway.
//
// The page is halves: the sources on the left, and on the right one box
// switched by a menu -- this session's context first, then every system prompt
// in the app, in the order the pipeline sends them (prepedit.go). Not just this
// page's two. A prompt is read once, edited before the first run and then left
// alone for the rest of the project, while the pages that send them are where
// the session's work happens, so a prompt sitting on its own page cost that
// page room all session for a control used in the first ten minutes. Here they
// cost nothing and gain something: reading down the menu is reading the run.
//
// The jobs stay separate on disk -- step1/, step2/describe/ and
// step2/transcript/ -- because the describer resumes per chunk and the fixer
// does not, and because a folder layout that changes with the tabs orphans
// every project made before the change.
//
// The context is the box's first row and it is not a prompt: it is what the
// editor knows about THIS session, and every step's requests carry it
// (context.go). It leads because it is the one row a session actually has to
// write, and it sits among the prompts because writing it beside them is what
// stops it being written into them.
//
// Runners: pipeline.go (transcripts and frames), describe.go (describe)
// and cut.go (fix + session timeline).

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotk4/pkg/pango"
)

type preproc struct {
	a *App

	inputs        *gtk.Label // one line: what the sources are, and what they become
	inputsOut     *gtk.Label // how much is already in each of the three output folders
	describeOut   *gtk.Label
	transcriptOut *gtk.Label
}

// ---- page ------------------------------------------------------------------

func (a *App) buildPrep() gtk.Widgetter {
	p := &preproc{a: a}
	a.prep = p

	// One line, not a listing. The names and the per-source arithmetic go to
	// the log when the run starts -- that is scrollable and this is not, and a
	// block of grey text at the top of the page only pushes the page down.
	//
	// The same row as Cut's and Narrate's, down to the word and the margins: an
	// "Inputs:" in heading weight and the line in plain text beside it.
	p.inputs = gtk.NewLabel("")
	p.inputs.SetXAlign(0)
	p.inputs.SetHExpand(true)
	p.inputs.SetEllipsize(pango.EllipsizeEnd) // never a floor under the window
	inLbl := gtk.NewLabel("Inputs:")
	inLbl.AddCSSClass("heading")
	inRow := gtk.NewBox(gtk.OrientationHorizontal, 8)
	inRow.SetMarginStart(12)
	inRow.SetMarginEnd(12)
	inRow.SetMarginTop(6)
	inRow.Append(inLbl)
	inRow.Append(p.inputs)

	// The session's files down the left, the context and every prompt the
	// pipeline sends down the right (prepedit.go), and a handle between them,
	// opening at the middle.
	//
	// Half each, because neither side is a sidebar any more: the right box is
	// where the context gets written and where every prompt in the run is read,
	// which is as much of the work as the list of files is. Both sides resize with the
	// window, so the 50/50 holds as the window grows, and the handle is still
	// there for the sessions where one side earns more than half.
	//
	// Both sides stand the same distance off the handle, so neither frame runs
	// into it on the side where the two are compared.
	bench := a.prepEditor()
	gtk.BaseWidget(bench).SetMarginStart(12)
	sources := a.buildSources()
	sources.SetMarginEnd(12)

	outer := gtk.NewPaned(gtk.OrientationHorizontal)
	outer.SetStartChild(sources)
	outer.SetEndChild(bench)
	outer.SetResizeStartChild(true)
	outer.SetResizeEndChild(true)
	// The handle opens at the middle. A Paned with no position set gives each
	// child what it asks for, which here is whatever the file list happens to
	// want -- so the split is set to half the real width once the pane is on
	// screen and measured. Map fires again every time the tab is shown, hence
	// the once guard; after that first placement the handle is the user's.
	split := false
	outer.ConnectMap(func() {
		if split {
			return
		}
		split = true
		glib.IdleAdd(func() {
			outer.SetPosition(outer.AllocatedWidth() / 2)
		})
	})
	// Shrink off on both ends: shrink means a child may be allocated less than
	// it needs, and what these two need includes a heading row and a button
	// row. With it off, GtkPaned clamps the handle to what both children need,
	// so a shorter window moves the handle instead of hiding half a column.
	outer.SetShrinkStartChild(false)
	outer.SetShrinkEndChild(false)
	outer.SetVExpand(true)
	// 12 from the window's edges, like the columns on Cut and Narrate, so the
	// boxes line up with the Inputs row above them and with the same boxes one
	// tab over.
	outer.SetMarginStart(12)
	outer.SetMarginEnd(12)
	// and 6 off the shared bar below, so the Freq row and the editor frame do
	// not sit on the transport buttons -- the breathing room every other edge
	// of the page already has
	outer.SetMarginBottom(6)

	// The three folders one press of ▶ writes, and no path above them: the
	// output folder is set once, in the row under the list, and repeating it
	// here would be a line of chrome for something that changes once a project.
	//
	// What was written is in the log, by name, so these are not a listing --
	// they are the open-folder symbol under the name each half of the run calls
	// its output, with how much is in it. The question they get asked before a
	// run is whether this already happened and to which part; the age of the
	// newest file answers "is that from today?" and is one hover away. The
	// group rides the shared bottom bar (outStack in main.go), like every
	// step's, so this page holds only the three triples, not the heading.
	outRow := gtk.NewBox(gtk.OrientationHorizontal, 8)
	for i, o := range []struct {
		name, tip string
		dir       func() string
		into      **gtk.Label
	}{
		{"Inputs", "step1/ — the transcripts, the frames, and who spoke when",
			a.inputsDir, &p.inputsOut},
		{"Describe", "step2/describe/ — the event logs, one per video",
			a.describeDir, &p.describeOut},
		{"Transcript", "step2/transcript/ — the fixed transcripts, the subtitles and the session timeline",
			a.transcriptDir, &p.transcriptOut},
	} {
		dir := o.dir
		btn := gtk.NewButtonFromIconName("folder-open-symbolic")
		btn.SetTooltipText(o.tip)
		btn.ConnectClicked(func() { a.openFolder(dir()) })
		if i > 0 {
			btn.SetMarginStart(12) // the triples read as triples; even spacing reads as nine things
		}
		lbl := gtk.NewLabel("")
		*o.into = lbl
		outRow.Append(btn)
		outRow.Append(gtk.NewLabel(o.name + ":"))
		outRow.Append(lbl)
	}
	a.outStack.AddNamed(outRow, "prep")

	// Inputs at the top and the work below -- no prompt row at the bottom any
	// more, because this page's prompts live in the right-hand box now, behind
	// its menu. The Outputs group is on the shared bar below all of it.
	page := gtk.NewBox(gtk.OrientationVertical, 4)
	page.Append(inRow)
	page.Append(outer)

	p.refresh()
	return page
}

func (a *App) buildSources() *gtk.Box {
	// every edit to the list changes what a run would see and who the narration
	// would be spoken by, so the snapshot and the voice picker are refreshed
	// from here rather than from each of the six things that can edit a row
	a.srcList = newSourceList(func() {
		a.snapSources()
		if a.voicePick != nil {
			a.voicePick.refreshNarrators()
		}
		a.prep.refresh()
		a.refreshCut() // the tracks ARE this list: a row added or unmarked changes them
	})

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
	// The buttons say "source" so the list above them does not need a heading
	// to. A "Sources" title over two buttons reading "Add files…" and a list
	// of file names was a line of the page spent on a word the buttons were
	// one adjective away from carrying themselves -- and this half of the page
	// is the list, so there was nothing for the heading to tell it apart from.
	addBtn := gtk.NewButtonWithLabel("Add source files…")
	addBtn.SetTooltipText("Add recordings or footage — several at once")
	addBtn.ConnectClicked(a.addFilesDialog)
	addDirBtn := gtk.NewButtonWithLabel("Add source folder…")
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

	listScroll := gtk.NewScrolledWindow()
	listScroll.SetChild(a.srcList.box)
	listScroll.SetVExpand(true)
	// what the heading used to say, kept on the thing it was about
	listScroll.SetTooltipText("Every file here is transcribed, and placed on the session clock by the timestamp in its name")
	sources := gtk.NewBox(gtk.OrientationVertical, 4)
	sources.SetVExpand(true)
	sources.Append(addRow)
	sources.Append(listScroll)

	// The bottom line, one line: how the frames are taken, and what language
	// they are spoken in. Where it all lands used to be chosen here too; it is
	// the project file's own folder now (project.go), so there is nothing to
	// choose, and what has been written into it is counted along the bottom of
	// the page where the other two folders this step writes are.
	bottom := gtk.NewBox(gtk.OrientationHorizontal, 8)
	bottom.Append(gtk.NewLabel("Freq:"))
	bottom.Append(a.interval.box)
	bottom.Append(a.scalePick)
	bottom.Append(gtk.NewLabel("Language:"))
	bottom.Append(a.langEntry)

	// No margins of its own: it is one side of the page's divider now, and the
	// page keeps its columns 12 from the window's edges as Cut and Narrate do.
	box := gtk.NewBox(gtk.OrientationVertical, 12)
	box.SetVExpand(true)
	box.Append(sources)
	box.Append(bottom) // how the frames are taken, under the list they come from
	return box
}

// ---- what will be sent -------------------------------------------------------

// refresh re-reads everything on the page that is derived from the sources or
// from the output folder: what a run would send, and how much of it is already
// there. Safe on a page that has not been built -- it is called from the
// project loader and from the rescan, both of which can run first.
func (p *preproc) refresh() {
	if p == nil || p.inputs == nil {
		return
	}
	line, detail := p.a.inputsSummary()
	p.inputs.SetText(line)
	p.inputs.SetTooltipText(detail) // the per-file arithmetic, on hover
	setOutCount(p.inputsOut, p.a.inputsDir())
	setOutCount(p.describeOut, p.a.describeDir())
	setOutCount(p.transcriptOut, p.a.transcriptDir())

	if p.a.progress == nil || p.a.running {
		return // the runner owns the bar's text while it is going
	}
	// The bar says what this project has already got when nothing is running:
	// which is the one thing the three counts above cannot say at a glance,
	// since a project can have transcripts and no frames.
	if frames, _ := os.ReadDir(filepath.Join(p.a.inputsDir(), "frames")); len(frames) > 0 {
		p.a.progress.SetText(fmt.Sprintf("prepared (%d frame set(s))", len(frames)))
	} else {
		p.a.progress.SetText("Prepare has not run yet")
	}
}

// setOutCount fills one of the three output readings: how many files on the
// row, when the newest was written on hover. Three of the one-line form side by
// side is a paragraph across the bottom of the page, and the question the row
// is asked is how much is there.
func setOutCount(l *gtk.Label, dir string) {
	if l == nil {
		return
	}
	n, newest := countOutputs(dir)
	if n == 0 {
		l.SetText("nothing yet")
		l.SetTooltipText("")
		return
	}
	l.SetText(fmt.Sprintf("%d files", n))
	l.SetTooltipText("newest " + humanAgo(newest))
}

// inputsSummary walks the session's sources once and answers both questions
// about them: line is what the page shows -- how many files came in and how many
// requests they become -- and detail is the same thing per file, for the tooltip
// and for the log at the start of a run. One walk, because it reads every
// video's frame directory and this runs on every edit to the list.
//
// framesPerReq and fixBlock decide the request counts and are compiled in;
// these two strings are the only place they are visible.
func (a *App) inputsSummary() (line, detail string) {
	if a.srcList == nil {
		return "", ""
	}
	vids, auds := a.srcList.split()
	if len(vids)+len(auds) == 0 {
		return "no input files — add some above", "(no sources)"
	}
	var b strings.Builder
	frames, vision, lines, fixes := 0, 0, 0, 0
	descDir := a.describeDir()
	count := func(base string) {
		n := len(loadSeg4(a.transcriptPath(base)))
		lines += n
		fixes += (n + fixBlock - 1) / fixBlock
	}
	for _, v := range vids {
		base := baseName(v)
		fmt.Fprintf(&b, "%s\n", base)
		if p, err := a.planVideo(v, descDir); err != nil {
			fmt.Fprintf(&b, "  frames: %v\n", err)
		} else {
			scale := p.scale
			if scale == "" {
				scale = "original"
			}
			frames += len(p.frames)
			vision += p.chunks
			fmt.Fprintf(&b, "  %d frames @ %gs (%s)  ->  %d vision requests\n",
				len(p.frames), p.interval, scale, p.chunks)
		}
		count(base)
		b.WriteString(fixerLine(a.transcriptPath(base)))
	}
	for _, aud := range auds {
		base := baseName(aud)
		fmt.Fprintf(&b, "%s\n", base)
		count(base)
		b.WriteString(fixerLine(a.transcriptPath(base)))
	}
	line = fmt.Sprintf("%d input files loaded (%d footage, %d voice) · %d frames → %d vision requests · "+
		"%d transcript lines → %d fixer requests · names in the log",
		len(vids)+len(auds), len(vids), len(auds), frames, vision, lines, fixes)
	return line, strings.TrimRight(b.String(), "\n")
}

func (a *App) transcriptPath(base string) string {
	return filepath.Join(a.inputsDir(), base, "transcript.tsv")
}

func fixerLine(path string) string {
	if !exists(path) {
		return "  transcript.tsv: missing -- not transcribed yet\n"
	}
	n := len(loadSeg4(path))
	return fmt.Sprintf("  transcript.tsv, %d lines  ->  %d fixer requests\n",
		n, (n+fixBlock-1)/fixBlock)
}

// ---- run --------------------------------------------------------------------

// prepRun validates the sources and starts the whole step: transcripts and
// frames, then describing and fixing, in one press. They were two buttons on
// two tabs, and the second refused to start until the first had finished -- so
// what the two buttons offered was the chance to press them in the wrong order.
//
// There is still no describe-only or fix-only run inside the second half:
// describe resumes per chunk, so running the pair costs no more than running
// the fixer alone, and half of what the fixer is for is the events the
// describer just wrote.
func (a *App) prepRun() {
	if a.running {
		return
	}
	// one source is enough: a session can be a single screen recording that is
	// both the footage and every voice on it, and it can equally be a recording
	// with no footage at all
	vids, auds := a.snapSources()
	if len(vids)+len(auds) == 0 {
		a.setStatus("add at least one source")
		return
	}
	// two sources of the same name would write into one folder under step1/,
	// and the second would quietly overwrite the first's transcript
	if x, y := a.srcList.clash(); x != "" {
		a.logf("!!! %s and %s are both step1/%s -- rename one", x, y, baseName(x))
		a.setStatus(fmt.Sprintf("%s and %s have the same name — rename one",
			filepath.Base(x), filepath.Base(y)))
		return
	}
	// The working copy always reflects what actually ran -- and so does the
	// named project, since saveProjectNow writes every target. It used to be
	// saveProjectTo(project.json), which also RENAMED the open project to the
	// working copy: running the step quietly stopped the autosave following the
	// file you had opened, and the header bar changed under you to say so.
	a.saveProjectNow()
	// ⏹ then ▶ is "do it again", not "carry on" -- and this is the moment to
	// act on that rather than the stop itself: pressing ⏹ has to be safe to do
	// at the end of the day, with the half-run still on disk in the morning.
	// It is the press that starts the work over that throws the work away.
	if err := a.undFreshStart(); err != nil {
		// nothing here is worth refusing to run over: say what could not be
		// cleared, and let the run pick up from what is still on disk
		a.logf(">>> could not clear the last run (%v) -- resuming it", err)
	}
	scaleName, scaleVF := a.frameScale()
	a.startPrep(vids, auds, a.frameInterval(), scaleName, scaleVF)
}

// undFreshStart is the whole difference between ⏸ and ⏹ on this page. Paused
// is parked: the goroutine is still sitting in checkpoint and ▶ lets it go on.
// Stopped is abandoned, and the ▶ after it starts the describing from the
// beginning -- which means dropping the event logs it resumes from. For the
// fixer it means nothing, since it never resumed in the first place, and for
// the transcripts and the frames it means nothing either: those are per-file
// and already on disk, and re-listening to an hour of audio to reach the same
// answer is not what "start over" was asking for.
//
// It does nothing unless the last run ended in a stop that had reached the
// describing, so a ▶ on a project described days ago still resumes it, and a ⏹
// during the transcribing does not throw away last week's describe. Only ⏹
// arms this.
func (a *App) undFreshStart() error {
	if !a.undRestart {
		return nil
	}
	a.undRestart = false
	return a.resetDescribe()
}

func (a *App) startPrep(videos, audios []string, interval float64, scaleName, scaleVF string) {
	a.running = true
	a.stopFlag.Store(false)
	a.pauseFlag.Store(false)
	a.runCtx, a.runCancel = context.WithCancel(context.Background())
	a.qReset()
	a.updateRunControls()
	a.prog(trackSTT, 0, "preparing")
	a.logExp.SetExpanded(true)
	// what went in, by name -- the page has room for a count and nothing more
	a.logf(">>> prepare: %d input files", len(videos)+len(audios))
	for _, f := range append(append([]string{}, videos...), audios...) {
		a.logf("    %s", f)
	}
	if _, detail := a.inputsSummary(); detail != "" {
		a.logf("%s", detail)
	}
	a.logCtx("prepare")
	go func() {
		described, err := a.prepare(videos, audios, interval, scaleName, scaleVF)
		glib.IdleAdd(func() {
			a.running = false
			a.updateRunControls()
			switch {
			case errors.Is(err, errStopped):
				// the work stays on disk until the next ▶, which is the press
				// that decides it was not wanted (undFreshStart) -- and only
				// the describing is thrown away, so a stop during the
				// transcribing arms nothing
				a.undRestart = described
				a.progress.SetText("stopped — finished work is kept; ⏸ was the way to keep a place")
			case err != nil:
				a.logf("prepare FAILED: %v", err)
				a.progress.SetText("failed — see log")
			default:
				a.progress.SetFraction(1)
				a.logf(">>> prepare wrote:")
				n := a.logOutputs("inputs", a.inputsDir()) +
					a.logOutputs("describe", a.describeDir()) +
					a.logOutputs("transcript", a.transcriptDir())
				a.progress.SetText(fmt.Sprintf("prepared — %d files", n))
			}
			a.prep.refresh()
			a.updateGates()
			// the frames and the session timeline the Cut page draws are what
			// this run just wrote -- including the first time, where the tab it
			// unlocks would otherwise open on nothing
			a.refreshCut()
		})
	}()
}

// prepInputsShare is how much of the progress bar the first half gets. The two
// halves are weighted by how long they TAKE, as the tracks inside each of them
// are: transcribing an hour of audio and pulling a frame out of it every couple
// of seconds is minutes, and describing those frames is a model call per
// handful of them and is the longest wait in the app. A third to the front is
// a rule of thumb from real sessions, and being roughly right is the whole
// requirement -- what a progress bar owes you is a direction, not an ETA.
const prepInputsShare = 0.3

// prepare is the whole press: the transcripts and the frames, then the
// describing and the fixing. It reports whether the second half had begun,
// which is what a ⏹ needs to know -- that is the half a restart throws away.
//
// Neither half knows the other exists: each still divides its own work into two
// tracks that add up to a whole bar. qPhase is what puts each of them in its
// own slice of it, so the needle crosses the middle once and never goes back.
func (a *App) prepare(videos, audios []string, interval float64, scaleName, scaleVF string) (bool, error) {
	a.qPhase(0, prepInputsShare)
	if err := a.ingest(videos, audios, interval, scaleName, scaleVF); err != nil {
		return false, err
	}
	a.qPhase(prepInputsShare, 1-prepInputsShare)
	return true, a.understand(videos, audios)
}

func (a *App) understand(videos, audios []string) error {
	// two jobs, one after the other, and the bar says which of the two it is
	// on: this page's ▶ is the longest press in the app, and "1/2" is the
	// difference between halfway through and nearly done.
	a.qJob(trackDescribe, "describe", 1, 2)
	if err := a.describeAll(videos, audios, 0.5); err != nil {
		return err
	}
	a.qJob(trackFix, "transcript", 2, 2)
	return a.fixTranscripts(videos, audios, 0.5)
}
