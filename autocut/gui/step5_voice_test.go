package main

// The voice step has failure modes a mouse would not catch quickly: a voice
// that does not survive being written to the project folder, a pitch slider
// that moves nothing or moves the wrong file, and a synthesis cache that
// ignores either -- which would serve the previous speaker after a switch, or
// throw away every line already spoken. All are checked here; the GUI wiring
// around them is not testable without a display.

import (
	"bytes"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
)

// sineWav writes a pure tone, which toneHz can read back exactly -- the point
// of the pitch tests is the ratio, not how speech survives rubberband.
func sineWav(t *testing.T, hz int) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cv-xx-female-30s.wav")
	if err := exec.Command("ffmpeg", "-v", "error", "-y", "-f", "lavfi",
		"-i", "sine=frequency="+strconv.Itoa(hz)+":duration=2", p).Run(); err != nil {
		t.Skipf("no usable ffmpeg: %v", err)
	}
	return p
}

// toneHz counts zero crossings over the middle second, away from the edges
// where a phase vocoder has nothing to work with yet.
func toneHz(t *testing.T, wav string) float64 {
	t.Helper()
	const sr = 16000
	raw, err := exec.Command("ffmpeg", "-v", "error", "-ss", "0.5", "-t", "1", "-i", wav,
		"-f", "s16le", "-ac", "1", "-ar", "16000", "-").Output()
	if err != nil {
		t.Fatalf("decode %s: %v", wav, err)
	}
	n, cross, prev := len(raw)/2, 0, int16(0)
	if n < sr/2 {
		t.Fatalf("%s decoded to %d samples -- too short to measure", wav, n)
	}
	for i := 0; i < n; i++ {
		s := int16(uint16(raw[2*i]) | uint16(raw[2*i+1])<<8)
		if (s >= 0) != (prev >= 0) && i > 0 {
			cross++
		}
		prev = s
	}
	return float64(cross) / 2 * sr / float64(n)
}

func TestVoiceRoundTrip(t *testing.T) {
	a := &App{outDir: t.TempDir(), curCmds: map[*exec.Cmd]bool{}}
	if got := a.voiceID(); got != ownVoice {
		t.Fatalf("a fresh project speaks in %q, want %q", got, ownVoice)
	}

	// a real wav through the real conversion path; ffmpeg is a hard dependency
	// of the pipeline anyway
	src := sineWav(t, 220)
	v := voiceOpt{id: "cv-xx-female-30s", name: prettyVoice("cv-xx-female-30s.wav"), path: src}
	if err := a.setVoice(v); err != nil {
		t.Fatalf("setVoice: %v", err)
	}
	if !exists(a.refBase()) {
		t.Fatal("choosing a voice installed no base recording to clone")
	}
	if err := a.ensureVoiceRef(); err != nil {
		t.Fatalf("ensureVoiceRef: %v", err)
	}
	if !exists(a.refPath()) {
		t.Fatal("no voice_ref.wav for the server to clone")
	}

	// a later session reads the choice back out of the folder
	if got := (&App{outDir: a.outDir}).voiceID(); got != v.id {
		t.Fatalf("reopened project speaks in %q, want %q", got, v.id)
	}

	// back to own: both stale files must go, or ensureVoiceRef short-circuits
	// on them and keeps cloning the CC0 speaker forever
	if err := a.setVoice(voiceOpt{id: ownVoice}); err != nil {
		t.Fatalf("setVoice(own): %v", err)
	}
	if exists(a.refPath()) || exists(a.refBase()) {
		t.Fatal("switching back to your own voice left the previous reference in place")
	}
	if got := (&App{outDir: a.outDir}).voiceID(); got != ownVoice {
		t.Fatalf("reopened project speaks in %q, want %q", got, ownVoice)
	}
}

