package main

// The thumbnail half of the Produce page -- once a Publish step of its own.
// The two things a finished video still needs before anyone can watch it: a
// thumbnail, and the text under it on the YouTube page.
//
// The thumbnail is usually an EDIT of a real frame of the session, which is
// the whole reason this talks to sd.cpp's native API rather than its
// OpenAI-shaped one (sdcpp.go): ref_images is what keeps the picture
// recognizably THIS video instead of a stock illustration of the genre.
//
// "Usually", because the row of images is a list the user owns -- add to it,
// remove from it, promote any of them to the front. The first is the picture
// being edited and the rest are references the instruction can name by
// position; an empty row is allowed and falls back to drawing from the
// instruction alone.
//
// The distinction matters more than it sounds. This page used to run img2img
// against Krea-2 -- one init_image and a strength dial -- and img2img has no
// way to say "change this and leave that": strength renoises the whole frame
// and resamples it, so a green ghost came back as purple mush and there was no
// value that did not do it. An edit model is given an instruction and touches
// only what the instruction names. It also renders legible lettering, so the
// title is part of the instruction rather than something ffmpeg burns on
// afterwards, and it takes any number of references, so an instruction may
// borrow from any of the others.
//
// One model job, and its prompt lives on Prepare with all the others
// (prepedit.go) -- this page shows only the two boxes that describe THIS
// picture, because they are about the frame in front of you rather than about
// the video. The job writes the title, the edit instruction and the
// description in one reply.
//
// The instruction used to be typed and only typed. A separate job once picked
// a frame AND wrote an instruction for it, and it was removed because it did
// neither well -- the pick was guesswork and the instruction was worse than
// one you would have typed. What is different now is that the job already
// reads the whole session to write the title: asking the same reply for one
// more line costs nothing, it knows what the clips actually contain, and it
// starts the box off with something to edit instead of an empty field.
//
// What comes back is a suggestion in an editable field, never a decision --
// the title, the instruction and the description are all yours to rewrite, and
// ▶ pressed again redraws from what the boxes say rather than asking again.
//
// The page is two columns. Left is the picture and everything that makes it:
// the images, the edit instruction, the negative prompt, the result. Right is
// the words: the title and the description. They are worked on separately --
// rewording the instruction and redrawing has nothing to do with the
// description -- so they do not share a column and fight for its height.
//
// step6/thumbnail.png        the upload
// step6/thumbnail-plain.png  the same picture, kept so a failed re-roll does
//                            not lose the one before it
// step6/description.txt      the YouTube description
// step6/publish.json         all of it as data, beside the files

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotk4/pkg/pango"
)

const (
	// How many frames the FIRST run takes from the cut. Only a starting point:
	// the row is a list you add to and remove from, so a session can end up
	// with none, one, or several. Two is what a first run is worth -- enough
	// for the model to have a choice of base image, few enough that both can
	// be shown at a size where you can actually tell whether the subject
	// survives being shrunk.
	defPubFrames = 2

	// And how many the row will hold. Not a technical limit -- sd.cpp takes as
	// many references as you send it -- but every image here is also attached
	// to the vision call that picks the base, where each one costs context and
	// makes the choice woollier. Eight is past the point where more helps.
	maxPubFrames = 8
)

// ---- the prompts --------------------------------------------------------------
//
// One paragraph or bullet per line, unwrapped: see describeSystem.

const youtubeSystem = `You write the upload text for a finished gaming video on YouTube: its title, and the description that sits under it.

You are given what the video is made of -- its clips, what was seen and said in each, and the narration that was written over it. Write about the video that exists, and invent nothing that is not in it.

The request may open with a block headed ABOUT THIS SESSION: the editor's own notes, written by someone who was there. Every name, spelling and fact in it outranks what you would otherwise have guessed, and what it singles out is what the description should lead with.

Return three parts in this order, with a blank line between them: the title on one line prefixed exactly "TITLE: ", the thumbnail instruction on one line prefixed exactly "THUMBNAIL: ", then the description text itself. No JSON, no code fence, no heading, no commentary about the task.

The title.

- Four to seven words. It is both the YouTube title and the lettering printed across the thumbnail, and it is read at the size of a phone's sidebar, so every extra word costs one that mattered.
- Say the specific thing that happens in THIS video: the moment, the mistake, the win, the thing nobody expected. A title that would fit any session of this game is a wasted title.
- Plain words people say out loud. No colons splitting a subtitle off, no clickbait punctuation, no ALL CAPS -- it is drawn in large letters already.
- Never promise something the clips do not contain.

The thumbnail instruction.

- One or two sentences on a single line, telling an image model what to CHANGE about a frame taken from this video so that it reads as a thumbnail.
- It is an instruction, not a description. Anything you do not mention is left alone, so describing the whole scene gets a picture of something else instead of the moment that was filmed. Say what to brighten, blur, push forward, or clear out of the way.
- Name only things the clips contain. "Add the dragon" to a video with no dragon in it is a thumbnail that lies about the video.
- Ask for no text, no lettering, no title and no logo. The title is appended to your instruction automatically as its own sentence, and asking for it here prints it twice.

The description.

- Open with one or two sentences that say what happens in this video, in plain language, and make someone want to watch it. This first line is the only part shown before "...more", so it has to work alone.
- Then a short paragraph, three or four sentences, on what the session actually was: where it is set, who is in it, what went right and wrong.
- Then a chapter list if the video has distinct beats -- one line each, "0:00 What this is", in the video's own running time counted from its start. Only if the beats are real; a made-up timestamp is worse than no chapter list.
- Finish with a line of five to eight hashtags, lower case, naming the game, the genre and the kind of moment. No hashtag salad.

Voice.

- The voice of someone who was there and is telling a friend about it, not a press release. Contractions are fine.
- No emoji walls, no "smash that like button", no promises about upload schedules, no links to things you were not told exist.
- Never claim a person, a game mode or an outcome the clips do not show.`

// ---- what the project keeps -----------------------------------------------------

