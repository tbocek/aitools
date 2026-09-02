package main

// The prompt bench: one box on Prepare that holds everything the models are
// told.
//
// The prompts used to be spread over the pages that send them -- two here, two
// behind a dropdown on Cut, one on Narrate, one on Publish -- each page with its
// own picker, its own Edit button, and its own way of showing the editor (a
// column on Cut, a window everywhere else). That put a prompt next to the run
// that sends it, which sounds right and was not: a prompt is read once, edited
// before the first run, and then left alone for the whole project, while the
// pages it sat on are where the actual work happens. So every page paid, all
// session, for a control used in the first ten minutes.
//
// They are all here now, in the order the pipeline sends them, behind one menu
// in one box. Prepare is where a project is set up -- the sources, the language,
// what the editor knows -- and setting up what the models are told is the same
// job at the same moment. It also puts the whole chain in one list: reading down
// the menu is reading the run, which is a thing no page could show while each
// page owned one prompt.
//
// The context is the first row and it is not a prompt: it is what the editor
// knows about THIS session, and every request carries it (context.go). It comes
// first because it is the one row a session actually has to write, and it sits
// among the prompts because writing it beside them is what stops it being
// written INTO them.
//
// One editor, one registration. Because this box is the only place a prompt is
// shown, promptViews/promptRows hold exactly the row on screen -- there is no
// second editor for the same key to fight with, which is what the old
// open/closed/forget dance existed to prevent.

import (
	"fmt"

	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotk4/pkg/pango"
)

// prepRow is one row of the menu: what it is called, which store it reads and
// writes (key "" is the session context), and the detail that would otherwise
// make the name a sentence.
type prepRow struct{ menu, key, tip string }

// title is what the heading says while the row is shown. The menu names the
// job; the heading says which of the two kinds of text this is, because "Cut"
// on its own over a box of rules reads like the cut itself.
func (r prepRow) title() string {
	if r.key == "" {
		return r.menu
	}
	return r.menu + " prompt"
}

// prepRows is the menu, in the order the pipeline sends them: this page's two
// jobs, then the cut and its audit, then the narration, then the upload text.
//
// Written as a function rather than a var because the tips quote sizes that are
// constants elsewhere, and a var initialised from those reads as if the numbers
// were free to change at runtime.
func prepRows() []prepRow {
	return []prepRow{
		{"User Context", "", "Who is in this session, what they were doing, how names are " +
			"spelled, what to make sure ends up in the video.\n\nSent with every request " +
			"this project makes: the frame describer, the transcript fixer, the cut and " +
			"its audit, the narration and the upload text. Left empty, nothing is sent."},
		{"System context", "system", "The formats every job works to: the three kinds " +
			"of line, which clock a request stamps them on, and that the answer is read " +
			"by a machine.\n\nSent in front of every prompt below, so a fact about this " +
			"tool is written once instead of in each of them. It has no wordings: the " +
			"formats are the same whichever style the video is cut in."},
		{"Describe", "describe", fmt.Sprintf(
			"%d frames per request, plus the last %d descriptions and up to %d spoken "+
				"lines either side as context. No frame is ever sent twice: those "+
				"descriptions are the model's only memory of what it already saw.",
			framesPerReq, recentEvents, ctxSegs)},
		{"Transcript", "fix", fmt.Sprintf(
			"The fixer: %d transcript lines per request, each block given what every "+
				"other source showed or said at the same moment.", fixBlock)},
		{"Cut", "cut", "The rules ▶ Suggest works to, plus what this session was and " +
			"what matters in it. Its wordings are the styles: the Style dropdown under " +
			"the sources picks between them, for every prompt at once."},
		{"Audit", "audit", "How the suggestion is read back: what counts as ending too " +
			"early, and how readily a segment is dropped."},
		{"Narration", "narrate", "The rules the narration is written to, plus what this " +
			"session was and what matters in it."},
		{"Upload text", "youtube", "Gets the cut and the narration — no images — and " +
			"answers with the YouTube title, the thumbnail instruction and the description."},
	}
}

