package main

import (
	"os"
	"strings"
	"testing"
)

// The tab row and the gates are two lists that have to agree. When they drift,
// a finished step is barred or an unfinished one opens onto an empty page, and
// both read as a bug in the step rather than in the table -- which is where the
// renumbering that comes with every merged page actually breaks things.
func TestEachGateLocksExactlyItsOwnTab(t *testing.T) {
	for _, c := range []struct {
		page string
		set  func(*App)
	}{
		{"step2", func(a *App) { a.step2Locked = true }},
		{"step3", func(a *App) { a.step3Locked = true }},
		{"step4", func(a *App) { a.step4Locked = true }},
		{"step5", func(a *App) { a.step5Locked = true }},
	} {
		a := &App{}
		c.set(a)
		for i, s := range steps {
			if got, want := a.stepLocked(i), s.name == c.page; got != want {
				t.Errorf("with only %s gated, tab %d (%s) locked = %v, want %v",
					c.page, i, s.name, got, want)
			}
		}
	}

	// the first tab is where a locked tab bounces to, so it must never lock:
	// a locked landing page is a window with nowhere to go
	all := &App{step2Locked: true, step3Locked: true, step4Locked: true, step5Locked: true}
	if all.stepLocked(0) {
		t.Error("the first tab locked -- it is where a locked tab bounces to")
	}
	if all.stepLocked(stepIndex("no such page")) {
		t.Error("an unknown page counts as locked; refusing to show a page is the worse mistake")
	}
}

// Every tab says something on hover, and a locked one has to say what is
// missing -- the tooltip is the only place it can, since the tab stays
// clickable precisely so that it can be hovered. help is the same obligation
// one level down: the ⓘ popover is now the only place a step is explained at
// length, so a step added without one is a step nothing describes anywhere.
func TestEveryTabExplainsItself(t *testing.T) {
	for i, s := range steps {
		if stepIndex(s.name) != i {
			t.Errorf("stepIndex(%q) = %d, want %d", s.name, stepIndex(s.name), i)
		}
		if s.label == "" || s.tip == "" {
			t.Errorf("tab %d (%s): label %q, tip %q -- both are shown", i, s.name, s.label, s.tip)
		}
		if (s.wait == "") != (i == 0) {
			t.Errorf("tab %d (%s): wait hint %q, want one on every tab but the first",
				i, s.name, s.wait)
		}
		// long enough to be an explanation rather than the tooltip again: the
		// popover exists because the tooltip had no room, and one that only
		// repeats it is a button that answers nothing
		if len(s.help) < len(s.tip)+80 {
			t.Errorf("tab %d (%s): help is %d chars against a %d-char tip -- "+
				"the ⓘ popover is where the paragraph went, not a second tooltip",
				i, s.name, len(s.help), len(s.tip))
		}
	}
}

// Every page that reads something says so the same way. "What does this step
// get?" is asked once per tab and answered in the same place, in the same
// words, at the same indent -- Describe answered it with an unlabelled line of
// grey text set 4 px further in than the identical row on Cut and Narrate,
// which is enough to make a reader stop and check they are on the page they
// think they are. Source-level: nothing at run time can tell that three rows
// are meant to be one row.
func TestEveryStepSaysWhatItReadsTheSameWay(t *testing.T) {
	// the row, line for line, as all three build it
	same := []string{
		`inLbl := gtk.NewLabel("Inputs:")`,
		`inLbl.AddCSSClass("heading")`,
		`inRow := gtk.NewBox(gtk.OrientationHorizontal, 8)`,
		`inRow.SetMarginStart(12)`,
		`inRow.SetMarginEnd(12)`,
		`inRow.SetMarginTop(6)`,
		`inRow.Append(inLbl)`,
		`.SetEllipsize(pango.EllipsizeEnd)`, // never a floor under the window
	}
	for _, f := range []string{"step2.go", "step3.go", "step4.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		for _, want := range same {
			if !strings.Contains(src, want) {
				t.Errorf("%s's Inputs row is missing %s", f, want)
			}
		}
		// and it is the page's own text that is dimmed nowhere: the heading
		// carries the weight, the reading itself is plain, as on Inputs
		if strings.Contains(src, `inputs.AddCSSClass("dim-label")`) {
			t.Errorf("%s dims its Inputs line where the other pages do not", f)
		}
	}
}
