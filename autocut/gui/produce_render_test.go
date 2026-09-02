package main

// Drives the Produce render directly, without the GUI, against the real cut.
// Doubles as diagnosis (a crash here explains a dead Produce button) and as a
// smoke test of the ffmpeg graph: cut from the right recording, concat, mux.
//
//   go test -run TestPublishRender -v          # first 2 clips, 480p
//   AUTOCUT_ALL=1 go test -run TestPublishRender -v -timeout 60m   # everything
//
// It reads the live session -- the recordings, the cut, the narration -- and
// writes into a temporary folder. It used to write into the live session too,
// which is where the produce/ folder came from in an output nobody had produced:
// every `go test ./...` rendered a smoke.mp4 into the user's own out/test/step5
// and wiped its clips. Read from the session, write to the temp dir; renderApp
// below is the one place that split is made.

import (
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// renderApp is the app these tests drive, pointed at the live session's inputs
// and output. Everything it then writes goes wherever outDir is moved to --
// see redirectOutput.
func renderApp() (*App, []string, []string) {
	root := "/home/draft/git/aitools/autocut"
	a := &App{
		root:    root,
		vidDir:  filepath.Join(root, "input_video"),
		audDir:  filepath.Join(root, "input_audio"),
		outDir:  filepath.Join(root, "out", "test"),
		curCmds: map[*exec.Cmd]bool{},
	}
	vids := []string{
		filepath.Join(a.vidDir, "com.AnotherAxiom.GorillaTag-20260808-195900-0.mp4"),
		filepath.Join(a.vidDir, "com.AnotherAxiom.GorillaTag-20260808-200145-0.mp4"),
	}
	auds := []string{filepath.Join(a.audDir, "jan2-2026-08-08_19-55-15.flac")}
	a.selVid, a.selAud = vids, auds
	return a, vids, auds
}

// redirectOutput sends everything written from here on into the test's own
// folder -- produceDir above all -- while leaving the named step folders
// readable through a link, because a render still needs the diarization and the
// synthesis cache the session already has. What is linked is written through,
// so link only folders this test has no business adding to: never step4 for a
// test that puts a stand-in line in the cache.
func redirectOutput(t *testing.T, a *App, link ...string) {
	t.Helper()
	tmp := t.TempDir()
	for _, n := range link {
		src := filepath.Join(a.outDir, n)
		if !exists(src) {
			continue // that step has not run; the render will say so itself
		}
		if err := os.Symlink(src, filepath.Join(tmp, n)); err != nil {
			t.Fatal(err)
		}
	}
	a.outDir = tmp
}

func TestPublishRender(t *testing.T) {
	a, vids, auds := renderApp()
	segs, entries := a.produceSegs(), a.produceEntries()
	if len(segs) == 0 {
		t.Skip("no cut.json -- run the Cut step first")
	}
	if os.Getenv("AUTOCUT_ALL") == "" && len(segs) > 2 {
		segs = segs[:2]
	}
	// read the session, write nowhere near it; the narration this cut already
	// has is spoken, so the synthesis cache comes along read-only
	redirectOutput(t, a, "inputs", "narrate")
	st := prodSettings{
		Container: "mp4", Codec: "h264", CRF: 24, Preset: "ultrafast",
		Height: 480, FPS: 30, AudioKbps: 128, GameVol: 0.22, Subs: "sidecar",
		OutFile: filepath.Join(a.produceDir(), "smoke.mp4"),
	}
	if err := a.produce(segs, entries, st, vids, auds); err != nil {
		t.Fatalf("produce: %v", err)
	}
	dur, err := ffprobeDur(st.OutFile)
	if err != nil {
		t.Fatalf("no output: %v", err)
	}
	want := 0.0
	for _, s := range segs {
		want += s.E - s.S
	}
	// clips may be shortened where a segment runs off the end of its
	// recording, and grown where narration needs room -- but not by much
	if dur < want*0.8 || dur > want*1.3 {
		t.Fatalf("output is %.1f s, expected roughly %.1f s", dur, want)
	}
	t.Logf("%s: %.1f s (cut asked for %.1f s)", st.OutFile, dur, want)
}

// TestPublishNarrated covers the parts the plain render never touches: mixing a
// voice track under/over the game audio, growing a slot for a line that does
// not fit, and burning the subtitle in. The "voice" is a synthesized tone
// dropped straight into the TTS cache, so this needs no TTS server.
func TestPublishNarrated(t *testing.T) {
	a, vids, auds := renderApp()
	segs := a.produceSegs()
	if len(segs) == 0 {
		t.Skip("no cut.json -- run the Cut step first")
	}
	segs = segs[:1]
	// before the tone below: ttsWav is under outDir too, and a stand-in line in
	// the session's own synthesis cache is a wrong voice waiting to be played.
	// Hence no step4 here -- this test brings its own.
	redirectOutput(t, a, "inputs")
	// a line far longer than its slot: forces both the grow and the speed-up
	entries := []narrEntry{{
		S: segs[0].S, E: segs[0].E, Emotion: "excited",
		Text: "This is a stand-in narration line that is deliberately long enough " +
			"to wrap across two subtitle rows and to overrun its slot.",
	}}
	wav := a.ttsWav(entries[0])
	if err := os.MkdirAll(filepath.Dir(wav), 0o755); err != nil {
		t.Fatal(err)
	}
	tone := exec.Command("ffmpeg", "-v", "error", "-y", "-f", "lavfi",
		"-i", "sine=frequency=440:duration="+fmtDur(segs[0].E-segs[0].S+6),
		"-ac", "1", wav)
	if out, err := tone.CombinedOutput(); err != nil {
		t.Fatalf("tone: %v %s", err, out)
	}
	st := prodSettings{
		Container: "mkv", Codec: "h264", CRF: 28, Preset: "ultrafast",
		Height: 360, FPS: 24, AudioKbps: 128, GameVol: 0.22, Subs: "burn",
		OutFile: filepath.Join(a.produceDir(), "smoke_narrated.mkv"),
	}
	if err := a.produce(segs, entries, st, vids, auds); err != nil {
		t.Fatalf("produce: %v", err)
	}
	dur, err := ffprobeDur(st.OutFile)
	if err != nil {
		t.Fatalf("no output: %v", err)
	}
	slot := segs[0].E - segs[0].S
	if dur < slot+0.5 {
		t.Fatalf("slot did not grow for the narration: %.1f s vs %.1f s", dur, slot)
	}
	if dur > slot+maxExtend+1 {
		t.Fatalf("slot grew past the cap: %.1f s vs %.1f s", dur, slot+maxExtend)
	}
	t.Logf("%s: %.1f s (slot %.1f s, grown for the line)", st.OutFile, dur, slot)
}

func fmtDur(d float64) string { return strconv.FormatFloat(d, 'f', 2, 64) }

// TestFrameTimingFlagsAreOnesFFmpegTakes pins what the VFR checkbox sends. The
// combination that looks obvious -- a ceiling written as -r or -fpsmax next to
// an explicit -fps_mode vfr -- is refused outright ("this is contradictory")
// and every clip fails to encode, so the pairing is worth a test even though
// the function is four lines. Needs no ffmpeg: it is the argument list that was
// wrong, not the pipeline around it.
func TestFrameTimingFlagsAreOnesFFmpegTakes(t *testing.T) {
	for _, c := range []struct {
		name string
		st   prodSettings
		want string
	}{
		{"constant rate", prodSettings{FPS: 30}, "-fps_mode cfr"},
		{"constant, source rate", prodSettings{}, "-fps_mode cfr"},
		{"peak rate", prodSettings{FPS: 30, VFR: true}, "-fpsmax 30"},
		{"peak, nothing to cap", prodSettings{VFR: true}, "-fps_mode vfr"},
	} {
		got := strings.Join(fpsArgs(c.st), " ")
		if got != c.want {
			t.Errorf("%s: fpsArgs = %q, want %q", c.name, got, c.want)
		}
		if strings.Contains(got, "vfr") && strings.Contains(got, "fpsmax") {
			t.Errorf("%s: a ceiling and -fps_mode vfr together are refused by ffmpeg: %q",
				c.name, got)
		}
	}
	// and the fps filter, which duplicates frames onto a grid, is only for the
	// constant branch -- under a ceiling it would undo the ceiling's point
	if fps := fpsArgs(prodSettings{FPS: 30, VFR: true}); fps[0] == "-fps_mode" {
		t.Errorf("a chosen peak rate was dropped on the floor: %v", fps)
	}
}

// Produce ends the chain, and it used to be the page that said least about it:
// no Inputs row, no Outputs row, and in their place one dim paragraph that ran
// the cut, the narration, the resolution and the CRF together in a single
// sentence. It also had two ▶s -- its own beside "Preview result", and the run
// bar's, which meant something else -- for a video the finished run already
// cues up by itself.
//
// So: one line per question, in the places every other step puts them, and one
// ▶ on the page. Source-level, because nothing at run time can tell that a row
// is in the wrong place or that a button is one too many.
func TestProduceSaysWhatItReadsAndWroteLikeEveryOtherStep(t *testing.T) {
	b, err := os.ReadFile("produce.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	// the preview is gone, and so is the button that drew it
	for _, gone := range []string{
		`gtk.NewButtonWithLabel("Preview result")`,
		"playBtn",
		`gtk.NewLabel("Output:")`, // the destination row is "Save to:" now
	} {
		if strings.Contains(src, gone) {
			t.Errorf("produce.go still builds %s", gone)
		}
	}
	if p, err := os.ReadFile("pipeline.go"); err == nil && strings.Contains(string(p), "setPlayIcon(p.playBtn") {
		t.Error("the run bar still draws a play button Produce no longer has — that is a nil dereference")
	}
	// two rows, two jobs, one entry point that redraws both -- the encoder
	// summary line between them is gone: it repeated the grid beside it
	if strings.Contains(src, "updateSettings") {
		t.Error("produce.go grew the settings summary line back")
	}
	for _, want := range []string{
		"func (p *producer) updateInputs()",
		"func (p *producer) updateOut()",
		"p.updateInputs()\n\tp.updateOut()",
		`gtk.NewLabel("Save to:")`,
		// a dropdown at its own width, not stretched to the CRF slider's
		"d.SetHAlign(gtk.AlignStart)",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("produce.go no longer has %s", want)
		}
	}
	// ...and arriving on the page re-reads them, because everything they count
	// is made on a page upstream of this one
	m, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?s)func \(a \*App\) showStep.*?name == "produce".*?a\.updateProduceInfo\(\)`).Match(m) {
		t.Error("Produce no longer refreshes on arrival — its rows would show the cut as it was two edits ago")
	}
	// a subtitle mode saved by some later version must not index off the end of
	// the label list: the settings line reads it on every keystroke
	for _, c := range []struct {
		key  string
		want int
	}{{"burn", 0}, {"mux", 1}, {"sidecar", 2}, {"none", 3}, {"holographic", 0}, {"", 0}} {
		if got := subsIndex(c.key); got != c.want {
			t.Errorf("subsIndex(%q) = %d, want %d", c.key, got, c.want)
		}
	}
}

// Where a line actually starts in the mix.
//
// This is the overlapping-narration bug, and it is a units bug. The graph asked
// for "adelay=13.09s", meaning 13.09 seconds; adelay counts in milliseconds and
// reads a fractional value with a seconds suffix as nothing at all -- no error,
// no warning, the line simply started at the top of its clip. Every line
// dropped anywhere but on a whole second did that, all of them at once, over
// each other and over the first line. A whole number ("40s") happened to parse,
// which is why some clips were right and the fault looked intermittent.
//
// So the unit is pinned twice: once on the number this client computes, and
// once against ffmpeg itself, which is the only witness that can say what the
// filter did with it. No server, no session -- two synthetic tones.
func TestNarrationLandsWhereItWasPlaced(t *testing.T) {
	for _, c := range []struct {
		delay float64
		want  int
	}{
		{13.089980999259012, 13090}, // the line that started at zero
		{23.375761999258998, 23376},
		{40, 40000},   // the whole second that always worked
		{0, 300},      // never before the lead-in
		{-5, 300},     // ...including a placement dragged off the front
		{0.0006, 300}, // and the floor is applied before the rounding, not after
	} {
		if got := delayMS(c.delay); got != c.want {
			t.Errorf("a line placed at %gs is delayed by %d ms, want %d", c.delay, got, c.want)
		}
	}
	b, err := os.ReadFile("produce.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	// The other way a line can arrive early: the render places it by its offset
	// into the clip it was written against, and Cut may have moved that clip
	// since -- matchEntries accepts exactly that. Drag a border 20 s left and an
	// offset kept as written is 20 s early, which lands it on the head of the
	// clip over the line before it. Produce reads the saved narration directly,
	// so it cannot rely on Narrate having been visited to re-anchor them.
	if !strings.Contains(src, "at := (e.S + e.At - s.S) / c.speed()") {
		t.Error("the render places a line by its offset into a clip that may have moved underneath it")
	}
	// the format verb matters as much as the number: %g on 23376 would write
	// "23376" today and "2.3376e+06" for a longer clip, and the s suffix is the
	// spelling that started this
	if !strings.Contains(src, "adelay=%d:all=1") || strings.Contains(src, "adelay=%gs") {
		t.Error("the mix is back to giving adelay a fractional number of seconds, which it reads as no delay at all")
	}

	// ...and now ask ffmpeg. A half-second tone mixed over six seconds of
	// silence, delayed the way encodeClip delays a line: the first sound in the
	// result has to be at the delay, not at zero.
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg on this machine")
	}
	dir := t.TempDir()
	mix := filepath.Join(dir, "mix.wav")
	const want = 2.345
	out, err := exec.Command("ffmpeg", "-v", "error", "-y",
		"-f", "lavfi", "-t", "6", "-i", "anullsrc=channel_layout=stereo:sample_rate=48000",
		"-f", "lavfi", "-t", "0.5", "-i", "sine=frequency=1000:sample_rate=48000",
		"-filter_complex", fmt.Sprintf(
			"[1:a]aresample=48000,pan=stereo|c0=c0|c1=c0,adelay=%d:all=1[nr0];"+
				"[0:a]volume=0.220,aresample=48000[bg];"+
				"[bg][nr0]amix=inputs=2:duration=first:normalize=0,"+audFmt(prodSettings{})+"[a]", delayMS(want)),
		"-map", "[a]", mix).CombinedOutput()
	if err != nil {
		t.Fatalf("the narration graph does not run: %v\n%s", err, out)
	}
	// silencedetect reports the silence BEFORE the tone; where that silence ends
	// is where the line came in
	det, err := exec.Command("ffmpeg", "-v", "info", "-i", mix,
		"-af", "silencedetect=n=-50dB:d=0.1", "-f", "null", "-").CombinedOutput()
	if err != nil {
		t.Fatalf("silencedetect: %v\n%s", err, det)
	}
	m := regexp.MustCompile(`silence_end: ([0-9.]+)`).FindStringSubmatch(string(det))
	if m == nil {
		t.Fatalf("the tone never arrives in six seconds — the delay ran away with it\n%s", det)
	}
	got, _ := strconv.ParseFloat(m[1], 64)
	if math.Abs(got-want) > 0.05 {
		t.Errorf("a line placed at %gs is spoken at %gs — %s", want, got,
			"this is the units bug: adelay counts milliseconds")
	}
}
