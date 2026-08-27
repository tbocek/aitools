package main

// The prompts, one dropdown instead of a wall of boxes.
//
// Every step page used to show its system prompts in full, permanently: two on
// Describe, three on Cut, one each on Narrate and Publish. The reasoning was
// that a prompt you cannot see is a prompt you forget you changed -- true, and
// it cost every page a column. Cut gave up the whole right-hand half of its top
// row to three boxes that a working session never touches; Narrate gave up the
// width its caption lines wanted; Describe was two prompts and nothing else.
//
// A prompt is read once, edited rarely and then left alone for the rest of the
// project. So it is a dropdown naming the prompts this page sends, and a button
// that opens the one that is picked -- the same promptEditor as before, with
// its wording list, its ＋ and its Reset, only not while you are working.
//
// What is NOT hidden is that a prompt has been changed: an edited one, or one
// switched to a wording that is not the shipped default, reads with a ✎ in the
// menu. That is the part worth a permanent pixel -- "this project says
// something of its own here" -- and it is one glyph rather than a column.
//
// Where the editor appears depends on the page. Cut has a column for forms
// (cut.go), so it goes there; the others have nowhere to put one, so they open
// a window. Both are the same widget and the same storage: typing in it writes
// through setPrompt to the picked wording, and project.json keeps whatever
// differs from what the build ships (prompts.go).

