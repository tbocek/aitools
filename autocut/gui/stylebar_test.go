package main

// The kind of video ▶ builds -- highlights, a rating, a Short -- picked once,
// beside Language on Prepare. The only way to that choice used to be Prompts
// menu → Edit → the wording dropdown: three levels deep, the right depth for
// rewording a prompt and the wrong one for the one choice made before every
// run. The Style dropdown is that choice now, and it is the ONLY wording
// picker left: a pick turns every job to its wording named after the style,
// or to its default when it has none (applyStyle). What these tests hold is
// the store and the seams: a pick lands in every job's store, and the list
// refresh that follows an added or deleted wording reaches the surfaced
// dropdown.

import (
	"strings"
	"testing"
)

func TestAPickIsStoredNoMatterWhichDropdownMadeIt(t *testing.T) {
	ownConfig(t)
	// both dropdowns call pickPromptStyle; headless there are no widgets, so
	// this is the shared path below either one
	a := &App{root: t.TempDir()}
	if got := a.promptPickName("cut"); got == shortsStyleName {
		t.Fatal("the fixture already picks Shorts, so the test would show nothing")
	}
	a.pickPromptStyle("cut", shortsStyleName)
	if got := a.promptPickName("cut"); got != shortsStyleName {
		t.Errorf("after a pick the store says %q, want %q", got, shortsStyleName)
	}
	// prompt() must now send the Shorts wording: the pick IS the prompt choice
	if !strings.Contains(a.prompt("cut"), "YouTube Short") {
		t.Error("the picked wording is not what the cut prompt now says")
	}
}

func TestTheStyleBarIsWiredToTheOneStore(t *testing.T) {
	ownConfig(t)
	// the Prepare page carries the dropdown...
	if !strings.Contains(readSrc(t, "prep.go"), `bottom.Append(a.styleBar("cut", "Style",`) {
		t.Error("Prepare no longer surfaces the style dropdown")
	}
	// ...whose pick turns every prompt, through the selection-only half for
	// the reason showPromptStyle documents
	pick := readSrc(t, "stylebar.go")
	if !strings.Contains(pick, "a.applyStyle(d.names.String(i))") {
		t.Error("styleBar no longer turns every prompt through applyStyle")
	}
	// a pick made in the store lands back in the surfaced dropdown, and a
	// changed wording list reaches it too
	prompts := readSrc(t, "prompts.go")
	for _, want := range []string{
		"a.syncStylePicks(key, name)",
		"bar, ok := a.styleDrops[key]",
	} {
		if !strings.Contains(prompts, want) {
			t.Errorf("prompts.go does not contain %q — the store and the Style dropdown can drift apart", want)
		}
	}
}

// TestOneStyleTurnsEveryPrompt: applyStyle headless, against the stores. The
// style's name reaches every job that has a wording under it -- shipped or
// saved on this machine -- and leaves the rest on their defaults, so a job
// never silently keeps the LAST style's wording either: turning back releases
// it.
func TestOneStyleTurnsEveryPrompt(t *testing.T) {
	ownConfig(t)
	a := &App{root: t.TempDir()}
	// a narration wording saved under a shipped style's name is that style's
	// narration from then on
	a.savePromptStyle("narrate", shortsStyleName, "narrate for a Short")

	a.applyStyle(shortsStyleName)
	if got := a.promptPickName("cut"); got != shortsStyleName {
		t.Errorf("after the style pick the cut is on %q, want %q", got, shortsStyleName)
	}
	if got := a.prompt("narrate"); got != "narrate for a Short" {
		t.Errorf("the narration prompt reads %q, want the wording saved under the style's name", short(got))
	}
	if got := a.promptPickName("describe"); got != defStyle {
		t.Errorf("a job with nothing under the style's name is on %q, want its default", got)
	}

	// turning to another style releases the narration wording too: nothing
	// stays picked to a style that is no longer the project's
	a.applyStyle("General")
	if got := a.promptPickName("cut"); got != "General" {
		t.Errorf("after General the cut is on %q", got)
	}
	if got := a.promptPickName("narrate"); got != defStyle {
		t.Errorf("after General the narration is still on %q, want its default", got)
	}
}
