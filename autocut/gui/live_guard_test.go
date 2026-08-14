package main

// A test that reaches the LLM or the TTS server has to say so and be off by
// default. This one enforces that, because the failure it prevents is silent:
// TestStep3Pipeline drove the real fixer for months, so every plain `go test
// ./...` posted the whole transcript to whatever server llm.conf named, and
// nothing in the output said a request had left the machine.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The calls that leave the machine. Everything else in the tool is ffmpeg,
// files and arithmetic, and none of that needs a guard.
var remoteCalls = []string{
	"a.step1(", "a.describeAll(", "a.fixTranscripts(", // whole steps: STT, describe, fix
	"a.understand(",                 // the two of them back to back
	"a.llmChat(", "a.llmChatRetry(", // the chat client itself
	"a.speak(", // TTS
	"testLLM(", "testTTS(",
}

var liveGuards = []string{"AUTOCUT_LIVE", "AUTOCUT_TTS_LIVE"}

func TestRemoteTestsAreOptIn(t *testing.T) {
	files, err := filepath.Glob("*_test.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no test files found: %v", err)
	}
	// splits a file into whole top-level functions, so a guard in one test
	// cannot be credited to the next one down
	split := regexp.MustCompile(`(?m)^func `)
	for _, f := range files {
		if f == "live_guard_test.go" {
			continue // the call names above are not calls
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, fn := range split.Split(string(src), -1) {
			name := fn
			if i := strings.IndexAny(name, "(\n"); i >= 0 {
				name = name[:i]
			}
			// a test that stands up its own httptest server is calling
			// 127.0.0.1 with a URL it made itself -- local by construction
			if strings.Contains(fn, "httptest.NewServer") {
				continue
			}
			for _, call := range remoteCalls {
				if !strings.Contains(fn, call) {
					continue
				}
				guarded := false
				for _, g := range liveGuards {
					guarded = guarded || strings.Contains(fn, g)
				}
				if !guarded {
					t.Errorf("%s: %s calls %s with no %s skip -- "+
						"a plain `go test ./...` would hit the server",
						f, name, call, strings.Join(liveGuards, "/"))
				}
			}
		}
	}
}
