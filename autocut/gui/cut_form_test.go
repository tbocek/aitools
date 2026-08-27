package main

import (
	"strings"
	"testing"

	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// The three prompts the Cut page sends held the right-hand half of its top row,
// permanently, for every session -- three boxes of thirty lines each that a
// working cut never touches. They are one dropdown in the toolbar now, and the
// place to check is the toolbar: the menu has to be left of the view buttons,
// which is where the user asked for it and also the only place it can be. Past
// the rule are the controls that change what you SEE and never what is saved,
// and a prompt is the opposite of that -- it is what Suggest sends.
func TestTheCutPromptsAreOneMenuLeftOfTheViewButtons(t *testing.T) {
	src := readSrc(t, "cut.go")

	// the menu, in the order the run makes the calls: the rules, then the read
	// back of what they produced, then the decoration inside the result
	order := []string{
		`promptSlot{"cut", "Cut",`,
		`promptSlot{"audit", "Audit",`,
		`promptSlot{"effects", "Effects",`,
	}
	at := -1
	for _, want := range order {
		i := strings.Index(src, want)
		if i < 0 {
			t.Fatalf("cut.go does not offer %s in its prompt menu", want)
		}
		if i < at {
			t.Errorf("%s is out of order in the menu -- reading down it is reading the run", want)
		}
		at = i
	}
	if !strings.Contains(src, "promptRow := a.promptBar(ed,") {
		t.Error("the Cut page does not pass itself as the form host, so its prompts open in a window " +
			"over the timeline they are about")
	}

	// left of the − + pairs, and on the side of the rule where things change
	// the cut
	for _, after := range []string{
		"bar.Append(rule()) // past here nothing changes the cut",
		"viewRow.Append(linked(zoomOut, zoomIn))",
		"viewRow.Append(linked(thumbMinus, thumbPlus))",
	} {
		i, j := strings.Index(src, "bar.Append(promptRow)"), strings.Index(src, after)
		if i < 0 {
			t.Fatal("the prompt menu is not in the bar at all")
		}
		if j < 0 {
			t.Fatalf("cut.go no longer has %q", after)
		}
		if i > j {
			t.Errorf("the prompt menu is built after %q -- it belongs left of the view controls", after)
		}
	}

	// and the wall of boxes it replaced is gone rather than merely hidden: a
	// second way to reach the same prompt is a second place to fix when the
	// storage behind it changes
	for _, gone := range []string{`a.promptEditor("cut"`, `a.promptEditor("audit"`, "promptBox"} {
		if strings.Contains(src, gone) {
			t.Errorf("cut.go still builds %s beside the video", gone)
		}
	}
}

// The forms moved out of modal windows and into the column the prompts left
// behind. That is not a decoration: every question these two ask is a question
// about the footage -- how many seconds a card runs, how long a zoom holds --
// and a window over the page is exactly what stopped the lane the answer is
// drawn on from being looked at while it was being decided.
func TestTheCutFormsOpenBesideTheTimelineAndNotOverIt(t *testing.T) {
	for _, c := range []struct{ file, head, verb string }{
		{"cut.go", `func \(a \*App\) askInsertParams\(`, "form.showForm(verb+\" \"+filepath.Base(path), box, nil)"},
		{"cut_fx.go", `func \(a \*App\) fxWin\(`, "form.showForm(title, box, nil)"},
	} {
		body := funcBody(t, c.file, c.head)
		if strings.Contains(body, "gtk.NewWindow()") {
			t.Errorf("%s still opens a window over the page", c.head)
		}
		if !strings.Contains(body, c.verb) {
			t.Errorf("%s does not put its form in the column (%s)", c.head, c.verb)
		}
		// it has to be reachable from a page that exists: the column is the
		// page's, and there is no fallback window to fall back to
		if !strings.Contains(body, "form := a.cutForm()") || !strings.Contains(body, "if form == nil {") {
			t.Errorf("%s does not check that there is a column to open in", c.head)
		}
		// Cancel and the verb both take the form down -- a form that answers
		// and stays is a form that can be answered twice
		if n := strings.Count(body, "form.hideForm()"); n < 2 {
			t.Errorf("%s takes its form down %d times, want Cancel and the verb both", c.head, n)
		}
	}
}

// One column and two forms fighting over it is worse than either, so showing a
// form takes the last one out. The order that matters is inside dropForm: the
// widgets leave the box FIRST and their owner is told SECOND. Told first, the
// prompt registry lets go of a text view that is still on screen, and the next
// project load fills a box the dropdown no longer knows about.
func TestOneColumnMeansTheFormBeforeItIsToldItIsGone(t *testing.T) {
	body := funcBody(t, "cut_form.go", `func \(ed \*cutEditor\) dropForm\(`)
	rm, told := strings.Index(body, "ed.formBox.Remove(ed.formCur)"), strings.Index(body, "g()")
	if rm < 0 || told < 0 {
		t.Fatalf("dropForm no longer both empties the column and tells its owner:\n%s", body)
	}
	if rm > told {
		t.Error("dropForm tells the owner before taking the widgets out")
	}
	if !strings.Contains(body, "ed.formGone = nil") {
		t.Error("dropForm keeps the callback, so the next drop calls it a second time")
	}
	for _, fn := range []string{`func \(ed \*cutEditor\) showForm\(`, `func \(ed \*cutEditor\) hideForm\(`} {
		if !strings.Contains(funcBody(t, "cut_form.go", fn), "ed.dropForm()") {
			t.Errorf("%s leaves the last form in the column", fn)
		}
	}

	// what being told is FOR, which is testable without a display: the picker
	// drops the editor it had open, so showPromptStyle stops filling a box
	// nobody can see and the next editor for that key is the one on screen
	a := &App{
		promptViews: map[string]*gtk.TextView{"cut": nil, "audit": nil},
		promptRows:  map[string]promptRow{"cut": {}, "audit": {}},
	}
	p := &promptPicker{a: a, open: "cut"} // no dropdown: sync is a no-op here
	p.closed()
	if _, ok := a.promptViews["cut"]; ok {
		t.Error("the closed editor is still registered")
	}
	if _, ok := a.promptRows["cut"]; ok {
		t.Error("the closed editor's wording row is still registered")
	}
	if _, ok := a.promptViews["audit"]; !ok {
		t.Error("closing one editor forgot another one")
	}
	if p.open != "" {
		t.Errorf("the picker still thinks %q is open", p.open)
	}
	p.closed() // idempotent: the column is emptied by Close and by the next form
	if _, ok := a.promptViews["audit"]; !ok {
		t.Error("a second close forgot an editor that was never opened here")
	}
}

// Swapping one prompt form for another is the one order in this file that is
// load-bearing, and getting it wrong shows up nowhere.
//
// Both editors are registered under their prompt KEY (promptEditor writes
// a.promptViews[key]; forgetPromptEditor deletes it), the column holds one form,
// and putting the second one up takes the first one down. So if the picker
// records the incoming key before showForm runs, the outgoing form's `gone`
// fires with the wrong name in hand and forgets the editor that is now on
// screen. Nothing visibly happens: the box is drawn and filled, and it is only
// LATER -- when a different wording is picked in it, or a project is loaded --
// that showPromptStyle writes to a widget nobody is holding and the box on
// screen sits there unchanged.
//
// And re-opening the prompt already in the column is the same trap from the
// other side: the fresh editor takes the key, then the old one's closing takes
// it away again. There is nothing to rebuild, so it is not rebuilt.
func TestSwappingOnePromptFormForAnotherForgetsTheOutgoingOne(t *testing.T) {
	body := funcBody(t, "promptpick.go", `func \(p \*promptPicker\) openPicked\(`)
	iShow := strings.Index(body, "p.host.showForm(")
	iOpen := strings.Index(body, "p.open = s.key")
	if iShow < 0 || iOpen < 0 {
		t.Fatalf("openPicked no longer puts the form up and remembers it:\n%s", body)
	}
	if iOpen < iShow {
		t.Error("openPicked names the incoming prompt before the outgoing form is taken " +
			"down -- its `gone` would forget the editor that just went on screen")
	}
	if !strings.Contains(body, `if p.host != nil && p.open == s.key {`) {
		t.Errorf("openPicked rebuilds the prompt already in the column:\n%s", body)
	}
	// the build has to come after the early return, or the discarded editor has
	// already overwritten the registration of the one on screen
	if i := strings.Index(body, "p.a.promptEditor("); i >= 0 && i < strings.Index(body, "p.open == s.key") {
		t.Error("openPicked builds the editor before deciding whether it needs one")
	}
}

// An effect form is not a window. It opens in the page's column with the
// timeline live under it, which is the point -- the numbers being typed are
// seconds of a band drawn four inches below, and it should be possible to look
// at that band while typing them. What it also means is that the effect the
// form was opened FOR can stop being the effect the page is holding: click
// another band, or press Undo, and "the held one" is somebody else. Writing a
// zoom's numbers onto that caption is the one mistake here that cannot be seen
// happening, so the effect is found again by what it was.
func TestASavedFormLandsOnTheEffectItWasOpenedFor(t *testing.T) {
	root := t.TempDir()
	a := &App{root: root, outDir: root}
	ed := &cutEditor{a: a, pps: 4, thumbHt: 64}
	ed.a, a.ed = a, ed
	ed.fx = []cutFx{
		{Kind: "zoom", T: 10, Dur: 2, Hf: 0.5},
		{Kind: "text", T: 30, Dur: 3, Text: "hello"},
	}
	was := ed.fx[0]

	// the hold has moved to the caption while the zoom's form is open
	ed.fxOn, ed.fxSel = true, 1
	ed.updateFx(was, cutFx{Kind: "zoom", T: 10, Dur: 5, Hf: 0.25})
	if got := ed.fx[0].Dur; got != 5 {
		t.Errorf("the zoom kept its old length %g -- the form wrote somewhere else", got)
	}
	if ed.fx[1].Text != "hello" || ed.fx[1].Kind != "text" {
		t.Errorf("the caption was overwritten by the zoom's form: %+v", ed.fx[1])
	}

	// the same lookup after a renumbering: the effect ahead of it is gone, so
	// the index the form opened at is now a different effect
	ed.fx = []cutFx{{Kind: "text", T: 30, Dur: 3, Text: "hello"}, {Kind: "zoom", T: 10, Dur: 5}}
	if got := ed.indexOfFx(was); got != 1 {
		t.Errorf("the zoom was found at %d after the list was reordered, want 1", got)
	}

	// and an effect that has been deleted under the form is not resurrected as
	// a new one, nor written onto whatever took its place
	ed.fx = []cutFx{{Kind: "text", T: 30, Dur: 3, Text: "hello"}}
	before := len(ed.undo)
	ed.updateFx(was, cutFx{Kind: "zoom", T: 10, Dur: 9})
	if len(ed.fx) != 1 || ed.fx[0].Kind != "text" {
		t.Errorf("saving a deleted effect changed the cut: %+v", ed.fx)
	}
	if len(ed.undo) != before {
		t.Error("a save that changed nothing still pushed an undo step")
	}
}