// prepEditNames is the menu's rows, each prompt named with the wording the
// Style turned it to -- "Cut (Highlights)" -- because the choice is made once,
// beside Language, and this menu is where its reach over every job has to be
// readable. The ✎ still marks a wording this machine reworded (promptOwned).
// The context wears neither: it is this session's own text, with no wording to
// name and nothing built-in to differ from.
func (a *App) prepEditNames() []string {
	rows := prepRows()
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.menu
		if r.key == "" {
			continue
		}
		if !promptDefFor(r.key).solo {
			out[i] += " (" + a.promptPickName(r.key) + ")"
		}
		if a.promptOwned(r.key) {
			out[i] += " ✎"
		}
	}
	return out
}

// prepEditor is the right-hand half of the Prepare page: one box, and in its
// heading the menu for what the box shows plus the controls that belong to a
// prompt -- save this as a new wording, put the built-in back. Which wording
// the box shows is not chosen here: the Style dropdown on the bottom row sets
// it for every job at once (applyStyle), and the menu's row names say what it
// chose.
//
// There is nothing to save before switching away: every keystroke writes
// through, to the context cache or through setPrompt to the picked wording. And
// nothing to close: the box IS the editor, so a project load, a wording added
// and a wording deleted all land in the same place.
//
// Still editorBody, like every text box on a step page: same font, same frame,
// same floor, same heading height (see prompts.go).
func (a *App) prepEditor() gtk.Widgetter {
	rows := prepRows()

	tv := gtk.NewTextView()
	tv.SetWrapMode(gtk.WrapWord)
	tv.SetMonospace(true)
	tv.SetTopMargin(4)
	tv.SetBottomMargin(4)
	tv.SetLeftMargin(6)
	tv.SetRightMargin(6)

	lbl := gtk.NewLabel(rows[0].title())
	lbl.SetXAlign(0)
	lbl.SetHExpand(true)
	lbl.SetEllipsize(pango.EllipsizeEnd) // one line, like every other heading row
	lbl.AddCSSClass("heading")

	// Editing a prompt is what stops the project from tracking the shipped
	// wording -- a real consequence with nothing to show for it on screen, and
	// the whole reason there used to be a second "notes" box beside it. Say it
	// instead, right next to the button that undoes it. markPromptRow writes
	// this; the context row has no built-in to differ from, so it stays empty.
	mark := gtk.NewLabel("")
	mark.AddCSSClass("dim-label")
	mark.SetTooltipText("Your wording is kept in ~/.config/autocut/prompts, so a newer " +
		"built-in prompt will not replace it. Reset puts it back.")

	add := gtk.NewButtonWithLabel("＋")
	add.AddCSSClass("flat")
	add.SetTooltipText("Save what is in the box as a new wording. The Style " +
		"dropdown finds wordings by name: saved for the cut, a new name is a " +
		"new style; saved for another job under a style's name, it is what " +
		"that style sends for the job.")
	drop := gtk.NewButtonWithLabel("Reset")
	drop.AddCSSClass("flat")

	cur := 0
	quiet := false

	// showCtx is the heading with the prompt controls taken off it. The context
	// has one wording by definition -- the one you wrote -- so a ＋ and a Reset
	// would each be a control with nothing to do, and a greyed row of them says
	// "this row is broken" rather than "this row is different".
	showCtx := func(r prepRow) {
		prompt := r.key != ""
		mark.SetVisible(prompt)
		drop.SetVisible(prompt)
		// ＋ saves what is in the box as a NEW wording, which only means
		// something for a job that HAS wordings. The system context is one
		// text under every style, so a second one of it would be a name
		// nothing ever picks -- Reset stays, because putting the built-in back
		// is exactly as useful here as anywhere else.
		add.SetVisible(prompt && !promptDefFor(r.key).solo)
	}

	tv.Buffer().ConnectChanged(func() {
		if quiet || a.promptQuiet {
			return // show below, or showPromptStyle, is filling the box
		}
		b := tv.Buffer()
		s := b.Text(b.StartIter(), b.EndIter(), false)
		if r := rows[cur]; r.key == "" {
			a.setSessionCtx(s)
		} else {
			a.setPrompt(r.key, s)
			a.markPromptRow(r.key)
		}
	})

	// show puts row i in the box. Registration follows the selection: the box
	// stands in promptViews/promptRows only while it shows that prompt, and is
	// a.ctxView only while it shows the context, so a project load fills
	// whichever store changed without clobbering what is actually on screen --
	// the box rereads its store on the next switch anyway.
	show := func(i int) {
		if i < 0 || i >= len(rows) {
			i = 0
		}
		if old := rows[cur]; old.key != "" {
			delete(a.promptViews, old.key)
			delete(a.promptRows, old.key)
		}
		cur = i
		r := rows[i]
		lbl.SetText(r.title())
		lbl.SetTooltipText(r.tip)
		showCtx(r)
		quiet = true
		if r.key == "" {
			a.ctxView = tv
			tv.Buffer().SetText(a.sessionCtx())
			quiet = false
			return
		}
		a.ctxView = nil
		if a.promptViews == nil {
			a.promptViews = map[string]*gtk.TextView{}
		}
		if a.promptRows == nil {
			a.promptRows = map[string]promptRow{}
		}
		a.promptViews[r.key] = tv
		a.promptRows[r.key] = promptRow{mark: mark, drop: drop}
		quiet = false
		// fills the box and the mark from the store -- which is why switching
		// away and back is lossless however much was typed in between
		a.showPromptStyle(r.key, a.promptPickName(r.key))
	}

	add.ConnectClicked(func() {
		r := rows[cur]
		if r.key == "" {
			return
		}
		a.askName("Name this wording", "Kept on this machine, and what "+r.menu+
			" sends from now on. The Style dropdown finds wordings by name: for the "+
			"cut a new name is a new style, and for any other job a style's name is "+
			"what that style sends here.",
			func(name string) {
				b := tv.Buffer()
				a.savePromptStyle(r.key, name, b.Text(b.StartIter(), b.EndIter(), false))
				a.showPromptStyle(r.key, name)
				if r.key == "cut" {
					// a new cut wording is a new style, and saving one is
					// picking it -- the rest of the prompts have to turn with
					// it, exactly as the dropdown would have turned them
					a.applyStyle(name)
				}
			})
	})
	drop.ConnectClicked(func() {
		r := rows[cur]
		if r.key == "" {
			return
		}
		name := a.promptPickName(r.key)
		if a.shippedPromptStyle(r.key, name) {
			a.dropPromptStyle(r.key, name) // revert: the shipped wording is still called this
			a.showPromptStyle(r.key, name)
			return
		}
		a.confirm("Remove the “"+name+"” wording?",
			"It is in your settings folder and nowhere else, so this is the only copy. "+
				"The box goes back to “"+promptDefFor(r.key).styleName()+"”.",
			"Remove", func() {
				a.dropPromptStyle(r.key, name)
				a.showPromptStyle(r.key, promptDefFor(r.key).styleName())
				if r.key == "cut" {
					// the removed wording was a style; a project cannot stay
					// on a style that no longer exists, so every prompt turns
					// back to the default the box just went to
					a.applyStyle(promptDefFor(r.key).styleName())
				}
			})
	})

	menu := gtk.NewStringList(nil)
	pick := gtk.NewDropDown(menu, nil)
	pick.SetTooltipText("What the box shows. The context is this session's facts, " +
		"sent with every request; the rest are the prompts, in the order the " +
		"pipeline sends them, each named with the wording the Style turned it " +
		"to. ✎ marks a wording edited on this machine.")
	pick.NotifyProperty("selected", func() {
		if a.promptQuiet {
			return // prepSync is redrawing the rows; the pick did not move
		}
		if i := pick.Selected(); i < menu.NItems() && int(i) != cur {
			show(int(i))
		}
	})
	// the ✎ redraw, reached through syncPromptMarks. Quiet around the splice:
	// Splice resets the selection, and the notify above must not read that
	// reset as the user switching rows.
	a.prepSync = func() {
		fresh := a.prepEditNames()
		if sameStrings(menu, fresh) {
			return
		}
		sel := pick.Selected()
		a.promptQuiet = true
		menu.Splice(0, menu.NItems(), fresh)
		if int(sel) < len(fresh) {
			pick.SetSelected(sel) // the picked row did not move, only its label
		}
		a.promptQuiet = false
	}
	a.prepSync()
	show(0) // the context first: the row that is this session's own

	head := gtk.NewBox(gtk.OrientationHorizontal, 8)
	head.Append(lbl)
	head.Append(mark)
	head.Append(pick)
	head.Append(add)
	head.Append(drop)
	return a.editorBody(head, tv)
}
