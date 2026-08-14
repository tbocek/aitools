package main

// The Describe + Transcript page: one screen for the two LLM jobs that turn
// step 1's raw output into something the cut step can read.
//
// They were two pages, five text boxes and two run buttons. Three of those
// boxes did nothing a fourth could not: each page had a "notes" field that was
// glued onto its system prompt just before the request went out, so what the
// model was actually told existed nowhere on screen. One box per job now, and
// the box IS the prompt (see prompts.go).
//
// The jobs stay separate on disk -- step2/describe/ and step2/transcript/ --
// because the describer resumes per chunk and the fixer does not.
//
// The third box on the page is not a prompt: it is what the editor knows about
// THIS session, and every step's requests carry it (context.go). It lives here
// because this is the first page whose jobs can use it, and because writing it
// beside the two prompts is what stops it being written into them.
//
// What is left on screen is a line of counts and the two prompts. Everything
// that used to be a paragraph or a listing here -- what the jobs do, which file
// is how long, what came back -- is in the log, where it scrolls and where it
// stays after the run instead of competing with the boxes for the window.
//
// Runners: step2_describe.go (describe) and step3.go (fix + session timeline).

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotk4/pkg/pango"
)

type understander struct {
	a *App

	inputs *gtk.Label // one line: what step 1 left behind, and what it becomes
	s2out  *gtk.Label // how much is already in each half of the output folder
	s3out  *gtk.Label
}

// ---- page ------------------------------------------------------------------

