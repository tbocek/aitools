package main

// Step 1, natively: STT both inputs and dump frames at an interval, into
// step1/. ffmpeg and the audio.cpp container are driven via os/exec; the
// anchored diarization and the segment merge are real code here, not awk.
//
// step1/
//   <input-basename>/  voice16k.wav, transcript.{txt,tsv,srt}, words.json,
//                      turns.json  (per input, video and voice alike)
//   frames/f%06d.jpg   frame n covers t = (n-1) * interval seconds
//   meta.env           chosen inputs + interval, read by the GUI and later steps
//
// Finished stages are skipped, so re-running resumes.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/diamondburned/gotk4/pkg/glib/v2"
)

var errStopped = errors.New("stopped by user")

// Where the local audio.cpp stack lives, what it runs on and what language it
// expects are settings now, not constants -- see appConf in setup.go. What
// stays here is what is measured rather than chosen: change these and the
// models misbehave, which is not a preference anyone can hold.
const (
	sampleRate = 16000 // what the ASR and diarization models are trained on

	// Sortformer refuses requests past its encoder position table (measured:
	// 90 s passes, 150 s does not), and its slot names mean nothing across
	// requests. Every window therefore carries the same short anchor clip of
	// known voices in front; whichever slot owns an anchor block IS that voice.
	diarWin     = 90.0
	diarScanHop = 60.0 // pass 1 stride: only has to see each voice once
	anchorPer   = 12.0 // seconds of each voice in the anchor
	anchorMin   = 4.0  // speech that makes a slot count as a voice
	minAnchorOv = 0.5  // anchor-block overlap to claim a slot
	diarTurnGap = 0.5  // merge same-speaker turns closer than this

	// segment building
	mergeGap     = 0.7  // silence that ends a segment
	mergeMaxLen  = 12.0 // hard cap on segment length
	mergeMaxWord = 2.0  // Parakeet stretches word ends across silence; clamp
	mergeNear    = 1.0  // attribute a word to a turn this far away
)

type span struct {
	s, e float64
	slot string
}

// ---- run control -----------------------------------------------------------

