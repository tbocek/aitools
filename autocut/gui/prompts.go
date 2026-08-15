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

// promptDef is one editable prompt. A blurb explaining what changing it would
// do used to sit between the heading and the box; the prompt itself says that
// better than a paragraph above it can, and the paragraph was what made these
// pages a wall of prose.
type promptDef struct {
	key, def string
}

// Keys name the prompt in project.json and are therefore permanent -- renaming
// one silently drops what a user wrote under the old name.
var promptDefs = []promptDef{
	{"describe", strings.TrimSpace(describeSystem)},
	{"fix", strings.TrimSpace(fixSystem)},
	{"cut", strings.TrimSpace(suggestSystem)},
	{"audit", strings.TrimSpace(auditSystem)},
	{"narrate", strings.TrimSpace(narrSystem)},
	// "thumbnail" was here: a second Publish prompt that picked which frame to
	// edit and wrote the instruction for it. Removed, not renamed -- the key is
	// gone from the registry, so a project that saved an edited copy of it just
	// keeps a dead key nobody reads.
	{"youtube", strings.TrimSpace(youtubeSystem)},
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
// the built-in. Callable from a step runner's goroutine -- it reads a cached
// string, never the GtkTextBuffer, which belongs to the GUI thread.
func (a *App) prompt(key string) string {
	a.promptMu.Lock()
	s := strings.TrimSpace(a.promptTxt[key])
	a.promptMu.Unlock()
	if s != "" {
		return s
	}
	return promptDefFor(key).def
}

func (a *App) setPrompt(key, text string) {
	a.promptMu.Lock()
	defer a.promptMu.Unlock()
	if a.promptTxt == nil {
		a.promptTxt = map[string]string{}
	}
	a.promptTxt[key] = text
}

// currentPrompts is what the project stores: only what differs from the
// built-in, so an untouched project keeps tracking the shipped wording.
func (a *App) currentPrompts() map[string]string {
	out := map[string]string{}
	for _, d := range promptDefs {
		if s := a.prompt(d.key); s != d.def {
			out[d.key] = s
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// applyPrompts loads a project's prompts. A key the project does not mention
// goes back to the built-in: loading a project is a full switch, and leaving
// the previous project's wording in a box would be the worst kind of bug --
// invisible, and it changes what the model writes.
func (a *App) applyPrompts(m map[string]string) {
	for _, d := range promptDefs {
		s := strings.TrimSpace(m[d.key])
		if s == "" {
			s = d.def
		}
		a.setPrompt(d.key, s)
		if tv := a.promptViews[d.key]; tv != nil {
			tv.Buffer().SetText(s)
		}
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
		"prompt will not replace it. Reset to default puts it back.")

	// the cache the runners read, refreshed on every keystroke: cheap at a few
	// kB, and it means no step has to remember to snapshot the box before it
	// goes async
	tv.Buffer().ConnectChanged(func() {
		b := tv.Buffer()
		s := b.Text(b.StartIter(), b.EndIter(), false)
		a.setPrompt(key, s)
		if strings.TrimSpace(s) == d.def {
			mark.SetText("")
		} else {
			mark.SetText("edited — kept in this project")
		}
	})
	tv.Buffer().SetText(d.def)
	if a.promptViews == nil {
		a.promptViews = map[string]*gtk.TextView{}
	}
	a.promptViews[key] = tv

	reset := gtk.NewButtonWithLabel("Reset to default")
	reset.AddCSSClass("flat")
	reset.SetTooltipText("Put the built-in prompt back")
	reset.ConnectClicked(func() { tv.Buffer().SetText(d.def) })

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
	head.Append(reset)

	return a.editorBody(head, tv)
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
