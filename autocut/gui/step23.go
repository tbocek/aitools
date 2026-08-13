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
// The jobs stay separate on disk -- step2/ and step3/ -- because describe
// resumes per chunk and the fixer does not, and because a project written by
// an older build must still find its event logs where it left them.
//
// Runners: step2.go (describe) and step3.go (fix + session timeline).

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

type understander struct {
	a *App

	inputs *gtk.Label // what step 1 left behind, and what will be sent
	state  *gtk.Label // how far this step has got with each source
	out    *gtk.Label // the two output folders
}

// ---- page ------------------------------------------------------------------

func (a *App) buildUnderstand() gtk.Widgetter {
	u := &understander{a: a}
	a.und = u

	expl := gtk.NewLabel("Two LLM jobs, in this order. Describe walks every checked video's stored " +
		"frames with the vision model and writes one event log per video. Transcript then fixes " +
		"every recording's ASR lines against those events and against what the other recordings " +
		"heard at the same moment, and merges everything into one session timeline for the cut " +
		"step. ▶ runs both; describe resumes where it stopped, the fixer always redoes every block.")
	expl.SetXAlign(0)
	expl.SetWrap(true)
	expl.AddCSSClass("dim-label")

	// Inputs first, and in full: every complaint about this step so far has
	// been about not knowing what was actually sent.
	inLbl := gtk.NewLabel("Inputs — this is what goes to the LLM")
	inLbl.SetXAlign(0)
	inLbl.AddCSSClass("heading")
	u.inputs = gtk.NewLabel("")
	u.inputs.SetXAlign(0)
	u.inputs.SetYAlign(0)
	u.inputs.SetWrap(true)
	u.inputs.AddCSSClass("monospace")
	inScroll := gtk.NewScrolledWindow()
	inScroll.SetChild(u.inputs)
	inScroll.SetPropagateNaturalHeight(true)
	inScroll.SetPolicy(gtk.PolicyNever, gtk.PolicyAutomatic)
	inScroll.SetMaxContentHeight(200)
	inScroll.AddCSSClass("frame")

	rides := gtk.NewLabel("Every vision request also carries the running state of the game, the last " +
		"three events and the words heard in those seconds; every fixer request carries what the " +
		"other recordings showed and said at the same moment. Frames are resized to 896 px wide " +
		"unless step 1 already stored them that way.")
	rides.SetXAlign(0)
	rides.SetWrap(true)
	rides.AddCSSClass("dim-label")

	dOnly := gtk.NewButtonWithLabel("Describe only")
	dOnly.SetTooltipText("Run just this half — resumes per chunk, so already-described " +
		"footage costs nothing")
	dOnly.ConnectClicked(func() { a.understandRun(true, false) })

	fOnly := gtk.NewButtonWithLabel("Fix only")
	fOnly.SetTooltipText("Run just the fixer against the event logs that are already there — " +
		"what you want after editing this prompt")
	fOnly.ConnectClicked(func() { a.understandRun(false, true) })

	u.state = gtk.NewLabel("")
	u.state.SetXAlign(0)
	u.state.SetWrap(true)

	u.out = gtk.NewLabel("")
	u.out.SetXAlign(0)
	u.out.SetYAlign(0)
	u.out.AddCSSClass("monospace")
	outScroll := gtk.NewScrolledWindow()
	outScroll.SetChild(u.out)
	outScroll.SetPropagateNaturalHeight(true)
	outScroll.SetMaxContentHeight(200)
	outScroll.SetVExpand(true)

	outLbl := gtk.NewLabel("Outputs")
	outLbl.SetXAlign(0)
	outLbl.AddCSSClass("heading")
	openD := gtk.NewButtonWithLabel("Open step2 folder")
	openD.SetTooltipText("The event logs")
	openD.ConnectClicked(func() { a.openFolder(filepath.Join(a.outDir, "step2")) })
	openF := gtk.NewButtonWithLabel("Open step3 folder")
	openF.SetTooltipText("The fixed transcripts, subtitles and the session timeline")
	openF.ConnectClicked(func() { a.openFolder(filepath.Join(a.outDir, "step3")) })
	outHead := gtk.NewBox(gtk.OrientationHorizontal, 8)
	outHead.Append(outLbl)
	outHead.Append(openD)
	outHead.Append(openF)

	box := gtk.NewBox(gtk.OrientationVertical, 12)
	box.SetMarginTop(16)
	box.SetMarginStart(16)
	box.SetMarginEnd(16)
	box.SetMarginBottom(8)
	box.Append(expl)
	box.Append(inLbl)
	box.Append(inScroll)
	box.Append(rides)
	box.Append(jobHead("1 · Describe — the vision model, "+
		fmt.Sprintf("%d frames per request", framesPerReq), dOnly))
	box.Append(a.promptEditor("describe"))
	box.Append(jobHead("2 · Transcript — the fixer, "+
		fmt.Sprintf("%d lines per request", fixBlock), fOnly))
	box.Append(a.promptEditor("fix"))
	box.Append(u.state)
	box.Append(outHead)
	box.Append(outScroll)
	u.refresh()
	// the page scrolls: two whole system prompts are taller than any window,
	// and folding one away is how you forget you changed it
	page := gtk.NewScrolledWindow()
	page.SetChild(box)
	page.SetPolicy(gtk.PolicyNever, gtk.PolicyAutomatic) // never sideways: the labels must keep wrapping
	page.SetVExpand(true)
	return page
}

