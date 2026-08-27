package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The whole point of the file, in one round trip: what was open when the window
// closed is what the next launch opens. The bug it replaces is the one the user
// hit -- save the session as jan-video.json, quit, come back to project.json --
// and it was invisible, because the header bar said the right name for as long
// as the session lasted.
func TestTheOpenProjectSurvivesARestart(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	a := &App{root: root}

	if got := a.lastProject(); got != "" {
		t.Errorf("a first launch remembered %q", got)
	}

	named := filepath.Join(root, "jan-video.json")
	if err := os.WriteFile(named, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a.rememberProject(named)

	// a fresh App is the restart: nothing is carried in memory
	if got := (&App{root: root}).lastProject(); got != named {
		t.Errorf("after a restart the project is %q, want %q", got, named)
	}
	if p := settingsPath(); !exists(p) {
		t.Errorf("nothing was written to %s", p)
	} else if !strings.HasSuffix(p, filepath.Join("autocut", "settings.json")) {
		t.Errorf("the settings live at %s, which is not the path the user was told", p)
	}

	// and going back to the unnamed session is a decision, not an absence
	work := filepath.Join(root, "project.json")
	if err := os.WriteFile(work, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a.rememberProject(work)
	if got := (&App{root: root}).lastProject(); got != work {
		t.Errorf("after saving back to the working copy the project is %q, want %q", got, work)
	}
}

// Every path inside a project file is relative to its root, so a project
// remembered against one autocut folder must never be opened from another: its
// sources would resolve against the wrong directory and be dropped one warning
// at a time. Two sessions on one machine is the normal case, not the exotic one.
func TestEachAutocutFolderRemembersItsOwnProject(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	one, two := t.TempDir(), t.TempDir()
	pOne := filepath.Join(one, "jan-video.json")
	pTwo := filepath.Join(two, "the-other-raid.json")
	for _, p := range []string{pOne, pTwo} {
		if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	(&App{root: one}).rememberProject(pOne)
	(&App{root: two}).rememberProject(pTwo)

	if got := (&App{root: one}).lastProject(); got != pOne {
		t.Errorf("the first folder opens %q, want %q", got, pOne)
	}
	if got := (&App{root: two}).lastProject(); got != pTwo {
		t.Errorf("the second folder opens %q, want %q -- the second save overwrote the first", got, pTwo)
	}
	if got := (&App{root: t.TempDir()}).lastProject(); got != "" {
		t.Errorf("a folder that has never been used opens %q", got)
	}
}

// Nothing here is worth a failed launch. The file is a convenience: half-written
// by a kill -9, hand-edited into invalid JSON, pointing at a project on a drive
// that is not mounted this morning, or unwritable because there is no HOME at
// all -- every one of those has to end with autocut opening the working copy.
func TestABrokenSettingsFileIsNotAFailedLaunch(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	root := t.TempDir()

	if err := os.MkdirAll(filepath.Join(cfg, "autocut"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath(), []byte("{not json at al"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := (&App{root: root}).lastProject(); got != "" {
		t.Errorf("a corrupt settings file yielded %q", got)
	}
	// and it is overwritten rather than being a permanent state
	named := filepath.Join(root, "jan-video.json")
	if err := os.WriteFile(named, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	(&App{root: root}).rememberProject(named)
	if got := (&App{root: root}).lastProject(); got != named {
		t.Errorf("after a corrupt file was replaced the project is %q, want %q", got, named)
	}

	// a project that has since been renamed, moved or deleted
	if err := os.Remove(named); err != nil {
		t.Fatal(err)
	}
	if got := (&App{root: root}).lastProject(); got != "" {
		t.Errorf("a project that is not there any more was still opened: %q", got)
	}
	// ...but the name is kept, because an unmounted drive looks the same as a
	// deletion and forgetting it would make the difference permanent
	if loadSettings().Projects[root] != named {
		t.Error("the remembered name was pruned the first time the file was missing")
	}

	// nowhere to put the file at all
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	if p := settingsPath(); p != "" {
		t.Errorf("with no HOME the settings path is %q, want it disabled", p)
	}
	b := &App{root: root}
	b.rememberProject(named) // must not panic, must not fail
	if got := b.lastProject(); got != "" {
		t.Errorf("with no HOME the last project is %q", got)
	}
}

// The wiring, which is the half that rots: remembering has to happen in both
// places that assign projPath, the launch has to prefer what was remembered
// over the working copy, and running preprocessing must not rename the open project
// back to project.json -- that last one is how a Save could be undone by
// pressing ▶, which would defeat all of the above.
func TestWhatIsRememberedIsWhatTheNextLaunchOpens(t *testing.T) {
	p := readSrc(t, "project.go")
	for _, fn := range []string{"saveProjectTo", "loadProjectFrom"} {
		body := regexp.MustCompile(`(?s)func \(a \*App\) ` + fn + `\(path string\) \{.*?\n}\n`).FindString(p)
		if body == "" {
			t.Fatalf("%s is gone", fn)
		}
		if !strings.Contains(body, "a.rememberProject(") {
			t.Errorf("%s changes the open project without recording it for the next launch", fn)
		}
	}
	m := readSrc(t, "main.go")
	if !strings.Contains(m, "if pj := a.lastProject(); pj != \"\" {") {
		t.Error("the launch no longer prefers the remembered project over the working copy")
	}
	if strings.Contains(m, `a.saveProjectTo(filepath.Join(a.root, "project.json"))`) {
		t.Error("running a step still renames the open project to the working copy, " +
			"which un-names a project the user saved")
	}
}