// checkpoint is where pause and stop take effect: between subprocesses, never
// inside one -- a GPU job cannot be meaningfully frozen halfway.
func (a *App) checkpoint() error {
	for {
		if a.stopFlag.Load() {
			return errStopped
		}
		if !a.pauseFlag.Load() {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// prog feeds the shared bar from one of two concurrent tracks (0 = STT,
// 1 = frames). Each track reports its own absolute contribution; the bar
// shows the sum and both latest messages -- letting the tracks write raw
// fractions would make the bar bounce between them.
const (
	trackSTT    = 0
	trackFrames = 1
	// the same two tracks under the names the Describe + Transcript step uses
	// them for: its two jobs run one after the other, but each still owns half
	// the bar and its own message
	trackDescribe = trackSTT
	trackFix      = trackFrames
)

func (a *App) prog(track int, f float64, format string, args ...any) {
	txt := fmt.Sprintf(format, args...)
	a.progMu.Lock()
	a.progParts[track] = f
	a.progTexts[track] = txt
	total := math.Max(0, math.Min(1, a.progParts[0]+a.progParts[1]))
	var parts []string
	for _, t := range a.progTexts {
		if t != "" {
			parts = append(parts, t)
		}
	}
	joined := strings.Join(parts, "  ·  ")
	a.progMu.Unlock()
	glib.IdleAdd(func() {
		a.progress.SetFraction(total)
		a.progress.SetText(joined)
	})
}

// transport is what a page offers the run bar when it plays media rather than
// running a pipeline. With it, ▶ and ⏹ mean the same thing on every step: ▶
// starts what the page does and then pauses and resumes it, ⏹ ends it. Without
// it a page's playback was reachable only through the page's own buttons --
// which is why ⏸ and ⏹ sat there doing nothing on the voice step.
type transport interface {
	playing() bool // running right now
	cued() bool    // loaded but paused, so ▶ means resume rather than start over
	toggle()
	stop()
}

// pageTransport is the visible page's playback, if that page has any.
func (a *App) pageTransport() transport {
	switch a.stack.VisibleChildName() {
	case "voice":
		if a.voice5 != nil {
			return a.voice5
		}
	case "step4":
		if a.ed != nil {
			return a.ed
		}
	case "narrate":
		if a.narr5 != nil {
			return a.narr5
		}
	case "produce":
		if a.prod != nil {
			return a.prod
		}
	}
	return nil
}

// playClicked runs whatever step is on screen, pauses it, or resumes it.
func (a *App) playClicked() {
	if a.running {
		// while a run is under way ▶ is the pause button, and says so
		if a.pauseFlag.Load() {
			a.pauseFlag.Store(false)
			a.setStatus("resumed")
		} else {
			a.pauseFlag.Store(true)
			a.setStatus("pausing after the current stage…")
		}
		a.updateRunControls()
		return
	}
	// playback beats the page's action: once something is playing, the button
	// belongs to it until it is stopped or reaches the end
	if t := a.pageTransport(); t != nil && (t.playing() || t.cued()) {
		t.toggle()
		a.updateRunControls()
		return
	}
	a.logf(">>> play: %s", a.stack.VisibleChildName())
	a.snapSources()
	switch a.stack.VisibleChildName() {
	case "step1":
		a.step1Clicked()
	case "voice":
		// the voice page runs nothing -- ▶ there means what the page's own
		// button means, a sample in the selected voice
		if a.voice5 != nil {
			a.voice5.playSample()
		}
	case "understand":
		a.understandRun(true, true)
	case "step4":
		// per the spec: with a selection pending, play ADDS it; on an empty
		// cut it asks for the first suggestion; otherwise it explains itself
		switch {
		case a.ed != nil && a.ed.sel.active:
			a.addSelClicked()
		case a.ed != nil && len(a.ed.segs) == 0:
			a.suggestClicked()
		default:
			a.setStatus("drag a region and ▶ adds it; Suggest only fills an empty cut")
		}
	case "narrate":
		if a.narr5 != nil && len(a.narr5.entries) == 0 {
			a.generateNarration()
		} else {
			a.setStatus("narration exists — use Generate to redo, 🔊 per line, Synthesize all for the rest")
		}
	case "produce":
		a.produceClicked()
	}
}

func (a *App) stopClicked() {
	if t := a.pageTransport(); t != nil && (t.playing() || t.cued()) {
		t.stop()
		a.setStatus("playback stopped")
		a.updateRunControls()
	}
	if !a.running {
		return
	}
	a.stopFlag.Store(true)
	a.pauseFlag.Store(false)
	if a.runCancel != nil {
		a.runCancel() // aborts LLM requests, which are not subprocesses
	}
	a.ctlMu.Lock()
	for cmd := range a.curCmds {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}
	a.ctlMu.Unlock()
	a.setStatus("stopping…")
}

// updateRunControls draws the two buttons from whatever is actually under way:
// a run, this page's playback, or nothing. ▶ is a toggle rather than a separate
// pair, because a dedicated ⏸ was dead on every page whose ▶ is not a run.
func (a *App) updateRunControls() {
	t := a.pageTransport()
	busy := (a.running && !a.pauseFlag.Load()) || (t != nil && t.playing())
	if busy {
		a.playBtn.SetIconName("media-playback-pause-symbolic")
		a.playBtn.SetTooltipText("Pause")
	} else {
		a.playBtn.SetIconName("media-playback-start-symbolic")
		a.playBtn.SetTooltipText("Run this step — or resume what is paused")
	}
	a.stopBtn.SetSensitive(a.running || busy || (t != nil && t.cued()))
}

// ---- process plumbing ------------------------------------------------------

// runCmd runs a subprocess and remembers it, so the stop button can kill it.
// A kill while stopFlag is set reports as errStopped, not as a failure.
func (a *App) runCmd(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	a.ctlMu.Lock()
	a.curCmds[cmd] = true
	a.ctlMu.Unlock()
	err := cmd.Run()
	a.ctlMu.Lock()
	delete(a.curCmds, cmd)
	a.ctlMu.Unlock()
	if err != nil {
		if a.stopFlag.Load() {
			return errStopped
		}
		tail := out.String()
		if len(tail) > 400 {
			tail = tail[len(tail)-400:]
		}
		return fmt.Errorf("%s: %w\n%s", name, err, tail)
	}
	return nil
}

// ffmpegProgress runs ffmpeg reporting completion against a known duration,
// for the long single-invocation phases (frame extraction).
func (a *App) ffmpegProgress(dur float64, cb func(float64), args ...string) error {
	full := append([]string{"-progress", "pipe:1", "-nostats"}, args...)
	cmd := exec.Command("ffmpeg", full...)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	a.ctlMu.Lock()
	a.curCmds[cmd] = true
	a.ctlMu.Unlock()
	if err := cmd.Start(); err != nil {
		a.ctlMu.Lock()
		delete(a.curCmds, cmd)
		a.ctlMu.Unlock()
		return err
	}
	sc := bufio.NewScanner(out)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "out_time_us="); ok {
			var us float64
			fmt.Sscanf(v, "%f", &us)
			if dur > 0 {
				cb(us / 1e6 / dur)
			}
		}
	}
	err = cmd.Wait()
	a.ctlMu.Lock()
	delete(a.curCmds, cmd)
	a.ctlMu.Unlock()
	if err != nil {
		if a.stopFlag.Load() {
			return errStopped
		}
		tail := errBuf.String()
		if len(tail) > 400 {
			tail = tail[len(tail)-400:]
		}
		return fmt.Errorf("ffmpeg: %w\n%s", err, tail)
	}
	return nil
}

// acpp runs audiocpp_cli inside the audio container, with the same mounts as
// the pipeline scripts: models shared with the WebUI, a.root as /work.
func (a *App) acpp(args ...string) error {
	c := a.readConf()
	full := append([]string{
		"run", "--rm",
		"--device", "/dev/kfd", "--device", "/dev/dri",
		"--group-add", "render", "--group-add", "video",
		"--security-opt", "seccomp=unconfined",
		"-v", c.Models + ":/home/arch/audio.cpp/models",
		"-v", a.outDir + ":/work",
		"-w", "/home/arch/audio.cpp",
		c.Image, c.CLI,
	}, args...)
	return a.runCmd("docker", full...)
}

func ffprobeDur(f string) (float64, error) {
	out, err := exec.Command("ffprobe", "-v", "error",
		"-show_entries", "format=duration", "-of", "csv=p=0", f).Output()
	d := strings.TrimSpace(string(out))
	if err != nil || d == "" || d == "N/A" {
		// stream-to-disk recorders never finalize the header; decode and count
		out, err = exec.Command("bash", "-c",
			fmt.Sprintf(`ffmpeg -v error -progress /dev/stdout -i %q -f null - 2>/dev/null | awk -F= '/^out_time_us/ { t = $2 } END { printf "%%.2f", t / 1e6 }'`, f)).Output()
		if err != nil {
			return 0, fmt.Errorf("duration of %s: %w", f, err)
		}
		d = strings.TrimSpace(string(out))
	}
	var v float64
	fmt.Sscanf(d, "%f", &v)
	if v <= 0 {
		return 0, fmt.Errorf("cannot determine duration of %s", f)
	}
	return v, nil
}

// walk any decoded JSON, visiting every object -- survives the CLI and server
// wrapping payloads differently
func walkObjects(v any, fn func(map[string]any)) {
	switch t := v.(type) {
	case map[string]any:
		fn(t)
		for _, vv := range t {
			walkObjects(vv, fn)
		}
	case []any:
		for _, vv := range t {
			walkObjects(vv, fn)
		}
	}
}

func loadJSON(path string) (any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var v any
	return v, json.Unmarshal(b, &v)
}

// spans from a diarization JSON: sample counts -> seconds
func loadSpans(path string) ([]span, error) {
	v, err := loadJSON(path)
	if err != nil {
		return nil, err
	}
	var out []span
	walkObjects(v, func(m map[string]any) {
		slot, ok := m["speaker_id"].(string)
		if !ok {
			slot, ok = m["speaker"].(string)
		}
		ss, okS := m["start_sample"].(float64)
		es, okE := m["end_sample"].(float64)
		if ok && okS && okE {
			out = append(out, span{ss / sampleRate, es / sampleRate, slot})
		}
	})
	return out, nil
}

// ---- step 1 entry ----------------------------------------------------------

func (a *App) startStep1(videos, audios []string, interval float64, scaleName, scaleVF string) {
	a.running = true
	a.stopFlag.Store(false)
	a.pauseFlag.Store(false)
	a.runCtx, a.runCancel = context.WithCancel(context.Background())
	a.progMu.Lock()
	a.progParts = [2]float64{}
	a.progTexts = [2]string{}
	a.progMu.Unlock()
	a.updateRunControls()
	a.setStatus("step 1 running…")
	a.logExp.SetExpanded(true)
	go func() {
		err := a.step1(videos, audios, interval, scaleName, scaleVF)
		glib.IdleAdd(func() {
			a.running = false
			a.updateRunControls()
			a.loadMeta()
			switch {
			case errors.Is(err, errStopped):
				a.progress.SetText("stopped — finished stages are kept")
				a.setStatus("step 1 stopped")
			case err != nil:
				a.logf("step 1 FAILED: %v", err)
				a.progress.SetText("failed — see log")
				a.setStatus("step 1 failed")
			default:
				a.progress.SetFraction(1)
				a.setStatus("step 1 done")
			}
			a.updateStep1Info()
			a.und.refresh() // the next step's input counts just changed
			a.updateGates()
		})
	}()
}

// ensureModels makes a fresh machine work: verify the container image exists
// and pull any missing GGUF through the model manager (stdlib-only, runs in
// the container, same shared models mount as the WebUI).
func (a *App) ensureModels() error {
	c := a.readConf()
	if err := a.runCmd("docker", "image", "inspect", c.Image); err != nil {
		return fmt.Errorf("docker image %s missing -- build it with: cd ../cpp && ./run.sh "+
			"(or point the settings dialog at the image you have)", c.Image)
	}
	needs := []struct{ gguf, pkg string }{
		{c.ASRGGUF, "parakeet_tdt_q8_0"},
		{c.DiarGGUF, "sortformer_diar_4spk_v1_q8_0"},
	}
	for _, n := range needs {
		host := filepath.Join(c.Models, strings.TrimPrefix(n.gguf, "models/"))
		if exists(host) {
			continue
		}
		a.logfIdle(">>> downloading model %s", n.pkg)
		a.prog(trackSTT, 0.01, "downloading %s…", n.pkg)
		if err := a.runCmd("docker", "run", "--rm",
			"-v", c.Models+":/home/arch/audio.cpp/models",
			"-w", "/home/arch/audio.cpp", c.Image,
			"python3", "tools/model_manager_v2.py", "install", n.pkg,
			"--models-root", "models"); err != nil {
			return fmt.Errorf("model download %s: %w", n.pkg, err)
		}
	}
	return nil
}

func (a *App) step1(videos, audios []string, interval float64, scaleName, scaleVF string) error {
	s1 := filepath.Join(a.outDir, "step1")
	if err := os.MkdirAll(s1, 0o755); err != nil {
		return err
	}
	if err := a.ensureModels(); err != nil {
		return err
	}
	// progress plan: half the bar each. This step is two jobs -- speech
	// recognition (GPU, via the container) and frame extraction (CPU ffmpeg) --
	// which do not contend, so they run as parallel tracks. Weighting them by
	// file count instead gave whichever job had more inputs most of the bar,
	// and the bar then crossed that job's share and sat still through the other.
	inputs := append(append([]string{}, videos...), audios...)
	var unit, funit float64
	if len(inputs) > 0 {
		unit = 0.5 / float64(len(inputs))
	} else {
		a.prog(trackSTT, 0.5, "") // a job with nothing to do is done, or its
	}
	if len(videos) > 0 {
		funit = 0.5 / float64(len(videos))
	} else {
		a.prog(trackFrames, 0.5, "") // half would never fill and neither would the bar
	}

	var wg sync.WaitGroup
	var framesErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		if len(videos) == 0 {
			return
		}
		fb := 0.0
		for _, v := range videos {
			if framesErr = a.checkpoint(); framesErr != nil {
				return
			}
			if framesErr = a.extractFrames(v, interval, scaleName, scaleVF, s1, fb, funit); framesErr != nil {
				return
			}
			fb += funit
		}
		a.prog(trackFrames, fb, "frames done")
	}()

	var sttErr error
	base := 0.0
	for _, in := range inputs {
		if sttErr = a.checkpoint(); sttErr != nil {
			break
		}
		if sttErr = a.transcribe(in, s1, base, unit); sttErr != nil {
			if !errors.Is(sttErr, errStopped) {
				sttErr = fmt.Errorf("%s: %w", filepath.Base(in), sttErr)
			}
			break
		}
		base += unit
	}
	if sttErr == nil {
		a.prog(trackSTT, base, "transcription done")
	}
	wg.Wait()

	// one stop is one stop, not two errors; otherwise report whatever failed
	if errors.Is(sttErr, errStopped) && errors.Is(framesErr, errStopped) {
		return errStopped
	}
	if err := errors.Join(sttErr, framesErr); err != nil {
		return err
	}

	// primary pair for the single-source consumers (review page, align);
	// the full ordered lists live in project.json
	meta := fmt.Sprintf("VIDEO_FILE=%s\nAUDIO_FILE=%s\nVIDEO_BASE=%s\nAUDIO_BASE=%s\nINTERVAL=%g\nSCALE=%s\n",
		videos[0], audios[0], baseName(videos[0]), baseName(audios[0]), interval, scaleName)
	return os.WriteFile(filepath.Join(s1, "meta.env"), []byte(meta), 0o644)
}