func (a *App) buildUnderstand() gtk.Widgetter {
	u := &understander{a: a}
	a.und = u

	// One line, not a listing. The names and the per-source arithmetic go to
	// the log when the run starts -- that is scrollable and this is not, and a
	// block of grey text above the prompts only pushed them off the screen.
	//
	// The same row as Cut's and Narrate's, down to the word and the margins: an
	// "Inputs:" in heading weight and the line in plain text beside it. This one
	// was a bare line of grey with no label, which on three pages that answer the
	// same question three ways is one page you have to stop and read.
	u.inputs = gtk.NewLabel("")
	u.inputs.SetXAlign(0)
	u.inputs.SetHExpand(true)
	u.inputs.SetEllipsize(pango.EllipsizeEnd) // never a floor under the window
	inLbl := gtk.NewLabel("Inputs:")
	inLbl.AddCSSClass("heading")
	inRow := gtk.NewBox(gtk.OrientationHorizontal, 8)
	inRow.SetMarginStart(12)
	inRow.SetMarginEnd(12)
	inRow.SetMarginTop(6)
	inRow.Append(inLbl)
	inRow.Append(u.inputs)

	// The titles used to be numbered and to spell out the whole batching scheme,
	// which made the two headings longer than some of the lines in the prompts
	// under them. The tab already says where in the run this is, and the counts
	// -- compiled in, and visible nowhere else -- are one hover away.
	describe := a.promptEditor("describe", "Describe", fmt.Sprintf(
		"%d frames per request, plus the last %d descriptions and up to %d spoken "+
			"lines either side as context. No frame is ever sent twice: those "+
			"descriptions are the model's only memory of what it already saw.",
		framesPerReq, recentEvents, ctxSegs))
	fix := a.promptEditor("fix", "Transcript", fmt.Sprintf(
		"The fixer: %d transcript lines per request, each block given what every "+
			"other source showed or said at the same moment.", fixBlock))

	// A divider, not two fixed boxes: which prompt you are working on is the
	// whole variable here, and the one you are not working on is a reference you
	// want a couple of lines of rather than half the window. Each side keeps its
	// own scrollbar, so dragging one small hides nothing -- it scrolls.
	split := gtk.NewPaned(gtk.OrientationVertical)
	split.SetStartChild(describe)
	split.SetEndChild(fix)
	split.SetResizeStartChild(true)
	split.SetResizeEndChild(true)
	// Shrink OFF on both ends. It was on, so that the drag would not stop at
	// whichever prompt was taller -- but shrink means a child may be allocated
	// less than it needs, and what a prompt box needs includes its own heading
	// and its Reset button. Made the window short and the divider stayed put:
	// the top box kept a height the page no longer had, and its heading went off
	// the top edge. With shrink off, GtkPaned clamps the divider to what both
	// children need, so a shorter window moves the divider instead of hiding a
	// box. The floor is four lines of text (see promptEditor), so the drag still
	// travels nearly the whole way and neither box can be pushed out of reach.
	split.SetShrinkStartChild(false)
	split.SetShrinkEndChild(false)
	split.SetVExpand(true)

	// ...and a second divider across it, with this session's context on the
	// right. It sits here because this is the first page that reads anything a
	// model can be told about, and because it is written once, before the first
	// run, next to the two prompts it is read alongside. Every later step gets
	// it too (context.go) -- that is the point of one box.
	//
	// The prompts resize with the window and the context does not: the context
	// is a paragraph or two of the user's own text, so a wider window is worth
	// nothing to it and is worth a great deal to a system prompt. Dragging the
	// handle is what makes it wider, and that drag is exactly what makes
	// Describe and Transcript narrower, which is what it is for.
	//
	// Both sides stand the same distance off the handle. The prompts had nothing
	// on that edge, so their frame ran into the divider and stopped reading as a
	// frame on the side where it is compared with the context box's.
	ctxPane := a.contextEditor()
	gtk.BaseWidget(ctxPane).SetSizeRequest(280, -1)
	gtk.BaseWidget(ctxPane).SetMarginStart(12)
	split.SetMarginEnd(12)
	outer := gtk.NewPaned(gtk.OrientationHorizontal)
	outer.SetStartChild(split)
	outer.SetEndChild(ctxPane)
	outer.SetResizeStartChild(true)
	outer.SetResizeEndChild(false)
	outer.SetShrinkStartChild(false)
	outer.SetShrinkEndChild(false)
	outer.SetVExpand(true)
	// 12 from the window's edges, like the columns on Cut and Narrate, so the
	// boxes line up with the Inputs row above them and with the same boxes one
	// tab over. It was 16 here and nowhere else.
	outer.SetMarginStart(12)
	outer.SetMarginEnd(12)

	// The two folders this page writes, and no path above them: the output
	// folder is Inputs' setting, said in full there, and repeating it on every
	// page that writes into it is a line of chrome per page for something that
	// changes once a project. These buttons lead to the right place anyway.
	//
	// What was written is in the log, by name, so these are not a listing --
	// they are the open-folder symbol under the names this page calls its two
	// halves, each with how much is in it. The question they get asked before a
	// run is whether this already happened, and to which half; "Open step2
	// folder" spelled out the path and answered neither.
	openD := gtk.NewButtonFromIconName("folder-open-symbolic")
	openD.SetTooltipText("step2/describe/ — the event logs, one per video")
	openD.ConnectClicked(func() { a.openFolder(a.describeDir()) })
	openF := gtk.NewButtonFromIconName("folder-open-symbolic")
	openF.SetTooltipText("step2/transcript/ — the fixed transcripts, the subtitles and the session timeline")
	openF.ConnectClicked(func() { a.openFolder(a.transcriptDir()) })
	openF.SetMarginStart(12) // the pairs read as pairs; even spacing reads as four things
	// Same words, same weight, same end of the row as on Inputs: "Outputs:" in
	// heading weight and the count in plain text, not dimmed. Inputs puts this
	// at the right-hand end of its bottom row, and a reading you have to look
	// for in a different place on each page is one you stop trusting.
	u.s2out = gtk.NewLabel("")
	u.s3out = gtk.NewLabel("")
	outLbl := gtk.NewLabel("Outputs:")
	outLbl.AddCSSClass("heading")
	outRow := gtk.NewBox(gtk.OrientationHorizontal, 8)
	outRow.SetHAlign(gtk.AlignEnd)
	outRow.SetMarginEnd(12)
	outRow.SetMarginBottom(6)
	outRow.Append(outLbl)
	outRow.Append(openD)
	outRow.Append(gtk.NewLabel("Describe:"))
	outRow.Append(u.s2out)
	outRow.Append(openF)
	outRow.Append(gtk.NewLabel("Transcript:"))
	outRow.Append(u.s3out)

	// Inputs at the top, work in the middle, Outputs at the bottom -- the three
	// rows in the order and the spacing every other step has.
	//
	// No scrollbar on the page itself. It had one because two whole system
	// prompts are taller than any window -- but a page that scrolls around a
	// divider makes the divider meaningless: the paned would be handed the full
	// natural height of both prompts and there would be no spare room to drag.
	// The scrolling belongs to the two halves, which already have it.
	page := gtk.NewBox(gtk.OrientationVertical, 4)
	page.Append(inRow)
	page.Append(outer)
	page.Append(outRow)

	u.refresh()
	return page
}

// ---- what will be sent -------------------------------------------------------

