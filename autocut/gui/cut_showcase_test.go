package main

import (
	"strings"
	"testing"
)

// The Showcase wording: a session whose subject is a thing being shown rather
// than a stretch of time. It is a fourth choice in the same dropdown, so what
// is worth testing is that it is reachable, that picking it is what the cut
// goes out with, and that it asks for the shape no other wording asks for --
// otherwise it is a name in a menu that quietly cuts like Highlights.
func TestTheShowcaseWordingIsPickableAndCutsForLooking(t *testing.T) {
	ownConfig(t)
	a := &App{}

	// picked by name from the one Style dropdown, and it is the cut that
	// changes: a job with no wording of that name keeps its default
	a.applyStyle("Showcase")
	if got := a.promptPickName("cut"); got != "Showcase" {
		t.Fatalf("after picking Showcase the cut is picked to %q", got)
	}
	if got := a.prompt("cut"); got != strings.TrimSpace(showcaseSystem) {
		t.Errorf("the cut job does not go out with the Showcase wording:\n%s",
			got[:min(200, len(got))])
	}
	// the narration has a Showcase wording too, and one pick turns both: a
	// video cut for looking at a thing wants a voice about that thing
	if got := a.prompt("narrate"); got != strings.TrimSpace(narrShowcaseSystem) {
		t.Errorf("the narration did not follow the style:\n%s", got[:min(200, len(got))])
	}
	// ...and it is the same craft under a different subject, not a second
	// narration to keep in step by hand
	if !strings.HasSuffix(strings.TrimSpace(narrShowcaseSystem), strings.TrimSpace(narrCraft)) ||
		!strings.HasSuffix(strings.TrimSpace(narrSystem), strings.TrimSpace(narrCraft)) {
		t.Error("the two narrations no longer share narrCraft")
	}
	// a job with no wording of that name is still on its own default
	if got := a.promptPickName("describe"); got != defStyle {
		t.Errorf("the describer is on %q, want the default -- there is no Showcase describer", got)
	}
	// and it rides the same seam as every other job
	if !strings.HasSuffix(a.sysPrompt("cut"), strings.TrimSpace(showcaseSystem)) {
		t.Error("the Showcase wording is not what follows the system context")
	}

	// what makes it a showcase rather than a highlight reel: the thing has to
	// be on screen, seen whole and seen close, and several things share the
	// length instead of the first one eating it
	for _, want := range []string{
		`{"segments":[{"start":<sec>,"end":<sec>,"speed":<rate, only on a segment that runs at that rate from end to end>}],"fx":[`, // the reply suggestParse reads
		"target length",             // the length the run checks
		"is not a showcase segment", // the subject out of frame is not one
		"The whole of it",           // the pass that shows its size and shape
		"Count the things.",         // several subjects is the same job repeated
		"divide the target length",
	} {
		if !strings.Contains(strings.TrimSpace(sysSystem)+"\n\n"+showcaseSystem, want) {
			t.Errorf("the Showcase wording never says %q", want)
		}
	}
	// it is a shape, not a genre: nothing in it assumes a game
	if low := strings.ToLower(showcaseSystem); strings.Contains(low, "gaming session") {
		t.Error("the Showcase wording assumes a gaming session; a painted figure on a " +
			"table is the case it exists for")
	}
}
