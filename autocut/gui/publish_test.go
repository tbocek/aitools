package main

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// The title does not travel in the instruction any more -- it is printed
// onto the picture after the draw (drawPubTexts), with the marked texts. What
// the instruction has to carry instead is the no-lettering sentence: an image
// model asked for a thumbnail letters something unless told not to, and its
// words would sit underneath ours. History: the title used to be appended
// here as its own "Write the exact text" sentence, and an instruction that
// already named the title got it lettered twice.
func TestTheInstructionAsksForNoLetteringInsteadOfTheTitle(t *testing.T) {
	edit := "brighten the character and blur the background"

	got := editInstruction(pubSettings{Prompt: edit, Title: "Ghost Ship Disaster"})
	if !strings.Contains(got, edit) {
		t.Error("the edit instruction was lost")
	}
	if strings.Contains(got, "Ghost Ship Disaster") {
		t.Errorf("the title is still sent to the image model, so it is lettered twice:\n%s", got)
	}
	if !strings.Contains(got, pubNoLettering) {
		t.Error("the instruction no longer forbids lettering -- the model's words end up under the printed title")
	}
	if !strings.Contains(got, "\n\n") {
		t.Error("the no-lettering sentence runs on from the edit instead of standing on its own")
	}
	// the sentence has to hold the title's band open too, or the print lands
	// on a busy top edge
	if !strings.Contains(strings.ToLower(pubNoLettering), "upper part") {
		t.Error("the no-lettering sentence does not ask for a calm upper part, where the title goes")
	}

	// an empty instruction is empty, not the boilerplate on its own: a model
	// with nothing to do but "don't letter" draws boilerplate, and the caller
	// treats "" as the stop-and-say-so case
	if got := editInstruction(pubSettings{Title: "Ghost Ship Disaster"}); got != "" {
		t.Errorf("a title with no edit conjured an instruction: %q", got)
	}
}

// The four candidates have to come from the video the viewer will see. A
// thumbnail painted over a moment the cut removed advertises a video that does
// not exist -- and it is exactly the moments a cut removes (the loading, the
// walking, the dead air) that a naive "every nth frame" pick lands on, because
// there are more of them.
func TestTheCandidateFramesComeFromTheCut(t *testing.T) {
	var shots []pubShot
	for i := 0; i < 100; i++ {
		shots = append(shots, pubShot{path: string(rune('a'+i%26)) + ".jpg", t: float64(i)})
	}
	// the cut keeps two windows, well away from each other
	segs := []cutSeg{{S: 10, E: 20}, {S: 70, E: 80}}
	got := pickShots(shots, segs, 4)
	if len(got) != 4 {
		t.Fatalf("asked for 4 frames, got %d", len(got))
	}
	kept := map[string]bool{}
	for _, s := range shots {
		for _, c := range segs {
			if s.t >= c.S && s.t <= c.E {
				kept[s.path] = true
			}
		}
	}
	for _, f := range got {
		if !kept[f] {
			t.Errorf("picked %s, which the cut removed", f)
		}
	}
	// and they are four different parts of the video, not four seconds of one
	seen := map[string]bool{}
	for _, f := range got {
		if seen[f] {
			t.Errorf("the same frame twice: %v", got)
		}
		seen[f] = true
	}

	// A cut too tight to hold four frames falls back to the whole session
	// rather than handing back one frame repeated: something to choose between
	// beats something faithful to a cut that has nothing in it.
	tight := pickShots(shots, []cutSeg{{S: 3, E: 4}}, 4)
	if len(tight) != 4 {
		t.Fatalf("a tight cut gave %d frames, want 4", len(tight))
	}
	if tight[0] == tight[3] {
		t.Errorf("a tight cut collapsed the candidates to one frame: %v", tight)
	}

	// nothing extracted is not a crash, it is a run that says so
	if got := pickShots(nil, segs, 4); got != nil {
		t.Errorf("pickShots with no frames = %v, want nil", got)
	}
}

