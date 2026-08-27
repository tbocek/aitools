package main

import (
	"encoding/json"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// The title is not drawn on afterwards any more -- it is lettered by the image
// model, which means it has to reach the model as part of the one instruction
// it is given. Two things can go wrong there and both are silent: the title
// gets appended in a way that reads as a continuation of the edit ("...and blur
// the background Ghost Ship Disaster"), or an empty title leaves behind an
// instruction asking for lettering that says nothing.
func TestTheTitleTravelsInTheInstruction(t *testing.T) {
	edit := "brighten the character and blur the background"

	both := editInstruction(pubSettings{Prompt: edit, Title: "Ghost Ship Disaster"})
	if !strings.Contains(both, edit) {
		t.Error("the edit instruction was lost when a title was added")
	}
	if !strings.Contains(both, `"Ghost Ship Disaster"`) {
		t.Error("the title is not quoted, so the model cannot tell where it ends")
	}
	if !strings.Contains(both, "\n\n") {
		t.Error("the title runs on from the edit instead of standing as its own sentence")
	}

	// an empty title means no lettering at all, not lettering of ""
	only := editInstruction(pubSettings{Prompt: edit})
	if only != edit {
		t.Errorf("an empty title still changed the instruction: %q", only)
	}
	if strings.Contains(strings.ToLower(only), "text") {
		t.Errorf("an untitled thumbnail is still asked for lettering: %q", only)
	}

	// and a title on its own is a complete instruction: this is the "keep my
	// frame, just put a title on it" case, which used to need the image model
	// bypassed entirely
	title := editInstruction(pubSettings{Title: "Ghost Ship Disaster"})
	if !strings.Contains(title, `"Ghost Ship Disaster"`) || strings.HasPrefix(title, "\n") {
		t.Errorf("a title with no edit did not stand alone: %q", title)
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

// One model call now writes both the title and the description, and the only
// thing separating them in the reply is a line the model was asked to prefix.
// It will forget that line, wrap the whole answer in a fence, or put the title
// in quotes -- none of which is worth a failed run. What must not happen is the
// TITLE: line surviving into the description box, or a forgotten line costing
// the description that was written underneath it.
func TestTheTitleIsPeeledOffTheDescription(t *testing.T) {
	const body = "We went in for the chest and came out with nothing.\n\n#tarkov #raid"
	for _, c := range []struct{ name, reply, title, desc string }{
		{"as asked", "TITLE: The Door Was A Lie\n\n" + body, "The Door Was A Lie", body},
		{"lowercase key", "title: The Door Was A Lie\n\n" + body, "The Door Was A Lie", body},
		{"quoted title", "TITLE: \"The Door Was A Lie\"\n\n" + body, "The Door Was A Lie", body},
		{"fenced", "```\nTITLE: The Door Was A Lie\n\n" + body + "\n```", "The Door Was A Lie", body},
		// no line at all: the description is still the product, so it is kept
		// whole and the title box is simply left for the user to fill
		{"no title line", body, "", body},
	} {
		title, desc := splitTitle(c.reply)
		if title != c.title || desc != c.desc {
			t.Errorf("%s: splitTitle = (%q, %q), want (%q, %q)", c.name, title, desc, c.title, c.desc)
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
	body := funcBody(t, "publish.go", `func \(a \*App\) drawThumbnail\(st pubSettings\) error \{`)
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

// The ffmpeg title is gone, and gone means gone. Half a removal is the worst
// outcome available here: the model letters the title AND ffmpeg burns a second
// copy underneath it, which is exactly what the screenshot that started this
// showed. The Settings probe is checked too -- it used to demand the drawtext
// filter, and an ffmpeg build without one nothing uses is not a broken build.
func TestNothingDrawsTheTitleAfterTheModelHas(t *testing.T) {
	src := readSrc(t, "publish.go")
	for _, gone := range []string{"drawtext", "title.txt", "func (a *App) drawTitle("} {
		if strings.Contains(src, gone) {
			t.Errorf("publish.go still refers to %q, so the title may be burned on twice", gone)
		}
	}
	if strings.Contains(readSrc(t, "setup.go"), `"drawtext"`) {
		t.Error("ffFilters still requires drawtext, failing builds over a filter nothing uses")
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
	if !strings.Contains(funcBody(t, "publish.go", `func \(p \*publisher\) putFrames\(fs \[\]string\) \{`),
		"maxPubFrames") {
		t.Error("putFrames does not cap the row, so a hand-edited project can send twenty images")
	}
	// and loading a project has to go through the same cap, not around it
	if !strings.Contains(funcBody(t, "publish.go", `func \(p \*publisher\) apply\(st pubSettings\) \{`),
		"p.putFrames(") {
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
	d := promptDefFor("youtube")
	if d.key != "youtube" || strings.TrimSpace(d.def) == "" {
		t.Fatal(`prompt "youtube" is not in promptDefs`)
	}
	if promptDefFor("thumbnail").def != "" {
		t.Error(`the "thumbnail" prompt is still in the registry -- ` +
			"the job it belonged to is gone, so it is a prompt nobody sends")
	}
	src := readSrc(t, "publish.go")
	for _, want := range []string{
		`promptSlot{"youtube"`, // in the dropdown, opened by its Edit button
		`a.prompt("youtube")`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("step6 is missing %s -- an editable prompt that is not sent is a lie", want)
		}
	}
	if strings.Contains(src, `"thumbnail"`) {
		t.Error("step6 still names the thumbnail prompt, which no longer exists")
	}
}

// What the one remaining prompt must and must not ask for. It writes two things
// in one reply, so the separator has to be spelled out exactly -- without the
// TITLE: line splitTitle has nothing to peel and the title arrives as the first
// paragraph of the description. And asking for JSON would throw away good prose
// over one unescaped quote.
func TestThePublishPromptMatchesHowTheAnswerIsUsed(t *testing.T) {
	for _, want := range []string{
		"TITLE: ",     // the separator splitTitle looks for, verbatim
		"first line",  // ...and where it will look for it
		"Four to sev", // a title short enough to be lettered across a thumbnail
		"No JSON",     // which models volunteer even when asked for prose
	} {
		if !strings.Contains(youtubeSystem, want) {
			t.Errorf("the upload-text prompt no longer says %q", want)
		}
	}
	if strings.Contains(youtubeSystem, "strict JSON") {
		t.Error("the upload-text prompt asks for JSON; the answer is prose and is read as prose")
	}
	// The edit instruction is the user's to write now. A prompt that also wrote
	// one would put a second, unasked-for instruction into a box the user had
	// already filled.
	for _, gone := range []string{"base_frame", "negative_prompt", "EDIT INSTRUCTION"} {
		if strings.Contains(youtubeSystem, gone) {
			t.Errorf("the upload-text prompt still asks for %q, which belonged to the deleted job", gone)
		}
	}
}

// The run's shape, which is what makes it survivable. The text is landed on the
// page before anything is drawn, so an image server that is down costs the
// picture and not the thinking call already paid for; and what is already
// written is never overwritten by ▶, which is what makes ▶ the button you press
// after rewording the instruction.
func TestPublishWritesTheTextBeforeItDraws(t *testing.T) {
	body := funcBody(t, "publish.go", `func \(a \*App\) publishRun\(textOnly bool\) \{`)
	iDesc := strings.Index(body, "writeDescription")
	iDraw := strings.Index(body, "drawThumbnail")
	if iDesc < 0 || iDraw < 0 {
		t.Fatalf("publishRun no longer does both: %d %d", iDesc, iDraw)
	}
	if iDesc > iDraw {
		t.Error("the thumbnail is drawn before the text is written -- a failed draw would then " +
			"throw away an LLM call that had already succeeded")
	}
	if !strings.Contains(body, "landPublish") {
		t.Error("nothing lands the written text on the page, so a failed draw would lose it")
	}
	// The model writes this page's text once per session. What makes ▶ safe to
	// press over and over -- with the instruction reworded, with a different base
	// frame -- is that the gate is the record on disk and not the state of the
	// boxes: a title you emptied is a deletion you made, not a gap to refill.
	if !strings.Contains(body, "a.publishRecorded()") ||
		!strings.Contains(body, "needText := textOnly || !written") ||
		!strings.Contains(body, "if needText {") {
		t.Error("publishRun no longer gates its model call on the step6 record; " +
			"▶ would go back to the LLM after the first run")
	}
	// and the ↻ beside the frames is the opposite: it rewrites and does NOT draw
	if !strings.Contains(body, "if textOnly {\n\t\t\treturn\n\t\t}") {
		t.Error("Suggest again no longer stops before drawing")
	}
}

// The page is split because the two halves are worked on at different times:
// you reword the instruction and press ▶ half a dozen times without touching
// the description, then you rewrite the description without redrawing. Stacked
// in one column they fought for height and the picture -- the thing you are
// judging -- ended up below the fold.
//
// This is a fact about the source because a GTK page cannot be built without a
// display. What it pins is the assignment: which widget went into which side.
func TestThePageIsSplitBetweenTheDrawingAndTheWords(t *testing.T) {
	body := funcBody(t, "publish.go", `func \(a \*App\) buildPublish\(\) gtk.Widgetter \{`)
	iSplit := strings.Index(body, "outer := gtk.NewPaned(gtk.OrientationHorizontal)")
	if iSplit < 0 {
		t.Fatal("the page is no longer two columns")
	}
	// left: the images, what to change, what to keep out, the result
	for _, want := range []string{
		"draw.Append(p.framesBox)",
		"draw.Append(promptBox)",
		"draw.Append(negBox)",
		"draw.Append(shotFrame)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the drawing column is missing %s", want)
		}
	}
	// right: the prompt that writes the words, then the words
	for _, want := range []string{
		`a.promptBar(nil, promptSlot{"youtube"`,
		"text.Append(promptRow)",
		"said.Append(p.title)",
		"said.Append(descBox)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the text column is missing %s", want)
		}
	}
	// and the two lines that belong to neither stay top and bottom, full width
	iRun := strings.Index(body, "page.Append(inRow)")
	iOuter := strings.Index(body, "page.Append(outer)")
	iOut := strings.Index(body, "page.Append(outRow)")
	if iRun < 0 || iOuter < 0 || iOut < 0 || !(iRun < iOuter && iOuter < iOut) {
		t.Errorf("Inputs/Outputs are no longer above and below the split: %d %d %d", iRun, iOuter, iOut)
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

// The tab exists, is in the stack under its own name, and ▶ on it runs the
// step. All three are separate lists, and a page missing from any one of them
// is a tab that opens onto nothing or a ▶ that does nothing.
func TestPublishIsWiredIntoTheShell(t *testing.T) {
	if stepIndex("publish") != len(steps)-1 {
		t.Errorf("step6 is at %d of %d; Publish is the last step", stepIndex("publish"), len(steps))
	}
	if steps[stepIndex("publish")].label != "Publish" {
		t.Errorf("the tab is labelled %q", steps[stepIndex("publish")].label)
	}
	if !strings.Contains(readSrc(t, "main.go"), `a.stack.AddNamed(a.buildPublish(), "publish")`) {
		t.Error("the Publish page is not in the stack; the tab would show an empty page")
	}
	body := funcBody(t, "pipeline.go", `func \(a \*App\) playClicked\(\) \{`)
	if !strings.Contains(body, `case "publish":`) || !strings.Contains(body, "a.publishRun(false)") {
		t.Error("▶ does nothing on the Publish page")
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
