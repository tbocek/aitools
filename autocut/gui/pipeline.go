package main

// Step 1, natively: STT both inputs and dump frames at an interval, into
// step1/. ffmpeg is driven via os/exec and the audio.cpp server over HTTP
// (audiocpp.go); the anchored diarization and the segment merge are real code
// here, not awk.
//
// step1/
//   <input-basename>/  voice16k.wav, transcript.{txt,tsv,srt}, words.json,
//                      turns.json  (per input, video and voice alike)
//   frames/<input-basename>/2026-08-08_19-59-00.jpg   one per interval, named
//                      for the wall-clock second it was shot in; frame n is
//                      still exactly t = (n-1) * interval into the recording
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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
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

// The tracks the two halves of the bar are reported on, and prog itself, live
// in runqueue.go with the work queue they feed.

// transport is what a page offers the run bar when it plays media rather than
// running a pipeline. With it, ▶ and ⏹ mean the same thing on every step: ▶
// starts what the page does and then pauses and resumes it, ⏹ ends it. Without
// it a page's playback was reachable only through the page's own buttons --
// which is why ⏸ and ⏹ sat there doing nothing while a voice sample played.
type transport interface {
	playing() bool // running right now
	cued() bool    // loaded but paused, so ▶ means resume rather than start over
	toggle()
	stop()
}