func baseName(p string) string {
	b := filepath.Base(p)
	return strings.TrimSuffix(b, filepath.Ext(b))
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// ---- one input: 16 kHz -> ASR -> diarization -> segments -------------------

func (a *App) transcribe(input, s1 string, base, unit float64) error {
	name := baseName(input)
	out := filepath.Join(s1, name)
	rel := "step1/" + name // container path under /work
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}

	wav := filepath.Join(out, "voice16k.wav")
	if !exists(wav) {
		a.prog(trackSTT, base+0.01*unit, "[%s] extracting audio", name)
		a.logfIdle(">>> [%s] extracting 16 kHz mono", name)
		if err := a.runCmd("ffmpeg", "-v", "error", "-y", "-i", input,
			"-vn", "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le", wav); err != nil {
			return err
		}
	}
	dur, err := ffprobeDur(wav)
	if err != nil {
		return err
	}
	a.logfIdle(">>> [%s] %.1f s of audio", name, dur)

	if err := a.checkpoint(); err != nil {
		return err
	}
	if !exists(filepath.Join(out, "words.json")) {
		a.prog(trackSTT, base+0.05*unit, "[%s] speech recognition…", name)
		a.logfIdle(">>> [%s] ASR (parakeet)", name)
		// The empty --text is load-bearing: it is the only carrier of the
		// language option, --language alone is silently dropped.
		c := a.readConf()
		if err := a.acpp("--task", "asr", "--family", "parakeet_tdt",
			"--model", c.ASRGGUF, "--backend", c.Backend,
			"--audio", "/work/"+rel+"/voice16k.wav",
			"--session-option", "parakeet_tdt.offline_mode=long_form",
			"--language", c.Language, "--text", "",
			"--text-out", "/work/"+rel+"/transcript.txt",
			"--words-out", "/work/"+rel+"/words.json"); err != nil {
			return fmt.Errorf("ASR: %w", err)
		}
	} else {
		a.logfIdle(">>> [%s] ASR already done", name)
	}

	if err := a.checkpoint(); err != nil {
		return err
	}
	if !exists(filepath.Join(out, "turns.json")) {
		if err := a.diarize(out, rel, dur, name, base, unit); err != nil {
			if errors.Is(err, errStopped) {
				return err
			}
			return fmt.Errorf("diarization: %w", err)
		}
	} else {
		a.logfIdle(">>> [%s] diarization already done", name)
	}

	a.prog(trackSTT, base+0.98*unit, "[%s] building segments", name)
	if err := a.mergeSegments(out); err != nil {
		return fmt.Errorf("merge: %w", err)
	}
	return nil
}

