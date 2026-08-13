package main

// Step 2: describe. Every checked video's stored frames (step1/frames/<v>/)
// go to the vision LLM in small batches together with the game-audio words
// heard in those seconds; a rolling "state of the game" plus the last events
// make each batch a description of what is HAPPENING, not stills.
// Output: step2/<video>/events.tsv, resumable per chunk.
// Alignment against the voice recordings is step 3's job (step3.go).

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

const framesPerReq = 4

// The primer stays empty of game knowledge on purpose: whatever is specific
// about this footage belongs in the user's notes box, which is appended here.
const describeSystem = `You describe screen-recorded footage for a video editor.
You get consecutive frames covering a few seconds, the words heard in the
recording's own audio during them, and the running state from the previous
chunk. Say what you actually see; never assume a genre or a game you have not
been told about.
Reply with exactly two lines and nothing else:
EVENT: what happens in these seconds -- concrete and specific, max 35 words.
STATE: updated running state, max 50 words: location, current activity,
who is around, the ongoing goal. Carry forward what is still true.`

type tsvRow struct {
	s, e float64
	text string
}

func loadTSVRows(path string) []tsvRow {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []tsvRow
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Split(line, "\t")
		if len(f) < 4 {
			continue
		}
		var r tsvRow
		fmt.Sscanf(f[0], "%f", &r.s)
		fmt.Sscanf(f[1], "%f", &r.e)
		r.text = f[3]
		out = append(out, r)
	}
	return out
}

// ---- page ------------------------------------------------------------------

func (a *App) buildStep2() gtk.Widgetter {
	a.s2info = gtk.NewLabel("")
	a.s2info.SetXAlign(0)
	a.s2info.SetWrap(true)

	a.s2out = gtk.NewLabel("")
	a.s2out.SetXAlign(0)
	a.s2out.SetYAlign(0)
	a.s2out.AddCSSClass("monospace")
	outScroll := gtk.NewScrolledWindow()
	outScroll.SetChild(a.s2out)
	outScroll.SetPropagateNaturalHeight(true)
	outScroll.SetMaxContentHeight(200)
	outScroll.SetVExpand(true)

	outLbl := gtk.NewLabel("Outputs")
	outLbl.SetXAlign(0)
	outLbl.AddCSSClass("heading")
	open := gtk.NewButtonWithLabel("Open step2 folder")
	open.ConnectClicked(func() { a.openFolder(filepath.Join(a.outDir, "step2")) })
	outHead := gtk.NewBox(gtk.OrientationHorizontal, 8)
	outHead.Append(outLbl)
	outHead.Append(open)

	expl := gtk.NewLabel("Describe walks the stored frames of every checked video with the vision " +
		"model — each request carries the running state and the last events, so it describes " +
		"what is happening, not stills — and builds one event log per video. " +
		"Press ▶ to run; resumes per chunk.")
	expl.SetXAlign(0)
	expl.SetWrap(true)
	expl.AddCSSClass("dim-label")

	hintLbl := gtk.NewLabel("Context for the vision model (optional) — what should it know about " +
		"this footage? What it is, what the recurring on-screen elements mean, what to ignore.")
	hintLbl.SetXAlign(0)
	hintLbl.SetWrap(true)
	a.s2hints = gtk.NewTextView()
	a.s2hints.SetWrapMode(gtk.WrapWord)
	a.s2hints.SetTopMargin(4)
	a.s2hints.SetLeftMargin(6)
	a.s2hints.SetRightMargin(6)
	hintScroll := gtk.NewScrolledWindow()
	hintScroll.SetChild(a.s2hints)
	hintScroll.SetSizeRequest(-1, 70)
	hintScroll.AddCSSClass("frame") // border, so it reads as an input field

	box := gtk.NewBox(gtk.OrientationVertical, 12)
	box.SetMarginTop(16)
	box.SetMarginStart(16)
	box.SetMarginEnd(16)
	box.SetMarginBottom(8)
	box.Append(expl)
	box.Append(hintLbl)
	box.Append(hintScroll)
	box.Append(a.s2info)
	box.Append(outHead)
	box.Append(outScroll)
	a.updateStep2Info()
	return box
}

func (a *App) describeHints() string {
	if a.s2hints == nil {
		return ""
	}
	buf := a.s2hints.Buffer()
	return strings.TrimSpace(buf.Text(buf.StartIter(), buf.EndIter(), false))
}