// One model call writes the title, the thumbnail instruction and the
// description, and the only thing separating them in the reply is a line the
// model was asked to prefix. It will forget a line, wrap the whole answer in a
// fence, or put the title in quotes -- none of which is worth a failed run.
// What must not happen is a label surviving into the description box, or a
// forgotten line costing the description written underneath it.
func TestTheLabelledLinesArePeeledOffTheDescription(t *testing.T) {
	const body = "We went in for the chest and came out with nothing.\n\n#tarkov #raid"
	const head = "TITLE: The Door Was A Lie\nTHUMBNAIL: a raider holding an empty case\n\n"
	for _, c := range []struct{ name, reply, title, instr, desc string }{
		{"as asked", head + body, "The Door Was A Lie", "a raider holding an empty case", body},
		{"lowercase keys", strings.ToLower(head[:len(head)-2]) + "\n\n" + body,
			"the door was a lie", "a raider holding an empty case", body},
		{"quoted title", "TITLE: \"The Door Was A Lie\"\n\n" + body, "The Door Was A Lie", "", body},
		{"fenced", "```\n" + head + body + "\n```", "The Door Was A Lie", "a raider holding an empty case", body},
		// order is what the prompt asks for, not what it gets: a model that
		// answers the two labels the other way round is still answering
		{"swapped", "THUMBNAIL: a raider holding an empty case\nTITLE: The Door Was A Lie\n\n" + body,
			"The Door Was A Lie", "a raider holding an empty case", body},
		// no lines at all: the description is still the product, so it is kept
		// whole and the other two boxes are simply left as they were
		{"no labels", body, "", "", body},
		// and one label without the other is half an answer, not a failed one
		{"title only", "TITLE: The Door Was A Lie\n\n" + body, "The Door Was A Lie", "", body},
	} {
		title, instr, desc := splitUpload(c.reply)
		if title != c.title || instr != c.instr || desc != c.desc {
			t.Errorf("%s: splitUpload = (%q, %q, %q), want (%q, %q, %q)",
				c.name, title, instr, desc, c.title, c.instr, c.desc)
		}
	}
}

// Nothing the model writes is allowed to empty a box the user filled. A reply
// that forgets a label loses that line and nothing else, which is why the two
// suggestions land conditionally and the description -- always present, since
// whatever is left over IS the description -- lands flat.
func TestAForgottenLabelKeepsWhatWasAlreadyThere(t *testing.T) {
	body := funcBody(t, "publish.go", `func \(a \*App\) publishStage\(`)
	for _, want := range []string{
		`if title != "" {`,
		`if instr != "" {`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("publishStage is missing %s -- a reply that forgot one label "+
				"would blank a box the user had filled", want)
		}
	}
}

// The description is prose, and prose asked of a chat model comes back with a
// lead-in it was not asked for. What is stripped is only the wrapping: the text
// itself is the product, so nothing inside it is touched.
func TestTheDescriptionIsUnwrappedButNotRewritten(t *testing.T) {
	body := "We went in for the chest and came out with nothing.\n\nIt was a long night.\n\n#tarkov #raid"
	for _, reply := range []string{
		body,
		"```\n" + body + "\n```",
		"```markdown\n" + body + "\n```",
		"Description:\n" + body,
	} {
		if got := cleanDescription(reply); got != body {
			t.Errorf("cleanDescription(%q)\n = %q\nwant %q", reply, got, body)
		}
	}
	// a first line that is genuinely part of the description is not a lead-in,
	// even with a colon on the end -- length is what tells them apart
	keep := "This is the raid where everything that could go wrong did, in order:\n\nand here is the list."
	if got := cleanDescription(keep); got != keep {
		t.Errorf("cleanDescription ate a real first line: %q", got)
	}
}