// pageTransport is the visible page's playback, if that page has any.
func (a *App) pageTransport() transport {
	switch a.stack.VisibleChildName() {
	case "step3":
		// ▶ here suggests a cut, the page's long job, and it stays that way
		// until the preview is actually running. The player is loaded the
		// moment you click the timeline -- that cues a frame to look at, it is
		// not playback, and it used to mean ▶ played the video on a page whose
		// cut was still empty. Once the preview HAS been started, the run bar
		// is its transport until ⏹.
		if a.ed != nil && (a.ed.playing() || a.ed.started) {
			return a.ed
		}
	case "step4":
		// ▶ here is the step itself -- write the narration, then speak it -- and
		// it stays that way until the preview is actually running: the voice
		// sample has its own two buttons beside it, and a clip merely cued by
		// clicking a line to look at it is not playback. Once the preview HAS
		// been started, the run bar is its transport until ⏹ -- otherwise ⏸
		// would have nothing to pause and ⏹ nothing to end.
		if a.narr5 != nil && (a.narr5.playing() || a.narr5.started) {
			return a.narr5
		}
	case "step5":
		// same rule as narrate: the page's job owns ▶ until the result is
		// actually playing, so a video watched once cannot leave ▶ meaning
		// "watch it again" for the rest of the session
		if a.prod != nil && (a.prod.playing() || a.prod.started) {
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
	case "step2":
		a.understandRun()
	case "step3":
		// ▶ is this step's job, suggesting, exactly as it is on every other page.
		// It used to be three things at once -- add the pending selection, or
		// suggest but only into an empty cut, or else print a sentence explaining
		// which of the two you had asked for -- because "Suggest cut" was also a
		// button in the toolbar. That button is gone, so ▶ is the one way to run
		// the step, and adding a selection is ＋ Add, which is where it always was.
		// Suggesting over hand edits still refuses, in suggestClicked, and says to
		// Revert first: that is a rule about the cut, not about which button ran.
		if a.ed != nil {
			a.suggestClicked()
		}
	case "step4":
		// the whole step in one press: write the narration if the cut has no
		// narration or has moved under the one it has, then speak every line
		// that is not already in the cache (narrateRun). It used to be only the
		// speaking half, with the writing on a button beside the video.
		a.narrateRun()
	case "step5":
		a.produceClicked()
	case "step6":
		// same shape as step 4's: one press does whatever is still missing.
		// Empty boxes are written first, then the picture is drawn -- and with
		// everything already written it only draws, which is what makes ▶ the
		// button you press after rewording the instruction or swapping the base
		// frame.
		a.publishRun(false)
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
	a.syncPlayIcons()
}

// setPlayIcon draws one transport button from what its player is doing. Every
// play button in the app is this one button in two states -- a ▶ that has
// already started something is a lie, and a separate ⏸ beside it is a button
// that is dead more often than not.
func setPlayIcon(b *gtk.Button, playing bool, playTip, pauseTip string) {
	if b == nil {
		return
	}
	if playing {
		b.SetIconName("media-playback-pause-symbolic")
		b.SetTooltipText(pauseTip)
	} else {
		b.SetIconName("media-playback-start-symbolic")
		b.SetTooltipText(playTip)
	}
}

// syncPlayIcons redraws the pages' own play buttons. They hang off four
// different players, all of which report here through Player.OnState, so this
// runs on every start, pause and end-of-stream -- including the ones nobody
// clicked for, like a clip simply finishing.
func (a *App) syncPlayIcons() {
	if a.ed != nil {
		setPlayIcon(a.ed.playBtn, a.ed.playing(),
			"play the preview from the playhead — same as ▶ below", "pause the preview")
	}
	if n := a.narr5; n != nil {
		// the preview has no button of its own any more: the picture is its
		// play/pause, and the run bar is the rest of its transport
		n.syncSpeakIcons()
	}
	if vp := a.voice5; vp != nil {
		setPlayIcon(vp.playBtn, vp.playing(),
			"Speak the sample in the selected voice", "pause the sample")
		if vp.stopBtn != nil {
			// nothing loaded is nothing to stop: the sample's ⏹ is the one
			// button on this page that the run bar's ⏹ no longer covers
			vp.stopBtn.SetSensitive(vp.playing() || vp.cued())
		}
	}
	// Produce is not here: it has no play button of its own. A finished run cues
	// its result into the picture and the run bar is that video's transport from
	// there, which is one ▶ for the page instead of the two it used to have.
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

// ffprobeSize is a still's pixel size. The Publish step needs it because the
// title is drawn at a fraction of the picture's height, and the picture's
// height is the image server's decision, not ours: it is asked for 1280x720
// and a model with a fixed latent size may hand back something else.
func ffprobeSize(f string) (w, h int, err error) {
	out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=width,height", "-of", "csv=p=0:s=x", f).Output()
	if err != nil {
		return 0, 0, fmt.Errorf("size of %s: %w", f, err)
	}
	if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%dx%d", &w, &h); err != nil || w <= 0 || h <= 0 {
		return 0, 0, fmt.Errorf("cannot read the size of %s (ffprobe said %q)", f, strings.TrimSpace(string(out)))
	}
	return w, h, nil
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
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return spansFrom(b)
}

// spansFrom is the same for an answer that never reached disk -- which is what
// a window of the diarization scan is.
func spansFrom(b []byte) ([]span, error) {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
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
	a.qReset()
	a.updateRunControls()
	a.setStatus("step 1 running…")
	a.logExp.SetExpanded(true)
	// what went in, by name -- the page has room for a count and nothing more
	a.logf(">>> step 1: %d input files", len(videos)+len(audios))
	for _, f := range append(append([]string{}, videos...), audios...) {
		a.logf("    %s", f)
	}
	go func() {
		err := a.step1(videos, audios, interval, scaleName, scaleVF)
		glib.IdleAdd(func() {
			a.running = false
			a.updateRunControls()
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
				a.logf(">>> step 1 wrote:")
				n := a.logOutputs("step1", filepath.Join(a.outDir, "step1"))
				a.setStatus(fmt.Sprintf("step 1 done — %d files in step1/", n))
			}
			a.updateStep1Info()
			a.und.refresh() // the next step's input counts just changed
			a.updateGates()
			a.refreshCut() // new frames, new interval: the track strip is drawn from them
		})
	}()
}

func (a *App) step1(videos, audios []string, interval float64, scaleName, scaleVF string) error {
	s1 := filepath.Join(a.outDir, "step1")
	if err := os.MkdirAll(s1, 0o755); err != nil {
		return err
	}
	// the models are the server's to load, but that it HAS them is worth
	// finding out now rather than after the frame extraction
	if err := a.ensureAudioModels(); err != nil {
		return err
	}
	// progress plan: half the bar each. This step is two jobs -- speech
	// recognition (GPU, on the server) and frame extraction (CPU ffmpeg) --
	// which do not contend, so they run as parallel tracks. Weighting them by
	// file count instead gave whichever job had more inputs most of the bar,
	// and the bar then crossed that job's share and sat still through the other.
	//
	// Both jobs run at once, so neither is "1 of 2": the bar names them instead
	// and shows both lines. Each queues a task per file it was given, and the
	// speech side queues more as it goes -- a recording's diarization windows
	// are not countable until its length is known.
	inputs := append(append([]string{}, videos...), audios...)
	a.qJob(trackSTT, "speech", 0, 0)
	a.qPush(trackSTT, len(inputs), "recording")
	a.qJob(trackFrames, "frames", 0, 0)
	a.qPush(trackFrames, len(videos), "video")
	var unit, funit float64
	if len(inputs) > 0 {
		unit = 0.5 / float64(len(inputs))
	} else {
		a.qDone(trackSTT, 0.5) // a job with nothing to do is done, or its
	}
	if len(videos) > 0 {
		funit = 0.5 / float64(len(videos))
	} else {
		a.qDone(trackFrames, 0.5) // half would never fill and neither would the bar
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
			a.qTake(trackFrames)
			if framesErr = a.extractFrames(v, interval, scaleName, scaleVF, s1, fb, funit); framesErr != nil {
				return
			}
			fb += funit
		}
		a.qDone(trackFrames, fb)
	}()

	var sttErr error
	base := 0.0
	for _, in := range inputs {
		if sttErr = a.checkpoint(); sttErr != nil {
			break
		}
		a.qTake(trackSTT)
		if sttErr = a.transcribe(in, s1, base, unit); sttErr != nil {
			if !errors.Is(sttErr, errStopped) {
				sttErr = fmt.Errorf("%s: %w", filepath.Base(in), sttErr)
			}
			break
		}
		base += unit
	}
	if sttErr == nil {
		a.qDone(trackSTT, base)
	}
	wg.Wait()

	// one stop is one stop, not two errors; otherwise report whatever failed
	if errors.Is(sttErr, errStopped) && errors.Is(framesErr, errStopped) {
		return errStopped
	}
	if err := errors.Join(sttErr, framesErr); err != nil {
		return err
	}

	// primary pair for the single-source consumers (review page, align); the
	// full ordered lists live in project.json. Either half can be absent now:
	// a session may be one screen recording that is both, or recordings with no
	// footage at all. So each is written only if there is one -- and what says
	// "step 1 has run" is that this file exists, not what is in it.
	var lines []string
	if len(videos) > 0 {
		lines = append(lines, "VIDEO_FILE="+videos[0], "VIDEO_BASE="+baseName(videos[0]))
	}
	// the narrator, not the first recording: that tag is who this session speaks
	// as, and untagged it falls back to exactly the file audios[0] used to be
	if voice := a.narratorSource(1); voice != "" {
		lines = append(lines, "AUDIO_FILE="+voice, "AUDIO_BASE="+baseName(voice))
	}
	lines = append(lines, fmt.Sprintf("INTERVAL=%g", interval), "SCALE="+scaleName)
	return os.WriteFile(filepath.Join(s1, "meta.env"),
		[]byte(strings.Join(lines, "\n")+"\n"), 0o644)
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
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}

	wav := filepath.Join(out, "voice16k.wav")
	if !exists(wav) {
		a.prog(trackSTT, base+0.01*unit, "extracting audio")
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
		a.prog(trackSTT, base+0.05*unit, "recognising speech")
		a.logfIdle(">>> [%s] ASR (%s)", name, a.readConf().ASRModel)
		body, text, err := a.asrJSON(wav)
		if err != nil {
			return fmt.Errorf("ASR: %w", err)
		}
		if err := os.WriteFile(filepath.Join(out, "transcript.txt"),
			[]byte(strings.TrimRight(text, "\n")+"\n"), 0o644); err != nil {
			return err
		}
		// words.json last, and whole: it is this stage's resume marker, so it
		// must not exist until the answer it stands for is on disk
		if err := os.WriteFile(filepath.Join(out, "words.json"), body, 0o644); err != nil {
			return err
		}
	} else {
		a.logfIdle(">>> [%s] ASR already done", name)
	}

	if err := a.checkpoint(); err != nil {
		return err
	}
	if !exists(filepath.Join(out, "turns.json")) {
		if err := a.diarize(out, dur, name, base, unit); err != nil {
			if errors.Is(err, errStopped) {
				return err
			}
			return fmt.Errorf("diarization: %w", err)
		}
	} else {
		a.logfIdle(">>> [%s] diarization already done", name)
	}

	a.prog(trackSTT, base+0.98*unit, "building segments")
	if err := a.mergeSegments(out); err != nil {
		return fmt.Errorf("merge: %w", err)
	}
	return nil
}