func (a *App) updateStep2Info() {
	if a.s2out == nil {
		return
	}
	s2 := filepath.Join(a.outDir, "step2")
	a.s2out.SetText(describeOutputs(s2))

	var lines []string
	if a.vidList != nil {
		for _, v := range a.vidList.selected() {
			base := baseName(v)
			line := base + ": "
			if b, err := os.ReadFile(filepath.Join(s2, base, "events.tsv")); err == nil {
				line += fmt.Sprintf("%d chunks described", strings.Count(string(b), "\n"))
			} else {
				line += "not described yet"
			}
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		lines = append(lines, "step 2 has not run yet")
	}
	a.s2info.SetText(strings.Join(lines, "\n"))
}

// ---- run --------------------------------------------------------------------

// All checked sources take part: every video is described on its own
// timeline, and every (video, voice) pair gets its own alignment offset.
type videoPlan struct {
	base     string
	video    string // absolute path
	dir      string // step2/<base>
	frames   []string
	interval float64
	scale    string
	chunks   int
}

func (a *App) planVideo(video, s2 string) (*videoPlan, error) {
	base := baseName(video)
	fdir := filepath.Join(a.outDir, "step1", "frames", base)
	ents, err := os.ReadDir(fdir)
	if err != nil {
		return nil, fmt.Errorf("no frames for %s -- run step 1", base)
	}
	p := &videoPlan{base: base, video: video, dir: filepath.Join(s2, base)}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "f") && strings.HasSuffix(e.Name(), ".jpg") {
			p.frames = append(p.frames, filepath.Join(fdir, e.Name()))
		}
	}
	sort.Strings(p.frames)
	if len(p.frames) == 0 {
		return nil, fmt.Errorf("frame folder is empty: %s", fdir)
	}
	if b, err := os.ReadFile(filepath.Join(fdir, ".interval")); err == nil {
		parts := strings.SplitN(strings.TrimSpace(string(b)), "|", 2)
		fmt.Sscanf(parts[0], "%f", &p.interval)
		if len(parts) > 1 {
			p.scale = parts[1]
		}
	}
	if p.interval <= 0 {
		return nil, fmt.Errorf("%s was extracted as every-frame; describe needs a fixed interval — rerun step 1 with e.g. 1s", base)
	}
	p.chunks = (len(p.frames) + framesPerReq - 1) / framesPerReq
	return p, nil
}

func (a *App) step2Clicked() {
	if a.running {
		return
	}
	vids := a.vidList.selected()
	auds := a.audList.selected()
	if len(vids) == 0 || len(auds) == 0 {
		a.setStatus("select at least one video and one voice recording on step 1")
		return
	}
	abs := func(rels []string) []string {
		out := make([]string, len(rels))
		for i, r := range rels {
			out[i] = filepath.Join(a.root, r)
		}
		return out
	}
	for _, f := range append(abs(vids), abs(auds)...) {
		if !exists(filepath.Join(a.outDir, "step1", baseName(f), "transcript.tsv")) {
			a.setStatus("run step 1 first — transcript missing for " + baseName(f))
			return
		}
	}
	a.saveProjectTo(filepath.Join(a.root, "project.json"))
	a.startStep2(abs(vids), abs(auds), a.describeHints())
}

func (a *App) startStep2(videos, audios []string, hints string) {
	a.running = true
	a.stopFlag.Store(false)
	a.pauseFlag.Store(false)
	a.runCtx, a.runCancel = context.WithCancel(context.Background())
	a.progMu.Lock()
	a.progParts = [2]float64{}
	a.progTexts = [2]string{}
	a.progMu.Unlock()
	a.updateRunControls()
	a.setStatus("step 2 running…")
	a.logExp.SetExpanded(true)
	go func() {
		err := a.step2(videos, audios, hints)
		glib.IdleAdd(func() {
			a.running = false
			a.updateRunControls()
			switch {
			case errors.Is(err, errStopped):
				a.progress.SetText("stopped — progress is kept")
				a.setStatus("step 2 stopped")
			case err != nil:
				a.logf("step 2 FAILED: %v", err)
				a.progress.SetText("failed — see log")
				a.setStatus("step 2 failed")
			default:
				a.progress.SetFraction(1)
				a.setStatus("step 2 done")
			}
			a.updateStep2Info()
			a.updateGates()
		})
	}()
}

func (a *App) step2(videos, audios []string, hints string) error {
	s2 := filepath.Join(a.outDir, "step2")
	if err := os.MkdirAll(s2, 0o755); err != nil {
		return err
	}
	var plans []*videoPlan
	total := 0
	for _, v := range videos {
		p, err := a.planVideo(v, s2)
		if err != nil {
			return err
		}
		plans = append(plans, p)
		total += p.chunks
	}
	// this step ONLY describes; alignment is its own later step that reads
	// these event logs
	done := 0
	for _, p := range plans {
		if err := a.describeVideo(p, hints, done, total); err != nil {
			return err
		}
		done += p.chunks
	}
	return nil
}

// ---- describe ---------------------------------------------------------------