// diarWindow runs one clip through sortformer. A window with no speech exits
// 0 and writes no file at all, so a missing JSON means silence, not failure.
func (a *App) diarWindow(wavRel, jsonRel, jsonHost string) ([]span, error) {
	os.Remove(jsonHost)
	c := a.readConf()
	if err := a.acpp("--task", "diar", "--family", "sortformer_diar",
		"--model", c.DiarGGUF, "--backend", c.Backend,
		"--audio", "/work/"+wavRel,
		"--session-option", "sortformer_diar.graph_capacity_mode=grow",
		"--turns-out", "/work/"+jsonRel); err != nil {
		return nil, err
	}
	if !exists(jsonHost) {
		return nil, nil
	}
	return loadSpans(jsonHost)
}

func (a *App) diarize(out, rel string, dur float64, name string, base, unit float64) error {
	dir := filepath.Join(out, "diar")
	os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	turnsPath := filepath.Join(out, "turns.json")

	// -- pass 1: where are the voices? --------------------------------------
	nwin := int(math.Ceil(dur / diarScanHop))
	scan := map[int][]span{}
	for i := 0; i < nwin; i++ {
		if err := a.checkpoint(); err != nil {
			return err
		}
		a.prog(trackSTT, base+(0.55+0.20*float64(i)/float64(nwin))*unit,
			"[%s] voices: scanning window %d/%d", name, i+1, nwin)
		start := float64(i) * diarScanHop
		if err := a.runCmd("ffmpeg", "-v", "error", "-y",
			"-ss", fmt.Sprint(start), "-t", fmt.Sprint(diarWin),
			"-i", filepath.Join(out, "voice16k.wav"),
			"-c:a", "pcm_s16le", filepath.Join(dir, "s.wav")); err != nil {
			return err
		}
		spans, err := a.diarWindow(rel+"/diar/s.wav", rel+"/diar/s.json",
			filepath.Join(dir, "s.json"))
		if err != nil {
			return err
		}
		for _, sp := range spans {
			scan[i] = append(scan[i], span{sp.s + start, sp.e + start, sp.slot})
		}
		a.logfIdle(">>> [%s] scanning window %d/%d", name, i+1, nwin)
	}
	if len(scan) == 0 {
		return os.WriteFile(turnsPath, []byte("[]\n"), 0o644)
	}

	// -- pick the anchor window ---------------------------------------------
	// Most distinct voices; on a tie, the window whose QUIETEST voice is best
	// represented -- ranking by total speech picks whoever talks most and
	// leaves the other voice too thin to anchor.
	best, bestN, bestLo := -1, 0, 0.0
	for w, spans := range scan {
		durBy := map[string]float64{}
		for _, sp := range spans {
			durBy[sp.slot] += sp.e - sp.s
		}
		n, lo := 0, math.MaxFloat64
		for _, d := range durBy {
			if d >= anchorMin {
				n++
				if d < lo {
					lo = d
				}
			}
		}
		if n > bestN || (n == bestN && n > 0 && lo > bestLo) {
			best, bestN, bestLo = w, n, lo
		}
	}
	if best < 0 {
		for w := range scan {
			if best < 0 || w < best {
				best = w
			}
		}
	}

	// -- build the anchor: up to anchorPer seconds of each voice ------------
	rows := append([]span(nil), scan[best]...)
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].slot != rows[j].slot {
			return rows[i].slot < rows[j].slot
		}
		return rows[i].s < rows[j].s
	})
	type piece struct {
		src, len float64
		slot     string
	}
	var pieces []piece
	acc := map[string]float64{}
	for _, r := range rows {
		if acc[r.slot] >= anchorPer {
			continue
		}
		d := r.e - r.s
		if d > anchorPer-acc[r.slot] {
			d = anchorPer - acc[r.slot]
		}
		if d < 0.3 { // too short to carry a voice
			continue
		}
		acc[r.slot] += d
		pieces = append(pieces, piece{r.s, d, r.slot})
	}
	var list strings.Builder
	for i, p := range pieces {
		f := filepath.Join(dir, fmt.Sprintf("a%02d.wav", i))
		if err := a.runCmd("ffmpeg", "-v", "error", "-y",
			"-ss", fmt.Sprint(p.src), "-t", fmt.Sprint(p.len),
			"-i", filepath.Join(out, "voice16k.wav"),
			"-c:a", "pcm_s16le", f); err != nil {
			return err
		}
		fmt.Fprintf(&list, "file '%s'\n", f)
	}
	if err := os.WriteFile(filepath.Join(dir, "anchor.list"), []byte(list.String()), 0o644); err != nil {
		return err
	}
	if err := a.runCmd("ffmpeg", "-v", "error", "-y", "-f", "concat", "-safe", "0",
		"-i", filepath.Join(dir, "anchor.list"),
		"-c:a", "pcm_s16le", filepath.Join(dir, "anchor.wav")); err != nil {
		return err
	}
	// block b of the anchor is voice b: [bs, be) in anchor-local seconds
	type block struct{ s, e float64 }
	var blocks []block
	t := 0.0
	for i, p := range pieces {
		if i == 0 || p.slot != pieces[i-1].slot {
			blocks = append(blocks, block{t, t})
		}
		t += p.len
		blocks[len(blocks)-1].e = t
	}
	alen := t
	hop := math.Floor(diarWin - alen - 1)
	if hop < 15 {
		return fmt.Errorf("anchor too long (%.1f s of %.0f s window)", alen, diarWin)
	}
	a.logfIdle(">>> [%s] anchor: %d voice(s) in %.1f s, from window %d -- %.0f s of new audio per window",
		name, len(blocks), alen, best, hop)

	// -- pass 2: every window carries the anchor ----------------------------
	nwin = int(math.Ceil(dur / hop))
	all := map[int][]span{}
	for i := 0; i < nwin; i++ {
		if err := a.checkpoint(); err != nil {
			return err
		}
		a.prog(trackSTT, base+(0.75+0.22*float64(i)/float64(nwin))*unit,
			"[%s] speakers: window %d/%d", name, i+1, nwin)
		start := float64(i) * hop
		if err := a.runCmd("ffmpeg", "-v", "error", "-y",
			"-ss", fmt.Sprint(start), "-t", fmt.Sprint(hop),
			"-i", filepath.Join(out, "voice16k.wav"),
			"-c:a", "pcm_s16le", filepath.Join(dir, "seg.wav")); err != nil {
			return err
		}
		cc := fmt.Sprintf("file '%s'\nfile '%s'\n",
			filepath.Join(dir, "anchor.wav"), filepath.Join(dir, "seg.wav"))
		if err := os.WriteFile(filepath.Join(dir, "cc.list"), []byte(cc), 0o644); err != nil {
			return err
		}
		if err := a.runCmd("ffmpeg", "-v", "error", "-y", "-f", "concat", "-safe", "0",
			"-i", filepath.Join(dir, "cc.list"),
			"-c:a", "pcm_s16le", filepath.Join(dir, "win.wav")); err != nil {
			return err
		}
		spans, err := a.diarWindow(rel+"/diar/win.wav", rel+"/diar/win.json",
			filepath.Join(dir, "win.json"))
		if err != nil {
			return err
		}
		all[i] = spans
		a.logfIdle(">>> [%s] diarizing window %d/%d", name, i+1, nwin)
	}

	// -- resolve slots against the anchor, one-to-one per window ------------
	// Strongest claim first, each block claimed once: independent argmax per
	// slot would let two slots claim the same voice. A slot silent throughout
	// the anchor is a voice the anchor does not carry -- it gets its own id
	// rather than a guess, and surfaces as an extra speaker.
	type gspan struct {
		s, e float64
		g    int
	}
	var glob []gspan
	nunk := 0
	for w := 0; w < nwin; w++ {
		spans := all[w]
		if len(spans) == 0 {
			continue
		}
		var slots []string
		ov := map[string][]float64{}
		for _, sp := range spans {
			if _, seen := ov[sp.slot]; !seen {
				ov[sp.slot] = make([]float64, len(blocks))
				slots = append(slots, sp.slot)
			}
			for b, bl := range blocks {
				o := math.Min(sp.e, bl.e) - math.Max(sp.s, bl.s)
				if o > 0 {
					ov[sp.slot][b] += o
				}
			}
		}
		gid := map[string]int{}
		tookSlot := map[string]bool{}
		tookBlock := make([]bool, len(blocks))
		for {
			bo, bs, bb := minAnchorOv, "", -1
			for _, sl := range slots {
				if tookSlot[sl] {
					continue
				}
				for b := range blocks {
					if !tookBlock[b] {
						if ov[sl][b] > bo {
							bo, bs, bb = ov[sl][b], sl, b
						}
					}
				}
			}
			if bb < 0 {
				break
			}
			gid[bs] = bb
			tookSlot[bs] = true
			tookBlock[bb] = true
		}
		for _, sl := range slots {
			if !tookSlot[sl] {
				gid[sl] = len(blocks) + nunk
				nunk++
			}
		}
		for _, sp := range spans {
			if sp.s < alen { // the anchor itself, not content
				continue
			}
			glob = append(glob, gspan{
				float64(w)*hop + sp.s - alen,
				float64(w)*hop + sp.e - alen,
				gid[sp.slot]})
		}
	}
	sort.Slice(glob, func(i, j int) bool { return glob[i].s < glob[j].s })

	// glue same-speaker runs: sortformer reports frame-level bursts
	var outSpans []gspan
	for _, g := range glob {
		n := len(outSpans)
		if n > 0 && outSpans[n-1].g == g.g && g.s-outSpans[n-1].e <= diarTurnGap {
			if g.e > outSpans[n-1].e {
				outSpans[n-1].e = g.e
			}
			continue
		}
		outSpans = append(outSpans, g)
	}
	var sb strings.Builder
	sb.WriteString("[")
	for i, g := range outSpans {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `{"start_sample":%d,"end_sample":%d,"speaker_id":"SPEAKER_%02d"}`,
			int64(g.s*sampleRate), int64(g.e*sampleRate), g.g)
	}
	sb.WriteString("]\n")
	unident := ""
	if nunk > 0 {
		unident = fmt.Sprintf(" (%d slot(s) the anchor could not identify)", nunk)
	}
	a.logfIdle(">>> [%s] %d speaker(s), %d turns%s", name, len(blocks)+nunk, len(outSpans), unident)
	return os.WriteFile(turnsPath, []byte(sb.String()), 0o644)
}

