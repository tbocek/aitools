package main

// Drives the Produce render directly, without the GUI, against the real cut.
// Doubles as diagnosis (a crash here explains a dead Produce button) and as a
// smoke test of the ffmpeg graph: cut from the right recording, concat, mux.
//
//   go test -run TestStep6Render -v          # first 2 clips, 480p
//   AUTOCUT_ALL=1 go test -run TestStep6Render -v -timeout 60m   # everything
//
// It reads the live session -- the recordings, the cut, the narration -- and
// writes into a temporary folder. It used to write into the live session too,
// which is where the step5/ folder came from in an output nobody had produced:
// every `go test ./...` rendered a smoke.mp4 into the user's own out/test/step5
// and wiped its clips. Read from the session, write to the temp dir; renderApp
// below is the one place that split is made.

import (
	"os"
	"os/exec"
	"path/filepath"
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

func TestStep6Render(t *testing.T) {
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
	redirectOutput(t, a, "step1", "step4")
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

// TestStep6Narrated covers the parts the plain render never touches: mixing a
// voice track under/over the game audio, growing a slot for a line that does
// not fit, and burning the subtitle in. The "voice" is a synthesized tone
// dropped straight into the TTS cache, so this needs no TTS server.
func TestStep6Narrated(t *testing.T) {
	a, vids, auds := renderApp()
	segs := a.produceSegs()
	if len(segs) == 0 {
		t.Skip("no cut.json -- run the Cut step first")
	}
	segs = segs[:1]
	// before the tone below: ttsWav is under outDir too, and a stand-in line in
	// the session's own synthesis cache is a wrong voice waiting to be played.
	// Hence no step4 here -- this test brings its own.
	redirectOutput(t, a, "step1")
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