// TestRefPitchShift is the whole point of the slider: the file the server is
// handed really is moved, by the amount asked for, and the base it came from is
// left alone so the next move is not a shift of a shift.
func TestRefPitchShift(t *testing.T) {
	a := &App{outDir: t.TempDir(), curCmds: map[*exec.Cmd]bool{}}
	src := sineWav(t, 220)
	if err := a.setVoice(voiceOpt{id: "cv-xx-female-30s", path: src}); err != nil {
		t.Fatalf("setVoice: %v", err)
	}
	if err := a.ensureVoiceRef(); err != nil {
		t.Fatalf("ensureVoiceRef: %v", err)
	}
	if got := toneHz(t, a.refPath()); math.Abs(got-220) > 22 {
		t.Fatalf("unshifted reference is %.0f Hz, want ~220", got)
	}

	for _, st := range []float64{6, -6, 0} {
		if err := a.setPitchST(st); err != nil {
			t.Fatalf("setPitchST(%v): %v", st, err)
		}
		want := 220 * math.Pow(2, st/12)
		if got := toneHz(t, a.refPath()); math.Abs(got-want) > want*0.1 {
			t.Errorf("%+.0f semitones: reference is %.0f Hz, want ~%.0f", st, got, want)
		}
		if got := toneHz(t, a.refBase()); math.Abs(got-220) > 22 {
			t.Fatalf("%+.0f semitones moved the base too (%.0f Hz) -- shifts would compound", st, got)
		}
		if got := (&App{outDir: a.outDir}).pitchST(); got != st {
			t.Errorf("reopened project is at %+.1f semitones, want %+.1f", got, st)
		}
	}

	// past the cap the clone stops sounding human; the slider cannot ask for it
	// but a hand-edited pitch.txt can
	if err := a.setPitchST(40); err != nil {
		t.Fatalf("setPitchST(40): %v", err)
	}
	if got := a.pitchST(); got != pitchRange {
		t.Errorf("40 semitones was stored as %+.1f, want the %+.1f cap", got, pitchRange)
	}
}

