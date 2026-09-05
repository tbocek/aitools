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
type promptStyle struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

// promptRow is the widgets above one prompt box: the label that says what this
// machine is holding for the shown wording, and the button that lets go of it.
// The wording itself is not picked here any more -- the Style dropdown on
// Prepare's bottom row turns every job at once (applyStyle).
type promptRow struct {
	mark *gtk.Label
	drop *gtk.Button
}

// promptDef is one editable prompt. A blurb explaining what changing it would
// do used to sit between the heading and the box; the prompt itself says that
// better than a paragraph above it can, and the paragraph was what made these
// pages a wall of prose.
type promptDef struct {
	key, def string
	// the other wordings shipped for the same job; def is the one called
	// defStyle. Style names are stored on disk and in project.json exactly as
	// they read here, so renaming one orphans an edit of it the same way
	// renaming a key would -- add a style, do not rename one. legacyDefStyle is
	// what undoing that costs, for the one rename there has been.
	alts []promptStyle
	// solo is a prompt that has no wordings at all: one text, the same under
	// every style. The system context is the only one -- it is the formats the
	// material and the answers are in, which a style has no opinion about --
	// and being solo is what keeps a wording name off its bench row, a ＋ off
	// its heading and a style pick off its store.
	solo bool
}

// defStyle names the wording a job ships with when it ships only one. It is the
// cut's generic wording by the same name on purpose: the Style beside Language
// turns every job to the wording called what it is set to, and a job that has
// only its shipped one is still the general answer to that job. A bench reading
// "Cut (General), Describe (Default)" made the one state look like two, which is
// the whole thing the parentheticals are there to say.
const defStyle = "General"

// legacyDefStyle is what defStyle was called before that. It is still on disk
// wherever a machine edited the shipped wording of a job -- prompts/fix/
// Default.txt -- and still in any project written then, and it means what
// defStyle means now, so both are read as it (styleAlias). Left unhandled the
// edit comes back as a second wording nothing picks, sitting in the dropdown
// under the old name.
const legacyDefStyle = "Default"

// styleName is what a job's shipped wording is called in the dropdown, and it
// is defStyle for every job: the wording you get before you have picked a shape
// is the general one, whether or not the job ships any other. A job could name
// its own -- the cut did, spelling "General" out a second time -- and two copies
// of the fallback's name is exactly the drift that put "Cut (General)" beside
// "Describe (Default)". There is one copy now.
func (promptDef) styleName() string { return defStyle }

