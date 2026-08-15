package main

// The voice picker has failure modes a mouse would not catch quickly: a voice
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
	"regexp"
	"sort"
	"strconv"
	"strings"
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
	v := voiceOpt{id: "cv-xx-female-30s", name: "cv-xx-female-30s.wav", path: src}
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
	if err := os.MkdirAll(a.narrateDir(), 0o755); err != nil {
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
	src := filepath.Join(a.voicesDir(), "audio-sample-tbocek.wav")
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
			if err := a.speak(line, "", ttsSeed(out), out); err != nil {
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

// The imported id becomes a file name in the sample cache and the contents of
// voice.txt, so anything that could steer a write out of the folder has to be
// gone before it gets there.
func TestSanitizeVoiceID(t *testing.T) {
	for in, want := range map[string]string{
		"cv-gb-female-30s":  "cv-gb-female-30s",
		"my recording":      "my-recording",
		"../../etc/passwd":  "etc-passwd",
		"Tom's take #2":     "Tom-s-take--2",
		"jan2_2026-08-08":   "jan2_2026-08-08",
		"...":               "voice",
		"":                  "voice",
		"/absolute/path/wv": "absolute-path-wv",
	} {
		got := sanitizeVoiceID(in)
		if got != want {
			t.Errorf("sanitizeVoiceID(%q) = %q, want %q", in, got, want)
		}
		if strings.ContainsAny(got, `/\`) || got == "." || got == ".." {
			t.Errorf("sanitizeVoiceID(%q) = %q, which is not a plain file name", in, got)
		}
	}
}

// An import must never land on a name already in use: the id is what every
// project's voice.txt points at, so overwriting one would silently change the
// speaker in a project that was never opened.
func TestImportVoiceKeepsNamesDistinct(t *testing.T) {
	root, models := t.TempDir(), t.TempDir()
	a := &App{root: root, outDir: t.TempDir(), curCmds: map[*exec.Cmd]bool{}}
	if err := os.WriteFile(a.confPath(),
		[]byte("AUDIOCPP_MODELS="+strconv.Quote(models)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	src := sineWav(t, 330)
	first, err := a.importVoice(src)
	if err != nil {
		t.Fatalf("importVoice: %v", err)
	}
	second, err := a.importVoice(src)
	if err != nil {
		t.Fatalf("importVoice (again): %v", err)
	}
	if first.id == second.id {
		t.Fatalf("both imports claimed the id %q", first.id)
	}
	if !exists(first.path) || !exists(second.path) {
		t.Fatal("an imported voice is missing from the voices folder")
	}
	names := map[string]bool{}
	for _, v := range a.listVoices() {
		names[v.id] = true
	}
	if !names[first.id] || !names[second.id] {
		t.Fatalf("imported voices are not listed: %v", names)
	}
}

// toneFile writes a one-second tone under a name of the caller's choosing --
// what a folder import cares about is which files it picks up, not how they
// sound.
func toneFile(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := exec.Command("ffmpeg", "-v", "error", "-y", "-f", "lavfi",
		"-i", "sine=frequency=330:duration=1", p).Run(); err != nil {
		t.Skipf("no usable ffmpeg: %v", err)
	}
	return p
}

// A folder import has three ways to go wrong that a mouse would find slowly: it
// takes files it was not pointed at, it takes the same folder twice and buries
// the list in duplicates, or it is aimed at the voices folder and copies it onto
// itself.
func TestImportVoiceDir(t *testing.T) {
	root, models := t.TempDir(), t.TempDir()
	a := &App{root: root, outDir: t.TempDir(), curCmds: map[*exec.Cmd]bool{}}
	if err := os.WriteFile(a.confPath(),
		[]byte("AUDIOCPP_MODELS="+strconv.Quote(models)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	src := t.TempDir()
	toneFile(t, src, "alice.wav")
	toneFile(t, src, "bob.flac")
	if err := os.WriteFile(filepath.Join(src, "notes.txt"), []byte("not a voice"), 0o644); err != nil {
		t.Fatal(err)
	}
	deep := filepath.Join(src, "rejects")
	if err := os.Mkdir(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	toneFile(t, deep, "carol.wav")

	res, err := a.importVoiceDir(src)
	if err != nil {
		t.Fatalf("importVoiceDir: %v", err)
	}
	if len(res.added) != 2 || res.skipped != 0 || res.failed != 0 {
		t.Fatalf("first import: %+v (%s), want the two recordings and nothing else",
			res.added, res.summary())
	}
	got := map[string]bool{}
	for _, v := range a.listVoices() {
		got[v.id] = true
	}
	if !got["alice"] || !got["bob"] {
		t.Fatalf("the folder's recordings are not listed: %v", got)
	}
	if got["carol"] {
		t.Fatal("a sub-folder was walked into — pointing this at a music library would import the lot")
	}
	if got["notes"] {
		t.Fatal("a text file was handed to ffmpeg and listed as a voice")
	}

	// the same folder again: nothing new, and nothing doubled
	res, err = a.importVoiceDir(src)
	if err != nil {
		t.Fatalf("importVoiceDir (again): %v", err)
	}
	if len(res.added) != 0 || res.skipped != 2 {
		t.Fatalf("re-import: %s — re-adding a folder must not duplicate what is in it", res.summary())
	}
	if n := len(a.listVoices()); n != 3 { // own + alice + bob
		t.Fatalf("%d rows after re-importing the same folder, want 3", n)
	}

	// aimed at the voices folder itself, which would copy every voice beside
	// itself as "-2" on each attempt
	if _, err := a.importVoiceDir(a.voicesDir()); err == nil {
		t.Fatal("importing the voices folder into itself was allowed")
	}
	if _, err := a.importVoiceDir(t.TempDir()); err == nil {
		t.Fatal("a folder with nothing usable in it reported success")
	}
}

// TestEveryPlayerSaysWhenItFails: GStreamer reports a file it cannot decode on
// the bus and nowhere else, so a player without OnError swallows it -- the
// window goes on showing ⏸ over silence and the log never mentions it. Nothing
// at run time notices a missing hookup, hence source level, and hence counting
// rather than spot-checking: the next player added is the one that would be
// forgotten.
func TestEveryPlayerSaysWhenItFails(t *testing.T) {
	made := regexp.MustCompile(`NewPlayer\(\)`)
	wired := regexp.MustCompile(`(?m)^\s*\S+\.OnError = `)
	for _, f := range []string{"main.go", "step3.go", "step4.go", "step4_voice.go"} {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		n, w := len(made.FindAll(src, -1)), len(wired.FindAll(src, -1))
		if n != w {
			t.Errorf("%s builds %d players but wires OnError on %d — one of them fails silently", f, n, w)
		}
	}
	// and the handler has to reach the log, not stdout, which is where these
	// went before and where nobody running the app from a launcher looks
	src, err := os.ReadFile("player.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "func (a *App) playerErr(") {
		t.Fatal("playerErr is gone; the OnError hookups above point at nothing")
	}
	if !regexp.MustCompile(`(?s)func \(a \*App\) playerErr\(.*?a\.logf\(`).Match(src) {
		t.Error("playerErr does not write to the log")
	}
	if !regexp.MustCompile(`(?s)func \(a \*App\) playerErr\(.*?a\.setStatus\(`).Match(src) {
		t.Error("playerErr does not touch the status line")
	}
}

// TestSampleAccountsForItself: the sample is the one thing on this page that
// leaves no artifact to inspect -- no clip, no row, no file the user goes
// looking for -- so if the run says nothing, the run is gone. Each branch of
// playSample must reach either the log or the status line.
func TestSampleAccountsForItself(t *testing.T) {
	src, err := os.ReadFile("step4_voice.go")
	if err != nil {
		t.Fatal(err)
	}
	body := regexp.MustCompile(`(?s)func \(vp \*voicePicker\) playSample\(\).*?\n}\n`).Find(src)
	if body == nil {
		t.Fatal("playSample not found")
	}
	for _, want := range []string{
		`a.logf(">>> sample:`,   // what was asked for, before it can fail
		`a.logf("sample FAILED`, // the server said no
		`a.logf("!!! sample:`,   // the server said yes and sent nothing
		`a.logf("    sample:`,   // it worked: which file, how big, how long
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("playSample has no %s line — that path runs silently", want)
		}
	}
	// the already-playing branch of the button, which changed the icon and said
	// nothing at all
	tog := regexp.MustCompile(`(?s)func \(vp \*voicePicker\) playClicked\(\).*?\n}\n`).Find(src)
	if !strings.Contains(string(tog), "setStatus") {
		t.Error("playClicked's pause/resume branch reports nothing")
	}
}