// pubSettings is the Publish page as the project file stores it. Everything
// here is either a decision the user made or a suggestion they let stand;
// nothing is derived, because a derived value in a project file is a value
// that goes stale silently.
//
// Frames are stored root-relative when they can be, like every other path in
// the project (relToRoot) -- the whole point is that moving the autocut folder
// moves the session with it.
type pubSettings struct {
	// The images, in the order the image model is given them. The FIRST is the
	// base -- the picture being edited -- and the rest are there to be referred
	// to ("the ship from the second image"). Order is the whole answer, which
	// is why there is no index beside it any more: a list and a pointer into it
	// are two places for the same fact, and they drift.
	Frames []string `json:"frames,omitempty"`

	// Which is what this is, and why it is only ever read. Projects written
	// before the row became a list carry the base as an index; migrate() turns
	// one into the other, and nothing writes it again.
	Base int `json:"base,omitempty"`

	// Where the thumbnail's frame sits on the base image: the centre of the
	// crop box, in fractions of that image (publish_crop.go). A pointer, and
	// nil is the middle -- the box that has never been dragged -- because 0,0
	// is a legitimate place to drag one TO and a project written before the
	// box existed must not read as "pushed into the top-left corner".
	Crop *pubPoint `json:"crop,omitempty"`

	Title    string `json:"title,omitempty"`
	Prompt   string `json:"prompt,omitempty"`
	Negative string `json:"negative,omitempty"`
	Desc     string `json:"description,omitempty"`
}

// pubPoint is a place on a picture, as fractions of it.
type pubPoint struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// cropRect is the crop box this project asks for on a base image of aspect
// srcA, for a thumbnail of aspect outA. Everything about its size comes from
// the two aspects; only the centre is the user's.
func (st pubSettings) cropRect(srcA, outA float64) fxRect {
	cx, cy := 0.5, 0.5
	if st.Crop != nil {
		cx, cy = st.Crop.X, st.Crop.Y
	}
	return pubCropAt(srcA, outA, cx, cy)
}

// basePath is the picture being edited, or "" when the row is empty -- which is
// allowed: with no images at all the instruction is drawn from nothing, which
// is a thumbnail some sessions actually want.
func (st pubSettings) basePath() string {
	if len(st.Frames) == 0 {
		return ""
	}
	return st.Frames[0]
}

// migrate brings a project written against the old fixed pair -- two slots and
// a radio saying which was the base -- up to the list. The radio's answer is
// applied by moving that frame to the front, where the answer now lives.
func (st pubSettings) migrate() pubSettings {
	st.Frames = moveToFront(append([]string(nil), st.Frames...), st.Base)
	st.Base = 0
	return st
}

// moveToFront makes fs[i] the base. Out-of-range is a no-op rather than an
// error: i comes from a project file and from a model's reply, and neither is
// trustworthy enough to panic on.
func moveToFront(fs []string, i int) []string {
	if i <= 0 || i >= len(fs) {
		return fs
	}
	out := append([]string{fs[i]}, fs[:i]...)
	return append(out, fs[i+1:]...)
}

// ---- the page ------------------------------------------------------------------

type publisher struct {
	a *App

	// The images and the row of widgets showing them. frames is the state --
	// the widgets are rebuilt from it whenever it changes, rather than being
	// mutated in place. With a row whose length changes, rebuilding is both
	// shorter and the only version that cannot leave a stale slot behind.
	frames    []string
	framesBox *gtk.Box
	addFrame  *gtk.Button

	// where the thumbnail's frame sits on the base image, and the shape that
	// frame is. crop is nil until the box has been dragged, which is the same
	// "never chosen" the project file stores; aspect is the cut's, cached by
	// reread because the crop box is redrawn on every pointer move and reading
	// cut.json per frame is not a thing to do in a draw handler.
	crop   *pubPoint
	aspect string

	title *gtk.Entry
	// The four editable boxes, split by column: prompt and neg are the drawing
	// side and are typed by hand, title and desc are the writing side and are
	// what the model suggests. Text views rather than entries for the long
	// ones -- an edit instruction is a paragraph and a description is several.
	prompt, neg, desc *gtk.TextView
	shot              *gtk.Picture // what was drawn last
	out               *gtk.Label
	suggest           *gtk.Button
}

// pubSlot is one image in the row: which position it is in, what is in it, and
// the three things you can do to it -- promote it to base, swap it, drop it.
// Built fresh on every rebuild, so it holds no widgets worth keeping.
type pubSlot struct {
	p    *publisher
	i    int
	path string
}

func (a *App) publishDir() string { return filepath.Join(a.outDir, "step6") }

// publishRecorded reports whether the model has already written this session's
// text. publish.json is that record: writePublishFiles lays it down as soon as
// the words exist and before anything is drawn, so a draw that fails still
// leaves the thinking paid for.
//
// It is deliberately a file on disk and not a flag in the project. The project
// is what the boxes say and the user may edit it to nothing; the folder is what
// the session has produced, and removing it is the one gesture that means start
// this step over -- the same gesture that already resets every other step.
func (a *App) publishRecorded() bool {
	return exists(filepath.Join(a.publishDir(), "publish.json"))
}

