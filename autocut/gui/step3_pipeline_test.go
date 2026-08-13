package main

// Drives the step-3 pipeline directly, without the GUI, against the real
// project data. Doubles as diagnosis (a crash here explains a dead button)
// and as the actual run (its outputs are the real step3/ artifacts).

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestStep3Pipeline(t *testing.T) {
	root := "/home/draft/git/aitools/autocut"
	a := &App{
		root:    root,
		outDir:  filepath.Join(root, "out", "test"),
		curCmds: map[*exec.Cmd]bool{},
	}
	videos := []string{
		filepath.Join(root, "input_video", "com.AnotherAxiom.GorillaTag-20260808-195900-0.mp4"),
		filepath.Join(root, "input_video", "com.AnotherAxiom.GorillaTag-20260808-200145-0.mp4"),
	}
	audios := []string{filepath.Join(root, "input_audio", "jan2-2026-08-08_19-55-15.flac")}
	if err := a.step3(videos, audios, ""); err != nil {
		t.Fatalf("step3: %v", err)
	}
}
