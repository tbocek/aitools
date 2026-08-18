package main

// The system prompts, in one registry and editable per project.
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
// An edited prompt is stored in the project; an untouched one is not. So a
// project that never touched a prompt picks up improvements from a new build
// instead of freezing today's wording forever, and project.json stays a record
// of what the user decided rather than a copy of the binary.

import (
	"strings"

	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotk4/pkg/pango"
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
type promptStyle struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

// promptRow is the widgets above one prompt box: the wording picker, the label
// that says what the project is holding, and the button that lets go of it.
// Held so that loading a project can redraw a page that is already built.
type promptRow struct {
	pick  *gtk.DropDown
	names *gtk.StringList
	mark  *gtk.Label
	drop  *gtk.Button
}

// promptDef is one editable prompt. A blurb explaining what changing it would
// do used to sit between the heading and the box; the prompt itself says that
// better than a paragraph above it can, and the paragraph was what made these
// pages a wall of prose.
type promptDef struct {
	key, def string
	// what def is called in the dropdown, and the other wordings shipped for
	// the same job. Style names are stored in project.json exactly as they read
	// here, so renaming one orphans a project's edit of it the same way
	// renaming a key would -- add a style, do not rename one.
	style string
	alts  []promptStyle
}

// defStyle names the wording a job ships with when it ships only one. It reads
// as a name in the dropdown rather than as a description of what the prompt
// does, which is deliberate: every box says what it is for in its heading, and
// six dropdowns all reading "Describe" would say nothing at all.
const defStyle = "Default"

func (d promptDef) styleName() string {
	if d.style != "" {
		return d.style
	}
	return defStyle
}

// builtins is every wording shipped for this job, the default first -- which is
// also the order the dropdown lists them in, and which style a project that has
// never picked one gets.
func (d promptDef) builtins() []promptStyle {
	return append([]promptStyle{{d.styleName(), d.def}}, d.alts...)
}

// Keys name the prompt in project.json and are therefore permanent -- renaming
// one silently drops what a user wrote under the old name.
var promptDefs = []promptDef{
	{key: "describe", def: strings.TrimSpace(describeSystem)},
	{key: "fix", def: strings.TrimSpace(fixSystem)},
	{key: "cut", def: strings.TrimSpace(suggestSystem), style: "Highlights",
		alts: []promptStyle{{"Rating / tier list", strings.TrimSpace(ratingSystem)}}},
	{key: "audit", def: strings.TrimSpace(auditSystem)},
	{key: "narrate", def: strings.TrimSpace(narrSystem)},
	// "thumbnail" was here: a second Publish prompt that picked which frame to
	// edit and wrote the instruction for it. Removed, not renamed -- the key is
	// gone from the registry, so a project that saved an edited copy of it just
	// keeps a dead key nobody reads.
	{key: "youtube", def: strings.TrimSpace(youtubeSystem)},
}

func promptDefFor(key string) promptDef {
	for _, d := range promptDefs {
		if d.key == key {
			return d
		}
	}
	return promptDef{key: key}
}

// prompt returns the system prompt for key: what the user has in the box, or
// the wording the picked style ships with. Callable from a step runner's
// goroutine -- it reads a cached string, never the GtkTextBuffer, which belongs
// to the GUI thread.
func (a *App) prompt(key string) string {
	a.promptMu.Lock()
	s := strings.TrimSpace(a.promptTxt[key])
	a.promptMu.Unlock()
	if s != "" {
		return s
	}
	return a.promptStyleText(key, a.promptPickName(key))
}

// setPrompt records what is in the box. It writes twice on purpose: promptTxt
// is what a runner reads, and the style list is what the dropdown offers and
// what the project stores. Typing into the box IS editing the picked style, so
// switching wording and switching back cannot lose what was typed.
func (a *App) setPrompt(key, text string) {
	a.promptMu.Lock()
	if a.promptTxt == nil {
		a.promptTxt = map[string]string{}
	}
	a.promptTxt[key] = text
	a.promptMu.Unlock()
	a.savePromptStyle(key, a.promptPickName(key), text)
}

// promptPickName is which wording this project uses for the job -- the shipped
// default until something picks another.
func (a *App) promptPickName(key string) string {
	a.promptMu.Lock()
	defer a.promptMu.Unlock()
	if n := a.promptPick[key]; n != "" {
		return n
	}
	return promptDefFor(key).styleName()
}

// promptStyleList is what the dropdown offers for a job: every shipped wording,
// each replaced by the project's edit of it where there is one, then whatever
// the project added of its own. Shipped first and in table order, so the menu
// does not reshuffle itself as styles are added.
func (a *App) promptStyleList(key string) []promptStyle {
	a.promptMu.Lock()
	own := append([]promptStyle(nil), a.promptSty[key]...)
	a.promptMu.Unlock()
	byName := map[string]string{}
	for _, s := range own {
		byName[s.Name] = s.Text
	}
	var out []promptStyle
	shipped := map[string]bool{}
	for _, s := range promptDefFor(key).builtins() {
		shipped[s.Name] = true
		if t, ok := byName[s.Name]; ok {
			s.Text = t
		}
		out = append(out, s)
	}
	for _, s := range own {
		if !shipped[s.Name] {
			out = append(out, s)
		}
	}
	return out
}

// promptStyleText is the wording behind a name: the project's if it has one,
// the shipped one otherwise. A name that is neither -- a style deleted while it
// was picked -- falls back to the default rather than to an empty prompt, which
// would silently send the model nothing.
func (a *App) promptStyleText(key, name string) string {
	for _, s := range a.promptStyleList(key) {
		if s.Name == name {
			return s.Text
		}
	}
	return promptDefFor(key).def
}

// savePromptStyle stores a wording under a name, and is also how an edit is
// undone: text equal to what that name ships with drops the override instead of
// storing a copy of it. That is the same rule the old single-prompt storage
// had, and it is what lets an untouched project keep tracking a newer build's
// wording rather than freezing today's.
func (a *App) savePromptStyle(key, name, text string) {
	text = strings.TrimSpace(text)
	shipped := ""
	for _, s := range promptDefFor(key).builtins() {
		if s.Name == name {
			shipped = s.Text
		}
	}
	a.promptMu.Lock()
	defer a.promptMu.Unlock()
	if a.promptSty == nil {
		a.promptSty = map[string][]promptStyle{}
	}
	list := a.promptSty[key]
	for i, s := range list {
		if s.Name != name {
			continue
		}
		if text == shipped { // back to the shipped wording: stop storing it
			a.promptSty[key] = append(list[:i:i], list[i+1:]...)
			return
		}
		list[i].Text = text
		return
	}
	if text == shipped || text == "" {
		return
	}
	a.promptSty[key] = append(list, promptStyle{Name: name, Text: text})
}

// dropPromptStyle forgets what the project says about a name. For a wording
// the project invented that is a delete; for an edited built-in it is a revert,
// since the shipped one is still under the same name.
func (a *App) dropPromptStyle(key, name string) {
	a.promptMu.Lock()
	defer a.promptMu.Unlock()
	list := a.promptSty[key]
	for i, s := range list {
		if s.Name == name {
			a.promptSty[key] = append(list[:i:i], list[i+1:]...)
			return
		}
	}
}

// currentPromptStyles is what the project stores: only the wordings it has
// something of its own to say about -- one it edited, or one it invented.
func (a *App) currentPromptStyles() map[string][]promptStyle {
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

// currentPromptPick stores only a choice that is not the shipped default, so a
// project that never touched the dropdown says nothing about it and follows
// whatever a later build ships as the default.
func (a *App) currentPromptPick() map[string]string {
	out := map[string]string{}
	for _, d := range promptDefs {
		if n := a.promptPickName(d.key); n != d.styleName() {
			out[d.key] = n
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// applyPromptStyles loads a project's wordings and its choices. A key the
// project does not mention goes back to the shipped default: loading a project
// is a full switch, and leaving the previous project's wording in a box would
// be the worst kind of bug -- invisible, and it changes what the model writes.
//
// legacy is the pre-styles storage, where a project held one edited prompt per
// key and no name for it. Those belong to whichever style was default at the
// time, which is the default now: the shipped wording of a job did not move,
// it only gained company.
func (a *App) applyPromptStyles(styles map[string][]promptStyle, pick map[string]string, legacy map[string]string) {
	a.promptMu.Lock()
	a.promptSty = map[string][]promptStyle{}
	a.promptPick = map[string]string{}
	for k, list := range styles {
		a.promptSty[k] = append([]promptStyle(nil), list...)
	}
	for k, n := range pick {
		a.promptPick[k] = n
	}
	a.promptMu.Unlock()
	for _, d := range promptDefs {
		if s := strings.TrimSpace(legacy[d.key]); s != "" && len(styles[d.key]) == 0 {
			a.savePromptStyle(d.key, d.styleName(), s)
		}
	}
	// the box and the dropdown last, once the state they draw from is settled
	for _, d := range promptDefs {
		a.showPromptStyle(d.key, a.promptPickName(d.key))
	}
}

// promptEditor is the box a step page shows -- title, then the box, and no
// disclosure triangle: a prompt you cannot see is a prompt you forget you
// changed, and not knowing what the model was told is how a baffling result
// stays baffling. title says which job this prompt belongs to; tip carries the
// detail that would otherwise make the title a paragraph -- batch sizes and
// what else rides along are compiled in and visible nowhere else, so they have
// to be somewhere, just not somewhere that costs a line of the page.
func (a *App) promptEditor(key, title, tip string) gtk.Widgetter {
	d := promptDefFor(key)

	tv := gtk.NewTextView()
	tv.SetWrapMode(gtk.WrapWord)
	tv.SetMonospace(true)
	tv.SetTopMargin(4)
	tv.SetBottomMargin(4)
	tv.SetLeftMargin(6)
	tv.SetRightMargin(6)
	// Editing this box is what stops the project from tracking the shipped
	// wording -- a real consequence with nothing to show for it on screen, and
	// the whole reason there used to be a second "notes" box beside it. Say it
	// instead, right next to the button that undoes it.
	mark := gtk.NewLabel("")
	mark.AddCSSClass("dim-label")
	mark.SetTooltipText("Your wording is stored in the project, so a newer built-in " +
		"prompt will not replace it. Reset puts it back.")

	names := gtk.NewStringList(nil)
	pick := gtk.NewDropDown(names, nil)
	pick.SetTooltipText("Which wording this project uses for this job.\n" +
		"＋ copies what is in the box to a new name; the button beside it " +
		"undoes your edits to a built-in wording, or deletes one you added.")
	drop := gtk.NewButtonWithLabel("Reset")
	drop.AddCSSClass("flat")

	// the cache the runners read, refreshed on every keystroke: cheap at a few
	// kB, and it means no step has to remember to snapshot the box before it
	// goes async
	tv.Buffer().ConnectChanged(func() {
		b := tv.Buffer()
		s := b.Text(b.StartIter(), b.EndIter(), false)
		if a.promptQuiet {
			return // showPromptStyle is filling the box; it marks the row itself
		}
		a.setPrompt(key, s)
		a.markPromptRow(key)
	})
	if a.promptViews == nil {
		a.promptViews = map[string]*gtk.TextView{}
		a.promptRows = map[string]promptRow{}
	}
	a.promptViews[key] = tv
	a.promptRows[key] = promptRow{pick: pick, names: names, mark: mark, drop: drop}

	// Selecting is showing: the box follows the dropdown, and because every
	// keystroke has already been stored under the name being left, switching
	// away and back is lossless.
	//
	// pickPromptStyle, not showPromptStyle: this runs inside GtkDropDown's own
	// notify::selected, with the popup still closing, and rebuilding the list
	// model from there hangs the list view that is drawing it (see the note on
	// showPromptStyle). The name comes out of the model rather than out of a
	// freshly computed list, so it is the row the user actually clicked even if
	// the two ever drift.
	pick.NotifyProperty("selected", func() {
		if a.promptQuiet {
			return
		}
		if i := pick.Selected(); i < names.NItems() {
			a.pickPromptStyle(key, names.String(i))
		}
	})
	drop.ConnectClicked(func() {
		name := a.promptPickName(key)
		if a.shippedPromptStyle(key, name) {
			a.dropPromptStyle(key, name) // revert: the shipped wording is still called this
			a.showPromptStyle(key, name)
			return
		}
		a.confirm("Remove the “"+name+"” wording?",
			"It is stored in this project and nowhere else, so this is the only copy. "+
				"The box goes back to “"+d.styleName()+"”.",
			"Remove", func() {
				a.dropPromptStyle(key, name)
				a.showPromptStyle(key, d.styleName())
			})
	})

	add := gtk.NewButtonWithLabel("＋")
	add.AddCSSClass("flat")
	add.SetTooltipText("Save what is in the box under a new name")
	add.ConnectClicked(func() {
		a.askName("Name this wording", "It joins the list for "+title+" in this project.",
			func(name string) {
				b := tv.Buffer()
				a.savePromptStyle(key, name, b.Text(b.StartIter(), b.EndIter(), false))
				a.showPromptStyle(key, name)
			})
	})

	a.showPromptStyle(key, a.promptPickName(key))

	lbl := gtk.NewLabel(title)
	lbl.SetXAlign(0)
	lbl.SetHExpand(true)
	// Ellipsized, not wrapped. The heading row is one line high everywhere --
	// that is the premise the alignment in editorBody rests on -- and a title
	// that wraps in a column dragged narrow would make one page's rows taller
	// than another's. These titles are a word or two and the tooltip has the
	// rest, so there is nothing here worth a second line.
	lbl.SetEllipsize(pango.EllipsizeEnd)
	lbl.AddCSSClass("heading")
	if tip != "" {
		lbl.SetTooltipText(tip)
	}

	head := gtk.NewBox(gtk.OrientationHorizontal, 8)
	head.Append(lbl)
	head.Append(mark)
	head.Append(pick)
	head.Append(add)
	head.Append(drop)

	return a.editorBody(head, tv)
}

// shippedPromptStyle says whether a name is one of the built-ins for a job --
// which decides whether the button beside the dropdown reverts or deletes.
func (a *App) shippedPromptStyle(key, name string) bool {
	for _, s := range promptDefFor(key).builtins() {
		if s.Name == name {
			return true
		}
	}
	return false
}

// pickPromptStyle records a wording as this project's choice for the job and
// puts it in the box. It deliberately does not touch the dropdown's model --
// see showPromptStyle for why that matters, and why this is the half that runs
// when the dropdown itself is what changed.
//
// promptQuiet is what keeps it from being a loop: filling the box fires
// "changed", whose handler would otherwise save the incoming wording straight
// back out under the same name. GUI thread only, which is why a plain bool is
// enough.
func (a *App) pickPromptStyle(key, name string) {
	a.promptMu.Lock()
	if a.promptPick == nil {
		a.promptPick = map[string]string{}
	}
	a.promptPick[key] = name
	delete(a.promptTxt, key) // the box is about to say it; prompt() reads the style
	a.promptMu.Unlock()

	if tv, ok := a.promptViews[key]; ok {
		a.promptQuiet = true
		tv.Buffer().SetText(a.promptStyleText(key, name))
		a.promptQuiet = false
	}
	a.markPromptRow(key)
}

// showPromptStyle is pickPromptStyle plus the menu itself, for the places where
// the list of wordings may have changed under it: a project load, an added
// wording, a deleted one. Safe before the widgets exist, so applyPromptStyles
// does not have to care whether a page has been built yet.
//
// It must not be called from notify::selected. That signal is emitted from
// inside GtkDropDown while its popup is still closing, and replacing the model
// there leaves the list view drawing a list that no longer exists -- the app
// stops answering, with the wording already switched and the popup already
// gone, which is exactly what it looks like: a freeze on choosing a wording
// rather than on anything the choice did. Hence the split, and hence one
// Splice instead of a Remove-then-Append loop, which was N items-changed
// emissions where one will do.
func (a *App) showPromptStyle(key, name string) {
	a.pickPromptStyle(key, name)

	row, ok := a.promptRows[key]
	if !ok {
		return
	}
	list := a.promptStyleList(key)
	sel := 0
	fresh := make([]string, len(list))
	for i, s := range list {
		fresh[i] = s.Name
		if s.Name == name {
			sel = i
		}
	}
	a.promptQuiet = true
	// only when the names really moved: a plain switch does not change them,
	// and a model replaced for nothing is a menu that flickers and a selection
	// that bounces through every index on the way
	if !sameStrings(row.names, fresh) {
		row.names.Splice(0, row.names.NItems(), fresh)
	}
	if row.pick.Selected() != uint(sel) {
		row.pick.SetSelected(uint(sel))
	}
	a.promptQuiet = false
	// no markPromptRow here: pickPromptStyle already did it, and nothing the
	// menu does changes what that row says
}

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

// markPromptRow says what the project is holding on to for the picked wording,
// and relabels the button that lets go of it.
func (a *App) markPromptRow(key string) {
	row, ok := a.promptRows[key]
	if !ok {
		return
	}
	name := a.promptPickName(key)
	shipped := a.shippedPromptStyle(key, name)
	edited := false
	a.promptMu.Lock()
	for _, s := range a.promptSty[key] {
		if s.Name == name {
			edited = true
		}
	}
	a.promptMu.Unlock()

	switch {
	case !shipped:
		row.mark.SetText("added here")
		row.drop.SetLabel("Remove")
		row.drop.AddCSSClass("destructive-action")
		row.drop.SetSensitive(true)
		row.drop.SetTooltipText("Delete this wording from the project")
	case edited:
		row.mark.SetText("edited — kept in this project")
		row.drop.SetLabel("Reset")
		row.drop.RemoveCSSClass("destructive-action")
		row.drop.SetSensitive(true)
		row.drop.SetTooltipText("Put the built-in wording back")
	default:
		row.mark.SetText("")
		row.drop.SetLabel("Reset")
		row.drop.RemoveCSSClass("destructive-action")
		// nothing to undo: a live button here reads as "there is something
		// stored", which is exactly what the empty mark is denying
		row.drop.SetSensitive(false)
		row.drop.SetTooltipText("This is the built-in wording, unchanged")
	}
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
