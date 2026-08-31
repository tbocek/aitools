package main

// The prompt registry without a display. What matters here is not the wording
// but the bookkeeping around it: which prompt a step runner gets, what a
// project stores, and what loading a project puts back.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestPromptFallsBackToBuiltIn: every runner asks by key, and a key that was
// never edited -- or was blanked -- must still produce a real prompt. An empty
// system message would not fail loudly; it would quietly produce worse output.
func TestPromptFallsBackToBuiltIn(t *testing.T) {
	ownConfig(t)
	a := &App{}
	for _, d := range promptDefs {
		if got := a.prompt(d.key); got != d.def {
			t.Errorf("prompt(%q) with nothing set = %q, want the built-in", d.key, short(got))
		}
		if d.def == "" {
			t.Errorf("prompt %q has no built-in text", d.key)
		}
	}
	a.setPrompt("cut", "   \n ") // an emptied box is not an instruction
	if got := a.prompt("cut"); got != promptDefFor("cut").def {
		t.Errorf("a blanked prompt returned %q, want the built-in", short(got))
	}
	a.setPrompt("cut", "Pick five clips, all of them boring.")
	if got := a.prompt("cut"); !strings.HasPrefix(got, "Pick five") {
		t.Errorf("an edited prompt did not reach the runner: %q", short(got))
	}
	// Keys are the project-file names: renaming one drops what a user wrote.
	// "align" is deliberately not here -- the content-based aligner it belonged
	// to was never wired to anything and was deleted, so an old project's
	// prompts.align is ignored on load and dropped on the next save.
	for _, want := range []string{"describe", "fix", "cut", "narrate"} {
		if promptDefFor(want).def == "" {
			t.Errorf("prompt key %q is gone -- projects that edited it lose their text", want)
		}
	}
}

// TestEveryPromptIsExposed guards the point of the registry: a prompt the tool
// sends but does not show is one the user cannot fix, and the way that happens
// is someone adding a `const fooSystem` and stopping there. Source-level on
// purpose -- there is nothing at run time that knows about a prompt nobody
// registered.
func TestEveryPromptIsExposed(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	declared := map[string]bool{}
	re := regexp.MustCompile(`(?m)^const (\w+)System = `)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			declared[m[1]] = true
		}
	}
	if len(declared) == 0 {
		t.Fatal("found no system prompts at all -- the pattern went stale, not the code")
	}
	// counted across the wordings, not the jobs: a job can ship several, so what
	// has to match is how many prompts exist against how many the dropdowns offer
	exposed := 0
	for _, d := range promptDefs {
		exposed += len(d.builtins())
	}
	if len(declared) != exposed {
		t.Errorf("%d system prompts declared %v, but %d are exposed in promptDefs -- "+
			"a new one needs a promptDef (or an alts entry) and an editor on its step",
			len(declared), keysOf(declared), exposed)
	}
}

// TestEveryTextBoxIsTheSameBox guards the shape, not the wording. The prompts
// and the session context are the same object to a reader -- a heading and a
// box of text, side by side on the Describe page -- and they were built by two
// functions that drifted: one box had a frame, a floor and a natural height,
// the other had a frame and nothing else, and one heading row carried a button
// while the other carried a label, so the two boxes started a button's height
// apart. Source-level, because nothing at run time notices a page looking
// wrong; the only thing that does is the person using it.
func TestEveryTextBoxIsTheSameBox(t *testing.T) {
	src := map[string]string{}
	for _, f := range []string{"prompts.go", "prepedit.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src[f] = string(b)
	}

	// editorFrame is the box itself and editorBody is that box plus the size
	// group. They are two functions because the prompt editor is opened from a
	// dropdown now (promptpick.go) and a window that outlives the page must not
	// be joined to a size group the page owns -- so the shared shape has to be
	// reachable without it.
	frame := regexp.MustCompile(`(?s)func editorFrame\(.*?\n}\n`).FindString(src["prompts.go"])
	if frame == "" {
		t.Fatal("editorFrame is gone -- the two boxes are being built separately again")
	}
	for what, must := range map[string]string{
		"a border":                     `AddCSSClass("frame")`,
		"the whole text asked for":     "SetPropagateNaturalHeight(true)",
		"a four-line floor":            "SetSizeRequest(-1, 72)",
		"a taller window a taller box": "SetVExpand(true)",
	} {
		if !strings.Contains(frame, must) {
			t.Errorf("the shared box no longer gives %s (%s):\n%s", what, must, frame)
		}
	}
	body := regexp.MustCompile(`(?s)func \(a \*App\) editorBody\(.*?\n}\n`).FindString(src["prompts.go"])
	if !strings.Contains(body, "headGroup") || !strings.Contains(body, "editorFrame(head, tv)") {
		t.Errorf("editorBody no longer lines the heading rows up over the shared box:\n%s", body)
	}

	// ...and the one box that shows every prompt still comes out of it, rather
	// than growing its own scroller alongside it. It takes the size-grouped
	// form, because it is a box on a step page beside other boxes on that page
	// and their heading rows have to line up.
	f := regexp.MustCompile(`(?s)func \(a \*App\) prepEditor\(.*?\n}\n`).FindString(src["prepedit.go"])
	if f == "" {
		t.Fatal("prepEditor is gone")
	}
	if !strings.Contains(f, "a.editorBody(head, tv)") {
		t.Error("prepEditor builds its own box instead of the shared one")
	}
	if strings.Contains(f, "NewScrolledWindow()") {
		t.Error("prepEditor grew a second scroller: whichever of the two is edited next, " +
			"the pair stops matching")
	}
}