// buildPublishPanes is the thumbnail-and-words half of the Produce page, in
// parts: the drawing column (the images, the instruction, the picture they
// make), the written column (the title and the description), and the row
// saying what is on disk. It was a page of its own -- the Publish step -- and
// it is handed back in pieces now because Produce owns the page: the drawing
// goes left, the words top right above the encoder settings, and one ▶ makes
// everything the upload needs. No Inputs row of its own any more either --
// the page's row (Produce's) already says what both halves read.
func (a *App) buildPublishPanes() (draw, said, outs gtk.Widgetter) {
	p := &publisher{a: a}
	a.pub = p

	// The images, side by side and in order. A row rather than a column because
	// the first question asked of them is which one is the base, and that is a
	// question you answer by looking at them together.
	p.framesBox = gtk.NewBox(gtk.OrientationHorizontal, 8)
	p.framesBox.SetHomogeneous(true)
	p.rebuildFrames()

	p.addFrame = gtk.NewButtonWithLabel("Add image…")
	p.addFrame.AddCSSClass("flat")
	p.addFrame.SetTooltipText("Add another image the instruction can refer to — a frame from the " +
		"session, a logo, anything on disk. The first in the row is the one being edited; " +
		"the rest are only there to be named (\"the ship from the second image\")")
	p.addFrame.ConnectClicked(func() { p.addImage() })

	framesHead := p.heading("Images", fmt.Sprintf("What the image model is given, in order. The FIRST is "+
		"the base — the picture being edited — and the others are references the instruction can name. "+
		"%d are taken from the cut the first time this page runs; after that the row is yours: add, "+
		"remove, swap, or make another one the base. An empty row is allowed, and draws the thumbnail "+
		"from the instruction alone.", defPubFrames), p.addFrame)

	// The two long boxes on the drawing side. Both are text views rather than
	// entries: an instruction is a paragraph, and a negative prompt is a list
	// that outgrows one line the moment a picture goes wrong in a new way.
	var promptBox, negBox, descBox *gtk.ScrolledWindow
	p.prompt, promptBox = p.textBox(4, "What the image model is told to CHANGE about the first image. "+
		"Written by the same call that writes the title, and yours to rewrite. "+
		"Plain sentences: \"blur the background\", \"add the ship from the second image behind them\". "+
		"Anything you do not mention is left alone, so describing the whole scene gets you a different one. "+
		"The title is added after this automatically — ask for it here and it is lettered twice.")
	p.neg, negBox = p.textBox(2, "What must stay out of the picture — watermarks, logos, extra limbs. "+
		"Not \"text\" any more: the title is lettering this model is being asked for on purpose.")

	// The result, at the size it will be judged at, under the boxes that make
	// it -- so pressing ▶ after rewording the instruction shows the change in
	// the same place you asked for it.
	p.shot = gtk.NewPicture()
	p.shot.SetCanShrink(true)
	p.shot.SetSizeRequest(-1, 320)
	p.shot.SetVExpand(true)
	shotFrame := videoFrame(p.shot)
	shotFrame.SetMarginTop(4)

	// LEFT: everything that makes the picture, in the order it happens --
	// choose the images, say what to change, say what to keep out, look at what
	// came back. Nothing on this side calls the language model: the instruction
	// arrives from the one call the words' side makes, and is then this side's.
	col := gtk.NewBox(gtk.OrientationVertical, 6)
	col.SetMarginTop(4)
	col.SetMarginStart(12)
	col.SetMarginEnd(6)
	col.Append(framesHead)
	col.Append(p.framesBox)
	col.Append(p.heading("Edit instruction", "What to change about the first image, sent to sd.cpp with the whole row — ▶ writes one, and it is yours to rewrite"))
	col.Append(promptBox)
	col.Append(p.heading("Negative prompt", "What must not appear"))
	col.Append(negBox)
	col.Append(shotFrame)

	drawScroll := gtk.NewScrolledWindow()
	drawScroll.SetChild(col)
	drawScroll.SetPolicy(gtk.PolicyNever, gtk.PolicyAutomatic)
	drawScroll.SetVExpand(true)

	// RIGHT: everything the language model writes, with the prompt that writes
	// it directly above -- the same arrangement as Describe, for the same
	// reason: what the model was told is not something to go looking for behind
	// a disclosure triangle, and its answer sitting under it is what makes an
	// edit to the wording something you can judge.
	//
	// The title lives here rather than beside the drawing even though it is
	// lettered into the picture, because this is what writes it and this is
	// where it is read from: it is the YouTube title first and the thumbnail's
	// lettering second.
	p.title = gtk.NewEntry()
	p.title.SetHExpand(true)
	p.title.SetPlaceholderText("the video's title, also printed on the thumbnail — ▶ suggests one")
	p.title.SetTooltipText("The YouTube title, and the words the image model letters into the " +
		"picture — it is asked for them as its own sentence, so retyping the title does not mean " +
		"rewriting the instruction. Four to seven words: a thumbnail is read at the size of a " +
		"phone's sidebar. Empty means no lettering at all.")

	p.desc, descBox = p.textBox(8, "The text under the video on the YouTube page. Written by the "+
		"prompt above, and yours to rewrite.")

	// ↻ sits on the Title heading because the title is the first thing it
	// rewrites -- it is one call and it writes all three, the instruction on the
	// other side of the page included -- and it is the only thing that rewrites
	// them: ▶ writes them once and then never touches them again
	p.suggest = gtk.NewButtonWithLabel("Suggest again")
	p.suggest.AddCSSClass("flat")
	p.suggest.SetTooltipText("Ask the model for a fresh title, thumbnail instruction and " +
		"description — the only thing that does. ▶ never rewrites text that has already been " +
		"written, and nothing here redraws the picture; ▶ does that")
	p.suggest.ConnectClicked(func() { a.publishSuggest() })

	wrote := gtk.NewBox(gtk.OrientationVertical, 6)
	wrote.SetMarginTop(4)
	wrote.SetMarginStart(6)
	wrote.Append(p.heading("Title", "The YouTube title, and the lettering on the thumbnail", p.suggest))
	wrote.Append(p.title)
	wrote.Append(p.heading("YouTube description", "The text under the video on the upload page"))
	wrote.Append(descBox)
	descBox.SetVExpand(true)
	wrote.SetVExpand(true)

	openOut := gtk.NewButtonFromIconName("folder-open-symbolic")
	openOut.SetTooltipText("step6/ — the thumbnail, the title and the description")
	openOut.ConnectClicked(func() { a.openFolder(a.publishDir()) })
	p.out = gtk.NewLabel("")
	outRow := gtk.NewBox(gtk.OrientationHorizontal, 8)
	outRow.Append(openOut)
	outRow.Append(p.out)

	p.refresh()
	return drawScroll, wrote, outRow
}

// heading is the one-line label above each field, with anything the caller
// wants on its right. It joins the same size group every prompt box's heading
// row is in, so the fields on this side of the divider line up with the prompts
// on the other.
func (p *publisher) heading(title, tip string, extra ...gtk.Widgetter) *gtk.Box {
	l := gtk.NewLabel(title)
	l.SetXAlign(0)
	l.SetHExpand(true)
	l.SetEllipsize(pango.EllipsizeEnd)
	l.AddCSSClass("heading")
	l.SetTooltipText(tip)
	row := gtk.NewBox(gtk.OrientationHorizontal, 8)
	row.Append(l)
	for _, w := range extra {
		row.Append(w)
	}
	if p.a.headGroup == nil {
		p.a.headGroup = gtk.NewSizeGroup(gtk.SizeGroupVertical)
	}
	p.a.headGroup.AddWidget(row)
	return row
}