// Which image is the base is now its position, and a base index arrives from
// two untrusted places -- a project file somebody edited, and a model's reply.
// Applying one must never index off the end of the list, and must never drop or
// duplicate an image: the row is what gets sent to sd.cpp, so a promotion that
// loses a reference loses it silently.
func TestPromotingAnImageIsWhatMakesItTheBase(t *testing.T) {
	fs := []string{"a.jpg", "b.jpg", "c.jpg"}
	for _, c := range []struct {
		i    int
		want string
	}{{0, "a b c"}, {1, "b a c"}, {2, "c a b"}, {-1, "a b c"}, {3, "a b c"}, {99, "a b c"}} {
		got := moveToFront(append([]string(nil), fs...), c.i)
		// compared on stems, so the table above stays readable
		if stems := strings.ReplaceAll(strings.Join(got, " "), ".jpg", ""); stems != c.want {
			t.Errorf("moveToFront(%d) = %q, want %q", c.i, stems, c.want)
		}
		if len(got) != len(fs) {
			t.Errorf("moveToFront(%d) changed the row from %d images to %d", c.i, len(fs), len(got))
		}
	}
	if (pubSettings{}).basePath() != "" {
		t.Error("an empty row reported a base image")
	}
	if got := (pubSettings{Frames: []string{"a.jpg"}}).basePath(); got != "a.jpg" {
		t.Errorf("the first image is the base, got %q", got)
	}

	// and a project written before the row became a list: its base lived in an
	// index beside the frames, and loading it has to apply that index rather
	// than quietly editing whichever image happens to be first
	old := pubSettings{Frames: []string{"a.jpg", "b.jpg"}, Base: 1}
	got := old.migrate()
	if got.basePath() != "b.jpg" {
		t.Errorf("an old project's base was lost: %+v", got)
	}
	if got.Base != 0 {
		t.Error("the retired index survived the migration and can now disagree with the order")
	}
	if len(got.Frames) != 2 {
		t.Errorf("migrating dropped an image: %+v", got.Frames)
	}
}

// What reaches sd.cpp is the row, in the row's order. Both halves matter: the
// prompt tells the model it may borrow "from the second image", which is a lie
// unless every image is actually sent, and "the first image is the one being
// edited" is a lie unless the order survives the trip.
//
// The two missing-file cases are deliberately different. A base that has been
// deleted since it was chosen is fatal -- drawing from the second image instead
// would be a thumbnail of the wrong moment with nothing on the page saying so
// -- while a missing reference is only ever named in passing, and losing the
// mention beats losing the run.
func TestTheWholeRowIsSentInOrder(t *testing.T) {
	body := funcBody(t, "publish.go", `func \(a \*App\) drawThumbnail\(`)
	if !strings.Contains(body, "range st.Frames") {
		t.Error("drawThumbnail no longer walks the row, so references never reach the model")
	}
	if !strings.Contains(body, "RefImages:") {
		t.Error("the images are not sent as ref_images")
	}
	if strings.Contains(body, "InitImage") || strings.Contains(body, "Strength") {
		t.Error("drawThumbnail is still building an img2img request")
	}
	iBase := strings.Index(body, "the base image is gone")
	iSkip := strings.Index(body, "drawing without it")
	if iBase < 0 {
		t.Error("a deleted base image no longer stops the run; the thumbnail would be of another moment")
	}
	if iSkip < 0 {
		t.Error("a deleted reference is no longer skipped, so one stale path kills the whole draw")
	}

	// an empty row is a state the page allows -- Remove works down to zero --
	// so the drawing side must treat it as text-to-image, not as an error
	if strings.Contains(body, `no base frame chosen`) {
		t.Error("an empty row is rejected, but the page lets you empty it")
	}
}

// One printer. The words go on through drawPubTexts and nothing else: not
// ffmpeg's drawtext (gone since the model lettered, and still gone now that
// cairo prints), and not the image model (editInstruction forbids it). Half a
// removal is the worst outcome here -- two printers is the title twice, which
// is the screenshot that started the original removal. The Settings probe is
// checked too: it used to demand the drawtext filter, and an ffmpeg build
// without one nothing uses is not a broken build.
func TestOnlyDrawPubTextsPrintsTheWords(t *testing.T) {
	src := readSrc(t, "publish.go")
	for _, gone := range []string{"drawtext", "title.txt", "Write the exact text"} {
		if strings.Contains(src, gone) {
			t.Errorf("publish.go still refers to %q — a second printer for the words", gone)
		}
	}
	if strings.Contains(readSrc(t, "setup.go"), `"drawtext"`) {
		t.Error("ffFilters still requires drawtext, failing builds over a filter nothing uses")
	}
	// the one printer runs inside the draw, from the plain copy
	body := funcBody(t, "publish.go", `func \(a \*App\) drawThumbnail\(st pubSettings, aspect string\) error \{`)
	if !strings.Contains(body, "drawPubTexts(") {
		t.Error("drawThumbnail never prints the words, so ▶ makes a thumbnail without its title")
	}
}

