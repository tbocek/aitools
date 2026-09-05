package main

// The system prompts, in one registry and editable in the app.
//
// These are the tool's taste: what counts as a highlight, how the narration
// sounds, what the vision model bothers to mention. Compiled in, they could
// only be changed by rebuilding -- backwards, since the footage varies far more
// than the code does. Every prompt the tool sends is here, and every step page
// shows its own in full.
//
// describe and fix have no separate "notes" box: the two used to be glued
// together before the request went out, which meant two fields, one string and
// no way to tell from the screen what the model was actually told. The box IS
// the prompt now.
//
// An edited prompt is stored, an untouched one is not, and where it is stored
// is ~/.config/autocut/prompts -- this machine's, not this project's, because
// how you like to be edited for does not change between two sessions the way
// the footage does (promptstore.go). So a job nobody has touched picks up
// improvements from a new build instead of freezing today's wording forever,
// and what is on disk is a record of what the user decided rather than a copy
// of the binary.

import (
	"strings"

	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// promptStyle is one named wording for a job -- what the box shows when that
// name is picked in the row above it.
//
// Most jobs have exactly one, because there is one good way to say what they
// are for: describing a frame is describing a frame. The cut is the exception,
// and it is not a close one. What makes a good segment is not a property of
// this tool at all, it is a property of what was filmed -- a session where a
// group scores nine maps and reads out a ranking wants a completely different
// video out of the same hour than a raid night does, and no single wording is
// even nearly right for both. So the cut ships several, the box picks between
// them, and a project that needs a shape nobody shipped adds its own.
// promptRow is the widgets above one prompt box: the label that says whether
// this machine is holding an edit of the shipped wording, and the button that
// puts the built-in back.
type promptRow struct {
	mark *gtk.Label
	drop *gtk.Button
}

// promptDef is one editable prompt: the job it is for, and the wording this
// build ships for it.
//
// There used to be several wordings per job -- a Style dropdown on Prepare
// turned every prompt at once to "Highlights", "Showcase", "Rating / tier
// list" or "YouTube Shorts", each a paragraph that already believed it knew
// what the footage was. It is one wording per job now, written to be true of
// any session, and what KIND of video this is goes in the user context, where
// the rest of the facts about the session are. That is the same information in
// one place instead of two, and the place it is in is the one that is read
// with every request and outranks the wordings (ctxRule).
//
// A blurb explaining what changing a prompt would do used to sit between the
// heading and the box; the prompt itself says that better than a paragraph
// above it can, and the paragraph was what made these pages a wall of prose.
type promptDef struct{ key, def string }

// Keys name the prompt in project.json and are therefore permanent -- renaming
// one silently drops what a user wrote under the old name.
var promptDefs = []promptDef{
	// first, because it goes in front of every one of the others: the formats
	// and the house rules they all work to (syscontext.go).
	{key: "system", def: strings.TrimSpace(sysSystem)},
	{key: "describe", def: strings.TrimSpace(describeSystem)},
	{key: "fix", def: strings.TrimSpace(fixSystem)},
	// the cut, and the three passes that follow it clip by clip: what was
	// said, how fast each clip runs, and the decorations. They were one reply
	// with the segments, and the one reply is what kept failing
	// (cut_suggest.go).
	//
	// "audit" was here: a second long call that read the suggestion back
	// against the same brief and moved its boundaries. It was worth having
	// when the cut was one reply doing three jobs; against a cut that is only
	// segments it spent ten minutes to move a border a few seconds. Removed,
	// not renamed -- a project's edited copy is a dead key nobody reads.
	{key: "cut", def: strings.TrimSpace(cutSystem)},
	{key: "captions", def: strings.TrimSpace(captionSystem)},
	{key: "speed", def: strings.TrimSpace(speedSystem)},
	{key: "effects", def: strings.TrimSpace(effectsSystem)},
	{key: "narrate", def: strings.TrimSpace(narrSystem)},
	// "thumbnail" was here: a second Publish prompt that picked which frame to
	// edit and wrote the instruction for it. Removed, not renamed -- the key is
	// gone from the registry, so a project that saved an edited copy of it just
	// keeps a dead key nobody reads.
	{key: "youtube", def: strings.TrimSpace(youtubeSystem)},
	// "improve" was here, and before that it had already come off the bench:
	// the Improve button asked the model why a step decided what it did and
	// offered edits to the prompts below. Button, prompt and cards are all
	// gone, and a project that saved an edited copy of the key just keeps a
	// dead key nobody reads.
}

func promptDefFor(key string) promptDef {
	for _, d := range promptDefs {
		if d.key == key {
			return d
		}
	}
	return promptDef{key: key}
}

// prompt returns the system prompt for key: what this machine has of its own,
// or the wording the build ships. Callable from a step runner's goroutine --
// it reads a cached string, never the GtkTextBuffer, which belongs to the GUI
// thread.
func (a *App) prompt(key string) string {
	a.promptMu.Lock()
	s := strings.TrimSpace(a.promptTxt[key])
	a.promptMu.Unlock()
	if s != "" {
		return s
	}
	return promptDefFor(key).def
}

// setPrompt records what is in the box. Kept per machine and written to disk
// on the autosave tick (flushPrompts): how you like to be edited for is the
// same in January's raid as in March's.
func (a *App) setPrompt(key, text string) {
	a.promptMu.Lock()
	if a.promptTxt == nil {
		a.promptTxt = map[string]string{}
	}
	a.promptTxt[key] = text
	a.promptMu.Unlock()
	a.markPromptRow(key)
}

// promptOwned is whether what the model reads is not what shipped. It is the
// one thing worth a permanent mark wherever a prompt is named (the ✎ in the
// bench's menu), and it is exact rather than a flag: typing an edit and typing
// it back is not an edit.
func (a *App) promptOwned(key string) bool {
	a.promptMu.Lock()
	s := strings.TrimSpace(a.promptTxt[key])
	a.promptMu.Unlock()
	return s != "" && s != promptDefFor(key).def
}

// resetPrompt puts the built-in back: the box, the mark, and -- on the next
// flush -- the file on disk, which is what makes Reset a delete.
func (a *App) resetPrompt(key string) {
	a.promptMu.Lock()
	delete(a.promptTxt, key)
	a.promptMu.Unlock()
	a.showPrompt(key)
}

// showPrompt fills the box for a job and settles the row above it. Safe before
// the widgets exist, so a project load does not have to care whether a page
// has been built yet.
//
// promptQuiet is what keeps it from being a loop: filling the box fires
// "changed", whose handler would otherwise write the wording straight back
// out. GUI thread only, which is why a plain bool is enough.
func (a *App) showPrompt(key string) {
	if tv, ok := a.promptViews[key]; ok {
		a.promptQuiet = true
		tv.Buffer().SetText(a.prompt(key))
		a.promptQuiet = false
	}
	a.markPromptRow(key)
}

// adoptProjectPrompts takes in what a project has to say about the prompts --
// which, now that they are the machine's (promptstore.go), is only ever what a
// project written before that has to say. It ADOPTS rather than replaces: a
// wording lands only where this machine has nothing of its own for the job.
//
// It used to be a full switch, everything the project did not mention going
// back to the built-in, because the prompts were the project's and leaving the
// last project's wording in a box would have been the worst kind of bug --
// invisible, and it changes what the model writes. With the prompts kept per
// machine the bug is the other way round: an old project opened for five
// minutes must not overwrite the wording four videos were tuned with.
func (a *App) adoptProjectPrompts(legacy map[string]string) {
	for _, d := range promptDefs {
		if a.promptOwned(d.key) {
			continue // this machine's own wording; a project does not overwrite it
		}
		if s := strings.TrimSpace(legacy[d.key]); s != "" {
			a.setPrompt(d.key, s)
		}
	}
	for _, d := range promptDefs {
		a.showPrompt(d.key)
	}
}

// markPromptRow says whether this machine is holding an edit, and whether the
// button that lets go of it has anything to do.
func (a *App) markPromptRow(key string) {
	// the ✎ in the bench's menu first, and unconditionally: it says the same
	// thing this row does -- "what the model reads is not what shipped" -- and
	// it is the only place that says it once the boxes are behind a button
	// (prepedit.go), so it cannot depend on a box being open.
	a.syncPromptMarks()

	row, ok := a.promptRows[key]
	if !ok {
		return
	}
	if a.promptOwned(key) {
		row.mark.SetText("edited — kept in your settings")
		row.drop.SetSensitive(true)
		row.drop.SetTooltipText("Put the built-in wording back")
		return
	}
	row.mark.SetText("")
	// nothing to undo: a live button here reads as "there is something
	// stored", which is exactly what the empty mark is denying
	row.drop.SetSensitive(false)
	row.drop.SetTooltipText("This is the built-in wording, unchanged")
}

// syncPromptMarks redraws the bench's menu, where the ✎ on a row is the only
// thing that says a prompt has been reworded once the boxes are behind a
// button (prepedit.go).
func (a *App) syncPromptMarks() {
	if a.prepSync != nil {
		a.prepSync()
	}
}

// sameStrings is whether a menu already holds exactly these rows. Splicing a
// model that did not change is a menu that flickers and a selection that
// bounces through every index on the way.
func sameStrings(model *gtk.StringList, want []string) bool {
	if int(model.NItems()) != len(want) {
		return false
	}
	for i, s := range want {
		if model.String(uint(i)) != s {
			return false
		}
	}
	return true
}

// askName is a modal one-line prompt. Same hand-rolled shape as confirm, and
// the same reasons; Enter is OK here rather than Cancel, because the only thing
// this asks for is a name and typing one is the whole interaction.
func (a *App) askName(question, detail string, ok func(string)) {
	win := gtk.NewWindow()
	win.SetTransientFor(&a.win.Window)
	win.SetModal(true)
	win.SetTitle(question)
	win.SetDefaultSize(380, -1)

	q := gtk.NewLabel(question)
	q.SetXAlign(0)
	q.SetWrap(true)
	q.AddCSSClass("heading")
	d := gtk.NewLabel(detail)
	d.SetXAlign(0)
	d.SetWrap(true)
	d.AddCSSClass("dim-label")

	entry := gtk.NewEntry()
	save := gtk.NewButtonWithLabel("Save")
	save.AddCSSClass("suggested-action")
	done := func() {
		name := strings.TrimSpace(entry.Text())
		if name == "" {
			return // a nameless wording could never be picked again
		}
		win.Close()
		ok(name)
	}
	save.ConnectClicked(done)
	entry.ConnectActivate(done)
	cancel := gtk.NewButtonWithLabel("Cancel")
	cancel.ConnectClicked(func() { win.Close() })

	btns := gtk.NewBox(gtk.OrientationHorizontal, 8)
	btns.SetHAlign(gtk.AlignEnd)
	btns.SetMarginTop(8)
	btns.Append(cancel)
	btns.Append(save)

	box := gtk.NewBox(gtk.OrientationVertical, 8)
	box.SetMarginTop(16)
	box.SetMarginBottom(16)
	box.SetMarginStart(16)
	box.SetMarginEnd(16)
	box.Append(q)
	box.Append(d)
	box.Append(entry)
	box.Append(btns)
	win.SetChild(box)
	entry.GrabFocus()
	win.SetVisible(true)
}

// editorBody is the one shape a text box on a step page has: a heading row,
// then the box under it, framed, scrolling and floored at four lines.
//
// It is one function because the boxes are seen side by side. The prompts and
// the context were built separately and drifted: the context box had no natural
// height and no floor, so a drag or a short window squeezed the two halves of
// the Describe page differently -- and, more visibly, a heading row carrying a
// Reset button is a good deal taller than one carrying a bare label, so the box
// under it started a dozen pixels lower than the box beside it. Two boxes of
// the same kind, misaligned by exactly the height of a button.
//
// The size group is what settles the second one: every heading row on every
// page joins it, so all of them are given the tallest one's height and every
// box under them starts at the same y, button or no button. It spans pages
// rather than a page, which costs nothing -- every row in it is either a label
// or a label and a button -- and means a new step gets the alignment by calling
// this rather than by remembering to.
//
// About the floor. Natural height is what makes the box open big where the page
// has room; the minimum is what a divider or a short window may squeeze it to,
// and it is the one number here that can push things off the page. It was 240
// once, which on the two-prompt page meant the pair could not fit a short
// window at all: the divider stayed where it was, the top box kept a height it
// no longer had room for, and its heading and Reset button went off the top.
// Four lines is a box you can still work in, and small enough that no window is
// too short for two of them.
func (a *App) editorBody(head *gtk.Box, tv *gtk.TextView) *gtk.Box {
	if a.headGroup == nil {
		a.headGroup = gtk.NewSizeGroup(gtk.SizeGroupVertical)
	}
	a.headGroup.AddWidget(head)
	return editorFrame(head, tv)
}

// editorFrame is editorBody without the shared heading height: the same box, for
// somewhere there is nothing beside it to line up with. That is where every
// prompt is shown now -- one at a time, in the picker's window or in the Cut
// page's form column (promptpick.go) -- and a size group is worse than useless
// there, since it goes on measuring rows belonging to windows that have closed.
func editorFrame(head *gtk.Box, tv *gtk.TextView) *gtk.Box {
	// vexpand is what makes a taller window a taller box. Without it the box
	// stops at the height of its text and the window's extra height piles up as
	// blank page below it -- growing the window then bought nothing on the one
	// page whose whole content is text. It propagates up through what this
	// returns, so a page only has to put the box somewhere that can grow.
	scroll := gtk.NewScrolledWindow()
	scroll.SetChild(tv)
	scroll.SetPropagateNaturalHeight(true)
	scroll.SetPolicy(gtk.PolicyNever, gtk.PolicyAutomatic) // wrapped text has no width to scroll
	scroll.SetSizeRequest(-1, 72)
	scroll.SetVExpand(true)
	scroll.AddCSSClass("frame")

	body := gtk.NewBox(gtk.OrientationVertical, 4)
	body.SetMarginTop(4)
	body.Append(head)
	body.Append(scroll)
	return body
}
