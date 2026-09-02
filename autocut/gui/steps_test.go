package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The tab row and the gates are two lists that have to agree. When they drift,
// a finished step is barred or an unfinished one opens onto an empty page, and
// both read as a bug in the step rather than in the table -- which is where the
// renumbering that comes with every merged page actually breaks things.
//
// Since Publish folded into Produce each flag gates exactly one tab again,
// and produceLocked waits on the cut and nothing else -- not on the rendered
// video, which its own ▶ is what makes.
func TestEachGateLocksExactlyItsOwnTab(t *testing.T) {
	for _, c := range []struct {
		pages []string
		set   func(*App)
	}{
		{[]string{"cut"}, func(a *App) { a.cutLocked = true }},
		{[]string{"narrate"}, func(a *App) { a.narrateLocked = true }},
		{[]string{"produce"}, func(a *App) { a.produceLocked = true }},
	} {
		a := &App{}
		c.set(a)
		gated := map[string]bool{}
		for _, p := range c.pages {
			gated[p] = true
		}
		for i, s := range steps {
			if got, want := a.stepLocked(i), gated[s.name]; got != want {
				t.Errorf("with only %v gated, tab %d (%s) locked = %v, want %v",
					c.pages, i, s.name, got, want)
			}
		}
	}

	// the first tab is where a locked tab bounces to, so it must never lock:
	// a locked landing page is a window with nowhere to go
	all := &App{cutLocked: true, narrateLocked: true, produceLocked: true}
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
	// publish.go is absent: since the merge it builds panes inside Produce's
	// page, and Produce's Inputs row is the one above them
	for _, f := range []string{"prep.go", "cut.go", "narrate.go", "produce.go"} {
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
		// The other half of the question -- what this step WROTE -- is not a
		// page row at all any more. Every step writes files, so the count and
		// the way into the folder ride the shared bottom bar and follow the
		// visible tab (outStack in main.go). A page owns only its group,
		// registered under its step name; a heading of its own would put a
		// second "Outputs:" on screen beside the global one.
		name := strings.TrimSuffix(f, ".go")
		if !strings.Contains(src, `a.outStack.AddNamed(outRow, "`+name+`")`) {
			t.Errorf("%s does not hand its Outputs group to the shared bar", f)
		}
		if strings.Contains(src, `gtk.NewLabel("Outputs:")`) {
			t.Errorf("%s grew its own Outputs heading back beside the global one", f)
		}
	}
	// ...and the bar itself: one heading, the pages' groups behind it, switched
	// with the tabs so ▶, the progress text and the outputs are always about
	// the same step. Non-homogeneous, or prep's three folders would reserve
	// their width under every tab.
	b, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	mainSrc := string(b)
	for _, want := range []string{
		`outLbl := gtk.NewLabel("Outputs:")`,
		`outLbl.AddCSSClass("heading")`,
		`ctlRow.Append(outLbl)`,
		`ctlRow.Append(a.outStack)`,
		`a.outStack.SetVisibleChildName(name)`,
		`a.outStack.SetHhomogeneous(false)`,
	} {
		if !strings.Contains(mainSrc, want) {
			t.Errorf("the shared Outputs group is missing %s", want)
		}
	}
}