func (a *App) describeVideo(p *videoPlan, hints string, chunkOff, chunkTotal int) error {
	if err := os.MkdirAll(p.dir, 0o755); err != nil {
		return err
	}
	game := loadTSVRows(filepath.Join(a.outDir, "step1", p.base, "transcript.tsv"))
	evPath := filepath.Join(p.dir, "events.tsv")
	statePath := filepath.Join(p.dir, "state.txt")

	// The model needs LLM-sized images; frames may be stored bigger. Scaled
	// copies are cached per frame, so resume and re-runs pay scaling once.
	needScale := true
	switch p.scale {
	case "896w (LLM)", "480p":
		needScale = false
	}
	llmDir := filepath.Join(p.dir, ".llmframes")
	if needScale {
		if err := os.MkdirAll(llmDir, 0o755); err != nil {
			return err
		}
	}
	scaledFrame := func(src string) (string, error) {
		if !needScale {
			return src, nil
		}
		dst := filepath.Join(llmDir, filepath.Base(src))
		if !exists(dst) {
			if err := a.runCmd("ffmpeg", "-v", "error", "-y", "-i", src,
				"-vf", "scale=896:-2", "-q:v", "4", dst); err != nil {
				return "", err
			}
		}
		return dst, nil
	}

	// resume: chunks already described are keyed by their start time; the
	// recent-events window picks up from the end of the existing log
	done := map[string]bool{}
	var recent []string
	if b, err := os.ReadFile(evPath); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			f := strings.Split(l, "\t")
			if len(f) >= 3 && f[0] != "" {
				done[f[0]] = true
				recent = append(recent, f[0]+"s: "+f[2])
				if len(recent) > 3 {
					recent = recent[1:]
				}
			}
		}
	}
	state := "Recording just started."
	if b, err := os.ReadFile(statePath); err == nil && len(b) > 0 {
		state = strings.TrimSpace(string(b))
	}

	for c := 0; c < p.chunks; c++ {
		if err := a.checkpoint(); err != nil {
			return err
		}
		lo := c * framesPerReq
		hi := min(lo+framesPerReq, len(p.frames))
		t0 := float64(lo) * p.interval
		t1 := float64(hi) * p.interval
		key := fmt.Sprintf("%.2f", t0)
		if done[key] {
			continue
		}
		a.prog(trackSTT, float64(chunkOff+c)/float64(chunkTotal),
			"[%s] describing %d/%d (t=%.0fs)", p.base, c+1, p.chunks, t0)

		heard := ""
		for _, r := range game {
			if r.e > t0-2 && r.s < t1+2 {
				heard += r.text + " "
			}
		}
		// past history rides along twice: the rolling STATE (what is true) and
		// the last events (what just happened) -- together they let the model
		// describe motion and consequences, not disconnected stills
		text := fmt.Sprintf("STATE so far: %s\nFrames cover t=%.0fs to t=%.0fs, %g s apart.", state, t0, t1, p.interval)
		if len(recent) > 0 {
			text += "\nJust before this:\n" + strings.Join(recent, "\n")
		}
		if heard != "" {
			text += "\nGame audio / voice chat during this: " + heard
		}
		content := []any{txtPart(text)}
		for _, f := range p.frames[lo:hi] {
			sf, err := scaledFrame(f)
			if err != nil {
				return err
			}
			part, err := imgPart(sf)
			if err != nil {
				return err
			}
			content = append(content, part)
		}
		system := describeSystem
		if hints != "" {
			system += "\nEditor's notes about this footage -- trust them:\n" + hints
		}
		reply, err := a.llmChatRetry([]map[string]any{
			msg("system", system), msg("user", content),
		}, false)
		if err != nil {
			if errors.Is(err, errStopped) {
				return errStopped
			}
			return fmt.Errorf("describe %s t=%.0f: %w", p.base, t0, err)
		}

		event, newState := "", ""
		for _, l := range strings.Split(reply, "\n") {
			if v, ok := strings.CutPrefix(l, "EVENT:"); ok {
				event = strings.TrimSpace(v)
			}
			if v, ok := strings.CutPrefix(l, "STATE:"); ok {
				newState = strings.TrimSpace(v)
			}
		}
		if event == "" {
			event = "(no event line: " + strings.ReplaceAll(reply, "\n", " ") + ")"
		}
		if newState != "" {
			state = newState
		}
		f, err := os.OpenFile(evPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		fmt.Fprintf(f, "%s\t%.2f\t%s\n", key, t1, event)
		f.Close()
		if err := os.WriteFile(statePath, []byte(state+"\n"), 0o644); err != nil {
			return err
		}
		recent = append(recent, key+"s: "+event)
		if len(recent) > 3 {
			recent = recent[1:]
		}
	}
	a.logfIdle(">>> [%s] event log complete (%d chunks)", p.base, p.chunks)
	return nil
}