// Adding and removing is the page's own state, and every path that changes the
// row goes through setFrames -- which is what keeps the widgets, the Inputs
// line and the project from disagreeing about what will be sent.
func TestTheImageRowIsAddableAndRemovable(t *testing.T) {
	src := readSrc(t, "publish.go")
	for _, want := range []string{
		"func (p *publisher) addImage()",      // the + button
		"func (p *publisher) setFrames(",      // the one way the row changes
		"list-remove-symbolic",                // the − on each image
		`gtk.NewButtonWithLabel("Make base")`, // promotion, in place of the old radio
	} {
		if !strings.Contains(src, want) {
			t.Errorf("the image row is missing %s", want)
		}
	}
	if strings.Contains(src, "gtk.NewCheckButtonWithLabel(\"Base\")") {
		t.Error("the base radio is still there; position is what says which image is the base now")
	}
	// the row is capped, and the cap has to be enforced where the row is set
	// rather than only where the button is pressed -- a project file can carry
	// any number of frames
	p := &publisher{a: &App{}} // widget-free: rebuildFrames and the info line both stand down
	long := make([]string, maxPubFrames+7)
	for i := range long {
		long[i] = fmt.Sprintf("f%d.jpg", i)
	}
	p.setFrames(long)
	if len(p.frames) != maxPubFrames {
		t.Errorf("setFrames kept %d images, want the row capped at %d -- a hand-edited "+
			"project would send them all to the vision call", len(p.frames), maxPubFrames)
	}
	// and loading a project has to go through the same cap, not around it
	if !strings.Contains(funcBody(t, "publish.go", `func \(p \*publisher\) apply\(st pubSettings\) \{`),
		"p.setFrames(") {
		t.Error("apply sets the row directly, so a project file skips the cap")
	}
}

// The prompt is the point of the step being editable at all: the user asked for
// a generic prompt they can change, and the registry is what makes an edited one
// stick to the project while an untouched one keeps tracking the built-in.
//
// There used to be two. The second one picked which frame to edit and wrote the
// instruction for it, and it did not work: the picking was guesswork and the
// instruction it wrote was worse than the one the user would have typed. The
// key is gone from the registry rather than renamed, so a project that saved an
// edited copy just keeps a dead key nobody reads.
func TestThePublishPromptIsEditable(t *testing.T) {
	ownConfig(t)
	d := promptDefFor("youtube")
	if d.key != "youtube" || strings.TrimSpace(d.def) == "" {
		t.Fatal(`prompt "youtube" is not in promptDefs`)
	}
	if promptDefFor("thumbnail").def != "" {
		t.Error(`the "thumbnail" prompt is still in the registry -- ` +
			"the job it belonged to is gone, so it is a prompt nobody sends")
	}
	src := readSrc(t, "publish.go")
	// the page sends it; the box that edits it is on Prepare (prepedit.go)
	if !strings.Contains(src, `a.sysPrompt("youtube")`) {
		t.Error(`step6 no longer sends a.sysPrompt("youtube") -- an editable prompt that is not sent is a lie`)
	}
	if strings.Contains(src, "a.promptBar(") || strings.Contains(src, "promptSlot{") {
		t.Error("step6 builds a prompt editor again -- every prompt lives in the one box on Prepare")
	}
	if strings.Contains(src, `"thumbnail"`) {
		t.Error("step6 still names the thumbnail prompt, which no longer exists")
	}
}

// What the one remaining prompt must and must not ask for. It writes three
// things in one reply, so the separators have to be spelled out exactly --
// without the labelled lines splitUpload has nothing to peel and the title
// arrives as the first paragraph of the description. And asking for JSON would
// throw away good prose over one unescaped quote.
func TestThePublishPromptMatchesHowTheAnswerIsUsed(t *testing.T) {
	for _, want := range []string{
		"TITLE: ",     // the separators splitUpload looks for, verbatim
		"THUMBNAIL: ", // ...both of them
		"Four to sev", // a title short enough to be lettered across a thumbnail
		"No JSON",     // which models volunteer even when asked for prose
	} {
		if !strings.Contains(strings.TrimSpace(sysSystem)+"\n\n"+youtubeSystem, want) {
			t.Errorf("the upload-text prompt no longer says %q", want)
		}
	}
	if strings.Contains(youtubeSystem, "strict JSON") {
		t.Error("the upload-text prompt asks for JSON; the answer is prose and is read as prose")
	}
	// The thumbnail line must not ask for lettering. The title is printed onto
	// the picture after the draw, so a model that asks for it in the picture
	// as well puts a second copy underneath the print.
	low := strings.ToLower(strings.TrimSpace(sysSystem) + "\n\n" + youtubeSystem) // the thumbnail's mechanics are the context's
	if !strings.Contains(low, "no text") && !strings.Contains(low, "no lettering") {
		t.Error("the upload-text prompt does not tell the model to keep words out of the " +
			"thumbnail instruction -- the title is added to it afterwards")
	}
	// and it is an instruction for an edit model, not a caption of the frame
	if !strings.Contains(low, "instruction") {
		t.Error("the upload-text prompt does not say the thumbnail line is an instruction")
	}
	// what belonged to the deleted frame-picking job stays deleted: this job
	// writes words, it does not choose an image or set sampler knobs
	for _, gone := range []string{"base_frame", "negative_prompt"} {
		if strings.Contains(youtubeSystem, gone) {
			t.Errorf("the upload-text prompt still asks for %q, which belonged to the deleted job", gone)
		}
	}
}