func (u *understander) refresh() {
	if u == nil || u.inputs == nil {
		return
	}
	line, detail := u.a.inputsSummary()
	u.inputs.SetText(line)
	u.inputs.SetTooltipText(detail) // the per-file arithmetic, on hover
	if u.s2out != nil {
		u.s2out.SetText(summarizeOutputs(u.a.describeDir()))
		u.s3out.SetText(summarizeOutputs(u.a.transcriptDir()))
	}
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
		return "no input files — add some on the Inputs step", "(nothing on the Inputs step)"
	}
	var b strings.Builder
	frames, vision, lines, fixes := 0, 0, 0, 0
	s2 := a.describeDir()
	count := func(base string) {
		n := len(loadSeg4(a.transcriptPath(base)))
		lines += n
		fixes += (n + fixBlock - 1) / fixBlock
	}
	for _, v := range vids {
		base := baseName(v)
		fmt.Fprintf(&b, "%s\n", base)
		if p, err := a.planVideo(v, s2); err != nil {
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
	return filepath.Join(a.outDir, "step1", base, "transcript.tsv")
}

func fixerLine(path string) string {
	if !exists(path) {
		return "  transcript.tsv: missing -- run the Inputs step\n"
	}
	n := len(loadSeg4(path))
	return fmt.Sprintf("  transcript.tsv, %d lines  ->  %d fixer requests\n",
		n, (n+fixBlock-1)/fixBlock)
}

// ---- run --------------------------------------------------------------------

// understandRun validates the selection and starts both jobs. There is no
// describe-only or fix-only button: describe already resumes per chunk, so
// running the pair costs no more than running the fixer alone, and half of
// what the fixer is for is the events the describer just wrote.
func (a *App) understandRun() {
	if a.running {
		return
	}
	vids, auds := a.snapSources()
	if len(vids)+len(auds) == 0 {
		a.setStatus("add at least one source on the Inputs step")
		return
	}
	for _, f := range append(append([]string{}, vids...), auds...) {
		if !exists(a.transcriptPath(baseName(f))) {
			a.setStatus("run the Inputs step first — transcript missing for " + baseName(f))
			return
		}
	}
	a.saveProjectNow() // the run is a moment worth a file, whatever the ticker is doing
	a.startUnderstand(vids, auds)
}

func (a *App) startUnderstand(videos, audios []string) {
	a.running = true
	a.stopFlag.Store(false)
	a.pauseFlag.Store(false)
	a.runCtx, a.runCancel = context.WithCancel(context.Background())
	a.progMu.Lock()
	a.progParts = [2]float64{}
	a.progTexts = [2]string{}
	a.progMu.Unlock()
	a.updateRunControls()
	a.setStatus("describe + transcript running…")
	a.logExp.SetExpanded(true)
	// what went in, by name -- the page only has room for the count
	a.logf(">>> describe + transcript: %d input files", len(videos)+len(audios))
	for _, f := range append(append([]string{}, videos...), audios...) {
		a.logf("    %s", f)
	}
	if _, detail := a.inputsSummary(); detail != "" {
		a.logf("%s", detail)
	}
	a.logCtx("describe + transcript")
	go func() {
		err := a.understand(videos, audios)
		glib.IdleAdd(func() {
			a.running = false
			a.updateRunControls()
			switch {
			case errors.Is(err, errStopped):
				a.progress.SetText("stopped — finished chunks are kept")
				a.setStatus("describe + transcript stopped")
			case err != nil:
				a.logf("describe + transcript FAILED: %v", err)
				a.progress.SetText("failed — see log")
				a.setStatus("describe + transcript failed")
			default:
				a.progress.SetFraction(1)
				a.logf(">>> describe + transcript wrote:")
				n := a.logOutputs("describe", a.describeDir()) +
					a.logOutputs("transcript", a.transcriptDir())
				a.setStatus(fmt.Sprintf("describe + transcript done — %d files under step2/", n))
			}
			a.und.refresh()
			a.updateGates()
		})
	}()
}

// understand runs the page's two jobs back to back, each owning half the
// progress bar. Weighting them by request count instead would have handed the
// bar to the describer -- it makes hundreds of small vision calls where the
// fixer makes dozens of big text ones -- which is the same trap step 1 fell
// into.
func (a *App) understand(videos, audios []string) error {
	if err := a.describeAll(videos, audios, 0.5); err != nil {
		return err
	}
	return a.fixTranscripts(videos, audios, 0.5)
}
