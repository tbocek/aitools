package main

// The Cut page's form column.
//
// It is the space the three prompt boxes used to fill. They are behind the
// toolbar's dropdown now (promptpick.go), which left the right-hand half of the
// top row empty -- and that half is exactly the shape a form wants: a column,
// as tall as the video beside it, wide enough for a labelled entry and a line
// of explanation under it.
//
// So the dialogs moved in. Inserting a card asks six questions; a zoom asks
// five; both used to ask them in a modal window over the page, which is the one
// place a question about the footage cannot be asked -- the answer depends on
// what is under the window. Here the timeline, the preview and the lane the
// effect sits on all stay visible and stay live while the form is up. Nothing
// is modal: the form is a part of the page, and pressing something else on the
// page is allowed to take the column away from it.
//
// Which is what gone is for. Every form is built by whoever opens it and holds
// widgets this column has been handed; when the column is given to something
// else, that owner has to hear about it. Showing a second form takes the first
// one down, and taking one down calls its gone -- the prompt picker forgets its
// editor there (promptpick.go), and a dialog that was waiting for an answer
// learns it will not get one.

import (
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotk4/pkg/pango"
)

// buildForm makes the column. It opens empty, saying what it is for -- an empty
// panel with no explanation reads as a page that failed to load.
func (ed *cutEditor) buildForm() *gtk.ScrolledWindow {
	ed.formTitle = gtk.NewLabel("")
	ed.formTitle.SetXAlign(0)
	ed.formTitle.SetHExpand(true)
	ed.formTitle.AddCSSClass("heading")
	ed.formTitle.SetEllipsize(pango.EllipsizeEnd)

	shut := gtk.NewButtonFromIconName("window-close-symbolic")
	shut.AddCSSClass("flat")
	shut.SetTooltipText("close this form — nothing is lost that was not already saved")
	shut.ConnectClicked(func() { ed.hideForm() })

	ed.formHead = gtk.NewBox(gtk.OrientationHorizontal, 6)
	ed.formHead.Append(ed.formTitle)
	ed.formHead.Append(shut)

	ed.formIdle = gtk.NewLabel("The prompts this step sends, and the forms its buttons " +
		"ask with, open here — beside the timeline rather than over it, so what a " +
		"question is about stays on screen while it is answered.")
	ed.formIdle.SetXAlign(0)
	ed.formIdle.SetYAlign(0)
	ed.formIdle.SetWrap(true)
	ed.formIdle.SetVExpand(true)
	ed.formIdle.AddCSSClass("dim-label")

	ed.formBox = gtk.NewBox(gtk.OrientationVertical, 8)
	ed.formBox.SetMarginTop(10)
	ed.formBox.SetMarginBottom(12)
	ed.formBox.SetMarginStart(12)
	ed.formBox.SetMarginEnd(12)
	ed.formBox.Append(ed.formHead)
	ed.formBox.Append(ed.formIdle)
	ed.hideForm() // the heading belongs to a form, and there is none yet

	// one scrollbar for the column: whatever is in it is given its full height
	// by this viewport, so a form never scrolls against this one.
	//
	// Not an overlay scrollbar, though it is the default: an overlay is drawn on
	// top of whatever is under it, and what is under it here is the right-hand
	// border of a framed box. A slider sitting on a box's own border, and with
	// no border of its own, is what a column of these looks wrong as. Given its
	// own gutter it is beside the boxes instead of on them.
	pane := gtk.NewScrolledWindow()
	pane.SetChild(ed.formBox)
	pane.SetPolicy(gtk.PolicyNever, gtk.PolicyAutomatic)
	pane.SetOverlayScrolling(false)
	return pane
}

// showForm puts a form in the column, in place of whatever was there.
func (ed *cutEditor) showForm(title string, body gtk.Widgetter, gone func()) {
	if ed.formBox == nil {
		return // headless: no page was built
	}
	ed.dropForm()
	ed.formTitle.SetText(title)
	ed.formHead.SetVisible(true)
	ed.formIdle.SetVisible(false)
	ed.formCur, ed.formGone = body, gone
	ed.formBox.Append(body)
}

// hideForm empties the column and puts its own words back.
func (ed *cutEditor) hideForm() {
	if ed.formBox == nil {
		return
	}
	ed.dropForm()
	ed.formTitle.SetText("")
	ed.formHead.SetVisible(false)
	ed.formIdle.SetVisible(true)
}

// dropForm takes the current form out and tells its owner, in that order: gone
// is where widgets are let go of, and letting go of a widget that is still in
// the box is how a dropdown ends up pointing at a text view nobody can see.
func (ed *cutEditor) dropForm() {
	if ed.formCur != nil {
		ed.formBox.Remove(ed.formCur)
		ed.formCur = nil
	}
	if g := ed.formGone; g != nil {
		ed.formGone = nil
		g()
	}
}

// cutForm is where a Cut-page dialog puts itself. Nil before the page is built,
// which is every headless test and nowhere else -- a form with nowhere to go is
// not shown at all rather than opened in a window, because everything that asks
// for one is a button on this page and cannot be pressed until it exists.
func (a *App) cutForm() *cutEditor {
	if a.ed == nil || a.ed.formBox == nil {
		return nil
	}
	return a.ed
}