func (a *App) diarize(out string, dur float64, name string, base, unit float64) error {
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
		a.prog(trackSTT, base+(0.55+0.20*float64(i)/float64(nwin))*unit, "finding voices")
		start := float64(i) * diarScanHop
		if err := a.runCmd("ffmpeg", "-v", "error", "-y",
			"-ss", fmt.Sprint(start), "-t", fmt.Sprint(diarWin),
			"-i", filepath.Join(out, "voice16k.wav"),
			"-c:a", "pcm_s16le", filepath.Join(dir, "s.wav")); err != nil {
			return err
		}
		spans, err := a.diarSpans(filepath.Join(dir, "s.wav"))
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
		a.prog(trackSTT, base+(0.75+0.22*float64(i)/float64(nwin))*unit, "placing speakers")
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
		spans, err := a.diarSpans(filepath.Join(dir, "win.wav"))
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
		// the pixels are already right; only a folder extracted before frames
		// were named for their second has anything left to do, and that is a
		// rename rather than the minutes of decoding a re-extract would cost
		if n, err := stampFrames(fdir, video, interval); err != nil {
			return err
		} else if n > 0 {
			a.logfIdle(">>> [%s] %d frames renamed to the second they were shot in", name, n)
		}
		a.logfIdle(">>> [%s] frames already extracted (%gs, %s), skipping", name, interval, scaleName)
		a.prog(trackFrames, base+unit, "already extracted")
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
			a.prog(trackFrames, base+f*unit, "extracting %.0f%%", f*100)
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
			a.prog(trackFrames, base+sum*unit, "extracting %.0f%%", sum*100)
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
	if _, err := stampFrames(fdir, video, interval); err != nil {
		return err
	}
	if err := os.WriteFile(marker, []byte(want+"\n"), 0o644); err != nil {
		return err
	}
	ents, _ := os.ReadDir(fdir)
	a.logfIdle(">>> [%s] %d frames extracted", name, len(ents)-1)
	return nil
}