// jobHead is the rule between the two halves of the page: which job this is,
// and the button that runs only it.
func jobHead(title string, run *gtk.Button) *gtk.Box {
	sep := gtk.NewSeparator(gtk.OrientationHorizontal)
	sep.SetMarginTop(8)

	lbl := gtk.NewLabel(title)
	lbl.SetXAlign(0)
	lbl.SetHExpand(true)
	lbl.AddCSSClass("heading")

	row := gtk.NewBox(gtk.OrientationHorizontal, 8)
	row.Append(lbl)
	row.Append(run)

	b := gtk.NewBox(gtk.OrientationVertical, 6)
	b.Append(sep)
	b.Append(row)
	return b
}

// ---- what will be sent, and what came back ----------------------------------

func (u *understander) refresh() {
	if u == nil || u.inputs == nil {
		return
	}
	u.inputs.SetText(u.a.inputsText())
	u.state.SetText(u.a.stateText())
	u.out.SetText("step2/\n" + indent(describeOutputs(filepath.Join(u.a.outDir, "step2"))) +
		"step3/\n" + indent(describeOutputs(filepath.Join(u.a.outDir, "step3"))))
}

func indent(s string) string {
	var b strings.Builder
	for _, l := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		fmt.Fprintf(&b, "  %s\n", l)
	}
	return b.String()
}