// One call, three answers, against a local fake server. The split is tested
// above on strings; what this adds is that the call actually returns all three
// -- a reader wired to two of them and dropping the third is the kind of thing
// that only shows up as an empty box.
func TestOneCallAnswersWithAllThreeParts(t *testing.T) {
	a := chatFake(t, 200, `{"choices":[{"message":{"content":`+
		`"TITLE: The Door Was A Lie\nTHUMBNAIL: the raider holding an empty case\n\nWe went in for the chest.\n\n#tarkov"}}]}`)
	title, instr, desc, err := a.writeUpload("THE CLIPS")
	if err != nil {
		t.Fatal(err)
	}
	if title != "The Door Was A Lie" {
		t.Errorf("title = %q", title)
	}
	if instr != "the raider holding an empty case" {
		t.Errorf("instruction = %q", instr)
	}
	if desc != "We went in for the chest.\n\n#tarkov" {
		t.Errorf("description = %q", desc)
	}

	// and a reply with nothing in it is an error rather than three empty boxes:
	// the description is the part with no fallback, so it is what is checked
	a = chatFake(t, 200, `{"choices":[{"message":{"content":"TITLE: A Title\n"}}]}`)
	if _, _, _, err := a.writeUpload("THE CLIPS"); err == nil {
		t.Error("a reply with no description was accepted -- the run would land an empty box " +
			"on the page and call it done")
	}
}

// The run's shape, which is what makes it survivable. The text is landed on the
// page before anything is drawn, so an image server that is down costs the
// picture and not the thinking call already paid for; and what is already
// written is never overwritten by ▶, which is what makes ▶ the button you press
// after rewording the instruction.
func TestPublishWritesTheTextBeforeItDraws(t *testing.T) {
	body := funcBody(t, "publish.go", `func \(a \*App\) publishStage\(`)
	iDesc := strings.Index(body, "a.writeUpload(brief)")
	iDraw := strings.Index(body, "drawThumbnail")
	if iDesc < 0 || iDraw < 0 {
		t.Fatalf("publishStage no longer does both: %d %d", iDesc, iDraw)
	}
	if iDesc > iDraw {
		t.Error("the thumbnail is drawn before the text is written -- a failed draw would then " +
			"throw away an LLM call that had already succeeded")
	}
	if !strings.Contains(body, "landPublish") {
		t.Error("nothing lands the written text on the page, so a failed draw would lose it")
	}
	// The model writes this page's text once per project. What makes ▶ safe to
	// press over and over -- with the instruction reworded, with a different base
	// frame -- is that the gate is the record on disk and not the state of the
	// boxes: a title you emptied is a deletion you made, not a gap to refill.
	// The gate is read where ▶ lands, on the GTK thread, and handed in.
	clicked := funcBody(t, "produce.go", `func \(a \*App\) produceClicked\(\) \{`)
	if !strings.Contains(clicked, "written := a.publishRecorded()") ||
		!strings.Contains(clicked, "!written, written, false") {
		t.Error("▶ no longer gates the model call on the step6 record; " +
			"it would go back to the LLM on every press")
	}
	// and Suggest again is the opposite: it always rewrites and never draws
	sug := funcBody(t, "publish.go", `func \(a \*App\) publishSuggest\(\) \{`)
	if !strings.Contains(sug, "true, written, true)") {
		t.Error("Suggest again no longer forces a rewrite (needText, textOnly)")
	}
	if !strings.Contains(body, "if textOnly {\n\t\treturn nil\n\t}") {
		t.Error("Suggest again no longer stops before drawing")
	}
}