// ---- words + turns -> speaker-tagged segments ------------------------------

func (a *App) mergeSegments(out string) error {
	v, err := loadJSON(filepath.Join(out, "words.json"))
	if err != nil {
		return err
	}
	type word struct {
		s, e float64
		w    string
	}
	var words []word
	walkObjects(v, func(m map[string]any) {
		w, ok := m["word"].(string)
		ss, okS := m["start_sample"].(float64)
		es, okE := m["end_sample"].(float64)
		if ok && okS && okE {
			words = append(words, word{ss / sampleRate, es / sampleRate, w})
		}
	})
	if len(words) == 0 {
		return fmt.Errorf("no word entries in words.json")
	}

	turns, _ := loadSpans(filepath.Join(out, "turns.json"))
	// glue same-speaker turns (idempotent over what diarize wrote)
	var ts []span
	for _, t := range turns {
		n := len(ts)
		if n > 0 && ts[n-1].slot == t.slot && t.s-ts[n-1].e <= diarTurnGap {
			if t.e > ts[n-1].e {
				ts[n-1].e = t.e
			}
			continue
		}
		ts = append(ts, t)
	}

	// speaker whose turn shares the most time with the word; else the nearest
	// turn within mergeNear -- diarization edges are not exact
	who := func(ws, we float64) string {
		best, bi := 0.0, -1
		for i, t := range ts {
			o := math.Min(we, t.e) - math.Max(ws, t.s)
			if o > best {
				best, bi = o, i
			}
		}
		if bi >= 0 {
			return ts[bi].slot
		}
		bd := mergeNear
		for i, t := range ts {
			d := 0.0
			if t.s > we {
				d = t.s - we
			} else if t.e < ws {
				d = ws - t.e
			}
			if d < bd {
				bd, bi = d, i
			}
		}
		if bi >= 0 {
			return ts[bi].slot
		}
		return "?"
	}

	type seg struct {
		s, e float64
		spk  string
		text string
	}
	var segs []seg
	var cur *seg
	prev := 0.0
	for _, w := range words {
		ws := w.s
		we := math.Min(w.e, ws+mergeMaxWord) // see mergeMaxWord
		spk := who(ws, we)
		if spk == "?" && cur != nil {
			spk = cur.spk // unknown keeps the running speaker
		}
		if cur != nil && (spk != cur.spk || ws-prev > mergeGap || we-cur.s > mergeMaxLen) {
			segs = append(segs, *cur)
			cur = nil
		}
		if cur == nil {
			cur = &seg{s: ws, spk: spk, text: w.w}
		} else {
			if cur.spk == "?" && spk != "?" {
				cur.spk = spk // upgrade once identified
			}
			cur.text += " " + w.w
		}
		cur.e = we
		prev = we
	}
	if cur != nil {
		segs = append(segs, *cur)
	}

	srt := func(t float64) string {
		if t < 0 {
			t = 0
		}
		h := int(t) / 3600
		m := (int(t) % 3600) / 60
		s := int(t) % 60
		ms := int((t-math.Floor(t))*1000 + 0.5)
		return fmt.Sprintf("%02d:%02d:%02d,%03d", h, m, s, ms)
	}
	var tsv, srtb strings.Builder
	for i, g := range segs {
		fmt.Fprintf(&tsv, "%.2f\t%.2f\t%s\t%s\n", g.s, g.e, g.spk, g.text)
		tag := ""
		if g.spk != "?" {
			tag = "[" + g.spk + "] "
		}
		fmt.Fprintf(&srtb, "%d\n%s --> %s\n%s%s\n\n", i+1, srt(g.s), srt(g.e), tag, g.text)
	}
	if err := os.WriteFile(filepath.Join(out, "transcript.tsv"), []byte(tsv.String()), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "transcript.srt"), []byte(srtb.String()), 0o644); err != nil {
		return err
	}
	a.logfIdle(">>> [%s] %d segments", filepath.Base(out), len(segs))
	return nil
}

