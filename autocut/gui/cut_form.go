package main

// The Cut page's form column.
//
// It is the space the three prompt boxes used to fill. Every prompt is on
// Prepare now (prepedit.go), which left the right-hand half of the top row
// empty -- and that half is exactly the shape a form wants: a column,
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
// one down, and taking one down calls its gone, so a dialog that was waiting
// for an answer learns it will not get one.

import (
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotk4/pkg/pango"
)

// buildForm makes the column. With no form in it, it is the page's quiet
// corner: the view controls and the totals (formIdle).
//
// Three pieces, and only the middle one scrolls. The heading is pinned to the
// top and the buttons to the bottom, because a form whose Place button is below
// the fold is a form that cannot be placed: the column is as tall as the video
// beside it and no taller, a five-question form is taller than that, and a
// scrollbar in a panel that is mostly entry boxes is not something a hand
// reaches for. Pinned, the answer to "what do I press now" is always on screen.
func (ed *cutEditor) buildForm() *gtk.Box {
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

	// What the column holds when no form is open: the zoom and the thumbnail
	// size, and what the cut comes to (filled in by buildCut).
	//
	// It used to hold a paragraph explaining that forms open here, which is a
	// thing you learn the first time a form opens and read for the rest of the
	// project. The two pairs of buttons are the other half of the same trade:
	// they are set once and then left, so they do not belong on the bar with
	// Add, Split and Remove -- and this column is empty except while a form is
	// up, which is exactly when nobody is zooming the timeline.
	ed.formIdle = gtk.NewBox(gtk.OrientationVertical, 6)
	ed.formIdle.SetVExpand(true)

	ed.formBox = gtk.NewBox(gtk.OrientationVertical, 8)
	ed.formBox.Append(ed.formIdle)

	// one scrollbar for the column, and it is around the questions only.
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
	pane.SetVExpand(true)

	ed.formFoot = gtk.NewBox(gtk.OrientationVertical, 0)

	col := gtk.NewBox(gtk.OrientationVertical, 8)
	col.SetMarginTop(8)
	col.SetMarginBottom(8)
	col.SetMarginStart(6) // the handle's side; the preview beside it matches
	col.SetMarginEnd(12)  // ...and the window's
	col.Append(ed.formHead)
	col.Append(pane)
	col.Append(ed.formFoot)
	ed.hideForm() // the heading belongs to a form, and there is none yet
	return col
}

// idleRow is one line of the quiet column: what it is, then the thing itself.
// The readings used to be captions under the buttons they belonged to, in
// small print on a bar that had no room for them; named and stacked they are a
// column of numbers, which is a thing to read rather than a label on a control.
//
// Every line, including the ones whose "reading" is a control: the thumbnail
// buttons say how big the pictures are and the dropdown says what shape the
// video is, and a row with no name in a column of named rows is the row you
// have to work out.
func idleRow(name string, w gtk.Widgetter) *gtk.Box {
	l := gtk.NewLabel(name + ":")
	l.SetXAlign(0)
	l.AddCSSClass("dim-label")
	l.SetWidthChars(13) // one column for the names, whatever the readings measure
	l.SetVAlign(gtk.AlignCenter)
	row := gtk.NewBox(gtk.OrientationHorizontal, 6)
	row.Append(l)
	row.Append(w)
	return row
}

// idleRead is one of that column's readings: left-aligned, dim, and in the
// numeric face the clock uses, so a column of them lines up digit under digit.
func idleRead() *gtk.Label {
	l := gtk.NewLabel("")
	l.SetXAlign(0)
	l.AddCSSClass("dim-label")
	l.AddCSSClass("numeric")
	return l
}

// showFormFoot puts a form in the column, in place of whatever was there, with
// foot pinned under the scrolling part of it. Pass a nil foot for a form whose
// buttons are its own business.
func (ed *cutEditor) showFormFoot(title string, body, foot gtk.Widgetter, gone func()) {
	if ed.formBox == nil {
		return // headless: no page was built
	}
	ed.dropForm()
	ed.formTitle.SetText(title)
	ed.formHead.SetVisible(true)
	ed.formIdle.SetVisible(false)
	ed.formCur, ed.formGone = body, gone
	ed.formBox.Append(body)
	if foot != nil {
		ed.formFootCur = foot
		ed.formFoot.Append(foot)
	}
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
	// a live form's last burst of typing, before the form it belongs to is
	// taken out from under it: the debounce is a few hundred ms, and closing
	// the panel a moment after the last letter must not be how that letter is
	// lost. flush is a no-op when nothing is owed (debounce.go).
	if l := ed.fxLiveCur; l != nil {
		l.d.flush()
		ed.fxLiveCur, ed.fxLiveOn = nil, nil
	}
	if ed.formFootCur != nil {
		ed.formFoot.Remove(ed.formFootCur)
		ed.formFootCur = nil
	}
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