// styleAlias reads a name that came from outside -- a file on disk, a name in a
// project -- as one of this job's own. Only the rename above is undone, and only
// where the job does not ship a wording that really is called that; anything
// else is a wording somebody invented and keeps the name they gave it.
func (d promptDef) styleAlias(name string) string {
	if name != legacyDefStyle {
		return name
	}
	for _, s := range d.builtins() {
		if s.Name == legacyDefStyle {
			return name
		}
	}
	return d.styleName()
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
	// first, because it goes in front of every one of the others: the formats
	// and the house rules they all work to (syscontext.go). Solo: there is one
	// of it, and picking Highlights or Showcase does not change how a stamp
	// reads.
	{key: "system", def: strings.TrimSpace(sysSystem), solo: true},
	{key: "describe", def: strings.TrimSpace(describeSystem)},
	{key: "fix", def: strings.TrimSpace(fixSystem)},
	// five wordings, the generic one first: def, so it is called defStyle like
	// every other job's, it is what a project that has never picked gets, and it
	// is the only one that does not already believe it knows what the footage is
	// (see genericSystem). The four after it are what the Style dropdown offers
	// beyond it, since the dropdown's list is this job's (styleBar).
	{key: "cut", def: strings.TrimSpace(genericSystem),
		alts: []promptStyle{{"Highlights", strings.TrimSpace(suggestSystem)},
			{"Rating / tier list", strings.TrimSpace(ratingSystem)},
			{"Showcase", strings.TrimSpace(showcaseSystem)},
			{shortsStyleName, strings.TrimSpace(shortsSystem)}}},
	// the two passes that follow the cut, clip by clip: what was said, on
	// screen; and the zooms, stops and volume. They were one reply with the
	// segments, and the one reply is what kept failing (cut_suggest.go).
	//
	// "audit" was here: a second long call that read the suggestion back
	// against the same brief and moved its boundaries. It was worth having
	// when the cut was one reply doing three jobs; against a cut that is only
	// segments it spent ten minutes to move a border a few seconds, and its
	// own schema could not say the one thing it kept noticing. Removed, not
	// renamed -- a project's edited copy is a dead key nobody reads.
	{key: "captions", def: strings.TrimSpace(captionSystem)},
	{key: "speed", def: strings.TrimSpace(speedSystem)},
	{key: "effects", def: strings.TrimSpace(effectsSystem)},
	// "effects" was here: the third call that decorated the audited cut. The
	// effects ride the cut reply again -- every style's, see fxRules -- and
	// the audit checks them, so the key is gone the way "thumbnail" below
	// went: a project's edited copy is a dead key nobody reads.
	// two wordings, and the second is the same craft about a different
	// subject: a showcase wants a voice about the thing on the table, where
	// the default wants one about what is happening (see narrShowcaseSystem).
	{key: "narrate", def: strings.TrimSpace(narrSystem),
		alts: []promptStyle{{"Showcase", strings.TrimSpace(narrShowcaseSystem)}}},
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

// applyPromptStyles takes in what a project has to say about the prompts --
// which, now that they are the machine's (promptstore.go), is only ever what a
// project written before that has to say. It ADOPTS rather than replaces: a
// wording lands only where this machine has nothing of its own for the job,
// and a pick only where the job is still on the shipped default.
//
// It used to be a full switch, everything the project did not mention going
// back to the built-in, because the prompts were the project's and leaving the
// last project's wording in a box would have been the worst kind of bug --
// invisible, and it changes what the model writes. With the prompts kept per
// machine the bug is the other way round: an old project opened for five
// minutes must not overwrite the wording four videos were tuned with. On the
// launch that first opens a pre-merge project this machine has nothing to say
// about any job, so that project's wordings are adopted whole and nothing is
// lost either.
//
// legacy is the pre-styles storage, where a project held one edited prompt per
// key and no name for it. Those belong to whichever style was default at the
// time, which is the default now: the shipped wording of a job did not move,
// it only gained company.
func (a *App) applyPromptStyles(styles map[string][]promptStyle, pick map[string]string, legacy map[string]string) {
	for _, d := range promptDefs {
		if a.promptStored(d.key) {
			continue // this machine's own wording; a project does not overwrite it
		}
		for _, s := range styles[d.key] {
			a.savePromptStyle(d.key, d.styleAlias(s.Name), s.Text)
		}
		if s := strings.TrimSpace(legacy[d.key]); s != "" && len(styles[d.key]) == 0 {
			a.savePromptStyle(d.key, d.styleName(), s)
		}
		if n := pick[d.key]; n != "" {
			a.pickPromptStyle(d.key, d.styleAlias(n))
		}
	}
	// the box and the dropdown last, once the state they draw from is settled
	for _, d := range promptDefs {
		a.showPromptStyle(d.key, a.promptPickName(d.key))
	}
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
	a.rememberPromptPick(key, name) // one short name: straight to the file, no tick

	if tv, ok := a.promptViews[key]; ok {
		a.promptQuiet = true
		tv.Buffer().SetText(a.promptStyleText(key, name))
		a.promptQuiet = false
	}
	a.markPromptRow(key)
	a.syncStylePicks(key, name)
}

// applyStyle makes name the whole project's style. One dropdown, beside
// Language: picking "Highlights" there turns every job to the wording called
// Highlights where it has one -- shipped, or saved on this machine under that
// name -- and to its shipped default where it does not. The User Context is
// not in promptDefs and is not touched: it is this session's facts, not a
// wording.
//
// Selection-only underneath (pickPromptStyle), so it is safe from the Style
// dropdown's own notify::selected.
func (a *App) applyStyle(name string) {
	for _, d := range promptDefs {
		if d.solo {
			continue // one wording, and no style has anything to say about it
		}
		target := d.styleName()
		for _, s := range a.promptStyleList(d.key) {
			if s.Name == name {
				target = name
				break
			}
		}
		a.pickPromptStyle(d.key, target)
	}
}

// syncStylePicks lands a pick in the dropdown that surfaces this key's wording
// on a page (styleBar). Selection only, never the list: this runs inside that
// dropdown's own notify::selected handler, where replacing the model hangs the
// view drawing the closing popup (see showPromptStyle), and setting a
// selection that is already set is a no-op.
func (a *App) syncStylePicks(key, name string) {
	bar, ok := a.styleDrops[key]
	if !ok {
		return
	}
	for i := uint(0); i < bar.names.NItems(); i++ {
		if bar.names.String(i) != name {
			continue
		}
		if bar.pick.Selected() != i {
			a.promptQuiet = true
			bar.pick.SetSelected(i)
			a.promptQuiet = false
		}
		return
	}
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

	bar, ok := a.styleDrops[key]
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
	if !sameStrings(bar.names, fresh) {
		bar.names.Splice(0, bar.names.NItems(), fresh)
	}
	if bar.pick.Selected() != uint(sel) {
		bar.pick.SetSelected(uint(sel))
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
	// the ✎ in every picker's menu first, and unconditionally: it says the same
	// thing this row does -- "the project is holding something here" -- and it
	// is the only place that says it once the boxes are behind a button, so it
	// cannot depend on a box being open (prepedit.go).
	a.syncPromptMarks()

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
		row.drop.SetTooltipText("Delete this wording from this machine")
	case edited:
		row.mark.SetText("edited — kept in your settings")
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
