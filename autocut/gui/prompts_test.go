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
	if len(declared) != len(promptDefs) {
		t.Errorf("%d system prompts declared %v, but %d are exposed in promptDefs -- "+
			"a new one needs a promptDef and an editor on its step",
			len(declared), keysOf(declared), len(promptDefs))
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

// TestCurrentPromptsStoresOnlyEdits is the reason a project can pick up a
// better shipped prompt: storing them all verbatim would freeze today's wording
// into every project file forever.
func TestCurrentPromptsStoresOnlyEdits(t *testing.T) {
	a := &App{}
	if got := a.currentPrompts(); got != nil {
		t.Errorf("an untouched app wants to store %v, want nothing", got)
	}
	// what the editor does on build: fill the box with the default
	for _, d := range promptDefs {
		a.setPrompt(d.key, d.def)
	}
	if got := a.currentPrompts(); got != nil {
		t.Errorf("boxes holding the defaults want to store %v, want nothing", got)
	}

	a.setPrompt("narrate", "Say nothing at all.")
	got := a.currentPrompts()
	if len(got) != 1 || got["narrate"] != "Say nothing at all." {
		t.Fatalf("currentPrompts = %v, want only the edited one", got)
	}
	// and it survives the file, under a key the next version will still know
	b, _ := json.Marshal(Project{Prompts: got})
	if !strings.Contains(string(b), `"prompts":{"narrate":`) {
		t.Errorf("project JSON does not carry the prompt: %s", b)
	}
	if b, _ := json.Marshal(Project{}); strings.Contains(string(b), "prompts") {
		t.Errorf("an untouched project mentions prompts: %s", b)
	}
}

// TestApplyPromptsIsAFullSwitch: loading a project must not leave the previous
// project's wording behind. That failure is invisible -- nothing looks wrong,
// the model just gets told something the user did not mean this time.
func TestApplyPromptsIsAFullSwitch(t *testing.T) {
	a := &App{}
	a.setPrompt("cut", "the previous project's idea of a highlight")
	a.setPrompt("narrate", "and its idea of a voice")

	a.applyPrompts(map[string]string{"narrate": "the new project's voice"})

	if got := a.prompt("cut"); got != promptDefFor("cut").def {
		t.Errorf("a key the project does not mention kept %q, want the built-in", short(got))
	}
	if got := a.prompt("narrate"); got != "the new project's voice" {
		t.Errorf("narrate = %q, want the loaded project's", short(got))
	}
	// a project saved before prompts existed has none at all
	a.applyPrompts(nil)
	for _, d := range promptDefs {
		if got := a.prompt(d.key); got != d.def {
			t.Errorf("after loading a prompt-less project, %s = %q, want the built-in",
				d.key, short(got))
		}
	}
}

// TestMigrateHintsFoldsNotesIntoThePrompt: describe and fix used to have a
// notes box that the runner glued onto the system prompt at request time. The
// box is gone, so a project written before the merge has to have its notes
// moved into the prompt -- dropping them would change what the model is told
// without changing anything visible.
func TestMigrateHintsFoldsNotesIntoThePrompt(t *testing.T) {
	a := &App{}
	a.applyPrompts(nil)
	p := Project{
		DescribeHints:   "the HUD number top left is ammo",
		TranscriptHints: "SPEAKER_00 is Jan",
	}
	a.migrateHints(p)

	for _, c := range []struct{ key, note string }{
		{"describe", "the HUD number top left is ammo"},
		{"fix", "SPEAKER_00 is Jan"},
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
	if got := a.currentPrompts(); got["describe"] == "" || got["fix"] == "" {
		t.Errorf("folded-in notes are not stored as prompt edits: %v", got)
	}
	// nothing to fold is the common case and must leave the built-ins alone
	b := &App{}
	b.applyPrompts(nil)
	b.migrateHints(Project{})
	if got := b.currentPrompts(); got != nil {
		t.Errorf("a project without notes wants to store %v, want nothing", got)
	}
}

func short(s string) string {
	if len(s) > 60 {
		return s[:60] + "…"
	}
	return s
}