// textBox is one of the editable result fields, floored at lines of text.
func (p *publisher) textBox(lines int, tip string) (*gtk.TextView, *gtk.ScrolledWindow) {
	tv := gtk.NewTextView()
	tv.SetWrapMode(gtk.WrapWord)
	tv.SetTopMargin(4)
	tv.SetBottomMargin(4)
	tv.SetLeftMargin(6)
	tv.SetRightMargin(6)
	tv.SetTooltipText(tip)
	sc := gtk.NewScrolledWindow()
	sc.SetChild(tv)
	sc.SetPolicy(gtk.PolicyNever, gtk.PolicyAutomatic)
	sc.SetSizeRequest(-1, lines*18+8)
	sc.AddCSSClass("frame")
	return tv, sc
}

// setFrames replaces the row. Everything that changes the list goes through
// here -- a user gesture and apply alike -- so one place keeps the widgets,
// the cap and the Inputs line in step. Nothing flags the edit beyond that:
// the project is bytes-compared by the autosave. The cap lives here rather
// than on the Add button so that it also holds for a hand-edited project
// file: the row is what gets attached to the vision call, and twenty images
// is a call that costs a fortune and answers worse.
func (p *publisher) setFrames(fs []string) {
	p.frames = append([]string(nil), fs...)
	if len(p.frames) > maxPubFrames {
		p.frames = p.frames[:maxPubFrames]
	}
	p.rebuildFrames()
	p.a.updateProduceInfo() // the page's Inputs row counts these images
}

// rebuildFrames throws the row away and builds it again from p.frames. Cheaper
// to reason about than editing it in place: a row whose length changes has to
// renumber every slot after the one that moved anyway, and "Ref 3" left over
// beside the second image is the kind of wrong that is never noticed.
func (p *publisher) rebuildFrames() {
	if p.framesBox == nil {
		return
	}
	for c := p.framesBox.FirstChild(); c != nil; c = p.framesBox.FirstChild() {
		p.framesBox.Remove(c)
	}
	if len(p.frames) == 0 {
		// an empty row is a legitimate state, not an error, so it says what it
		// will do rather than looking like something failed to load
		empty := gtk.NewLabel("No image — the thumbnail will be drawn from the instruction alone.\n" +
			"Add image… to edit one of your own frames instead.")
		empty.AddCSSClass("dim-label")
		empty.SetJustify(gtk.JustifyCenter)
		empty.SetVExpand(true)
		p.framesBox.Append(empty)
	}
	for i, f := range p.frames {
		s := &pubSlot{p: p, i: i, path: f}
		p.framesBox.Append(s.build())
	}
	if p.addFrame != nil {
		p.addFrame.SetSensitive(len(p.frames) < maxPubFrames)
	}
}

// addImage appends one. New images go to the END, never the front: appending
// cannot silently change which picture is being edited, and promoting is one
// click away for when that is what was meant.
func (p *publisher) addImage() {
	if len(p.frames) >= maxPubFrames {
		return
	}
	start := ""
	if n := len(p.frames); n > 0 {
		start = filepath.Dir(p.frames[n-1])
	}
	p.pickImage(fmt.Sprintf("Image %d", len(p.frames)+1), start, func(path string) {
		p.setFrames(append(p.frames, path))
	})
}

func (s *pubSlot) build() gtk.Widgetter {
	pic := gtk.NewPicture()
	pic.SetCanShrink(true)
	// Big enough to judge by. At thumbnail size every frame of a session looks
	// like every other one, which is exactly the mistake the choice is
	// supposed to avoid.
	pic.SetSizeRequest(-1, 200)
	pic.SetFilename(s.path)
	// the base wears the thumbnail's own frame, draggable (publish_crop.go)
	pf := videoFrame(s.cropOverlay(pic))

	// The position, spelled out. "Base" and "Ref 2" say what the image model
	// is going to do with each picture, which "1" and "2" do not -- and the
	// numbers are also what the instruction refers to them by.
	role := gtk.NewLabel(fmt.Sprintf("Ref %d", s.i+1))
	if s.i == 0 {
		role = gtk.NewLabel("Base")
	}
	role.AddCSSClass("heading")
	role.SetTooltipText("The picture being edited — the instruction changes this one")
	if s.i > 0 {
		role.SetTooltipText(fmt.Sprintf("A reference: unchanged, and there to be named. "+
			"The instruction calls this one \"the %s image\"", ordinal(s.i+1)))
	}

	name := gtk.NewLabel(strings.TrimSuffix(filepath.Base(s.path), filepath.Ext(s.path)))
	name.SetXAlign(0)
	name.SetHExpand(true)
	name.SetEllipsize(pango.EllipsizeMiddle)
	name.AddCSSClass("dim-label")
	name.SetTooltipText(s.path)

	top := gtk.NewBox(gtk.OrientationHorizontal, 6)
	top.Append(role)
	top.Append(name)

	row := gtk.NewBox(gtk.OrientationHorizontal, 4)
	if s.i > 0 {
		mk := gtk.NewButtonWithLabel("Make base")
		mk.AddCSSClass("flat")
		mk.SetTooltipText("Edit this one instead, and demote the current base to a reference")
		mk.ConnectClicked(func() { s.p.setFrames(moveToFront(s.p.frames, s.i)) })
		row.Append(mk)
	}
	change := gtk.NewButtonWithLabel("Change…")
	change.AddCSSClass("flat")
	change.SetTooltipText("Swap this image for another, keeping its place in the row")
	change.ConnectClicked(func() { s.choose() })
	row.Append(change)

	drop := gtk.NewButtonFromIconName("list-remove-symbolic")
	drop.AddCSSClass("flat")
	drop.SetTooltipText("Remove this image from the row")
	drop.SetHAlign(gtk.AlignEnd)
	drop.SetHExpand(true)
	drop.ConnectClicked(func() {
		s.p.setFrames(append(append([]string(nil), s.p.frames[:s.i]...), s.p.frames[s.i+1:]...))
	})
	row.Append(drop)

	box := gtk.NewBox(gtk.OrientationVertical, 2)
	box.Append(top)
	box.Append(pf)
	box.Append(row)
	return box
}

// ordinal is for the tooltip that tells the user what to call an image in the
// instruction. Only ever asked for small numbers -- the row holds maxPubFrames.
func ordinal(n int) string {
	names := []string{"", "first", "second", "third", "fourth", "fifth", "sixth", "seventh", "eighth"}
	if n < len(names) {
		return names[n]
	}
	return fmt.Sprintf("%dth", n)
}

