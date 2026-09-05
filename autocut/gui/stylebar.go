package main

// The Style dropdown on Prepare's bottom row, and the ✎ that says a project
// has changed a prompt.
//
// Every step page used to show its system prompts in full, permanently: two on
// Prepare, three on Cut, one each on Narrate and Publish. That cost every page a
// column for something read once and then left alone, so the boxes went behind a
// per-page dropdown, and then off the pages entirely -- every prompt is one menu
// on Prepare now (prepedit.go), which is also where a project is set up.
//
// Two things did not follow them, and both are here.
//
// The ✎, because "this project says something of its own here" is the one part
// worth a permanent pixel wherever a prompt is named, and it is one glyph rather
// than a column.
//
// And the Style, because it is not a prompt-editing control at all: which KIND
// of video ▶ builds -- highlights, a rating, a Short -- is a choice about the
// project, and burying it in the bench three levels deep was the wrong depth
// for it. It sat on the Cut bar for a while; it is one dropdown on Prepare's
// bottom row now, after Language, and it is the ONLY place a wording is picked:
// choosing a style turns every prompt to its wording of that name, or to its
// default when it has none (applyStyle). The bench edits wordings; it no
// longer chooses between them.

import (
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// styleDrop is the Style dropdown on the page. Its list is the cut's wordings
// -- the cut is the job every style ships one for, and saving a cut wording
// under a new name is how a new style is born -- and a pick is applied to
// every prompt at once (applyStyle). The bench's menu shows the result: each
// row named with the wording the style gave it (prepEditNames).
type styleDrop struct {
	names *gtk.StringList
	pick  *gtk.DropDown
}

// styleBar builds the surfaced dropdown: a dim label and the wordings the
// registry offers for key, this machine's own included. A pick here is the
// project's style -- every prompt's stored choice, the bench menu's labels
// all follow.
func (a *App) styleBar(key, label, tip string) *gtk.Box {
	d := styleDrop{names: gtk.NewStringList(nil), pick: nil}
	d.pick = gtk.NewDropDown(d.names, nil)
	d.pick.SetTooltipText(tip)
	if a.styleDrops == nil {
		a.styleDrops = map[string]styleDrop{}
	}
	a.styleDrops[key] = d
	// applyStyle is selection-only underneath (pickPromptStyle, never
	// showPromptStyle): this fires with the popup still closing, and splicing
	// the model under a closing popup hangs the view (see showPromptStyle)
	d.pick.NotifyProperty("selected", func() {
		if a.promptQuiet {
			return
		}
		if i := d.pick.Selected(); i < d.names.NItems() {
			a.applyStyle(d.names.String(i))
		}
	})
	a.showPromptStyle(key, a.promptPickName(key)) // fill the list, land on the stored pick

	lbl := gtk.NewLabel(label)
	lbl.AddCSSClass("dim-label")
	box := gtk.NewBox(gtk.OrientationHorizontal, 6)
	box.SetVAlign(gtk.AlignCenter)
	box.Append(lbl)
	box.Append(d.pick)
	return box
}

// promptOwned is whether this machine has a wording of its own stored under
// the name the job is picked to: an edit of a shipped wording, or one it
// invented. WHICH wording is picked stopped being worth the ✎ when the pick
// went into every row's name (prepEditNames) -- the mark is kept for the one
// thing the name cannot say, that what the model reads is not what shipped.
func (a *App) promptOwned(key string) bool {
	name := a.promptPickName(key)
	a.promptMu.Lock()
	defer a.promptMu.Unlock()
	for _, s := range a.promptSty[key] {
		if s.Name == name {
			return true
		}
	}
	return false
}

// syncPromptMarks redraws the ✎ on the bench's menu. One call site's worth of
// indirection, kept because markPromptRow runs long before the page is built
// and must not have to know that.
func (a *App) syncPromptMarks() {
	if a.prepSync != nil {
		a.prepSync()
	}
}
