package main

// The kind of cut ▶ builds -- highlights, a rating, a Short -- picked on the
// bar itself. The only way to that choice used to be Prompts menu → Edit → the
// wording dropdown: three levels deep, the right depth for rewording a prompt
// and the wrong one for the one choice made before every suggest run. styleBar
// surfaces the same list on the Cut toolbar. There is exactly one stored
// choice under both dropdowns (promptPick), so what these tests hold is the
// store and the seams: a pick lands in the store wherever it is made, every
// dropdown showing the choice is told about a pick made in the other, and the
// list refresh that follows an added or deleted wording reaches both.

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
	// the bar carries the dropdown...
	if !strings.Contains(readSrc(t, "cut.go"), `bar.Append(a.styleBar("cut", "Style",`) {
		t.Error("the Cut bar no longer surfaces the style dropdown")
	}
	// ...whose pick goes through the same store as the editor's dropdown,
	// selection-only for the reason showPromptStyle documents
	pick := readSrc(t, "stylebar.go")
	if !strings.Contains(pick, "a.pickPromptStyle(key, d.names.String(i))") {
		t.Error("styleBar no longer writes picks through pickPromptStyle")
	}
	// a pick made anywhere lands in every dropdown that shows the choice, and
	// a changed wording list reaches the surfaced dropdown as well as the
	// editor's
	prompts := readSrc(t, "prompts.go")
	for _, want := range []string{
		"a.syncStylePicks(key, name)",
		"bar, barOK := a.styleDrops[key]",
	} {
		if !strings.Contains(prompts, want) {
			t.Errorf("prompts.go does not contain %q — the two style dropdowns can drift apart", want)
		}
	}
}