// ---- frames ----------------------------------------------------------------

// extractFrames dumps one video's frames into step1/frames/<basename>/.
// A marker file records interval + size, so re-runs skip until either changes.
func (a *App) extractFrames(video string, interval float64, scaleName, scaleVF, s1 string, base, unit float64) error {
	name := baseName(video)
	fdir := filepath.Join(s1, "frames", name)
	marker := filepath.Join(fdir, ".interval")
	want := fmt.Sprintf("%g|%s", interval, scaleName)
	if b, err := os.ReadFile(marker); err == nil && strings.TrimSpace(string(b)) == want {
		a.logfIdle(">>> [%s] frames already extracted (%gs, %s), skipping", name, interval, scaleName)
		a.prog(trackFrames, base+unit, "[%s] frames ready", name)
		return nil
	}
	if interval == 0 {
		a.logfIdle(">>> [%s] extracting EVERY frame -- this can be many gigabytes", name)
	} else {
		a.logfIdle(">>> [%s] extracting a frame every %gs at %s", name, interval, scaleName)
	}
	os.RemoveAll(fdir)
	if err := os.MkdirAll(fdir, 0o755); err != nil {
		return err
	}
	var filters []string
	if interval > 0 {
		filters = append(filters, fmt.Sprintf("fps=%f", 1/interval))
	}
	if scaleVF != "" {
		filters = append(filters, scaleVF)
	}
	vf := strings.Join(filters, ",")
	pattern := filepath.Join(fdir, "f%06d.jpg")
	dur, err := ffprobeDur(video)
	if err != nil {
		return err
	}

	// Extraction is decode-bound: to keep one frame per second the decoder
	// still chews through every source frame. Splitting the timeline into
	// chunks and running one ffmpeg per chunk spreads that over the cores;
	// chunk lengths are multiples of the interval so the global mapping
	// frame n <-> t=(n-1)*interval stays exact across chunk borders.
	workers := runtime.NumCPU() / 4
	workers = max(2, min(8, workers))
	totalFrames := 0
	if interval > 0 {
		totalFrames = int(math.Ceil(dur / interval))
		if totalFrames < workers*4 {
			workers = 1
		}
	} else {
		workers = 1 // every-frame mode: frame count per chunk is not knowable
	}

	if workers == 1 {
		args := []string{"-v", "error", "-y", "-i", video}
		if vf != "" { // "-vf" with an empty graph is an ffmpeg error
			args = append(args, "-vf", vf)
		}
		args = append(args, "-q:v", "4", "-start_number", "1", pattern)
		if err := a.ffmpegProgress(dur, func(f float64) {
			a.prog(trackFrames, base+f*unit, "[%s] frames %.0f%%", name, f*100)
		}, args...); err != nil {
			return err
		}
	} else {
		chunkFrames := (totalFrames + workers - 1) / workers
		chunkDur := float64(chunkFrames) * interval
		var mu sync.Mutex
		fracs := make([]float64, workers)
		report := func() {
			mu.Lock()
			sum := 0.0
			for _, f := range fracs {
				sum += f
			}
			mu.Unlock()
			a.prog(trackFrames, base+sum*unit, "[%s] frames %.0f%%", name, sum*100)
		}
		var wg sync.WaitGroup
		errs := make([]error, workers)
		for k := 0; k < workers; k++ {
			n := chunkFrames
			if k == workers-1 {
				n = totalFrames - k*chunkFrames
			}
			if n <= 0 {
				continue
			}
			wg.Add(1)
			go func(k, n int) {
				defer wg.Done()
				weight := float64(n) / float64(totalFrames)
				errs[k] = a.ffmpegProgress(chunkDur, func(f float64) {
					mu.Lock()
					fracs[k] = math.Min(1, f) * weight
					mu.Unlock()
					report()
				}, "-v", "error", "-y",
					"-ss", fmt.Sprintf("%f", float64(k)*chunkDur),
					"-t", fmt.Sprintf("%f", chunkDur),
					"-i", video, "-vf", vf, "-q:v", "4",
					"-start_number", fmt.Sprint(k*chunkFrames+1),
					"-frames:v", fmt.Sprint(n), pattern)
			}(k, n)
		}
		wg.Wait()
		stopped := false
		for _, e := range errs {
			if errors.Is(e, errStopped) {
				stopped = true
			} else if e != nil {
				return e
			}
		}
		if stopped {
			return errStopped
		}
	}
	if err := os.WriteFile(marker, []byte(want+"\n"), 0o644); err != nil {
		return err
	}
	ents, _ := os.ReadDir(fdir)
	a.logfIdle(">>> [%s] %d frames extracted", name, len(ents)-1)
	return nil
}