// TestThePromptColumnsKeepTheirSliderOffTheBoxes: the Cut page puts its column
// in a viewport of its own, so the column has a scrollbar that belongs to no
// box. Left as an overlay it is drawn on top of the framed box under it -- a
// slider with no border of its own, sitting on somebody else's. Narrate was the
// other one; its prompt is behind the dropdown now and its column went with it.
// The Cut column outlived the prompts that named it: it is where every form on
// the page opens (cut_form.go), and a form is framed boxes the same way.
func TestThePromptColumnsKeepTheirSliderOffTheBoxes(t *testing.T) {
	for _, c := range []struct{ file, viewport string }{
		{"cut_form.go", "pane"},
	} {
		b, err := os.ReadFile(c.file)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), c.viewport+".SetOverlayScrolling(false)") {
			t.Errorf("%s's form column (%s) scrolls over its own boxes", c.file, c.viewport)
		}
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// held is what the store is holding of its own -- the wordings that will be
// written to ~/.config/autocut/prompts on the next flush, and nothing else.
// Nil when there is nothing, which is the state an untouched machine is in.
func held(a *App) map[string][]promptStyle {
	a.promptMu.Lock()
	defer a.promptMu.Unlock()
	out := map[string][]promptStyle{}
	for k, list := range a.promptSty {
		if len(list) > 0 {
			out[k] = append([]promptStyle(nil), list...)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// TestOnlyEditsAreStored is the reason a machine can pick up a better shipped
// prompt: storing them all verbatim would freeze today's wording forever.
func TestOnlyEditsAreStored(t *testing.T) {
	ownConfig(t)
	a := &App{}
	if got := held(a); got != nil {
		t.Errorf("an untouched app wants to store %v, want nothing", got)
	}
	// what the editor does on build: fill the box with the default
	for _, d := range promptDefs {
		a.setPrompt(d.key, d.def)
	}
	if got := held(a); got != nil {
		t.Errorf("boxes holding the defaults want to store %v, want nothing", got)
	}

	a.setPrompt("narrate", "Say nothing at all.")
	got := held(a)
	if len(got) != 1 || len(got["narrate"]) != 1 || got["narrate"][0].Text != "Say nothing at all." {
		t.Fatalf("the store holds %v, want only the edited one", got)
	}
	if n := got["narrate"][0].Name; n != defStyle {
		t.Errorf("the edit was stored under %q, want the picked style %q", n, defStyle)
	}
	// and the project file is out of it entirely: the prompts are the
	// machine's, and a project that carried a copy would put it back on every
	// other machine the folder is opened on
	if body := funcBody(t, "project.go", `func \(a \*App\) currentProject\(\)`); strings.Contains(body, "Prompt") {
		t.Error("a saved project still carries the prompts, which are the machine's now")
	}
	if b, _ := json.Marshal(Project{}); strings.Contains(string(b), "prompt") {
		t.Errorf("an untouched project mentions prompts: %s", b)
	}
	// typing the built-in wording back is how an edit is undone -- storing a
	// copy of it instead would freeze it exactly as storing them all would
	a.setPrompt("narrate", promptDefFor("narrate").def)
	if got := held(a); got != nil {
		t.Errorf("an edit typed back to the built-in still wants to store %v", got)
	}
}

// Opening a project must not rewrite the prompts. They are the machine's now,
// and the wording somebody tuned over four videos has to survive a five-minute
// look at an old session -- which is a project file still carrying the copy it
// saved back when the prompts were the project's.
//
// It is adopted where there is nothing to lose, and only there: that is the
// migration, and it is what makes the first launch after the move keep what
// the last project had.
func TestAProjectDoesNotOverwriteThisMachinesWording(t *testing.T) {
	ownConfig(t)
	a := &App{}
	a.setPrompt("cut", "what this machine was tuned to")

	a.applyPromptStyles(map[string][]promptStyle{
		"cut":     {{defStyle, "an old project's idea of a highlight"}},
		"narrate": {{defStyle, "and its idea of a voice"}}}, nil, nil)

	if got := a.prompt("cut"); got != "what this machine was tuned to" {
		t.Errorf("opening a project replaced the machine's wording with %q", short(got))
	}
	// nothing of ours to lose on narrate, so the project's is taken in --
	// which is how a pre-merge project's prompts reach the new store at all
	if got := a.prompt("narrate"); got != "and its idea of a voice" {
		t.Errorf("narrate = %q, want the old project's, adopted", short(got))
	}
	// and having adopted it, it is ours: the next project does not get it back
	a.applyPromptStyles(map[string][]promptStyle{
		"narrate": {{defStyle, "a second project's voice"}}}, nil, nil)
	if got := a.prompt("narrate"); got != "and its idea of a voice" {
		t.Errorf("a second project overwrote the adopted wording with %q", short(got))
	}
	// a project that says nothing changes nothing, which is every project
	// saved from this build on
	a.applyPromptStyles(nil, nil, nil)
	if got := a.prompt("cut"); got != "what this machine was tuned to" {
		t.Errorf("a prompt-less project cleared the machine's wording: %q", short(got))
	}
}

// A project written before a job could have more than one wording stored one
// string per key and no name for it. That is the default style's text -- the
// shipped wording of a job did not move, it only gained company -- and dropping
// it on load would silently hand the model the built-in instead of what the
// user wrote.
func TestALegacyPromptLoadsAsAnEditOfTheDefaultStyle(t *testing.T) {
	ownConfig(t)
	a := &App{}
	a.applyPromptStyles(nil, nil, map[string]string{"cut": "the old project's idea of a cut"})

	if got := a.prompt("cut"); got != "the old project's idea of a cut" {
		t.Errorf("cut = %q, want what the pre-styles project stored", short(got))
	}
	if got := a.promptPickName("cut"); got != promptDefFor("cut").styleName() {
		t.Errorf("it landed on style %q, want the default %q", got, promptDefFor("cut").styleName())
	}
	// and it is stored the new way, so the legacy key can go
	got := held(a)
	if len(got["cut"]) != 1 || got["cut"][0].Text != "the old project's idea of a cut" {
		t.Errorf("the migrated prompt is not stored as a style: %v", got)
	}
	// switching away and back must still find it: the migration put it in the
	// style list, not just in the box
	a.showPromptStyle("cut", "Rating / tier list")
	if got := a.prompt("cut"); got != strings.TrimSpace(ratingSystem) {
		t.Errorf("picking the other wording gave %q", short(got))
	}
	a.showPromptStyle("cut", promptDefFor("cut").styleName())
	if got := a.prompt("cut"); got != "the old project's idea of a cut" {
		t.Errorf("switching back lost the edit: %q", short(got))
	}
}

// The cut is the one job that ships more than one wording, because what makes a
// good segment is a property of the footage and not of this tool: a generic
// one, and three shapes to pick once you know which one you have.
func TestTheCutShipsMoreThanOneWording(t *testing.T) {
	got := promptDefFor("cut").builtins()
	if len(got) < 2 {
		t.Fatalf("the cut ships %d wording(s), want the generic one and the shapes", len(got))
	}
	if got[0].Text != promptDefFor("cut").def {
		t.Error("the default is not first -- a project that never picked would open on the wrong one")
	}
	seen := map[string]bool{}
	for _, s := range got {
		if s.Name == "" {
			t.Error("a wording with no name can never be picked back")
		}
		if seen[s.Name] {
			t.Errorf("two wordings both called %q", s.Name)
		}
		seen[s.Name] = true
	}
	// the rating cut exists to cover every item and to land the ranking whole;
	// a wording that forgot either is the bug it was written to fix
	rating := strings.ToLower(strings.TrimSpace(ratingSystem))
	for _, want := range []string{"chronological", "ranking", "every item"} {
		if !strings.Contains(rating, want) {
			t.Errorf("the rating cut never mentions %q", want)
		}
	}
}

// The wording a project gets before anyone chooses one has to be the wording
// that assumes least. Highlights was the default and says "gaming session" in
// its first sentence: point it at a woodworking video and it goes looking for
// wins and disasters, and it finds some, and they are the wrong sixty seconds.
// A shape is worth picking once you know you have one -- it is not worth
// guessing on a project's first run.
func TestTheDefaultCutWordingDoesNotGuessWhatTheFootageIs(t *testing.T) {
	d := promptDefFor("cut")
	def := strings.TrimSpace(d.def)
	if def != strings.TrimSpace(genericSystem) {
		t.Fatalf("the cut ships %q as its default, want the generic wording", d.styleName())
	}

	// no genre in it, and none of the three shaped wordings' vocabulary either
	low := strings.ToLower(def)
	for _, guess := range []string{"gaming", "game session", "tier list", "ranking", "short"} {
		if strings.Contains(low, guess) {
			t.Errorf("the default cut wording says %q -- it is a shape, and the "+
				"default is the one that has not decided on a shape yet", guess)
		}
	}
	// it asks what the session is instead of assuming, and the notes are where
	// the answer comes from when there is one
	for _, want := range []string{"about this session", "work it out"} {
		if !strings.Contains(low, want) {
			t.Errorf("the default cut wording never says %q -- with no genre to fall "+
				"back on, reading the session first is the whole method", want)
		}
	}

	// the shaped wordings stay reachable and stay themselves: this adds a
	// wording, it does not replace one
	names := map[string]bool{}
	for _, st := range d.builtins() {
		names[st.Name] = true
	}
	for _, want := range []string{"Highlights", "Rating / tier list", shortsStyleName} {
		if !names[want] {
			t.Errorf("the %q wording is gone -- moving the default is not the same as "+
				"dropping the one it replaced", want)
		}
	}
	// and Highlights is still the gaming one, so a project that had picked it
	// gets what it had
	if !strings.Contains(strings.ToLower(suggestSystem), "gaming session") {
		t.Error("the Highlights wording is no longer the gaming one; a project that " +
			"picked it by name would silently get something else")
	}

	// the contract every cut wording shares, because the parser and the audit
	// read the same reply whichever one wrote it
	for _, want := range []string{
		`{"segments":[{"start":<sec>,"end":<sec>}],"fx":[`, // what suggestParse reads
		"session seconds", // on which clock
		"target length",   // the length the run checks
		"EVENT lines",     // a span without them has no footage
	} {
		if !strings.Contains(def, want) {
			t.Errorf("the default cut wording never says %q -- the reply is read by the "+
				"same code whichever wording asked for it", want)
		}
	}
}

// Picking is what the box shows, and every keystroke is stored under the name
// that was picked when it was typed -- so a wording cannot be lost by looking
// at another one.
func TestSwitchingWordingKeepsBothEdits(t *testing.T) {
	ownConfig(t)
	a := &App{}
	def := promptDefFor("cut").styleName()

	a.setPrompt("cut", "my highlights wording")
	a.showPromptStyle("cut", "Rating / tier list")
	a.setPrompt("cut", "my rating wording")

	a.showPromptStyle("cut", def)
	if got := a.prompt("cut"); got != "my highlights wording" {
		t.Errorf("going back to %q gave %q", def, short(got))
	}
	a.showPromptStyle("cut", "Rating / tier list")
	if got := a.prompt("cut"); got != "my rating wording" {
		t.Errorf("the other wording came back as %q", short(got))
	}
	// and the pick is stored, since it decides what the next run sends. On
	// disk, in llm.conf beside the endpoints: it is one short name per job.
	if got := a.readGlobal().PromptPick; got["cut"] != "Rating / tier list" {
		t.Errorf("the file stores pick %v, want the rating cut", got)
	}
	if got := a.readGlobal().PromptPick; len(got) != 1 {
		t.Errorf("a job nobody switched is stored anyway: %v", got)
	}
	// and switching back to the shipped default takes the line out again,
	// rather than freezing today's default under its own name
	a.showPromptStyle("cut", def)
	if got := a.readGlobal().PromptPick; len(got) != 0 {
		t.Errorf("after going back to the default the file still says %v", got)
	}
}

// A wording the project invented has to survive the file and come back
// pickable; removing it falls back to the default rather than to an empty box,
// which would silently send the model nothing.
func TestAnAddedWordingRoundTrips(t *testing.T) {
	ownConfig(t)
	a := &App{}
	a.savePromptStyle("cut", "Speedrun", "Cut for splits and route.")
	a.showPromptStyle("cut", "Speedrun")

	a.flushPrompts()
	b := &App{}
	b.loadGlobalPrompts()
	if got := b.prompt("cut"); got != "Cut for splits and route." {
		t.Errorf("after a save and a load the added wording is %q", short(got))
	}
	names := []string{}
	for _, s := range b.promptStyleList("cut") {
		names = append(names, s.Name)
	}
	if len(names) != len(promptDefFor("cut").builtins())+1 {
		t.Errorf("the dropdown offers %v, want the built-ins plus Speedrun", names)
	}

	b.dropPromptStyle("cut", "Speedrun")
	b.showPromptStyle("cut", promptDefFor("cut").styleName())
	if got := b.prompt("cut"); got != promptDefFor("cut").def {
		t.Errorf("after removing it the box holds %q, want the built-in", short(got))
	}
	if got := held(b); got != nil {
		t.Errorf("the removed wording is still held: %v", got)
	}
	// and gone from disk, or the next launch would offer it again
	b.flushPrompts()
	c := &App{}
	c.loadGlobalPrompts()
	if got := held(c); got != nil {
		t.Errorf("a removed wording came back from disk: %v", got)
	}
}

// A style deleted while it was picked -- by hand in project.json, or by a build
// that stopped shipping it -- must not leave the runner with an empty prompt.
func TestAPickedWordingThatIsGoneFallsBackToTheBuiltIn(t *testing.T) {
	ownConfig(t)
	a := &App{}
	a.applyPromptStyles(nil, map[string]string{"cut": "a wording nobody has"}, nil)
	if got := a.prompt("cut"); got != promptDefFor("cut").def {
		t.Errorf("a missing wording sent %q, want the built-in", short(got))
	}
}

// TestMigrateHintsFoldsNotesIntoThePrompt: describe, fix and narrate used to
// have a notes box that the runner glued onto the system prompt at request
// time. The boxes are gone, so a project written before the merge has to have
// its notes moved into the prompt -- dropping them would change what the model
// is told without changing anything visible.
func TestMigrateHintsFoldsNotesIntoThePrompt(t *testing.T) {
	ownConfig(t)
	a := &App{}
	a.applyPromptStyles(nil, nil, nil)
	p := Project{
		DescribeHints:   "the HUD number top left is ammo",
		TranscriptHints: "SPEAKER_00 is Jan",
		CutHints:        "this was the tournament final, keep the last round whole",
		NarrateHints:    "open with an intro over the first clip",
	}
	a.migrateHints(p)

	for _, c := range []struct{ key, note string }{
		{"describe", "the HUD number top left is ammo"},
		{"fix", "SPEAKER_00 is Jan"},
		{"cut", "this was the tournament final, keep the last round whole"},
		{"narrate", "open with an intro over the first clip"},
	} {
		got := a.prompt(c.key)
		if !strings.Contains(got, c.note) {
			t.Errorf("%s prompt lost the project's notes: %q", c.key, short(got))
		}
		if !strings.HasPrefix(got, promptDefFor(c.key).def) {
			t.Errorf("%s prompt no longer starts with the built-in: %q", c.key, short(got))
		}
	}
	// the notes are prompt text now, so the project stores them as an edit --
	// and reloading the same file must not append them a second time
	before := a.prompt("describe")
	a.migrateHints(p)
	if got := a.prompt("describe"); got != before {
		t.Errorf("a second load appended the notes again:\n%q", short(got))
	}
	got := held(a)
	for _, key := range []string{"describe", "fix", "cut", "narrate"} {
		if len(got[key]) != 1 {
			t.Errorf("%s's folded-in notes are not stored as an edit of a wording: %v", key, got[key])
		}
	}
	// nothing to fold is the common case and must leave the built-ins alone
	b := &App{}
	b.applyPromptStyles(nil, nil, nil)
	b.migrateHints(Project{})
	if got := held(b); got != nil {
		t.Errorf("a project without notes wants to store %v, want nothing", got)
	}
}

func short(s string) string {
	if len(s) > 60 {
		return s[:60] + "…"
	}
	return s
}

// Choosing a wording froze the app.
//
// The dropdown's notify::selected is emitted from inside GtkDropDown, with the
// popup still closing, and the handler rebuilt the dropdown's own list model
// from there -- so the list view was left drawing a list that no longer
// existed. It hung after the switch had visibly worked: the box already showed
// the new wording and Reset had already dimmed, which is why it read as "the
// app freezes when I pick Highlights" rather than as anything the pick did.
//
// So the two jobs are split. pickPromptStyle records the choice and fills the
// box and is the only one the signal may call; showPromptStyle also rebuilds
// the menu and belongs to the places where the list of wordings really changed
// -- a load, an add, a delete -- none of which are inside that emission.
//
// Source-level, because reproducing it needs a display, a main loop and a real
// popup. What can be checked without one is that the handler stays on its side
// of the split.
func TestChoosingAWordingDoesNotRebuildTheMenuUnderItself(t *testing.T) {
	ownConfig(t)
	b, err := os.ReadFile("prepedit.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)

	m := regexp.MustCompile(`(?s)wording\.NotifyProperty\("selected".*?\n\t\}\)`).FindString(src)
	if m == "" {
		t.Fatal("the dropdown no longer connects notify::selected — find where a pick lands now")
	}
	if strings.Contains(m, "showPromptStyle") {
		t.Errorf("the pick handler rebuilds the menu it was called from:\n%s", m)
	}
	if !strings.Contains(m, "pickPromptStyle") {
		t.Errorf("the pick handler no longer shows the wording that was picked:\n%s", m)
	}
	// the half that may touch the model has to still exist as its own function,
	// or the split is only a rename away from being undone
	store := readSrc(t, "prompts.go")
	for _, want := range []string{
		"func (a *App) pickPromptStyle(key, name string)",
		"func (a *App) showPromptStyle(key, name string)",
	} {
		if !strings.Contains(store, want) {
			t.Errorf("%s is gone — the two jobs are one again", want)
		}
	}
	// and the rebuild that does happen is one emission, not one per item: a
	// Remove loop empties the model an item at a time, and an empty model is a
	// selection GTK moves for you at every step
	if strings.Contains(store, ".names.Remove(") {
		t.Error("the menu is rebuilt by removing items one at a time; Splice replaces it in one go")
	}
	if !strings.Contains(store, ".names.Splice(") {
		t.Error("the menu is no longer replaced in a single items-changed")
	}
	// the prep menu itself is spliced for the same reason: syncPromptMarks
	// redraws it on every project load
	if !strings.Contains(src, "menu.Splice(") {
		t.Error("the row menu is no longer replaced in a single items-changed")
	}
}

// ...and the rebuild is skipped entirely when the names did not move, which is
// every plain switch between wordings. Cheap to check and it is what keeps the
// dangerous path from running at all on the common one.
func TestTheMenuIsOnlyRebuiltWhenTheNamesChange(t *testing.T) {
	ownConfig(t)
	a := &App{}
	if !sameStringsSlice(namesOfStyles(a.promptStyleList("cut")), namesOfStyles(a.promptStyleList("cut"))) {
		t.Fatal("the shipped wordings for the cut are not stable between calls")
	}
	// switching wording does not add or remove one
	before := namesOfStyles(a.promptStyleList("cut"))
	a.promptPick = map[string]string{"cut": "Rating / tier list"}
	if after := namesOfStyles(a.promptStyleList("cut")); !sameStringsSlice(before, after) {
		t.Errorf("picking a wording changed the menu from %v to %v", before, after)
	}
	// editing one does not either -- it replaces the text under the same name
	a.setPrompt("cut", "Rank them, shortest first.")
	if after := namesOfStyles(a.promptStyleList("cut")); !sameStringsSlice(before, after) {
		t.Errorf("editing a wording changed the menu from %v to %v", before, after)
	}
	// adding one does, and that is the case showPromptStyle is for
	a.savePromptStyle("cut", "Speedrun", "One clip per split.")
	after := namesOfStyles(a.promptStyleList("cut"))
	if len(after) != len(before)+1 || after[len(after)-1] != "Speedrun" {
		t.Errorf("an added wording gave %v, want %v plus Speedrun at the end", after, before)
	}
}

func namesOfStyles(list []promptStyle) []string {
	out := make([]string, len(list))
	for i, s := range list {
		out[i] = s.Name
	}
	return out
}

func sameStringsSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestTheEditorRegistriesInitIndependently: promptViews and promptRows used to
// be created together, guarded by promptViews alone, and a box that filled one
// without the other then panicked on the first write into a nil map. There is
// one box now, so both are filled from the same place -- but they are still
// separate maps written on separate lines, and the guard has to match: a map
// created because the OTHER one was nil is the same bug with the names
// swapped.
func TestTheEditorRegistriesInitIndependently(t *testing.T) {
	ed := funcBody(t, "prepedit.go", `func \(a \*App\) prepEditor\(`)
	for _, want := range []string{
		"if a.promptViews == nil {",
		"if a.promptRows == nil {",
	} {
		if !strings.Contains(ed, want) {
			t.Errorf("prepEditor lacks %q -- one registry existing must not stand in for the other", want)
		}
	}
	if strings.Contains(ed, "a.promptViews = map[string]*gtk.TextView{}\n\t\ta.promptRows") {
		t.Error("the registries are created together again, guarded by one of them")
	}
}

// A wording's name is prose and a file name is not: the shipped cut styles
// already include "Rating / tier list", and writing that to disk under its own
// name would put the file in a folder called "Rating " -- or, for a name of
// "..", outside the prompts folder altogether. So the file name is flattened,
// and reading it back has to undo the flattening: a file matched by the name it
// WOULD have belongs to the built-in it was edited from, not to a fourth style
// spelled with a dash that nobody could tell apart in the dropdown.
func TestAWordingNamedWithASlashSurvivesTheFolder(t *testing.T) {
	ownConfig(t)
	a := &App{}
	a.showPromptStyle("cut", "Rating / tier list")
	a.setPrompt("cut", "rank them, worst first")
	a.flushPrompts()

	p := filepath.Join(promptsDir(), "cut", "Rating - tier list.txt")
	if !exists(p) {
		ents, _ := os.ReadDir(filepath.Join(promptsDir(), "cut"))
		var got []string
		for _, e := range ents {
			got = append(got, e.Name())
		}
		t.Fatalf("the cut folder holds %v, want the slash flattened into %s", got, filepath.Base(p))
	}

	b := &App{}
	b.loadGlobalPrompts()
	if got := b.prompt("cut"); got != "rank them, worst first" {
		t.Errorf("the edited wording came back as %q", short(got))
	}
	// under its own name, and as an edit of the shipped style rather than as a
	// new one sitting next to it
	if got := b.promptPickName("cut"); got != "Rating / tier list" {
		t.Errorf("the picked wording came back as %q, want the name that was typed", got)
	}
	if got := len(b.promptStyleList("cut")); got != len(promptDefFor("cut").builtins()) {
		var names []string
		for _, s := range b.promptStyleList("cut") {
			names = append(names, s.Name)
		}
		t.Errorf("the dropdown offers %v -- the flattened file was read as a style of its own", names)
	}
}

// Switching the dropdown is a decision too, even with nothing typed. A machine
// set to Highlights that opens a project saved when the default was picked must
// stay on Highlights: the project's prompts are adopted only where this machine
// has said nothing at all.
func TestAPickedWordingIsThisMachinesAnswerToo(t *testing.T) {
	ownConfig(t)
	a := &App{}
	a.showPromptStyle("cut", "Highlights")

	a.applyPromptStyles(map[string][]promptStyle{
		"cut": {{defStyle, "an old project's idea of a highlight"}}},
		map[string]string{"cut": defStyle}, nil)

	if got := a.promptPickName("cut"); got != "Highlights" {
		t.Errorf("a project moved the dropdown to %q", got)
	}
	var want string
	for _, s := range promptDefFor("cut").builtins() {
		if s.Name == "Highlights" {
			want = s.Text
		}
	}
	if got := a.prompt("cut"); got != want {
		t.Errorf("the box holds %q, want the shipped Highlights wording", short(got))
	}
}