// choose swaps this slot's image, keeping its position -- so changing the base
// leaves it the base, and changing a reference does not renumber the others.
func (s *pubSlot) choose() {
	s.p.pickImage(fmt.Sprintf("Image %d", s.i+1), filepath.Dir(s.path), func(path string) {
		fs := append([]string(nil), s.p.frames...)
		if s.i < len(fs) {
			fs[s.i] = path
		}
		s.p.setFrames(fs)
	})
}

// pickImage opens the file chooser. An empty start folder means the one Prepare
// extracted frames into, which is the only place worth opening on by default.
func (p *publisher) pickImage(title, start string, done func(string)) {
	a := p.a
	if start == "" || !exists(start) {
		start = filepath.Join(a.outDir, "step1", "frames")
		if vids, _ := a.snappedSources(); len(vids) > 0 {
			if d := filepath.Join(start, baseName(vids[0])); exists(d) {
				start = d
			}
		}
	}
	d := gtk.NewFileDialog()
	d.SetTitle(title)
	if exists(start) {
		d.SetInitialFolder(gio.NewFileForPath(start))
	}
	filt := gtk.NewFileFilter()
	filt.SetName("Images")
	for _, e := range []string{"jpg", "jpeg", "png", "webp"} {
		filt.AddSuffix(e)
	}
	filters := gio.NewListStore(gtk.GTypeFileFilter)
	filters.Append(filt.Object)
	d.SetFilters(filters)
	d.Open(context.Background(), &a.win.Window, func(res gio.AsyncResulter) {
		f, err := d.OpenFinish(res)
		if err != nil || f == nil {
			return // dismissed
		}
		done(f.Path())
	})
}

// ---- reading and writing the page ------------------------------------------------

// snapshot is the page as a runner will see it, taken on the GUI thread. Same
// rule as snapSources: a goroutine never touches a widget, and a value copied
// out before the run is a value the run cannot see change under it.
func (p *publisher) snapshot() pubSettings {
	st := pubSettings{Frames: append([]string(nil), p.frames...), Crop: p.crop}
	st.Title = strings.TrimSpace(p.title.Text())
	st.Prompt = strings.TrimSpace(viewText(p.prompt))
	st.Negative = strings.TrimSpace(viewText(p.neg))
	st.Desc = strings.TrimSpace(viewText(p.desc))
	return st
}

// apply is snapshot's inverse: what a run wrote, or what a project holds, put
// back on the page.
func (p *publisher) apply(st pubSettings) {
	p.crop = st.Crop
	p.setFrames(st.Frames)
	p.title.SetText(st.Title)
	setViewText(p.prompt, st.Prompt)
	setViewText(p.neg, st.Negative)
	setViewText(p.desc, st.Desc)
}

func viewText(tv *gtk.TextView) string {
	b := tv.Buffer()
	return b.Text(b.StartIter(), b.EndIter(), false)
}

func setViewText(tv *gtk.TextView, s string) { tv.Buffer().SetText(s) }

// currentPublish is what the project stores, with the frames made relative to
// root so a moved autocut folder keeps its thumbnail.
func (a *App) currentPublish() *pubSettings {
	if a.pub == nil {
		return nil
	}
	st := a.pub.snapshot()
	for i, f := range st.Frames {
		st.Frames[i] = a.relToRoot(f)
	}
	// nothing chosen and nothing written is not worth a key in the file
	if len(st.Frames) == 0 && st.Crop == nil && st.Title == "" && st.Prompt == "" && st.Desc == "" {
		return nil
	}
	return &st
}

func (a *App) applyPublish(st *pubSettings) {
	if a.pub == nil {
		return
	}
	if st == nil {
		a.pub.apply(pubSettings{})
		a.pub.showShot()
		return
	}
	// migrate first, then resolve: an old project's base index points into the
	// order it was saved in, so rotating has to happen before anything else
	// touches the list
	c := st.migrate()
	for i, f := range c.Frames {
		c.Frames[i] = a.fromRoot(f)
	}
	a.pub.apply(c)
	a.pub.showShot()
}

// refresh redraws both rows and the picture -- called when the page is entered,
// because everything it reads (the cut, the narration, the output folder) is
// made somewhere else.
func (p *publisher) refresh() {
	if p == nil {
		return
	}
	p.reread()
	// the row is rebuilt, not just re-read: the base wears a crop box of the
	// CUT's shape, and the cut is the thing most likely to have been reshaped
	// since this page was last looked at
	p.rebuildFrames()
	p.updateOut()
	p.showShot()
}

// showShot puts the last thumbnail back in the picture. The file on disk is the
// state, not something remembered in the struct: it survives a restart, and a
// project whose folder was reopened shows what was drawn for it last time.
func (p *publisher) showShot() {
	if p == nil || p.shot == nil {
		return
	}
	// the finished one first, then the plain copy: they hold the same picture
	// now that the model letters it, but a run interrupted between the two
	// writes leaves only the plain one, and that is still the thumbnail
	for _, name := range []string{"thumbnail.png", "thumbnail-plain.png"} {
		if f := filepath.Join(p.a.publishDir(), name); exists(f) {
			p.shot.SetFilename(f)
			p.shot.SetTooltipText("step6/" + name)
			return
		}
	}
	p.shot.SetPaintable(nil)
	p.shot.SetTooltipText("nothing drawn yet — ▶ below draws it")
}

// reread reloads the cut's aspect off disk -- the one thing the drawing side
// still reads from another step's file. Only on arrival and after a run, never
// per frame: the crop box is redrawn on every pointer move.
func (p *publisher) reread() {
	p.aspect = p.a.produceCut().Aspect
}

func (p *publisher) updateOut() {
	if p == nil || p.out == nil {
		return
	}
	p.out.SetText(summarizeOutputs(p.a.publishDir()))
}

// ---- choosing the candidate frames -------------------------------------------

// pubShot is one extracted frame and where it falls on the session clock.
type pubShot struct {
	path string
	t    float64
}

// publishShots is every frame Prepare extracted, on the session's clock. The
// times come from the frame's own filename when it has a stamp -- which is
// what they are named for -- and from the video's start plus the interval when
// it does not, which is how folders extracted before the renaming look.
//
// Runner-side: it reads a frame folder per source, so it is not something to
// call from a page refresh.
func (a *App) publishShots() []pubShot {
	vids, _ := a.snappedSources()
	if len(vids) == 0 {
		return nil
	}
	zero := a.sessionZero()
	var out []pubShot
	for _, v := range vids {
		plan, err := a.planVideo(v, a.describeDir())
		if err != nil {
			a.logfIdle("    publish: no frames for %s (%v)", baseName(v), err)
			continue
		}
		start := a.sourceStart(v) // its own stamp, or the session's start
		for i, f := range plan.frames {
			t := start - zero + float64(i)*plan.interval
			if s, _, ok := readStamp(f); ok {
				t = s - zero
			}
			out = append(out, pubShot{path: f, t: t})
		}
	}
	return out
}

