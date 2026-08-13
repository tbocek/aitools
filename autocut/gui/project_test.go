package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A project stores its input and output folders relative to root so that moving
// the autocut directory moves them too. The round trip has to be exact: a
// folder that comes back wrong points the whole pipeline at the wrong files,
// and nothing about that failure says "the project file lied".
func TestFolderRoundTripsThroughRoot(t *testing.T) {
	root := t.TempDir()
	a := &App{root: root}

	for _, dir := range []string{
		root,
		filepath.Join(root, "sessions", "gorilla"),
		filepath.Join(filepath.Dir(root), "elsewhere"),
		"/mnt/recordings",
	} {
		stored := a.relToRoot(dir)
		if got := a.fromRoot(stored); got != dir {
			t.Errorf("%s stored as %q came back as %s", dir, stored, got)
		}
	}

	inside := filepath.Join(root, "sessions")
	if got := a.relToRoot(inside); filepath.IsAbs(got) {
		t.Errorf("a folder under root should be stored relative, got %s", got)
	}
	outside := "/mnt/recordings"
	if got := a.relToRoot(outside); got != outside {
		t.Errorf("a folder outside root has nothing to be relative to, got %s", got)
	}
}

// An absent in_dir/out_dir means the root -- that is what every project written
// before the folders were settable looks like.
func TestEmptyFolderMeansRoot(t *testing.T) {
	a := &App{root: "/home/x/autocut"}
	if got := a.fromRoot(""); got != a.root {
		t.Fatalf("empty folder resolved to %s, want the root", got)
	}
}

// Step 1's outputs are frames in per-recording subfolders, so the count has to
// be recursive: a top-level count would report "2 files" about a few thousand.
func TestOutputSummaryCountsEveryFrame(t *testing.T) {
	dir := t.TempDir()
	if got := summarizeOutputs(dir); got != "nothing yet" {
		t.Fatalf("empty folder summarized as %q", got)
	}

	os.MkdirAll(filepath.Join(dir, "frames", "session1"), 0o755)
	os.WriteFile(filepath.Join(dir, "meta.env"), []byte("x=1\n"), 0o644)
	for _, n := range []string{"f0001.jpg", "f0002.jpg", "f0003.jpg"} {
		os.WriteFile(filepath.Join(dir, "frames", "session1", n), []byte("x"), 0o644)
	}
	got := summarizeOutputs(dir)
	if !strings.HasPrefix(got, "4 files") {
		t.Errorf("summary = %q, want it to start with 4 files", got)
	}
	// freshly written, so the age has to read as now rather than as a date
	if !strings.Contains(got, "just now") {
		t.Errorf("summary = %q, want the age of files written this second", got)
	}
}

// Sources are stored relative to the INPUT folder, not to root: the two are the
// same only until someone chooses a different input folder.
func TestSrcPathFollowsTheInputFolder(t *testing.T) {
	a := &App{root: "/home/x/autocut", inDir: "/mnt/recordings"}
	want := "/mnt/recordings/input_video/clip.mkv"
	if got := a.srcPath("input_video/clip.mkv"); got != want {
		t.Fatalf("srcPath = %s, want %s", got, want)
	}
}
