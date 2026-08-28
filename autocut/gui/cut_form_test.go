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
		{"cut.go", `func \(a \*App\) askInsertParams\(`, "form.showFormFoot(verb+\" \"+filepath.Base(path), box, btns, nil)"},
		{"cut_fx.go", `func \(a \*App\) fxWin\(`, "form.showFormFoot(title, box, btns, nil)"},
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
	for _, fn := range []string{`func \(ed \*cutEditor\) showFormFoot\(`, `func \(ed \*cutEditor\) hideForm\(`} {
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

// The column is as tall as the video beside it and no taller, and a five-row
// effect form is taller than that. So the form scrolls -- and for a while its
// Place button scrolled with it, off the bottom, where it was not found: the
// numbers got typed, nothing appeared to happen, and every effect in the
// dropdown read as broken. The heading and the buttons are pinned outside the
// scroller now, and only the questions between them move.
func TestTheFormsButtonsDoNotScrollAwayFromIt(t *testing.T) {
	build := funcBody(t, "cut_form.go", `func \(ed \*cutEditor\) buildForm\(`)
	// the scroller is around the questions alone, and the three pieces are
	// stacked outside it
	if !strings.Contains(build, "pane.SetChild(ed.formBox)") {
		t.Error("the column no longer scrolls the questions")
	}
	for _, want := range []string{"col.Append(ed.formHead)", "col.Append(pane)", "col.Append(ed.formFoot)"} {
		if !strings.Contains(build, want) {
			t.Errorf("buildForm does not %s -- that piece scrolls with the form", want)
		}
	}
	for _, gone := range []string{"ed.formBox.Append(ed.formHead)", "ed.formBox.Append(ed.formFoot)"} {
		if strings.Contains(build, gone) {
			t.Errorf("buildForm still does %s, so it scrolls away with the questions", gone)
		}
	}

	// and what is pinned is taken down with the form it belongs to: the foot
	// holds the LAST form's buttons otherwise, under the new one's questions
	drop := funcBody(t, "cut_form.go", `func \(ed \*cutEditor\) dropForm\(`)
	if !strings.Contains(drop, "ed.formFoot.Remove(ed.formFootCur)") {
		t.Error("dropForm leaves the old form's buttons pinned under the next one")
	}
	if !strings.Contains(drop, "ed.formFootCur = nil") {
		t.Error("dropForm keeps the removed buttons, so the next drop removes them twice")
	}

	// the two dialogs that were too tall for the column hand their buttons to
	// the foot rather than appending them to the form
	for _, c := range []struct{ file, head string }{
		{"cut.go", `func \(a \*App\) askInsertParams\(`},
		{"cut_fx.go", `func \(a \*App\) fxWin\(`},
	} {
		body := funcBody(t, c.file, c.head)
		if strings.Contains(body, "box.Append(btns)") {
			t.Errorf("%s puts its buttons in the scrolling part of the column", c.head)
		}
		if !strings.Contains(body, "btns, nil)") {
			t.Errorf("%s does not pin its buttons under the column", c.head)
		}
	}
}

// An effect form is four numbers and a button. It used to open with a
// paragraph explaining the effect above them -- five lines of prose, read once,
// re-read never, and pushing the numbers themselves down the panel. The words
// are on the fields as tooltips, where they are asked for; the form is the
// numbers, in two columns, with what the effect IS on the left and how it
// arrives and leaves on the right.
func TestAnEffectFormIsItsNumbersAndNothingElse(t *testing.T) {
	fx, svg := readSrc(t, "cut_fx.go"), readSrc(t, "fxsvg.go")
	if !strings.Contains(fx, "func (a *App) fxWin(title, verb string,") {
		t.Error("fxWin still takes a paragraph to print above the questions")
	}
	// the paragraphs themselves, by their opening words: gone from the source,
	// not merely unused by it
	for _, gone := range []string{
		"Those seconds play at the rate below",
		"The picture closes in on the framed region",
		"The words are drawn over the finished video",
		"The drawing is laid over the finished video",
	} {
		if strings.Contains(fx, gone) || strings.Contains(svg, gone) {
			t.Errorf("an effect dialog still opens with %q", gone)
		}
	}

	// two halves, and the same two everywhere: the length of the thing on the
	// left, its fades on the right. A speed puts its rate above the length,
	// because the rate is what makes it a speed
	for _, want := range []string{
		`fxGrid([]fxField{rRow, lRow}, []fxField{iRow, oRow, eRow})`, // speed
		`fxGrid([]fxField{dRow}, []fxField{gRow, oRow, eRow})`,       // zoom
		`fxGrid([]fxField{dRow}, []fxField{iRow, oRow, eRow})`,       // text, and the same for the svg
	} {
		if !strings.Contains(fx, want) {
			t.Errorf("cut_fx.go no longer lays a dialog out as %s", want)
		}
	}
	if !strings.Contains(svg, `fxGrid([]fxField{dRow}, []fxField{iRow, oRow, eRow})`) {
		t.Error("the svg dialog stacks its numbers in one column")
	}
	// stacked rows are what made the form too tall, so no dialog keeps a
	// hand-built row of times beside the columns
	for _, gone := range []string{"times.Append(dRow)", "times.Append(iRow)"} {
		if strings.Contains(fx, gone) || strings.Contains(svg, gone) {
			t.Errorf("a dialog still builds its own %s instead of using fxGrid", gone)
		}
	}
}

// The two halves are laid out by a grid, and the reason is what the screenshot
// of the box version showed: the labels were right-aligned and given the
// slack, so each entry floated in the middle of its own half with a hand's
// width of nothing on either side of it. A grid sizes the label column to the
// longest label and hands the slack to the controls instead.
func TestTheFormsTwoHalvesAreAGrid(t *testing.T) {
	fx := readSrc(t, "cut_fx.go")
	for _, want := range []string{
		"func fxGrid(left, right []fxField) *gtk.Grid",
		"g.Attach(f.lbl, c*2, r, 1, 1)",
		"g.Attach(f.ctl, c*2+1, r, 1, 1)",
		"f.lbl.SetMarginStart(18)", // the gutter down the middle
		"e.SetHExpand(true)",       // ... and the slack, to the entry
	} {
		if !strings.Contains(fx, want) {
			t.Errorf("the form's halves are no longer a grid: %q is gone", want)
		}
	}
	// the label is the grid's to place, so it cannot be sealed inside a row
	// box the grid cannot see into
	if strings.Contains(fx, "row := gtk.NewBox(gtk.OrientationHorizontal, 6)\n\trow.Append(l)") {
		t.Error("fxNumRow builds its own row box again, which the grid cannot line up")
	}
	if !strings.Contains(fx, "l.SetXAlign(0)") || strings.Contains(fx, "l.SetHExpand(true) //") {
		t.Error("the labels are right-aligned and eating the slack again")
	}
	// and greying a question out greys the label with it
	body := funcBody(t, "cut_fx.go", `func \(f fxField\) setSensitive\(on bool\) \{`)
	if !strings.Contains(body, "f.lbl.SetSensitive(on)") ||
		!strings.Contains(body, "gtk.BaseWidget(f.ctl).SetSensitive(on)") {
		t.Errorf("half a question greys out and half stays live:\n%s", body)
	}
	if !strings.Contains(fx, "oRow.setSensitive(!stay.Active())") {
		t.Error("the staying zoom no longer greys its fade out row")
	}
	// and the shape row goes quiet with the two fades it is about
	if n := strings.Count(fx, "etSensitive(!(stay.Active() && first))"); n != 2 {
		t.Errorf("%d rows go quiet on the zoom that has no fades at all, want 2", n)
	}
}

// The rate is picked off a list now, with a box for the rates that are not on
// it. Typing a number was the whole control before, which asks you to know
// that 0 is a stop, that 0.5 is half speed and that anything at all is legal --
// three facts that were only ever written in a tooltip.
func TestTheSpeedIsPickedOffAList(t *testing.T) {
	fx := readSrc(t, "cut_fx.go")
	// the ends of the range are why it is a list AND a box: a slow rate is one
	// of a handful, a fast one is anything up to a hundred
	for _, want := range []string{"×0.25", "×0.5", "×1", "×2", "×4", "×20", "×100"} {
		if !strings.Contains(fx, "fxRates = []float64{0, 0.25, 0.5, 0.75, 1, 1.5, 2, 4, 8, 20, 100}") {
			t.Fatalf("the common rates are no longer a list")
		}
		_ = want
	}
	for _, want := range []string{`s += " — stop"`, `s += " — as filmed"`, `"Custom…"`} {
		if !strings.Contains(fx, want) {
			t.Errorf("the rate list no longer says %s", want)
		}
	}
	// a rate that is not on the list opens on Custom with its own number in
	// the box, rather than silently becoming the nearest listed one
	for _, tc := range []struct {
		v    float64
		want uint
	}{{0, 0}, {0.5, 2}, {1, 4}, {100, 10}, {3, 11}, {0.6, 11}, {-1, 11}} {
		if got := fxRateIndex(tc.v); got != tc.want {
			t.Errorf("fxRateIndex(%v) = %d, want %d", tc.v, got, tc.want)
		}
	}
	body := funcBody(t, "cut_fx.go", `func \(p \*ratePick\) rate\(def float64\) float64 \{`)
	if !strings.Contains(body, "return fxRates[i]") || !strings.Contains(body, "fxNumOf(p.e, def)") {
		t.Errorf("the pick no longer reads back both ways:\n%s", body)
	}
	// the box is only there for Custom, so there are never two answers to the
	// one question sitting side by side disagreeing
	if !strings.Contains(fx, "p.e.SetVisible(p.custom())") {
		t.Error("the typed rate box is shown beside every pick")
	}
	if !strings.Contains(fx, "p.e.SetText(fxNum(fxRates[i]))") {
		t.Error("switching to Custom no longer starts from the rate on screen")
	}
}

// Both fades travel in a shape, and the form says which. There is one shape --
// straight -- and asking anyway is the point: the alternative is a form that
// answers the question on your behalf and never mentions it.
func TestTheFadeShapeIsAskedFor(t *testing.T) {
	fx, svg := readSrc(t, "cut_fx.go"), readSrc(t, "fxsvg.go")
	if !strings.Contains(fx, `fxEases = []string{"Linear"}`) {
		t.Error("the fade shapes are no longer a list of what is actually drawn")
	}
	if !strings.Contains(fx, `fxRowLabel("Fade curve"`) {
		t.Error("the fade shape row is gone")
	}
	// all four dialogs ask it, and all four store the answer
	if n := strings.Count(fx, "eRow, ec := fxEaseRow(f)") + strings.Count(svg, "eRow, ec := fxEaseRow(f)"); n != 4 {
		t.Errorf("%d dialogs ask for the fade shape, want all 4", n)
	}
	if n := strings.Count(fx, "f.Ease = fxEaseOf(ec.Selected())") +
		strings.Count(svg, "f.Ease = fxEaseOf(ec.Selected())"); n != 4 {
		t.Errorf("%d dialogs keep the fade shape they were given, want all 4", n)
	}
	// straight stores as nothing, so a cut saved with the row on disk is the
	// cut that was saved before the row existed
	if got := fxEaseOf(0); got != "" {
		t.Errorf("fxEaseOf(0) = %q, want \"\" — every effect now writes a fade shape into cut.json", got)
	}
	if got := fxEaseOf(uint(len(fxEases))); got != "" {
		t.Errorf("fxEaseOf(off the end) = %q, want the straight ramp", got)
	}
	// and a shape from a version that knows one this one does not reads back
	// as the ramp rather than as nothing at all
	for _, tc := range []struct {
		in   string
		want uint
	}{{"", 0}, {"linear", 0}, {"Linear", 0}, {"smoothstep", 0}} {
		if got := fxEaseIndex(tc.in); got != tc.want {
			t.Errorf("fxEaseIndex(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
	if !strings.Contains(readSrc(t, "cut_fx.go"), "Ease string `json:\"ease,omitempty\"`") {
		t.Error("the fade shape is not kept on the effect")
	}
}