// inputsText counts the step-1 output this page reads and turns it into the
// number of requests it becomes. framesPerReq and fixBlock decide that and are
// compiled in; stating them here is the only place they are visible.
func (a *App) inputsText() string {
	if a.vidList == nil {
		return "(nothing checked on the Inputs step)"
	}
	var b strings.Builder
	s2 := filepath.Join(a.outDir, "step2")
	for _, v := range a.vidList.selected() {
		base := baseName(v)
		fmt.Fprintf(&b, "%s\n", base)
		if p, err := a.planVideo(a.srcPath(v), s2); err != nil {
			fmt.Fprintf(&b, "  frames: %v\n", err)
		} else {
			scale := p.scale
			if scale == "" {
				scale = "original"
			}
			fmt.Fprintf(&b, "  %d frames @ %gs (%s)  ->  %d vision requests\n",
				len(p.frames), p.interval, scale, p.chunks)
		}
		b.WriteString(fixerLine(a.transcriptPath(base)))
	}
	for _, aud := range a.audList.selected() {
		base := baseName(aud)
		fmt.Fprintf(&b, "%s\n", base)
		b.WriteString(fixerLine(a.transcriptPath(base)))
	}
	if b.Len() == 0 {
		return "(nothing checked on the Inputs step)"
	}
	return strings.TrimRight(b.String(), "\n")
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

// stateText is the other half of the question: what has this step already
// produced for each source.
func (a *App) stateText() string {
	if a.vidList == nil {
		return ""
	}
	s2 := filepath.Join(a.outDir, "step2")
	s3 := filepath.Join(a.outDir, "step3")
	var lines []string
	for _, v := range a.vidList.selected() {
		base := baseName(v)
		line := base + ": "
		if b, err := os.ReadFile(filepath.Join(s2, base, "events.tsv")); err == nil {
			line += fmt.Sprintf("%d chunks described", strings.Count(string(b), "\n"))
		} else {
			line += "not described yet"
		}
		if exists(filepath.Join(s3, base, "subtitles.srt")) {
			line += ", subtitles ready"
		} else {
			line += ", not fixed yet"
		}
		lines = append(lines, line)
	}
	for _, aud := range a.audList.selected() {
		base := baseName(aud)
		if exists(filepath.Join(s3, base, "commentary.fixed.tsv")) {
			lines = append(lines, base+": commentary fixed")
		} else {
			lines = append(lines, base+": not fixed yet")
		}
	}
	if exists(filepath.Join(s3, "session.tsv")) {
		lines = append(lines, "session timeline ready")
	}
	if len(lines) == 0 {
		return "this step has not run yet"
	}
	return strings.Join(lines, "\n")
}

// ---- run --------------------------------------------------------------------

// understandRun validates the selection and starts the page's jobs. ▶ asks for
// both; the two small buttons ask for one.
func (a *App) understandRun(doDescribe, doFix bool) {
	if a.running {
		return
	}
	vids := a.vidList.selected()
	auds := a.audList.selected()
	if len(vids) == 0 || len(auds) == 0 {
		a.setStatus("select at least one video and one voice recording on the Inputs step")
		return
	}
	abs := func(rels []string) []string {
		out := make([]string, len(rels))
		for i, r := range rels {
			out[i] = a.srcPath(r)
		}
		return out
	}
	for _, f := range append(abs(vids), abs(auds)...) {
		if !exists(a.transcriptPath(baseName(f))) {
			a.setStatus("run the Inputs step first — transcript missing for " + baseName(f))
			return
		}
	}
	// the fixer grounds lines in the event logs; on its own it needs them to
	// already exist, which is exactly what ▶ does not have to worry about
	if doFix && !doDescribe {
		for _, v := range vids {
			if !exists(filepath.Join(a.outDir, "step2", baseName(v), "events.tsv")) {
				a.setStatus("nothing described for " + baseName(v) + " yet — press ▶ to run both")
				return
			}
		}
	}
	a.saveProjectTo(filepath.Join(a.root, "project.json"))
	a.startUnderstand(abs(vids), abs(auds), doDescribe, doFix)
}

func (a *App) startUnderstand(videos, audios []string, doDescribe, doFix bool) {
	a.running = true
	a.stopFlag.Store(false)
	a.pauseFlag.Store(false)
	a.runCtx, a.runCancel = context.WithCancel(context.Background())
	a.progMu.Lock()
	a.progParts = [2]float64{}
	a.progTexts = [2]string{}
	a.progMu.Unlock()
	a.updateRunControls()
	a.setStatus(jobName(doDescribe, doFix) + " running…")
	a.logExp.SetExpanded(true)
	go func() {
		err := a.understand(videos, audios, doDescribe, doFix)
		glib.IdleAdd(func() {
			a.running = false
			a.updateRunControls()
			switch {
			case errors.Is(err, errStopped):
				a.progress.SetText("stopped — finished chunks are kept")
				a.setStatus(jobName(doDescribe, doFix) + " stopped")
			case err != nil:
				a.logf("%s FAILED: %v", jobName(doDescribe, doFix), err)
				a.progress.SetText("failed — see log")
				a.setStatus(jobName(doDescribe, doFix) + " failed")
			default:
				a.progress.SetFraction(1)
				a.setStatus(jobName(doDescribe, doFix) + " done")
			}
			a.und.refresh()
			a.updateGates()
		})
	}()
}

func jobName(doDescribe, doFix bool) string {
	switch {
	case doDescribe && doFix:
		return "describe + transcript"
	case doDescribe:
		return "describe"
	default:
		return "transcript"
	}
}

// understand runs the page's two jobs back to back, each owning half the
// progress bar. Weighting them by request count instead would have handed the
// bar to the describer -- it makes hundreds of small vision calls where the
// fixer makes dozens of big text ones -- which is the same trap step 1 fell
// into. A job running alone gets the whole bar.
func (a *App) understand(videos, audios []string, doDescribe, doFix bool) error {
	span := 1.0
	if doDescribe && doFix {
		span = 0.5
	}
	if doDescribe {
		if err := a.step2(videos, span); err != nil {
			return err
		}
	}
	if doFix {
		if err := a.step3(videos, audios, span); err != nil {
			return err
		}
	}
	return nil
}
