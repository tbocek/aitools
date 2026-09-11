package main

// Running the whole pipeline in one press.
//
// Each step is its own goroutine that ends on the GUI thread through endRun,
// so a chain is a LIST that endRun advances: the step that finished says what
// became of it, and the next is started or the chain is abandoned. Nothing
// about a step has to know it is in one.
//
// The steps are the pages, in the order the pipeline runs them, because that
// is the only order that makes sense: the cut reads what Prepare wrote, the
// narration is written over the cut, and the render needs both.

import (
	"fmt"
	"strings"

	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// the steps a chain can hold, in pipeline order: the page each runs on, and
// what to call it in the menu.
var chainSteps = []struct{ page, name string }{
	{"prep", "Prepare"},
	{"cut", "Cut"},
	{"narrate", "Narrate"},
	{"produce", "Produce"},
}

// chainRun starts a chain: the ticked steps, in order, from the first one on.
func (a *App) chainRun() {
	if a.busy() {
		return
	}
	want := a.chainPicked()
	if len(want) == 0 {
		a.setStatus("nothing ticked beside ▶ — tick the steps to run")
		return
	}
	a.chain = want
	a.logf(">>> run: %s", strings.Join(a.chainNames(want), " → "))
	a.chainNext()
}

// chainNext takes the next step off the chain and runs it, on the page it
// belongs to: a step's own ▶ works on the widgets of its page, and a page that
// was never shown has none.
func (a *App) chainNext() {
	for len(a.chain) > 0 {
		page := a.chain[0]
		a.chain = a.chain[1:]
		if page == "narrate" && a.narrOff {
			a.logf(">>> run: Narrate skipped — this video has no narration")
			continue
		}
		// Cut refuses over hand edits, and in a chain that refusal would be a
		// halt nobody asked for: the edits are the answer, so the step has
		// nothing to do and the rest of the chain still does.
		if page == "cut" && a.ed != nil && len(a.ed.segs) > 0 && !sameCut(a.ed.segs, a.ed.base.segs) {
			a.logf(">>> run: Cut skipped — the cut has hand edits, which are kept")
			continue
		}
		a.stack.SetVisibleChildName(page)
		a.logf(">>> run: %s", a.pageName(page))
		a.runPage(page)
		if !a.running {
			// the step declined -- nothing to do, or it refused -- so the
			// chain has to carry itself rather than wait for an endRun that
			// is not coming
			continue
		}
		return
	}
	a.chain = nil
}

// chainDone is endRun's half: the step that just finished either hands the
// chain on or ends it. Stopped or failed, the rest is abandoned -- a chain
// exists to save presses, not to carry on past the answer going wrong.
func (a *App) chainDone() {
	if len(a.chain) == 0 {
		return
	}
	if a.stopFlag.Load() {
		a.logf(">>> run: stopped — %d step(s) left undone", len(a.chain))
		a.chain = nil
		return
	}
	a.chainNext()
}

// runPage is one step, the same call the page's own ▶ makes.
func (a *App) runPage(page string) {
	switch page {
	case "prep":
		a.prepRun()
	case "cut":
		if a.ed != nil {
			a.suggestClicked()
		}
	case "narrate":
		a.narrateRun()
	case "produce":
		a.produceClicked()
	}
}

func (a *App) pageName(page string) string {
	for _, s := range chainSteps {
		if s.page == page {
			return s.name
		}
	}
	return page
}

func (a *App) chainNames(pages []string) []string {
	var out []string
	for _, p := range pages {
		out = append(out, a.pageName(p))
	}
	return out
}

// chainPicked is the steps ticked, in pipeline order.
func (a *App) chainPicked() []string {
	var out []string
	for _, s := range chainSteps {
		if t := a.chainTicks[s.page]; t != nil && t.Active() {
			out = append(out, s.page)
		}
	}
	return out
}

// buildChainMenu is the ticks beside ▶ and the button that runs them. A menu
// rather than a second ▶: which steps is a standing answer, not a choice made
// on every press, and the button says which are on with the menu shut.
func (a *App) buildChainMenu() *gtk.Box {
	pop := gtk.NewPopover()
	list := gtk.NewBox(gtk.OrientationVertical, 4)
	list.SetMarginTop(6)
	list.SetMarginBottom(6)
	list.SetMarginStart(10)
	list.SetMarginEnd(10)
	a.chainTicks = map[string]*gtk.CheckButton{}
	for _, s := range chainSteps {
		s := s
		c := gtk.NewCheckButtonWithLabel(s.name)
		c.SetActive(true)
		c.ConnectToggled(func() {
			a.syncChain()
			if !a.chainQuiet {
				a.saveProjectNow()
			}
		})
		a.chainTicks[s.page] = c
		list.Append(c)
	}
	pop.SetChild(list)

	a.chainPick = gtk.NewMenuButton()
	a.chainPick.SetPopover(pop)
	a.chainPick.SetTooltipText("Which steps ▶▶ runs, in this order. Narrate skips itself " +
		"when the video has no narration, and Cut skips itself when the cut has hand edits.")
	a.chainBtn = gtk.NewButtonFromIconName("media-seek-forward-symbolic")
	a.chainBtn.SetTooltipText("Run the ticked steps, one after another")
	a.chainBtn.ConnectClicked(a.chainRun)

	// one control in two halves, the split button's shape: press the left for
	// the thing, press the arrow for which thing. GTK4 has no AdwSplitButton
	// without libadwaita, and does not need one -- a linked box of a button
	// and a menu button IS that control, and is what the tab row and the
	// stepper on this window are already made of (widgets.go).
	box := linked(a.chainBtn, a.chainPick)
	a.syncChain()
	return box
}

// syncChain puts the answer on the button.
func (a *App) syncChain() {
	if a.chainPick == nil {
		return
	}
	n := len(a.chainPicked())
	switch n {
	case 0:
		a.chainPick.SetLabel("none")
	case len(chainSteps):
		a.chainPick.SetLabel("all")
	default:
		a.chainPick.SetLabel(fmt.Sprintf("%d steps", n))
	}
}

// applyChain is a project's answer, put on the ticks without saving it back.
// A project written before this has none, and everything is ticked -- which is
// what "run it all" means and what a new project gets.
func (a *App) applyChain(pages []string) {
	if a.chainTicks == nil {
		a.chainSteps = pages
		return
	}
	a.chainQuiet = true
	if pages == nil {
		for _, t := range a.chainTicks {
			t.SetActive(true)
		}
	} else {
		on := map[string]bool{}
		for _, p := range pages {
			on[p] = true
		}
		for page, t := range a.chainTicks {
			t.SetActive(on[page])
		}
	}
	a.chainQuiet = false
	a.syncChain()
}
