package main

// Describe: every checked video's stored frames (step1/frames/<v>/) go to the
// vision LLM in small batches together with the game-audio words heard in
// those seconds; a rolling "state of the game" plus the last events make each
// batch a description of what is HAPPENING, not stills.
// Output: step2/<video>/events.tsv, resumable per chunk.
//
// The page is step23.go -- this half and the fixer (step3.go) share it.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const framesPerReq = 4 // frames per vision request

// The primer stays empty of game knowledge on purpose: whatever is specific
// about this footage goes into the prompt box on the page, which replaces this
// wholesale rather than being appended to it.
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

// step2 describes every checked video. span is how much of the progress bar
// this job owns -- all of it when Describe runs on its own, half when the
// fixer runs after it on the same page.
func (a *App) step2(videos []string, span float64) error {
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
	// this step ONLY describes; the fixer reads these event logs afterwards
	done := 0
	for _, p := range plans {
		if err := a.describeVideo(p, done, total, span); err != nil {
			return err
		}
		done += p.chunks
	}
	// chunks that resume skip their progress call, so a video that was already
	// described would leave the bar where it started -- claim the share here
	a.prog(trackDescribe, span, "described")
	return nil
}

// ---- describe ---------------------------------------------------------------

func (a *App) describeVideo(p *videoPlan, chunkOff, chunkTotal int, span float64) error {
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
		a.prog(trackDescribe, span*float64(chunkOff+c)/float64(chunkTotal),
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
		reply, err := a.llmChatRetry([]map[string]any{
			msg("system", a.prompt("describe")), msg("user", content),
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