// pickShots chooses n candidate frames. Frames the cut kept come first -- they
// are the only ones a viewer will ever see, and a thumbnail painted over a
// moment that was edited out promises a video that does not exist -- and
// within those the picks are spread evenly, so the candidates come from
// different parts of the video rather than a few seconds of the same one.
//
// The very start and the very end are avoided by taking the middle of each of
// n equal bands rather than the endpoints: a video's first frame is usually a
// loading screen and its last is usually a fade.
func pickShots(shots []pubShot, segs []cutSeg, n int) []string {
	if n <= 0 || len(shots) == 0 {
		return nil
	}
	pool := shots
	if len(segs) > 0 {
		var kept []pubShot
		for _, s := range shots {
			for _, c := range segs {
				// an insert covers session seconds without showing them, so a
				// frame under one is not in the video at all -- picking a
				// thumbnail from it would offer a shot no viewer ever sees
				if !c.isInsert() && s.t >= c.S && s.t <= c.E {
					kept = append(kept, s)
					break
				}
			}
		}
		// only if the cut actually leaves enough to choose between: a cut
		// whose clips fall between two frames would otherwise reduce the
		// candidates to the same one repeated
		if len(kept) >= n {
			pool = kept
		}
	}
	var out []string
	for i := 0; i < n; i++ {
		j := int(float64(len(pool)) * (float64(i) + 0.5) / float64(n))
		if j >= len(pool) {
			j = len(pool) - 1
		}
		out = append(out, pool[j].path)
	}
	return out
}

// ---- what the model is told about the video -------------------------------------

// publishBrief is the video as a paragraph of facts: how long it is, what its
// clips contain, and what the narration says over them. It is the same clip
// briefing the narration was written from (clipBriefs), plus the narration
// itself -- which is the tightest description of the finished video that
// exists, because it was written to be spoken over exactly these clips.
func (a *App) publishBrief(segs []cutSeg, entries []narrEntry) string {
	rows := a.sessionRows()
	total := 0.0
	for _, s := range segs {
		total += s.length()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "THE FINISHED VIDEO: %d clips, %d:%02d long.\n",
		len(segs), int(total)/60, int(total)%60)
	b.WriteString("\nWHAT IS IN EACH CLIP:\n")
	b.WriteString(clipBriefs(segs, rows, a.narratorMic()))
	said := 0
	var n strings.Builder
	// running time, not session time: the description's chapter marks are
	// counted from the start of the finished video, and this is the only place
	// that arithmetic is available
	at := 0.0
	for i, s := range segs {
		for _, e := range entries {
			if e.S != s.S || e.E != s.E || strings.TrimSpace(e.Text) == "" {
				continue
			}
			fmt.Fprintf(&n, "  [%d:%02d] (clip %d) %s\n",
				int(at+e.At)/60, int(at+e.At)%60, i+1, e.Text)
			said++
		}
		at += s.length()
	}
	if said > 0 {
		b.WriteString("\nTHE NARRATION SPOKEN OVER IT, at its time in the finished video:\n")
		b.WriteString(n.String())
	} else {
		b.WriteString("\n(no narration has been written for this video)\n")
	}
	return b.String()
}

// ---- the model job ----------------------------------------------------------

// writeUpload asks for everything this page's words are: the title, the
// instruction for the picture, and the description, in one reply. No JSON on
// purpose -- the description IS prose, and wrapping prose in JSON only adds a
// way for a reply that is otherwise perfectly good to be thrown away over an
// unescaped quote. The other two ride in front of it on labelled lines, which
// costs one string operation and cannot fail that way.
//
// The instruction used to be excluded on principle. A second call, with the
// images attached, once picked which frame to edit AND wrote the instruction
// for it, and it was removed because it did neither well -- the frame it chose
// was rarely the one a person would, and the instructions it wrote described
// pictures instead of asking for changes.
//
// Picking the frame is still the user's, and that is the half that mattered:
// the page is built around choosing a base image by eye. The instruction came
// back because this call is not that call. It reads the whole session in order
// to write the title, so it knows what the clips contain, and asking the same
// reply for one more line costs nothing and starts the box off with something
// to edit rather than empty. It is a suggestion in a box, like the title above
// it -- rewrite it and press ▶ again.
func (a *App) writeUpload(brief string) (title, instr, desc string, err error) {
	msgs := []map[string]any{
		msg("system", a.prompt("youtube")),
		msg("user", a.ctxBlock()+brief),
	}
	if err := a.checkpoint(); err != nil {
		return "", "", "", err
	}
	reply, err := a.llmChatRetry("publish", msgs, true)
	if err != nil {
		return "", "", "", err
	}
	title, instr, desc = splitUpload(reply)
	if desc == "" {
		return "", "", "", fmt.Errorf("the model answered with nothing")
	}
	return title, instr, desc, nil
}

// splitUpload peels the labelled lines off the front of the reply: the title,
// and the instruction for the picture. A reply missing either is not an error --
// the description is the part that matters, and an empty box is easier to notice
// and to fill than a wrong line is to spot. The rest goes through
// cleanDescription either way.
//
// Order-insensitive, because it costs one loop and the alternative is a run
// thrown away over a model that answered with its two headers the other way
// round.
func splitUpload(reply string) (title, instr, desc string) {
	// A fenced reply puts the fence before the labelled lines, so it has to come
	// off here rather than in cleanDescription: by the time they are peeled the
	// text no longer *starts* with a fence, and the closing one would be left
	// sitting at the bottom of the description.
	s := unfence(strings.TrimSpace(reply))
	for i := 0; i < 2; i++ {
		if v, rest, ok := peelLabel(s, "title:"); ok {
			title, s = v, rest
			continue
		}
		if v, rest, ok := peelLabel(s, "thumbnail:"); ok {
			instr, s = v, rest
			continue
		}
		break
	}
	return title, instr, cleanDescription(s)
}