// stampFrames renames ffmpeg's counting into the wall clock: every frame is
// called the second it was shot in, so a folder of them reads against the
// session timeline -- and against the recorders' own file names -- without
// anyone doing arithmetic. An interval under a second puts several frames in
// one second; those are numbered -1, -2 after the first.
//
// The name comes from the frame NUMBER, never from the position in the sorted
// listing: a chunk that yields fewer frames than planned leaves a gap in the
// numbering, and t = (n-1) * interval has to keep holding across it.
//
// Nothing numbered left in the folder means there is nothing to do, which is
// the normal case on a re-run -- so this is also what renames a folder
// extracted before frames were stamped, without decoding it again.
func stampFrames(fdir, video string, interval float64) (int, error) {
	ents, err := os.ReadDir(fdir)
	if err != nil {
		return 0, err
	}
	type frame struct {
		n    int
		name string
	}
	var fs []frame
	for _, e := range ents {
		n, ok := frameNum(e.Name())
		if e.IsDir() || !ok {
			continue
		}
		fs = append(fs, frame{n, e.Name()})
	}
	if len(fs) == 0 {
		return 0, nil
	}
	start, err := sourceStart(video)
	if err != nil {
		return 0, err
	}
	if interval <= 0 {
		// every-frame mode: the spacing is the video's own. Without a frame rate
		// there is no time to name a frame after, so it keeps its number.
		fps := ffprobeFPS(video)
		if fps <= 0 {
			return 0, nil
		}
		interval = 1 / fps
	}
	sort.Slice(fs, func(i, j int) bool { return fs[i].n < fs[j].n })
	var seq stampSeq
	for _, f := range fs {
		to := seq.name(start+float64(f.n-1)*interval, ".jpg")
		if err := os.Rename(filepath.Join(fdir, f.name), filepath.Join(fdir, to)); err != nil {
			return 0, err
		}
	}
	return len(fs), nil
}

// frameNum reads ffmpeg's f000001.jpg back. Frames are renamed the moment they
// are extracted, so this only ever sees a folder mid-extraction -- or one made
// before the frames carried their timestamp.
func frameNum(name string) (int, bool) {
	if !strings.HasPrefix(name, "f") || !strings.HasSuffix(name, ".jpg") {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSuffix(name[1:], ".jpg"))
	return n, err == nil && n >= 1
}
