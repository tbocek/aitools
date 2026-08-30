package main

import (
	"strings"
	"testing"
)

// The three prompts the Cut page sends held the right-hand half of its top row,
// permanently, for every session -- three boxes of thirty lines each that a
// working cut never touches. They became a dropdown in the toolbar, and then
// they left the page: every prompt in the app is one box on Prepare now
// (prepedit.go). A prompt is written once, before the first run, and this bar
// is where the session's actual work happens.
//
// What stayed is the STYLE -- highlights, a rating, a Short -- because that is
// not a prompt, it is a choice made before every suggest run, and it belongs
// next to the button it changes.
func TestTheCutPageSendsPromptsItDoesNotEdit(t *testing.T) {
	src := readSrc(t, "cut.go")

	// no editor, no dropdown, no Edit button: a second way to reach the same
	// prompt is a second place to fix when the storage behind it changes
	for _, gone := range []string{
		`a.promptEditor("cut"`, `a.promptEditor("audit"`, "promptBox",
		"a.promptBar(", "promptSlot{", "bar.Append(promptRow)",
	} {
		if strings.Contains(src, gone) {
			t.Errorf("cut.go still builds %s beside the video -- the prompts are on Prepare", gone)
		}
	}

	// and the page says where they went, because the next person to look for
	// them will look here first
	if !strings.Contains(src, "prepedit.go") {
		t.Error("cut.go drops the prompts without pointing at where they live now")
	}

	// the style bar is on the side of the rule where things change the cut
	i, j := strings.Index(src, `a.styleBar("cut", "Style"`), strings.Index(src, "bar.Append(rule()) // past here nothing changes the cut")
	if i < 0 {
		t.Fatal("the Cut page no longer offers the style choice on its bar")
	}
	if j < 0 {
		t.Fatal("cut.go no longer marks where the bar stops changing the cut")
	}
	if i > j {
		t.Error("the style menu is built past the rule, among the controls that only change what you see")
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
	// and only that row: the fade IN row stays live for every zoom, because
	// no framing reaches back before its own T any more and so every one of
	// them has a picture to travel from
	if strings.Contains(fx, "stay.Active() && first") {
		t.Error("the zoom dialog still greys a fade in for the cut's earliest framing")
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
	// all five dialogs ask it, and all five store the answer
	if n := strings.Count(fx, "eRow, ec := fxEaseRow(f)") + strings.Count(svg, "eRow, ec := fxEaseRow(f)"); n != 5 {
		t.Errorf("%d dialogs ask for the fade shape, want all 5", n)
	}
	if n := strings.Count(fx, "f.Ease = fxEaseOf(ec.Selected())") +
		strings.Count(svg, "f.Ease = fxEaseOf(ec.Selected())"); n != 5 {
		t.Errorf("%d dialogs keep the fade shape they were given, want all 5", n)
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