// The drawing and the words are two panes worked on at different times: you
// reword the instruction and press ▶ half a dozen times without touching the
// description, then rewrite the description without redrawing. Since the merge
// they are panes of Produce -- the drawing the whole left, the words above the
// encoder settings on the right -- and the split itself lives in buildProduce.
//
// This is a fact about the source because a GTK page cannot be built without a
// display. What it pins is the assignment: which widget went into which side.
func TestThePageIsSplitBetweenTheDrawingAndTheWords(t *testing.T) {
	body := funcBody(t, "publish.go", `func \(a \*App\) buildPublishPanes\(\)`)
	// left: the images, what to change, what to keep out, the result
	for _, want := range []string{
		"col.Append(p.framesBox)",
		"col.Append(promptBox)",
		"col.Append(negBox)",
		"col.Append(shotFrame)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the drawing column is missing %s", want)
		}
	}
	// right top: the words themselves, and only those -- the prompt that
	// writes them is on Prepare
	for _, want := range []string{
		"wrote.Append(p.title)",
		"wrote.Append(descBox)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the text column is missing %s", want)
		}
	}
	// and buildProduce is where the panes meet
	prod := funcBody(t, "produce.go", `func \(a \*App\) buildProduce\(\)`)
	if !strings.Contains(prod, "outer := gtk.NewPaned(gtk.OrientationHorizontal)") {
		t.Fatal("the page is no longer two columns")
	}
	iSaid := strings.Index(prod, "right.Append(said)")
	iSet := strings.Index(prod, "right.Append(scroll)")
	if iSaid < 0 || iSet < 0 || iSaid > iSet {
		t.Errorf("the words are no longer above the encoder settings: %d %d", iSaid, iSet)
	}
	// the Inputs line that belongs to neither pane stays above the split,
	// full width, and step6's files ride the shared Outputs group
	iRun := strings.Index(prod, "page.Append(inRow)")
	iOuter := strings.Index(prod, "page.Append(outer)")
	if iRun < 0 || iOuter < 0 || iRun > iOuter {
		t.Errorf("the Inputs row is no longer above the split: %d %d", iRun, iOuter)
	}
	if !strings.Contains(prod, "outRow.Append(gtk.NewSeparator(gtk.OrientationVertical))\n\toutRow.Append(pubOuts)") {
		t.Error("step6's files fell off the shared Outputs group, or sit unfenced against the video's")
	}
}

// Removing the step6 folder is the one gesture that lets the model write this
// page again. The record therefore has to be a file in that folder and nothing
// else -- a flag in the project would survive the deletion and leave the user
// with no way back short of editing JSON by hand.
func TestDeletingTheFolderIsWhatUnlocksAFreshSuggestion(t *testing.T) {
	a := &App{outDir: t.TempDir()}
	if a.publishRecorded() {
		t.Fatal("an empty output folder already counts as written")
	}
	if err := a.writePublishFiles(pubSettings{Title: "x", Prompt: "y", Desc: "z"}); err != nil {
		t.Fatal(err)
	}
	if !a.publishRecorded() {
		t.Error("the text was written but the run would ask the model for it again")
	}
	// what the user does to start over
	if err := os.RemoveAll(a.publishDir()); err != nil {
		t.Fatal(err)
	}
	if a.publishRecorded() {
		t.Error("the folder is gone but the page still refuses to suggest again")
	}
}

// Everything on the page belongs to the project: the frames chosen, the words
// written, and how far the drawing may travel from the base. A page whose state
// only lives in widgets is a page you have to redo after every restart -- and
// the frames are stored root-relative like every other path, so moving the
// autocut folder moves the thumbnail with it.
func TestThePublishPageIsSavedWithTheProject(t *testing.T) {
	src := readSrc(t, "project.go")
	for _, want := range []string{
		"Publish *pubSettings",
		"a.currentPublish()",
		"a.applyPublish(p.Publish)",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("project.go is missing %s", want)
		}
	}
	body := funcBody(t, "publish.go", `func \(a \*App\) currentPublish\(\) \*pubSettings \{`)
	if !strings.Contains(body, "a.relToRoot(f)") {
		t.Error("the frames are stored absolute; moving the autocut folder would break them")
	}
	if !strings.Contains(funcBody(t, "publish.go", `func \(a \*App\) applyPublish\(st \*pubSettings\) \{`),
		"a.fromRoot(f)") {
		t.Error("stored frames are not resolved back through root on load")
	}

	// and the words themselves survive the trip, which is what makes ▶ cheap:
	// a reopened project redraws from the instruction it already paid for
	// rather than asking the language model for a new one
	want := pubSettings{Title: "Ghost Ship Disaster", Prompt: "blur the background",
		Negative: "watermark, logo", Frames: []string{"a.jpg", "b.jpg"}}
	b, err := json.Marshal(Project{Publish: &want})
	if err != nil {
		t.Fatal(err)
	}
	var back Project
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Publish == nil || !reflect.DeepEqual(*back.Publish, want) {
		t.Errorf("the Publish page did not survive the project file: %+v", back.Publish)
	}
}

