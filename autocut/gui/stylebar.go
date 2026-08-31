package main

// The Style dropdown on the Cut bar, and the ✎ that says a project has changed
// a prompt.
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
// And the wording list for the cut, because it is not a prompt-editing control
// at all: which KIND of cut ▶ builds -- highlights, a rating, a Short -- is the
// choice made before every suggest run, and burying it in the bench three levels
// from the button that acts on it was the wrong depth for it.

import (
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// styleDrop is a wording dropdown that lives on the page itself rather than in
// the bench. The bench's own list is on another tab, which is the right place
// for REWORDING a prompt and the wrong one for a choice made before every run:
// which KIND of cut ▶ builds (highlights, a rating, a Short). styleBar
// surfaces that list where it can be seen; both dropdowns write through
// pickPromptStyle, which lands a pick in whichever of the two exists
// (syncStylePicks), and showPromptStyle refreshes both lists when the
// wordings themselves change.
type styleDrop struct {
	names *gtk.StringList
	pick  *gtk.DropDown
}

// styleBar builds the surfaced dropdown: a dim label and the wordings the
// registry offers for key, the project's own included. A pick here is exactly
// a pick in the editor -- the stored choice, the ✎ marks and the Shorts
// target correction (styleTarget) all follow.
func (a *App) styleBar(key, label, tip string) *gtk.Box {
	d := styleDrop{names: gtk.NewStringList(nil), pick: nil}
	d.pick = gtk.NewDropDown(d.names, nil)
	d.pick.SetTooltipText(tip)
	if a.styleDrops == nil {
		a.styleDrops = map[string]styleDrop{}
	}
	a.styleDrops[key] = d
	// pickPromptStyle, not showPromptStyle, for the same reason the editor's
	// dropdown uses it: this fires with the popup still closing, and splicing
	// the model under a closing popup hangs the view (see showPromptStyle)
	d.pick.NotifyProperty("selected", func() {
		if a.promptQuiet {
			return
		}
		if i := d.pick.Selected(); i < d.names.NItems() {
			a.pickPromptStyle(key, d.names.String(i))
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

// promptOwned is whether anything of your own is being said about a prompt:
// a wording it edited or invented, or a shipped wording other than the default
// picked. Either is worth the ✎ -- both change what the model is told, and
// neither is visible anywhere else while another row is shown.
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

// syncPromptMarks redraws the ✎ on the bench's menu. One call site's worth of
// indirection, kept because markPromptRow runs long before the page is built
// and must not have to know that.
func (a *App) syncPromptMarks() {
	if a.prepSync != nil {
		a.prepSync()
	}
}
