package main

import (
	"strings"
	"testing"
)

// What every job is told about the material, and that no job assembles a system
// message any other way. The point of the prompt is not that it exists -- it is
// that there is one of it: a wording added tomorrow is told how a stamp reads
// without its author having to remember to say so.
func TestEveryJobIsToldTheFormatsOnce(t *testing.T) {
	ownConfig(t)
	a := &App{}

	// it is a prompt like the others, and it is the first of them: the bench
	// lists them in the order they are sent, and this one is sent in front of
	// all of them
	if d := promptDefFor("system"); strings.TrimSpace(d.def) != strings.TrimSpace(sysSystem) {
		t.Fatal(`prompt "system" is not the registered system context`)
	}
	if promptDefs[0].key != "system" {
		t.Errorf("the system context is registered %q, not first -- the registry order "+
			"is the order the bench shows and the order the calls go out in", promptDefs[0].key)
	}

	for _, key := range []string{"describe", "fix", "cut", "audit", "narrate", "youtube"} {
		got := a.sysPrompt(key)
		if !strings.HasPrefix(got, strings.TrimSpace(sysSystem)) {
			t.Errorf("the %s job is not told the formats first:\n%s", key, got)
		}
		if !strings.HasSuffix(got, a.prompt(key)) {
			t.Errorf("the %s job's own prompt is not what follows the context", key)
		}
	}

	// and editing the box reaches every one of them, which is the only reason
	// it is a box and not a const
	a.setPrompt("system", "stamps are in minutes")
	for _, key := range []string{"cut", "narrate"} {
		if !strings.HasPrefix(a.sysPrompt(key), "stamps are in minutes") {
			t.Errorf("the edited system context is not what the %s job is sent", key)
		}
	}
	// emptied, it takes itself away rather than sending a blank run-up
	a.setPrompt("system", "   ")
	if got := a.sysPrompt("cut"); got != a.prompt("cut") {
		t.Errorf("an emptied system context still sends something in front of the job: %q",
			got[:min(80, len(got))])
	}
}

// Every system message in the app is built through the one seam. A job that
// reaches for its prompt directly is a job that is never told the formats, and
// it would look exactly like the others until its answers came back in the
// wrong clock.
func TestNoJobAssemblesItsOwnSystemMessage(t *testing.T) {
	for file, key := range map[string]string{
		"describe.go":    "describe",
		"transcript.go":  "fix",
		"cut_suggest.go": "cut",
		"narrate.go":     "narrate",
		"publish.go":     "youtube",
	} {
		src := readSrc(t, file)
		if !strings.Contains(src, `a.sysPrompt("`+key+`")`) {
			t.Errorf(`%s no longer sends a.sysPrompt(%q)`, file, key)
		}
		for _, raw := range []string{`msg("system", a.prompt("` + key, `system := a.prompt("` + key} {
			if strings.Contains(src, raw) {
				t.Errorf(`%s builds a system message out of a.prompt(%q), so that job is `+
					"the one that is never told how a stamp reads", file, key)
			}
		}
	}
	if src := readSrc(t, "cut_suggest.go"); !strings.Contains(src, `a.sysPrompt("audit")`) {
		t.Error(`the audit no longer goes out through sysPrompt`)
	}
}

// The wordings stop repeating what the context now says. Leaving both in is not
// wrong to read, but it is what the change was for: the sentences drifted apart
// precisely because they were written four times.
func TestTheCutWordingsDoNotRepeatTheSystemContext(t *testing.T) {
	for _, p := range []struct{ name, text string }{
		{"general", genericSystem},
		{"highlights", suggestSystem},
		{"rating", ratingSystem},
		{"showcase", showcaseSystem},
		{"shorts", shortsSystem},
		{"audit", auditSystem},
	} {
		for _, gone := range []string{"[12:04] EVENT", "counting past 59", "4350 seconds"} {
			if strings.Contains(p.text, gone) {
				t.Errorf("the %s wording still explains %q itself, which the system "+
					"context says to every job already", p.name, gone)
			}
		}
	}
	// what a style is for stays in the style: this took the facts out, not the
	// judgement
	if !strings.Contains(shortsSystem, "20 to 30 seconds") {
		t.Error("the Shorts wording lost the length that makes it Shorts")
	}
	if !strings.Contains(genericSystem, `{"segments":`) {
		t.Error("the default wording lost the reply shape its own parser reads")
	}
}