// Publish is no longer a tab: its panes live on Produce and its work runs off
// Produce's ▶. Each absence below is a way the old page could quietly come
// back -- as a step, as a stack page, or as a ▶ case -- and the order pin at
// the end is the merge's whole point: one press writes the words, draws the
// picture, and only then starts the long render.
func TestPublishIsFoldedIntoProduce(t *testing.T) {
	if i := stepIndex("publish"); i != -1 {
		t.Errorf("publish is still a tab (index %d) -- it was merged into Produce", i)
	}
	if stepIndex("produce") != len(steps)-1 {
		t.Errorf("produce is at %d of %d; the merged page is the last step",
			stepIndex("produce"), len(steps))
	}
	if strings.Contains(readSrc(t, "main.go"), `"publish"`) {
		t.Error(`main.go still routes something to a "publish" page`)
	}
	play := funcBody(t, "pipeline.go", `func \(a \*App\) playClicked\(\) \{`)
	if strings.Contains(play, `case "publish":`) {
		t.Error("▶ still dispatches to a Publish page that is not there")
	}
	// arriving on Produce reads the thumbnail folder, since the picture on
	// the page is the file on disk rather than something remembered
	show := funcBody(t, "main.go", `func \(a \*App\) showStep\(`)
	if !strings.Contains(show, "a.pub.refresh()") {
		t.Error("arriving on Produce no longer rereads the thumbnail from disk")
	}
	// ...and the page's Inputs row speaks for both halves: the images going
	// to the image model, and whether the first ▶ still owes the text
	ins := funcBody(t, "produce.go", `func \(p \*producer\) updateInputs\(`)
	if !strings.Contains(ins, "if pub := a.pub; pub != nil {") ||
		!strings.Contains(ins, "thumbnail image(s)") ||
		!strings.Contains(ins, "a.publishRecorded()") {
		t.Error("the merged Inputs row says nothing about the thumbnail half")
	}
	// an edit to the image row is an edit to that line, wherever it came from
	if !strings.Contains(funcBody(t, "publish.go", `func \(p \*publisher\) setFrames\(`),
		"p.a.updateProduceInfo()") {
		t.Error("changing the image row leaves a stale count on the Inputs line")
	}
	// cheap and fails-fast before long: the words and the picture, then the
	// render -- an sd.cpp that is down costs seconds, not a finished encode
	body := funcBody(t, "produce.go", `func \(a \*App\) produceClicked\(\) \{`)
	// the model call has no byte count to report, so the bar pulses until the
	// first real part is counted -- without this ▶ looks hung while it thinks
	if !strings.Contains(body, "a.pulseUntilCounted()") {
		t.Error("nothing moves the bar while the model thinks")
	}
	iStage := strings.Index(body, "a.publishStage(")
	iRender := strings.Index(body, "a.produce(segs, entries, st, vids, auds)")
	if iStage < 0 || iRender < 0 || iStage > iRender {
		t.Errorf("▶ no longer runs the publish stage before the render: %d %d", iStage, iRender)
	}
}

func readSrc(t *testing.T, file string) string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// funcBody is the source of one function, for the wiring that cannot be called
// headless: a GTK page needs a display, but what it is wired to is a fact about
// the text.
func funcBody(t *testing.T, file, head string) string {
	t.Helper()
	re := regexp.MustCompile(`(?s)` + head + `.*?\n}\n`)
	m := re.FindString(readSrc(t, file))
	if m == "" {
		t.Fatalf("%s: no function matching %s", file, head)
	}
	return m
}
