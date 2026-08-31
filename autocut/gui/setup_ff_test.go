package main

// Two things about the settings page: which ffmpeg the pipeline runs, and how
// much of the page is prose.
//
// ffmpeg used to be read-only -- taken off PATH, shown but not settable --
// which is right until a machine has two of them, or the GUI's PATH is not the
// shell's. It is a box now, and the box has to reach the runners: a setting
// that is saved and then ignored is worse than no setting.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The box, from typing to shelling out.
func TestTheFFmpegBoxIsWhatTheStepsRun(t *testing.T) {
	old := ffSet.Load()
	t.Cleanup(func() { ffSet.Store(old) })

	// empty is the ordinary answer: the bare name, which exec resolves off
	// PATH the way it always did
	e := ""
	ffSet.Store(&e)
	if got := ffTool("ffmpeg"); got != "ffmpeg" {
		t.Errorf("with an empty box the steps run %q, want the bare name", got)
	}
	if got := ffTool("ffprobe"); got != "ffprobe" {
		t.Errorf("with an empty box the steps probe with %q, want the bare name", got)
	}

	// a path is used as given, and ffprobe comes from BESIDE it -- a
	// hand-built ffmpeg paired with the distro's ffprobe is the mismatch the
	// box exists to fix, so probe must not fall back to PATH
	p := "/opt/ff7/bin/ffmpeg"
	ffSet.Store(&p)
	if got := ffTool("ffmpeg"); got != p {
		t.Errorf("ffTool = %q, want the path that was typed", got)
	}
	if got, want := ffTool("ffprobe"), "/opt/ff7/bin/ffprobe"; got != want {
		t.Errorf("ffprobe = %q, want %q -- beside the ffmpeg that was chosen", got, want)
	}
}

// Saved, re-read, and in force -- the runners read no config of their own, so
// readConf is where ffTool learns what was typed.
func TestTheFFmpegBoxSurvivesASaveAndReachesTheRunners(t *testing.T) {
	ownConfig(t)
	old := ffSet.Load()
	t.Cleanup(func() { ffSet.Store(old) })

	a := &App{root: t.TempDir()}
	c := a.readConf()
	if c.FFmpeg != "" {
		t.Errorf("a fresh config names ffmpeg %q, want empty -- PATH is the default", c.FFmpeg)
	}
	if got := ffTool("ffmpeg"); got != "ffmpeg" {
		t.Errorf("a fresh config left the steps running %q", got)
	}

	c.FFmpeg = "/opt/ff7/bin/ffmpeg"
	if err := a.writeConf(c); err != nil {
		t.Fatal(err)
	}
	// blank it, to prove the value comes back off disk and not out of memory
	blank := ""
	ffSet.Store(&blank)

	if got := a.readConf(); got.FFmpeg != c.FFmpeg {
		t.Errorf("after a save the config names %q, want %q", got.FFmpeg, c.FFmpeg)
	}
	if got := ffTool("ffmpeg"); got != c.FFmpeg {
		t.Errorf("reading the config left the steps running %q, want %q", got, c.FFmpeg)
	}

	// and it is a credential file: the key sits in it, so it stays 0600
	st, err := os.Stat(confPath())
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("llm.conf is mode %v, want 0600 -- it holds an API key", st.Mode().Perm())
	}
}

// Nothing shells out to a bare "ffmpeg" or "ffprobe" any more: one that does
// ignores the box on the machine the box was added for.
func TestNoStepGoesRoundTheFFmpegBox(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "setup.go" {
			continue // setup.go is where the box and the lookup live
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{`exec.Command("ffmpeg"`, `exec.Command("ffprobe"`,
			`runCmd("ffmpeg"`, `runCmd("ffprobe"`} {
			if strings.Contains(string(b), bad) {
				t.Errorf("%s still calls %s… — it must go through ffTool, "+
					"or the settings box does nothing there", f, bad)
			}
		}
	}
}

// The page keeps its titles and hides its paragraphs. Every section says which
// API it expects, because that is the one thing a box cannot show.
func TestEverySettingsSectionSaysWhichAPIItExpects(t *testing.T) {
	b, err := os.ReadFile("setup.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)

	// the heading is a title and an ⓘ, and the words live in its popover
	for _, want := range []string{
		"head := func(title, why string) *gtk.Box {",
		"info.SetIconName(\"help-about-symbolic\")",
		"info.SetPopover(pop)",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("the section heading no longer builds %q", want)
		}
	}
	// five sections, five titles, and nothing longer than a word or two on
	// the page itself -- the em-dash subtitles are what moved behind the ⓘ
	for _, title := range []string{
		`head("Writing", `, `head("Speaking", `, `head("Cutting", `,
		`head("Listening", `, `head("Drawing", `,
	} {
		if strings.Count(s, title) != 1 {
			t.Errorf("the settings page has %d sections titled %s, want one",
				strings.Count(s, title), title)
		}
	}
	// each one names the API its boxes are expected to speak
	for _, api := range []string{
		"/v1/chat/completions", // Writing
		"/v1/audio/speech",     // Speaking
		"/v1/tasks/run",        // Listening
		"/sdcpp/v1/img_gen",    // Drawing
		"/sdcpp/v1/capabilities",
		"/v1/models",
	} {
		if !strings.Contains(s, api) {
			t.Errorf("no settings section tells the user about %s", api)
		}
	}
	// and Cutting says what ffmpeg is: not a server, and empty means PATH
	if !strings.Contains(s, "Not a server: a ") {
		t.Error("the ffmpeg section no longer says it is a local binary, not an endpoint")
	}
	// the standing paragraph above the model rows is gone
	if strings.Contains(s, `foot.AddCSSClass("dim-label")`) {
		t.Error("the settings page still prints a paragraph above the model rows")
	}
}
