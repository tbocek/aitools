package main

// Drives the step-6 render directly, without the GUI, against the real cut.
// Doubles as diagnosis (a crash here explains a dead Produce button) and as a
// smoke test of the ffmpeg graph: cut from the right recording, concat, mux.
//
//   go test -run TestStep6Render -v          # first 2 clips, 480p
//   AUTOCUT_ALL=1 go test -run TestStep6Render -v -timeout 60m   # everything

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

func TestStep6Render(t *testing.T) {
	root := "/home/draft/git/aitools/autocut"
	a := &App{
		root:    root,
		inDir:   root,
		outDir:  filepath.Join(root, "out", "test"),
		curCmds: map[*exec.Cmd]bool{},
	}
	vids := []string{
		"input_video/com.AnotherAxiom.GorillaTag-20260808-195900-0.mp4",
		"input_video/com.AnotherAxiom.GorillaTag-20260808-200145-0.mp4",
	}
	auds := []string{"input_audio/jan2-2026-08-08_19-55-15.flac"}
	a.selVid, a.selAud = vids, auds

	segs := a.produceSegs()
	if len(segs) == 0 {
		t.Skip("no cut.json -- run step 4 first")
	}
	if os.Getenv("AUTOCUT_ALL") == "" && len(segs) > 2 {
		segs = segs[:2]
	}
	st := prodSettings{
		Container: "mp4", Codec: "h264", CRF: 24, Preset: "ultrafast",
		Height: 480, FPS: 30, AudioKbps: 128, GameVol: 0.22, Subs: "sidecar",
		OutFile: filepath.Join(a.outDir, "step6", "smoke.mp4"),
	}
	if err := a.produce(segs, a.produceEntries(), st, vids, auds); err != nil {
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
	root := "/home/draft/git/aitools/autocut"
	a := &App{
		root:    root,
		inDir:   root,
		outDir:  filepath.Join(root, "out", "test"),
		curCmds: map[*exec.Cmd]bool{},
	}
	vids := []string{
		"input_video/com.AnotherAxiom.GorillaTag-20260808-195900-0.mp4",
		"input_video/com.AnotherAxiom.GorillaTag-20260808-200145-0.mp4",
	}
	auds := []string{"input_audio/jan2-2026-08-08_19-55-15.flac"}
	a.selVid, a.selAud = vids, auds

	segs := a.produceSegs()
	if len(segs) == 0 {
		t.Skip("no cut.json -- run step 4 first")
	}
	segs = segs[:1]
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
		OutFile: filepath.Join(a.outDir, "step6", "smoke_narrated.mkv"),
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