// A screen capture with nobody talking over it has no words to fix and nothing
// to correct, so the describing half of Prepare is run for one reason
// only: it wrote the file the Cut tab was unlocked by. It is not what the Cut
// page reads. The tracks are built from the sources and the frames the first
// half pulled out of them, and the session timeline is text printed ON those
// tracks -- so no timeline is an empty track, and the page opens either way.
func TestCutOpensOnFramesSoASilentCaptureNeedNotBeDescribed(t *testing.T) {
	root := t.TempDir()
	vid := filepath.Join(root, "capture.mp4")
	if err := os.WriteFile(vid, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &App{root: root, outDir: root}
	a.selVid = []string{vid} // the snapshot a runner works from; no widgets here
	if a.canCut() {
		t.Error("the page opened with no frames behind it")
	}

	// Describe's own output does not stand in for them
	if err := os.MkdirAll(a.transcriptDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.transcriptDir(), "session.tsv"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if a.canCut() {
		t.Error("a session timeline unlocked the page with no footage to show")
	}

	fdir := a.framesDir("capture")
	if err := os.MkdirAll(fdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fdir, ".interval"), []byte("1|original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if a.canCut() {
		t.Error("the marker file in the frame folder counted as a frame")
	}
	if err := os.WriteFile(filepath.Join(fdir, "000010.jpg"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !a.canCut() {
		t.Error("frames are on disk and the Cut page is still locked")
	}
}

// The pages are named after what they do; the folders are named after what they
// were. Both halves matter, and they pull in opposite directions.
//
// The files and the identifiers were numbered -- a numbering nobody but the tab
// row knew, which had to be looked up every time and which lied twice already,
// once when Narrate became the fourth step and again when Inputs and Describe
// merged into one page. So the code is called Cut, Narrate, Produce and Publish
// now, wherever it can be.
//
// The folders on disk are too, since migrateFolders: a project written under
// step1/ to step6/ is moved to the named folders on the open that finds it,
// which is what makes the rename safe for somebody's finished work.
func TestThePagesAreNamedForWhatTheyDoAndTheFoldersForWhatTheyWere(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if regexp.MustCompile(`^step[0-9]`).MatchString(f) {
			t.Errorf("%s still carries a step number in its name", f)
		}
	}

	// one page file per tab, called what the tab is called -- publish.go
	// stays on disk as the thumbnail pane's source, but it is Produce's now
	for _, f := range []string{"prep.go", "cut.go", "narrate.go", "produce.go"} {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("%s is missing -- one file per page, named for the page", f)
		}
	}
	if len(steps) != 4 {
		t.Errorf("%d tabs against 4 page files", len(steps))
	}

	// ...and no identifier is numbered either. A page whose builder is called
	// after its tab number is a page whose name has to be looked up in this
	// very table.
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		// an identifier, not the word: stepN is still the name of a folder and
		// still the honest way to say "the fourth one" in a sentence
		for _, m := range regexp.MustCompile(
			`[a-z][A-Za-z0-9_]*Step[0-9][A-Za-z0-9_]*|\bstep[0-9][A-Za-z_][A-Za-z0-9_]*`).
			FindAllString(string(b), -1) {
			t.Errorf("%s still declares or names %s", f, m)
		}
	}

	// the folders, named for their steps, each reached through its one helper
	src := readSrc(t, "main.go")
	for _, want := range []string{
		`func (a *App) inputsDir() string     { return filepath.Join(a.outDir, "inputs") }`,
		`func (a *App) understandDir() string { return filepath.Join(a.outDir, "understand") }`,
		`func (a *App) narrateDir() string    { return filepath.Join(a.outDir, "narrate") }`,
		`func (a *App) produceDir() string    { return filepath.Join(a.outDir, "produce") }`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("a folder helper changed -- missing %s", want)
		}
	}
	if !strings.Contains(readSrc(t, "cut.go"), `filepath.Join(a.outDir, "cut")`) {
		t.Error("the cut folder is no longer cut/")
	}
	if !strings.Contains(readSrc(t, "publish.go"), `filepath.Join(a.outDir, "publish")`) {
		t.Error("the publish folder is no longer publish/")
	}
	// ...and nothing spells a folder out beside its helper: a literal is a path
	// the next rename misses
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "main.go" || f == "cut.go" || f == "publish.go" || f == "project.go" {
			continue
		}
		for _, lit := range []string{`"inputs"`, `"understand"`, `"narrate"`, `"produce"`, `"publish"`} {
			if strings.Contains(readSrc(t, f), "filepath.Join(a.outDir, "+lit) {
				t.Errorf("%s joins a.outDir with %s itself instead of through the helper", f, lit)
			}
		}
	}
}

// A project written under the numbered folders is moved to the named ones on
// open -- once, and never over a folder that already has the new name.
func TestANumberedProjectIsMovedToTheNamedFolders(t *testing.T) {
	a := &App{outDir: t.TempDir()}
	for _, old := range []string{"step1", "step3", "step6"} {
		if err := os.MkdirAll(filepath.Join(a.outDir, old), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(a.outDir, old, "x"), []byte(old), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// a folder already under its new name stays what it is
	if err := os.MkdirAll(filepath.Join(a.outDir, "publish"), 0o755); err != nil {
		t.Fatal(err)
	}
	a.migrateFolders()
	for _, c := range []struct{ dir, want string }{{"inputs", "step1"}, {"cut", "step3"}} {
		b, err := os.ReadFile(filepath.Join(a.outDir, c.dir, "x"))
		if err != nil || string(b) != c.want {
			t.Errorf("%s/x = %q, %v -- want the file moved from %s/", c.dir, b, err, c.want)
		}
	}
	for _, old := range []string{"step1", "step3"} {
		if _, err := os.Stat(filepath.Join(a.outDir, old)); !os.IsNotExist(err) {
			t.Errorf("%s/ is still there after the move", old)
		}
	}
	if _, err := os.Stat(filepath.Join(a.outDir, "step6", "x")); err != nil {
		t.Errorf("step6/ was moved over an existing publish/: %v", err)
	}
	if _, err := os.Stat(filepath.Join(a.outDir, "narrate")); !os.IsNotExist(err) {
		t.Error("a folder that never existed under the old name was created")
	}
	a.migrateFolders() // a second open changes nothing
	if b, _ := os.ReadFile(filepath.Join(a.outDir, "inputs", "x")); string(b) != "step1" {
		t.Error("the second migration disturbed the first")
	}
}