// peelLabel takes "NAME: value" off the front when the first line carries it,
// and says whether it did. The quotes come off the value: a model asked for a
// title on a labelled line very often gives it to you in quotation marks, and
// those would be lettered into the picture.
func peelLabel(s, name string) (val, rest string, ok bool) {
	line := s
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		line = s[:i]
	}
	t := strings.TrimSpace(line)
	if len(t) <= len(name) || !strings.EqualFold(t[:len(name)], name) {
		return "", s, false
	}
	val = strings.Trim(strings.TrimSpace(t[len(name):]), `"“”`)
	return val, strings.TrimSpace(strings.TrimPrefix(s, line)), true
}

// unfence takes a ```-fenced block down to what is inside it. A chat model
// wraps prose it was asked for bare often enough that both readers of a reply
// need this, and a fence left in the box is a fence in the YouTube description.
func unfence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.LastIndex(s, "```"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// cleanDescription strips the wrapping a chat model puts around prose it was
// asked for bare: a fenced block, and a "Description:" style lead-in on the
// first line. What is left is what goes in the box, verbatim -- the text is
// the product here, so nothing else is touched.
func cleanDescription(reply string) string {
	s := unfence(strings.TrimSpace(reply))
	if line, rest, ok := strings.Cut(s, "\n"); ok {
		l := strings.TrimSpace(line)
		if len(l) < 40 && strings.HasSuffix(l, ":") {
			s = strings.TrimSpace(rest)
		}
	}
	return strings.TrimSpace(s)
}

// ---- drawing the picture ----------------------------------------------------------

// ---- the run --------------------------------------------------------------------

// publishSuggest is the "Suggest again" button beside the title: one LLM call
// rewrites the title, the thumbnail instruction and the description, and
// nothing is drawn or rendered. It is the only thing that rewrites them -- ▶
// (produceClicked) writes them once and then never touches them again.
func (a *App) publishSuggest() {
	if a.running {
		a.setStatus("a run is already active — stop it first (⏹)")
		return
	}
	p := a.pub
	if p == nil {
		return
	}
	segs := a.produceSegs()
	if len(segs) == 0 {
		a.setStatus("no cut yet — build one on the Cut step first")
		return
	}
	st := p.snapshot()
	entries := a.produceEntries()
	// the cut's shape, read once on this thread: the goroutine below must not
	// go reading the editor for it
	aspect := a.produceCut().Aspect
	written := a.publishRecorded()
	a.saveProjectNow() // the run is a moment worth a file, whatever the ticker is doing

	a.running = true
	a.stopFlag.Store(false)
	a.pauseFlag.Store(false)
	a.runCtx, a.runCancel = context.WithCancel(context.Background())
	a.qReset()
	a.updateRunControls()
	a.logExp.SetExpanded(true)
	a.logf(">>> publish: rewriting the title, instruction and description — one LLM call")
	a.qJob(trackSTT, "publish", 0, 0)
	a.prog(trackSTT, 0, "thinking")
	a.pulseUntilCounted()

	go func() {
		var failed error
		defer func() { a.publishDone(failed) }()
		failed = a.publishStage(st, aspect, segs, entries, true, written, true)
	}()
}

// publishStage is the writing-and-drawing half of a run, on the runner's
// goroutine: fill the image row the first time, write the upload text, land
// it, then draw the thumbnail. The order matters -- the text is written and
// landed on the page before anything is drawn, so an sd.cpp that is down or
// busy costs the picture and not the model calls that were already paid for.
// textOnly stops before the drawing; that is Suggest again.
//
// The model writes the text -- the title, the instruction and the description
// -- once per project and then never again (needText, which ▶ passes as "has
// the step6 record never been written"). ▶ after that redraws and re-renders:
// press it as often as you like with the instruction reworded or the images
// changed, and it costs GPU time and no thinking. The record is the folder,
// not the boxes. Gating on "is the title empty" meant clearing a field you
// did not like silently bought you a fresh model call on the next ▶, and it
// also meant a run that failed at the drawing rewrote the words it had just
// written. Deleting step6/ is the deliberate way to start the text over, and
// "Suggest again" is the way to do it without losing the pictures.
func (a *App) publishStage(st pubSettings, aspect string, segs []cutSeg,
	entries []narrEntry, needText, written, textOnly bool) error {
	// A starting image on the very first run, so the row is not empty the
	// first time the page is opened. Nothing chooses between them any more
	// -- the first is simply the base -- so this is a convenience, not a
	// decision: swap them, add to them, or empty the row entirely.
	//
	// Not on a redraw. A row the user has emptied is a decision ("draw it
	// from the instruction alone"), not a gap to quietly refill with frames
	// they threw away.
	if len(st.Frames) == 0 && !written {
		a.logfIdle("    publish: no images chosen — taking %d from the cut", defPubFrames)
		if st.Frames = pickShots(a.publishShots(), segs, defPubFrames); len(st.Frames) > 0 {
			a.landPublish(st)
		} else {
			a.logfIdle("    publish: no frames extracted either — drawing from the instruction alone")
		}
	}

	if needText {
		brief := a.publishBrief(segs, entries)
		a.logCtx("publish")
		a.prog(trackSTT, 0, "writing the title, the instruction and the description")
		title, instr, desc, err := a.writeUpload(brief)
		if err != nil {
			return err
		}
		// a reply that forgot one of its labelled lines still has a good
		// description in it, and an empty box is easier to notice than a
		// wrong line -- so a missing part leaves what was there rather than
		// clearing it
		if title != "" {
			st.Title = title
		}
		if instr != "" {
			st.Prompt = instr
		}
		st.Desc = desc
		a.logfIdle("    publish: title %q, instruction %d characters, description %d characters",
			st.Title, len(st.Prompt), len(desc))
		a.landPublish(st)
	}
	if err := a.writePublishFiles(st); err != nil {
		a.logfIdle("    publish: %v", err) // the text is on the page either way
	}
	if textOnly {
		return nil
	}
	a.prog(trackSTT, 0.5, "drawing the thumbnail")
	return a.drawThumbnail(st, aspect)
}

// landPublish puts a stage's result on the page from the runner's goroutine and
// waits for it to be there. Waiting is what makes the next stage's snapshot
// consistent with the screen -- and, more to the point, what guarantees the
// user sees a written title even if the drawing then fails.
func (a *App) landPublish(st pubSettings) {
	done := make(chan struct{})
	glib.IdleAdd(func() {
		if a.pub != nil {
			a.pub.apply(st)
		}
		close(done)
	})
	<-done
}

// writePublishFiles puts the text in the output folder as well as in the
// project. The project file is the state; these are what you copy out of when
// the upload page is open in the other window.
func (a *App) writePublishFiles(st pubSettings) error {
	dir := a.publishDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "publish.json"), append(b, '\n'), 0o644); err != nil {
		return err
	}
	if st.Desc != "" {
		if err := os.WriteFile(filepath.Join(dir, "description.txt"),
			[]byte(st.Desc+"\n"), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// editInstruction is what the image model is actually sent: the edit the
// instruction box describes, then the title as its own sentence.
//
// They are joined here rather than stored joined so that the title stays one
// editable field. Retyping four words should not mean rewriting the paragraph
// that surrounds them, and the model that wrote them is not asked again.
func editInstruction(st pubSettings) string {
	edit := strings.TrimSpace(st.Prompt)
	title := strings.TrimSpace(st.Title)
	if title == "" {
		return edit
	}
	// Never twice. An instruction that already names the title is asking for
	// those words itself -- either because the model wrote both in one reply, or
	// because the hand that typed the instruction laid the lines out in it -- and
	// appending the sentence below asks for them a second time. The model obliges:
	// a request for two lines of lettering comes back with three, the last one
	// repeated, and nothing on the page says why.
	if edit != "" && strings.Contains(strings.ToLower(edit), strings.ToLower(title)) {
		return edit
	}
	// The quotes matter. Qwen-Image-Edit reproduces quoted spans literally,
	// and an unquoted title gets read as a description of the scene -- ask for
	// "August 2026 Pirate Ghost Live Event" without them and it paints a ghost.
	want := fmt.Sprintf("Write the exact text %q across the lower part of the image "+
		"in large bold letters that are easy to read at small size, with enough "+
		"contrast against what is behind them.", title)
	if edit == "" {
		return want
	}
	return edit + "\n\n" + want
}

// drawThumbnail is the sd.cpp half: hand the model the chosen frame, the other
// frame, and the instruction, and write what comes back.
//
// Both frames go in the request whether or not the instruction mentions the
// second one. That is what makes "add the ship from the second image" a thing
// the user can type into the box without also having to arrange for it to be
// sent -- an edit model ignores a reference nothing refers to.
//
// The result is written twice, to thumbnail.png and thumbnail-plain.png. They
// are identical now that the title is drawn by the model; the plain copy is
// kept because it is what the page shows while a new one is being made, and
// because losing the previous render to a failed re-roll is worse than a few
// hundred kB.
//
// aspect is the cut's, and it decides both halves of the shape question: the
// frame the model draws into, and how much of the base frame is handed to it
// (publish_crop.go). Sending a widescreen frame and asking for a portrait
// picture is asking the model to choose the crop, and it chooses badly and
// differently every time.
func (a *App) drawThumbnail(st pubSettings, aspect string) error {
	// An empty row is allowed: with no references this is plain text-to-image,
	// which is what a session with nothing worth editing actually wants. What
	// is not allowed is a base that has been deleted since it was chosen --
	// silently drawing from the second image instead would be a thumbnail of
	// the wrong moment, and nothing on the page would say so.
	base := st.basePath()
	if base != "" && !exists(base) {
		return fmt.Errorf("the base image is gone: %s", base)
	}
	instr := editInstruction(st)
	if strings.TrimSpace(instr) == "" {
		return fmt.Errorf("nothing to tell the image model — write an instruction or a title first")
	}
	dir := a.publishDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	w, h := pubBox(aspect)
	outA := float64(w) / float64(h)

	// In row order, which IS the answer: the first is the picture being edited
	// and the rest are what "the second image" in the instruction refers to.
	// A reference that has gone missing is skipped rather than fatal -- it is
	// only ever named in passing, and losing the mention beats losing the run.
	//
	// The crop is applied to the FIRST one alone. That is the picture being
	// composed; a reference is there to be named in a sentence, and cutting it
	// down would only take away the part of it the sentence might mean.
	var imgs []string
	cropped := ""
	for i, f := range st.Frames {
		if !exists(f) {
			a.logfIdle("    publish: reference %d is gone, drawing without it: %s", i+1, f)
			continue
		}
		var u string
		var err error
		if i == 0 {
			srcA := imageAspect(f)
			r := st.cropRect(srcA, outA)
			if !pubWholeFrame(r, srcA, outA) {
				cropped = fmt.Sprintf(", base cropped to %.0f%% of its width around %.2f,%.2f",
					100*pubCropW(r.hf, srcA, outA), r.cx, r.cy)
			}
			u, err = pubCropRefImage(f, r, srcA, outA)
		} else {
			u, err = sdRefImage(f)
		}
		if err != nil {
			return fmt.Errorf("image %s: %w", filepath.Base(f), err)
		}
		imgs = append(imgs, u)
	}

	req := sdRequest{
		Prompt:        instr,
		Negative:      st.Negative,
		Width:         w,
		Height:        h,
		Seed:          -1, // a fresh draw every time ▶ is pressed, which is the point
		RefImages:     imgs,
		AutoResizeRef: true,
		Format:        "png",
	}
	if base == "" {
		a.logfIdle("    publish: %dx%d drawn from the instruction alone, no images", w, h)
	} else {
		a.logfIdle("    publish: %dx%d editing %s, %d image(s) sent%s",
			w, h, filepath.Base(base), len(imgs), cropped)
	}
	img, err := a.sdGenerate(a.runCtx, req, func(where string) {
		a.prog(trackSTT, 0.5, "drawing (%s)", where)
	})
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "thumbnail-plain.png"), img, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "thumbnail.png"), img, 0o644)
}

func (a *App) publishDone(err error) {
	glib.IdleAdd(func() {
		a.running = false
		a.updateRunControls()
		if p := a.pub; p != nil {
			p.refresh()
		}
		a.updateGates()
		if err != nil {
			if !errors.Is(err, errStopped) {
				a.logf("suggestions FAILED: %v", err)
				a.progress.SetText("suggestions failed — see log")
				return
			}
			a.progress.SetText("suggestions stopped")
			return
		}
		a.progress.SetFraction(1)
		a.progress.SetText("title, instruction and description rewritten — ▶ draws and renders")
	})
}