// TestVoiceRefMigration covers projects narrated before this step gained a
// slider: they have the single unshifted voice_ref.wav and nothing else.
func TestVoiceRefMigration(t *testing.T) {
	a := &App{outDir: t.TempDir(), curCmds: map[*exec.Cmd]bool{}}
	if err := os.MkdirAll(filepath.Join(a.outDir, "step5"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := []byte("RIFF....WAVEfmt  -- stands in for the reference of an old project")
	if err := os.WriteFile(a.refPath(), old, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.ensureVoiceRef(); err != nil {
		t.Fatalf("ensureVoiceRef: %v", err)
	}
	got, err := os.ReadFile(a.refBase())
	if err != nil {
		t.Fatalf("the old reference was not kept as the base: %v", err)
	}
	if !bytes.Equal(got, old) {
		t.Fatal("the old reference was replaced rather than adopted as the base")
	}
	if got, _ := os.ReadFile(a.refPath()); !bytes.Equal(got, old) {
		t.Fatal("an unshifted project lost the reference the server reads")
	}
}

// TestVoicePitchLive is the end-to-end claim: shifting the reference shifts the
// clone. The same sentence, the same speaker, three settings, and a voice that
// actually moves with the slider. Needs the TTS server and writes under the
// project folder the server has mounted at its own path, so it is opt-in.
//
//	AUTOCUT_TTS_LIVE=1 go test -run TestVoicePitchLive -v -timeout 30m
func TestVoicePitchLive(t *testing.T) {
	if os.Getenv("AUTOCUT_TTS_LIVE") == "" {
		t.Skip("set AUTOCUT_TTS_LIVE=1 to speak against the real server")
	}
	root := "/home/draft/git/aitools/autocut"
	a := &App{
		root:    root,
		outDir:  filepath.Join(root, "out", "test", "pitchlive"),
		curCmds: map[*exec.Cmd]bool{},
	}
	src := filepath.Join(voicesDir(), "audio-sample-tbocek.wav")
	if !exists(src) {
		t.Skipf("no reference wav at %s", src)
	}
	if err := a.setVoice(voiceOpt{id: "audio-sample-tbocek", path: src}); err != nil {
		t.Fatalf("setVoice: %v", err)
	}
	const line = "This is the voice the narration will be spoken in."
	f0 := map[float64]float64{}
	for _, st := range []float64{-4, 0, 4} {
		if err := a.setPitchST(st); err != nil {
			t.Fatalf("setPitchST(%v): %v", st, err)
		}
		out := a.sampleWav(a.voiceKey(), line)
		if !exists(out) {
			if err := a.speak(line, "", out); err != nil {
				t.Fatalf("speak at %+.0f: %v", st, err)
			}
		}
		f0[st] = speechHz(t, out)
		t.Logf("%+.0f semitones: reference %.0f Hz -> clone %.0f Hz  (%s)",
			st, speechHz(t, a.refPath()), f0[st], filepath.Base(out))
	}
	if !(f0[-4] < f0[0] && f0[0] < f0[4]) {
		t.Errorf("the clone did not follow the slider: %.0f / %.0f / %.0f Hz for -4 / 0 / +4",
			f0[-4], f0[0], f0[4])
	}
}

// speechHz is a median f0 over the voiced frames -- autocorrelation, which
// unlike counting zero crossings survives speech.
func speechHz(t *testing.T, wav string) float64 {
	t.Helper()
	const sr, win = 16000, 640 // 40 ms
	raw, err := exec.Command("ffmpeg", "-v", "error", "-i", wav,
		"-f", "s16le", "-ac", "1", "-ar", "16000", "-").Output()
	if err != nil {
		t.Fatalf("decode %s: %v", wav, err)
	}
	x := make([]float64, len(raw)/2)
	for i := range x {
		x[i] = float64(int16(uint16(raw[2*i]) | uint16(raw[2*i+1])<<8))
	}
	lo, hi := sr/400, sr/70 // the range a human voice lives in
	var f0s []float64
	for i := 0; i+win <= len(x); i += win / 2 {
		f := append([]float64(nil), x[i:i+win]...)
		var mean, energy float64
		for _, v := range f {
			mean += v
		}
		mean /= win
		for j := range f {
			f[j] -= mean
			energy += f[j] * f[j]
		}
		if math.Sqrt(energy/win) < 300 { // silence between words
			continue
		}
		bestK, best := 0, 0.0
		for k := lo; k < hi; k++ {
			var c float64
			for j := 0; j+k < win; j++ {
				c += f[j] * f[j+k]
			}
			if c > best {
				bestK, best = k, c
			}
		}
		if bestK > 0 && best > 0.3*energy {
			f0s = append(f0s, float64(sr)/float64(bestK))
		}
	}
	if len(f0s) == 0 {
		t.Fatalf("no voiced frames in %s", wav)
	}
	sort.Float64s(f0s)
	return f0s[len(f0s)/2]
}

func TestTTSWavKeyedOnVoice(t *testing.T) {
	a := &App{outDir: t.TempDir()}
	e := narrEntry{Text: "the same line", Emotion: "calm"}

	a.voiceSel, a.pitchSel, a.pitchRead = ownVoice, 0, true
	own := a.ttsWav(e)
	a.voiceSel = "cv-gb-female-30s"
	other := a.ttsWav(e)
	if own == other {
		t.Fatal("two voices share one cache file — a switch would serve the old speaker")
	}
	// pitch is part of the voice: shifting it must not serve the unshifted take
	a.pitchSel = -3
	shifted := a.ttsWav(e)
	if shifted == other {
		t.Fatal("shifting the reference kept the cache file — the slider would seem to do nothing")
	}
	a.pitchSel = 0
	if again := a.ttsWav(e); again != other {
		t.Fatalf("returning to the unshifted voice missed its cache: %s vs %s", again, other)
	}
	a.voiceSel = ownVoice
	if again := a.ttsWav(e); again != own {
		t.Fatalf("switching back missed the earlier cache: %s vs %s", again, own)
	}
}

func TestPrettyVoice(t *testing.T) {
	for in, want := range map[string]string{
		"cv-gb-female-30s.wav":    "gb · female · 30s",
		"cv-us-male-teen.wav":     "us · male · teen",
		"audio-sample-tbocek.wav": "audio · sample · tbocek",
	} {
		if got := prettyVoice(in); got != want {
			t.Errorf("prettyVoice(%q) = %q, want %q", in, got, want)
		}
	}
}
