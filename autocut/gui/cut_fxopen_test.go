package main

// A double click on an effect opens its numbers -- the dialog ✎ Edit opens.
// The single press is already taken (hold, slide, resize, all the drag
// gesture's), so the second click was free, and "click it again to open it"
// is what every file manager has taught the hand to expect. The wiring is a
// closure over GTK gestures, so what is pinned is the source: the fx-lane
// branch inside the double-click handler, holding the marker under the
// pointer and opening editFx on an idle rather than in the middle of the
// press it answers.

import (
	"os"
	"strings"
	"testing"
)

func TestADoubleClickOpensTheEffect(t *testing.T) {
	b, err := os.ReadFile("cut.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{
		// the branch lives in the double-click handler, ahead of the
		// pics-band guard, and asks the lane which marker is under the press
		"if area == ed.srcArea && ed.fxHitLane(y) {",
		// the dialog opens on an idle: opened inline, the release would
		// never reach the drag gesture underneath and the press would stick
		"glib.IdleAdd(func() { ed.a.editFx() })",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("the double-click handler no longer contains %q", want)
		}
	}
}