import (
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// promptSlot is one prompt a page can open: the registry key, what the menu
// calls it, and the detail that would otherwise make the name a sentence.
type promptSlot struct{ key, title, tip string }

// formHost is somewhere a page can put a form. Cut implements it with the
// column its prompts used to fill; a page that does not have one leaves the
// field nil and the picker opens a window instead.
//
// title is what the form is called, and body is the whole of it. Showing a
// second form replaces the first: there is one column, and two forms fighting
// over it is worse than either. Which is why gone is not something the caller
// can be told by watching -- the host calls it when the form is taken down,
// whether that was the Close button, another form, or the page moving on, and
// the caller lets go of its widgets there.
//
// One method, because putting a form up is the only thing a picker asks of a
// page: taking one down again is the host's own business, and the form itself
// carries the button that does it.
type formHost interface {
	showForm(title string, body gtk.Widgetter, gone func())
}

// promptPicker is the dropdown and its button, held so the ✎ marks can be
// redrawn when a project is loaded or a wording is edited.
type promptPicker struct {
	a     *App
	slots []promptSlot
	names *gtk.StringList
	pick  *gtk.DropDown
	host  formHost // nil: the editor opens in a window
	// which key this picker currently has open in the host, so closing the form
	// forgets the right editor. Windows look after themselves.
	open string
}

// promptBar builds the control. slots is what the page sends, in the order it
// sends it -- reading down the menu is reading the run.
//
// host may be nil, in which case Edit opens a window. A page with a form column
// passes itself and gets the editor in the column, which is the whole reason
// the Cut page has one.
func (a *App) promptBar(host formHost, slots ...promptSlot) *gtk.Box {
	p := &promptPicker{a: a, slots: slots, host: host}
	p.names = gtk.NewStringList(nil)
	p.pick = gtk.NewDropDown(p.names, nil)
	p.pick.SetTooltipText("The prompts this step sends. ✎ marks one this project " +
		"has something of its own to say about — an edited wording, or a different " +
		"one picked. Edit opens it.")

	edit := gtk.NewButtonWithLabel("Edit…")
	edit.AddCSSClass("flat")
	edit.SetTooltipText("Open the picked prompt")
	edit.ConnectClicked(func() { p.openPicked() })
	// the menu is also a way in: picking the prompt you want to read and then
	// pressing a second button is one press more than the choice needs
	p.pick.NotifyProperty("selected", func() {
		if p.open != "" { // a form is up: follow the menu rather than wait
			p.openPicked()
		}
	})

	lbl := gtk.NewLabel("Prompts")
	lbl.AddCSSClass("dim-label")

	box := gtk.NewBox(gtk.OrientationHorizontal, 6)
	box.SetVAlign(gtk.AlignCenter)
	box.Append(lbl)
	box.Append(p.pick)
	box.Append(edit)

	a.promptPickers = append(a.promptPickers, p)
	p.sync()
	return box
}

// slotAt is the picked row, or the first one if the selection is somehow off the
// end -- a menu that cannot answer "which prompt" is worse than one that answers
// with the first.
func (p *promptPicker) slotAt(i uint) promptSlot {
	if int(i) < len(p.slots) {
		return p.slots[i]
	}
	return p.slots[0]
}

func (p *promptPicker) openPicked() {
	if len(p.slots) == 0 {
		return
	}
	s := p.slotAt(p.pick.Selected())
	// already in the column: Edit on the prompt you are looking at is a press
	// with nothing to do, and rebuilding it here would be worse than nothing --
	// the fresh editor registers under the same key as the one it replaces, and
	// then the old one's closing would unregister the new one.
	if p.host != nil && p.open == s.key {
		return
	}
	body := p.a.promptEditor(s.key, s.title, s.tip)
	if p.host == nil {
		p.a.promptWindow(s, body)
		return
	}
	// after showForm, never before. Putting this form up takes the last one
	// down, and taking it down calls closed, which forgets whatever p.open
	// names -- so p.open has to still name the OUTGOING editor while that runs.
	p.host.showForm(s.title+" prompt", body, p.closed)
	p.open = s.key
}

// closed is what the host calls when the column is given to something else. The
// editor's widgets are about to be dropped, so the registry that points the
// dropdown and the project loader at them has to let go too -- otherwise
// showPromptStyle spends the rest of the session filling a box nobody can see.
func (p *promptPicker) closed() {
	if p.open != "" {
		p.a.forgetPromptEditor(p.open)
		p.open = ""
	}
	p.sync()
}

// promptWindow is the editor for pages with nowhere to put one. Big by default,
// because a system prompt is thirty lines and a dialog that shows six of them
// makes editing one a scrolling exercise.
func (a *App) promptWindow(s promptSlot, body gtk.Widgetter) {
	win := gtk.NewWindow()
	win.SetTransientFor(&a.win.Window)
	win.SetModal(true)
	win.SetTitle(s.title + " prompt")
	win.SetDefaultSize(760, 560)

	close := gtk.NewButtonWithLabel("Done")
	close.AddCSSClass("suggested-action")
	close.ConnectClicked(func() { win.Close() })
	btns := gtk.NewBox(gtk.OrientationHorizontal, 8)
	btns.SetHAlign(gtk.AlignEnd)
	btns.Append(close)

	box := gtk.NewBox(gtk.OrientationVertical, 8)
	box.SetMarginTop(12)
	box.SetMarginBottom(12)
	box.SetMarginStart(12)
	box.SetMarginEnd(12)
	box.Append(body)
	box.Append(btns)
	win.SetChild(box)

	// Everything is already saved -- the box writes through on every keystroke
	// (promptEditor) -- so there is no OK to press and nothing to lose by
	// closing. What closing does have to do is let go of the widgets, so a
	// later project load does not fill a window that is gone.
	win.ConnectCloseRequest(func() bool {
		a.forgetPromptEditor(s.key)
		a.syncPromptPickers()
		return false
	})
	win.SetVisible(true)
}

// sync redraws the menu labels. A ✎ means the project holds something of its
// own for that prompt; the tooltip says which of the two it is.
func (p *promptPicker) sync() {
	if p.pick == nil {
		return
	}
	fresh := make([]string, len(p.slots))
	for i, s := range p.slots {
		fresh[i] = s.title
		if p.a.promptOwned(s.key) {
			fresh[i] += " ✎"
		}
	}
	sel := p.pick.Selected()
	if !sameStrings(p.names, fresh) {
		p.names.Splice(0, p.names.NItems(), fresh)
	}
	if int(sel) < len(fresh) {
		p.pick.SetSelected(sel) // Splice resets it; the picked row does not move
	}
}

// promptOwned is whether this project says anything of its own about a prompt:
// a wording it edited or invented, or a shipped wording other than the default
// picked. Either is worth the ✎ -- both change what the model is told, and
// neither is visible anywhere else once the boxes are behind a button.
func (a *App) promptOwned(key string) bool {
	name := a.promptPickName(key)
	if name != promptDefFor(key).styleName() {
		return true
	}
	a.promptMu.Lock()
	defer a.promptMu.Unlock()
	for _, s := range a.promptSty[key] {
		if s.Name == name {
			return true
		}
	}
	return false
}

func (a *App) syncPromptPickers() {
	for _, p := range a.promptPickers {
		p.sync()
	}
}

// forgetPromptEditor drops a closed editor's widgets from the registry the
// project loader and the dropdown write through. Without it, showPromptStyle
// keeps filling a text view whose window has been destroyed -- harmless in GTK,
// but it also means markPromptRow relabels a button nobody will ever see, and
// the next editor for the same key would be the second one registered rather
// than the one on screen.
func (a *App) forgetPromptEditor(key string) {
	delete(a.promptViews, key)
	delete(a.promptRows, key)
}
