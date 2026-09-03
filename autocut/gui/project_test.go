package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A project stores its input and output folders relative to root so that moving
// the autocut directory moves them too. The round trip has to be exact: a
// folder that comes back wrong points the whole pipeline at the wrong files,
// and nothing about that failure says "the project file lied".
func TestFolderRoundTripsThroughRoot(t *testing.T) {
	root := t.TempDir()
	a := &App{root: root}

	for _, dir := range []string{
		root,
		filepath.Join(root, "sessions", "gorilla"),
		filepath.Join(filepath.Dir(root), "elsewhere"),
		"/mnt/recordings",
	} {
		stored := a.relToRoot(dir)
		if got := a.fromRoot(stored); got != dir {
			t.Errorf("%s stored as %q came back as %s", dir, stored, got)
		}
	}

	inside := filepath.Join(root, "sessions")
	if got := a.relToRoot(inside); filepath.IsAbs(got) {
		t.Errorf("a folder under root should be stored relative, got %s", got)
	}
	outside := "/mnt/recordings"
	if got := a.relToRoot(outside); got != outside {
		t.Errorf("a folder outside root has nothing to be relative to, got %s", got)
	}
}

// An absent in_dir/out_dir means the root -- that is what every project written
// before the folders were settable looks like.
func TestEmptyFolderMeansRoot(t *testing.T) {
	a := &App{root: "/home/x/autocut"}
	if got := a.fromRoot(""); got != a.root {
		t.Fatalf("empty folder resolved to %s, want the root", got)
	}
}

// Prepare's outputs are frames in per-recording subfolders, so the count has to
// be recursive: a top-level count would report "2 files" about a few thousand.
func TestOutputSummaryCountsEveryFrame(t *testing.T) {
	dir := t.TempDir()
	if got := summarizeOutputs(dir); got != "nothing yet" {
		t.Fatalf("empty folder summarized as %q", got)
	}

	os.MkdirAll(filepath.Join(dir, "frames", "session1"), 0o755)
	os.WriteFile(filepath.Join(dir, "meta.env"), []byte("x=1\n"), 0o644)
	for _, n := range []string{"f0001.jpg", "f0002.jpg", "f0003.jpg"} {
		os.WriteFile(filepath.Join(dir, "frames", "session1", n), []byte("x"), 0o644)
	}
	got := summarizeOutputs(dir)
	if !strings.HasPrefix(got, "4 files") {
		t.Errorf("summary = %q, want it to start with 4 files", got)
	}
	// freshly written, so the age has to read as now rather than as a date
	if !strings.Contains(got, "just now") {
		t.Errorf("summary = %q, want the age of files written this second", got)
	}
}

// "Add folder" refuses a folder with nothing playable in it, rather than
// reporting that it added nothing: picking the wrong folder is the likely
// reason, and a silent no-op does not say which folder was looked at. This is
// the chooser's guard; listMedia is what it asks.
func TestOnlyAFolderWithMediaIsWorthSwitchingTo(t *testing.T) {
	full := t.TempDir()
	for _, n := range []string{"b.mkv", "a.flac", "notes.txt", "cover.png"} {
		os.WriteFile(filepath.Join(full, n), []byte("x"), 0o644)
	}
	os.MkdirAll(filepath.Join(full, "sub"), 0o755) // a folder is not a source

	if got := listMedia(full); len(got) != 2 || got[0] != "a.flac" || got[1] != "b.mkv" {
		t.Errorf("listMedia = %v, want the two media files sorted by name", got)
	}
	// both ways a pick can be worthless, and both must read the same to the
	// chooser: nothing to switch to
	empty := t.TempDir()
	os.WriteFile(filepath.Join(empty, "readme.md"), []byte("x"), 0o644)
	for _, c := range []struct{ name, dir string }{
		{"a folder with no media in it", empty},
		{"a folder that does not exist", filepath.Join(empty, "nope")},
	} {
		if got := listMedia(c.dir); len(got) != 0 {
			t.Errorf("%s: listMedia = %v, want none", c.name, got)
		}
	}
}

// The two folders are only where the choosers open now, but they are still what
// a pre-merge project's half-relative source names are resolved against -- so
// they have to come out of an old project pointing where they used to. The one
// parent an older project named had to hold input_video/ and input_audio/.
func TestOldProjectsKeepTheirSourceFolders(t *testing.T) {
	for _, c := range []struct {
		name         string
		p            Project
		wantV, wantA string
	}{
		{"a project written since the split",
			Project{VidDir: "footage", AudDir: "/mnt/rec"}, "footage", "/mnt/rec"},
		{"one written before it",
			Project{InDir: "/mnt/session"}, "/mnt/session/input_video", "/mnt/session/input_audio"},
		{"...whose input folder was the root itself",
			Project{}, "input_video", "input_audio"},
		{"one half-written by a version in between",
			Project{InDir: "/mnt/session", AudDir: "/mnt/rec"}, "/mnt/session/input_video", "/mnt/rec"},
	} {
		if v, a := srcDirs(c.p); v != c.wantV || a != c.wantA {
			t.Errorf("%s: srcDirs = (%q, %q), want (%q, %q)", c.name, v, a, c.wantV, c.wantA)
		}
	}

	// and an empty in_dir must resolve to the root's two subfolders, not to the
	// root twice -- that is the case that would silently list nothing
	a := &App{root: "/home/x/autocut"}
	v, _ := srcDirs(Project{})
	if got, want := a.fromRoot(v), "/home/x/autocut/input_video"; got != want {
		t.Fatalf("a project with no folders at all opens on %s, want %s", got, want)
	}
}

// Sources are stored relative to root where they can be, so a project folder
// that moves keeps working; anything outside it is stored as it is. The roles
// travel with them: a project that came back with the footage flags or the
// narrator tags lost is a session that renders the wrong frames in the wrong
// voice, and nothing about that says "the project file lied".
func TestSourcesRoundTripWithTheirRoles(t *testing.T) {
	a := &App{root: "/home/x/autocut"}
	items := []sourceItem{
		{path: "/home/x/autocut/input_video/clip.mkv", footage: true},
		{path: "/mnt/recordings/voice.flac", narrator: 1},
		{path: "/mnt/recordings/mate.flac", narrator: 3},
		{path: "/home/x/autocut/input_video/cam2.mp4", footage: true, narrator: 2},
	}
	var stored []ProjectSource
	for _, it := range items {
		stored = append(stored, ProjectSource{
			Path: a.relToRoot(it.path), Footage: it.footage, Narrator: it.narrator})
	}
	if got, want := stored[0].Path, "input_video/clip.mkv"; got != want {
		t.Errorf("a source under root is stored as %q, want %q", got, want)
	}
	if got, want := stored[1].Path, "/mnt/recordings/voice.flac"; got != want {
		t.Errorf("a source outside root has nothing to be relative to, stored as %q", got)
	}
	back := a.projectSources(Project{Sources: stored})
	if len(back) != len(items) {
		t.Fatalf("%d sources went in, %d came back", len(items), len(back))
	}
	for i, it := range items {
		if !sameSource(back[i], it) {
			t.Errorf("source %d came back as %+v, want %+v", i, back[i], it)
		}
	}
}

// A project written before the two lists became one still has to open on the
// same files, doing the same things. Its videos were the footage; its first
// recording was what "my own voice" cloned -- a convention nothing stated --
// and that is now the narrator 1 tag, which is the same choice said out loud.
func TestOldProjectsBecomeSourcesWithTheirRoles(t *testing.T) {
	root := t.TempDir()
	vid, aud := filepath.Join(root, "input_video"), filepath.Join(root, "rec")
	for _, d := range []string{vid, aud} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// the legacy names, and the file each one has to resolve to
	for _, n := range []string{filepath.Join(vid, "clip.mkv"),
		filepath.Join(aud, "me.flac"), filepath.Join(aud, "mate.flac")} {
		if err := os.WriteFile(n, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	a := &App{root: root, vidDir: vid, audDir: aud}
	got := a.projectSources(Project{
		Videos: []string{"input_video/clip.mkv"},
		// stored when the recordings folder was called something else, so it
		// resolves through the folder rather than through root
		Audios: []string{"input_audio/me.flac", "input_audio/mate.flac"},
	})
	want := []sourceItem{
		{path: filepath.Join(vid, "clip.mkv"), footage: true},
		{path: filepath.Join(aud, "me.flac"), narrator: 1},
		{path: filepath.Join(aud, "mate.flac")},
	}
	if len(got) != len(want) {
		t.Fatalf("migrated to %+v, want %d sources", got, len(want))
	}
	for i := range want {
		if !sameSource(got[i], want[i]) {
			t.Errorf("source %d migrated to %+v, want %+v", i, got[i], want[i])
		}
	}
}

// ---- autosave ---------------------------------------------------------------

// Where a save lands, and where the work lands. One file -- the open one -- and
// one folder, derived from that file's own name, so a project and everything it
// wrote can only be moved together. Two failures behind this: edits after a
// Save As going only into a working copy, leaving the named file a session
// behind, and a session writing into a folder that belonged to a project nobody
// had open any more.
func TestAProjectOwnsTheFolderBesideIt(t *testing.T) {
	const proj = "/mnt/rec/tom.json.autocut"
	if got, want := dataDir(proj), proj+".data"; got != want {
		t.Errorf("%s writes into %s, want %s", proj, got, want)
	}
	// Save takes whatever was typed into the name box. The extension is what
	// the desktop opens with Autocut and what .data hangs off, so it is added
	// rather than hoped for -- and capitals the user typed are theirs to keep.
	for _, c := range []struct{ in, want string }{
		{"/mnt/rec/tom", "/mnt/rec/tom" + projExt},
		{"/mnt/rec/tom.json", "/mnt/rec/tom.json" + projExt},
		{"/mnt/rec/tom" + projExt, "/mnt/rec/tom" + projExt},
		{"/mnt/rec/tom.AUTOCUT", "/mnt/rec/tom.AUTOCUT"},
	} {
		if got := withProjExt(c.in); got != c.want {
			t.Errorf("saving %q writes %q, want %q", c.in, got, c.want)
		}
	}
	// the session nobody has named is a file like any other: same rule, no
	// unsaved special case, and the desktop can open it too
	if !strings.HasSuffix(workName, projExt) {
		t.Errorf("the working copy is %q, which is not a file the desktop would open", workName)
	}
	// and the launch hands the session that file before anything can be saved
	// into it, which is what makes "not saved yet" not a case anywhere else
	main := funcBody(t, "main.go", `func main\(\)`)
	for _, want := range []string{"a.projPath = filepath.Join(wd, workName)", "a.outDir = dataDir(a.projPath)"} {
		if !strings.Contains(main, want) {
			t.Errorf("a session starts without %s, so it starts with no file of its own", want)
		}
	}
	// and a save goes to that one file. It used to go to two -- the named file
	// and a working copy -- which is how edits after a Save As reached only the
	// copy.
	body := funcBody(t, "project.go", `func \(a \*App\) saveProjectNow\(`)
	if !strings.Contains(body, "a.writeProject(a.projPath, b)") {
		t.Errorf("a save no longer writes the open project file:\n%s", body)
	}
}

// Save As moves the work. The folder is derived from the name, so renaming the
// project without renaming the folder leaves the transcripts, frames and
// renders filed under the name it used to have -- and every page of the project
// that is open reads as one that has never been run.
//
// The move is a rename and nothing else: it is instant, it cannot half-happen,
// and when it will not go through the files stay exactly where they are. Nobody
// presses Save expecting gigabytes of frames to be copied, and nobody presses
// it expecting them to be lost either.
func TestSavingUnderANewNameTakesTheWorkWithIt(t *testing.T) {
	root := t.TempDir()
	a := &App{root: root}
	from, to := filepath.Join(root, "a"+projExt+".data"), filepath.Join(root, "b"+projExt+".data")

	// nothing written yet: nothing to move, and no empty folder invented
	a.moveOutputs(from, to)
	if exists(to) {
		t.Errorf("%s was created for a project that had written nothing", to)
	}
	// an empty folder is not work either -- it is what a project that has been
	// opened and not run leaves behind, and moving it says something ran
	if err := os.MkdirAll(from, 0o755); err != nil {
		t.Fatal(err)
	}
	a.moveOutputs(from, to)
	if exists(to) || !exists(from) {
		t.Errorf("an empty %s was moved to %s", from, to)
	}

	if err := os.MkdirAll(filepath.Join(from, "inputs"), 0o755); err != nil {
		t.Fatal(err)
	}
	frame := filepath.Join(from, "inputs", "0001.jpg")
	if err := os.WriteFile(frame, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	a.moveOutputs(from, to)
	if !exists(filepath.Join(to, "inputs", "0001.jpg")) {
		t.Errorf("the work did not follow the project to %s", to)
	}
	if exists(from) {
		t.Errorf("%s is still there -- the work was copied, not moved", from)
	}

	// the new name already has work of its own: the rename fails on it, so
	// nothing is written over and nothing is half-merged in
	if err := os.MkdirAll(filepath.Dir(frame), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(frame, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	a.moveOutputs(from, to)
	if !exists(frame) {
		t.Errorf("%s was moved into a folder that was already in use", frame)
	}

	// and the move happens before the project is pointed at the new folder --
	// the other way round moves a folder the pages have already been drawn from
	body := funcBody(t, "project.go", `func \(a \*App\) saveProjectTo\(`)
	move, set := strings.Index(body, "a.moveOutputs("), strings.Index(body, "a.setProject(")
	if move < 0 || set < 0 || move > set {
		t.Errorf("Save As no longer moves the outputs before renaming the project:\n%s", body)
	}
	// the extension is forced here, or a project saved as "tom" and one saved
	// as "tom.autocut" are two projects with neither name nor folder in common
	if !strings.Contains(body, "withProjExt(path)") {
		t.Errorf("Save takes the typed name as it is:\n%s", body)
	}
	// and a plain Save over the open file is not a move: it would warn about a
	// folder already being in use -- its own
	if !strings.Contains(body, "if path != a.projPath {") {
		t.Errorf("saving the open project under its own name still moves its folder:\n%s", body)
	}
}

// setProject is where the two halves of a project meet: the file the autosave
// follows, and the folder every step writes into. Everything on screen that was
// read out of the LAST project's folder has to be re-read here, or the pages go
// on showing another project's work -- the narration that "was never saved" was
// exactly this, a page built once at startup against the empty session.
func TestPointingAtAProjectRedrawsWhatItsFolderHolds(t *testing.T) {
	body := funcBody(t, "project.go", `func \(a \*App\) setProject\(`)
	if !strings.Contains(body, "a.outDir = dataDir(path)") {
		t.Errorf("the folder is no longer derived from the file:\n%s", body)
	}
	for _, want := range []string{
		"a.showProject()",  // the header bar names the file
		"a.followOutDir()", // and the render target follows it
		`a.voiceSel = ""`,  // the voice is the project's, not the session's
		// ...and so are the seconds it is cloned from, which are read once and
		// held: without this the next project shows this one's picks under the
		// same recording name (narrate_take.go)
		"a.takesRead, a.takesMap = false, nil",
		"a.narr.load()",    // the narration lives in the folder
		"a.prep.refresh()", // and so do the counts on every page
		"a.updateProduceInfo()",
		"a.pub.refresh()",
		"a.refreshCut()", // the Cut page's tracks most of all
		"a.updateGates()",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("setProject no longer does %s, so that part keeps the last project's folder", want)
		}
	}
}

// The autosave writes bytes to a path and notices when they stop matching what
// it last wrote. Those two are all the ticker is; currentProject needs a built
// window, so this covers the half that can be tested without one.
func TestTheAutosaveWritesAndThenKnowsItIsUpToDate(t *testing.T) {
	root := t.TempDir()
	a := &App{root: root}
	p := filepath.Join(root, "project.json")
	if err := a.writeProject(p, []byte("{}\n")); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil || string(b) != "{}\n" {
		t.Fatalf("read back %q, %v", b, err)
	}
	// a write into a folder that is not there fails rather than panicking: the
	// ticker calls this every couple of seconds, and a project saved to a
	// removed drive must cost one log line, not the session
	if err := a.writeProject(filepath.Join(root, "gone", "project.json"), b); err == nil {
		t.Error("writing into a missing folder reported success")
	}
}

// The ticker is a poll, so there is always a window of up to autosaveTick in
// which a change exists only in the widgets -- and the change most likely to
// sit in that window is the last one made, because making it is what tells the
// user they are done. Closing the window has to write, or the file keeps a
// state the user already moved past: a narrator tag taken off, seen taken off,
// and back the next morning because the tick that would have written it never
// came. currentProject needs a built window, so the flush itself cannot run
// here; what this holds is that both the tick and the close go through it.
func TestClosingTheWindowWritesWhatTheTickHasNotSeen(t *testing.T) {
	src, err := os.ReadFile("project.go")
	if err != nil {
		t.Fatal(err)
	}
	// ...and everything the close writes is inside the one handler
	close := funcBody(t, "project.go", `func \(a \*App\) startAutosave\(\)`)
	close = close[strings.Index(close, "ConnectCloseRequest"):]
	for _, want := range []string{"a.narr.flushSave()", "a.flushProject()", "a.flushPrompts()"} {
		if !strings.Contains(close, want) {
			t.Errorf("closing the window no longer runs %s", want)
		}
	}
	for _, want := range []string{
		"glib.TimeoutAdd(autosaveTick, func() bool {\n\t\ta.flushProject()",
		"a.win.ConnectCloseRequest(func() bool {",
		"a.narr.flushSave() // ...and the same for the last line typed",
		"return false // false lets the window close; this only writes",
	} {
		if !strings.Contains(string(src), want) {
			t.Errorf("project.go no longer holds:\n%s", want)
		}
	}
}

// Narrate writes narrate/ and nothing else, and no test writes into the live
// session. Both halves of "there is a produce/ folder and I never opened
// Produce": the renumbering that made Narrate the fourth step left the old name in
// places that read either way, and the render smoke test rendered a smoke.mp4
// straight into the user's own out/test/step5 on every `go test ./...`,
// clearing its clips on the way past.
//
// The folder is produce/ now, and Produce is the ordinary name of a great many
// things, so what is banned is the folder's helper and the literal path.
func TestOnlyProduceWritesTheProduceFolder(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f == "produce.go" || f == "main.go" || strings.HasSuffix(f, "_test.go") {
			continue // main.go defines it; produce.go is the step that owns it
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "produceDir()") {
			t.Errorf("%s names produceDir() -- a produce/ folder now appears for a user who never opened Produce", f)
		}
		if strings.Contains(string(b), `filepath.Join(a.outDir, "produce")`) {
			t.Errorf("%s names the produce folder literally", f)
		}
	}
	// ...the narration's own files are all under step4...
	b, err := os.ReadFile("narrate.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `filepath.Join(a.outDir, "produce")`) {
		t.Error("narrate.go still writes into produce/ -- the narration used to go there")
	}
	// ...and the one test that renders for real reads the session but writes
	// into its own folder. A test that leaves files in someone's project is a
	// bug report from a user who did nothing wrong.
	b, err = os.ReadFile("produce_render_test.go")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(b), "\tredirectOutput(t, a"); n < 2 {
		t.Errorf("%d of the render tests redirect their output -- the rest write into the live out/test", n)
	}
}

// ---- the project name in the header bar --------------------------------------

// What the title bar can say about the open project: the short form, the long
// one, and the hover behind either. The short form is the file name, because
// variants of a session live beside each other in one folder and the leading
// directories are the identical half. The unnamed case has to say something
// too -- a blank space where a file name goes reads as a bug, not as "nothing
// has been saved yet" -- and it has no path to offer, so both forms are the
// same words and the bar may show whichever it has room for.
func TestTheHeaderBarNamesTheOpenProject(t *testing.T) {
	name, full, tip := projLabelText("/home/tom/cuts/before-the-recut.json")
	if name != "before-the-recut.json" {
		t.Errorf("the bar shows %q, want the file name alone", name)
	}
	if full != "/home/tom/cuts/before-the-recut.json" {
		t.Errorf("the long form is %q, want the whole path", full)
	}
	if tip != "/home/tom/cuts/before-the-recut.json" {
		t.Errorf("the tooltip shows %q, want the whole path -- it answers whichever form is on the bar", tip)
	}
	name, full, tip = projLabelText("  ")
	if !strings.Contains(name, "no project") {
		t.Errorf("with nothing saved the bar shows %q, want it to say so in words", name)
	}
	if full != name {
		t.Errorf("with nothing saved the long form is %q and the short one %q -- a bar with "+
			"room to spare would go blank on the difference", full, name)
	}
	if tip == "" {
		t.Error("with nothing saved the tooltip is empty -- hovering must still answer")
	}
}

// The wiring: the label is ellipsized (a project buried in a long name must not
// walk the centered tabs sideways) and both places that assign projPath refresh
// it. The second half is the one that rots quietly -- a Save As that renames
// the file the autosave follows while the bar keeps showing the old name is
// worse than showing no name at all. The cap is fitHeader's now, since it is
// the backstop for one of that ladder's rungs and not for the other.
func TestTheProjectNameFollowsSaveAndLoad(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"a.projLabel.SetEllipsize(pango.EllipsizeEnd)",
		"head.PackStart(a.projLabel)",
	} {
		if !strings.Contains(string(src), want) {
			t.Errorf("the header bar no longer does %s", want)
		}
	}
	p, err := os.ReadFile("project.go")
	if err != nil {
		t.Fatal(err)
	}
	// Both of the two that rename a project go through setProject, and that is
	// what tells the bar -- one place, so the name on screen cannot be the file
	// the autosave stopped following.
	for _, fn := range []string{"saveProjectTo", "loadProjectFrom"} {
		body := regexp.MustCompile(`(?s)func \(a \*App\) ` + fn + `\(path string\) \{.*?\n}\n`).Find(p)
		if body == nil {
			t.Fatalf("%s is gone", fn)
		}
		if !strings.Contains(string(body), "a.setProject(") {
			t.Errorf("%s renames the project without going through setProject", fn)
		}
	}
	if !strings.Contains(string(regexp.MustCompile(`(?s)func \(a \*App\) setProject\(path string\) \{.*?\n}\n`).Find(p)),
		"a.showProject()") {
		t.Error("setProject renames the project without telling the header bar")
	}
	// showProject is also where a rename gets re-priced: a Save As from
	// short.json to a path three folders deep may no longer fit as a path.
	body := regexp.MustCompile(`(?s)func \(a \*App\) showProject\(\) \{.*?\n}\n`).Find(p)
	if body == nil {
		t.Fatal("showProject is gone")
	}
	if !strings.Contains(string(body), "a.fitHeader()") {
		t.Error("showProject names the project without re-fitting the bar to it")
	}
}

// ---- the language, and a project that starts empty --------------------------

// The language is the project's, not the machine's. It used to be a line in
// llm.conf, which meant one language for every session that machine ever cut:
// the German session after the English one came back as gibberish, from a box
// three tabs away that nobody had reason to open. What this pins is the whole
// path -- what a runner reads, what the file stores, and that the setting is
// really gone from the config rather than quietly written in both places.
func TestTheLanguageBelongsToTheProject(t *testing.T) {
	ownConfig(t)
	a := &App{root: t.TempDir()}

	// nothing typed is the default, not an empty language field posted to a
	// server that would then transcribe into whatever it guesses
	if got := a.asrLanguage(); got != defLanguage {
		t.Errorf("an untouched session asks for %q, want %q", got, defLanguage)
	}
	if got := a.projectLanguage(); got != "" {
		t.Errorf("an untouched session STORES %q -- an unset box must not freeze today's default into the file", got)
	}

	// what the box says is what the request carries, whitespace and all removed
	a.setLanguage("  de \n")
	if got := a.asrLanguage(); got != "de" {
		t.Errorf("the run would ask for %q, want de", got)
	}
	if got := a.projectLanguage(); got != "de" {
		t.Errorf("the project would store %q, want de", got)
	}
	// ...and clearing it comes back to the default rather than to ""
	a.setLanguage("")
	if got := a.asrLanguage(); got != defLanguage {
		t.Errorf("a cleared box asks for %q, want the default", got)
	}
	// applyLanguage runs at load time, before any window exists in the tests
	a.applyLanguage("fr")
	if got := a.asrLanguage(); got != "fr" {
		t.Errorf("a loaded project's language came out as %q", got)
	}

	// through the file: stored when set, absent when not
	b, err := json.Marshal(Project{Language: "de"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"language":"de"`) {
		t.Errorf("a project with a language wrote %s", b)
	}
	var back Project
	if err := json.Unmarshal(b, &back); err != nil || back.Language != "de" {
		t.Errorf("the language came back as %q (%v)", back.Language, err)
	}
	if b, err = json.Marshal(Project{}); err != nil || strings.Contains(string(b), "language") {
		t.Errorf("a project with no language wrote %s (%v)", b, err)
	}

	// and it is out of the config: a value left in llm.conf would be a second
	// place to set it, disagreeing with the page silently
	if err := a.writeConf(appConf{Server: "https://x"}); err != nil {
		t.Fatal(err)
	}
	conf, err := os.ReadFile(confPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(conf), "AUDIOCPP_LANGUAGE") {
		t.Errorf("llm.conf still carries the language:\n%s", conf)
	}
}

// New Project hands applyProject a blank one, so "blank" has to mean the
// program's defaults and not Go's zero values. Two of them are the whole reason
// this function exists: interval 0 is not "unset", it is EVERY frame, which is
// gigabytes of jpegs nobody asked for; and a zeroed produce block renders at
// CRF 0 and 0 fps.
func TestANewProjectStartsFromTheDefaultsNotFromZero(t *testing.T) {
	p := blankProject()
	if p.Interval != frameStops[defFrameStop] || p.Interval == 0 {
		t.Errorf("a new project extracts a frame every %gs, want %gs", p.Interval, frameStops[defFrameStop])
	}
	if p.FrameScale != scalePresets[0].Name {
		t.Errorf("a new project's frame size is %q, want %q", p.FrameScale, scalePresets[0].Name)
	}
	if p.Produce == nil {
		t.Fatal("a new project has no produce settings -- the page would load zeroes")
	}
	if *p.Produce != defaultProdSettings() {
		t.Errorf("a new project renders with %+v, want %+v", *p.Produce, defaultProdSettings())
	}
	// everything else is legitimately empty: a new project is an empty session
	if len(p.Sources) > 0 || p.Context != "" || len(p.Prompts) > 0 ||
		p.Publish != nil || p.OutDir != "" || p.Language != "" {
		t.Errorf("a new project came out carrying something: %+v", p)
	}
}

// The reset has to go through applyProject rather than clearing what somebody
// remembered to clear: a page left out is last session's thumbnail, or its
// narration settings, sitting in a project that says it is new -- and nothing
// on screen says so. Pinned by reading the source, because applyProject touches
// widgets and there is no window here.
func TestNewProjectResetsEveryPageThroughApplyProject(t *testing.T) {
	ownConfig(t)
	src, err := os.ReadFile("project.go")
	if err != nil {
		t.Fatal(err)
	}
	body := regexp.MustCompile(`(?s)func \(a \*App\) newProject\(\) \{.*?\n}\n`).Find(src)
	if body == nil {
		t.Fatal("newProject is gone")
	}
	if !strings.Contains(string(body), "a.applyProject(blankProject())") {
		t.Errorf("newProject resets by hand instead of through applyProject:\n%s", body)
	}
	// the one thing it must NOT do is delete or overwrite the named project the
	// session was in: New is not a destructive file operation, it is a session
	// that stops being that file
	for _, no := range []string{"os.Remove", "a.writeProject("} {
		if strings.Contains(string(body), no) {
			t.Errorf("newProject calls %s -- it must leave the named project file alone:\n%s", no, body)
		}
	}
	// every page's applier belongs to the load path, so a new one added later is
	// in the reset by construction
	load := regexp.MustCompile(`(?s)func \(a \*App\) applyProject\(p Project\) \{.*?\n}\n`).Find(src)
	if load == nil {
		t.Fatal("applyProject is gone")
	}
	for _, want := range []string{"a.srcList.load(", "a.setFrameInterval(", "a.applyLanguage(",
		"a.applyPromptStyles(", "a.applySessionCtx(", "a.applyProdSettings(", "a.applyPublish("} {
		if !strings.Contains(string(load), want) {
			t.Errorf("applyProject no longer calls %s -- New Project would leave that page as it was", want)
		}
	}
	// The output folder is the one thing it must NOT take from the file: it is
	// derived from the project's own name, and a stored folder is a second
	// answer that can disagree with the first -- which is how a session ends up
	// writing where nobody is looking.
	if strings.Contains(string(load), "p.OutDir") {
		t.Errorf("applyProject reads the stored output folder instead of deriving it:\n%s", load)
	}
	// and both callers point the session at its file, which is what sets the
	// folder and redraws the pages read out of it
	for _, fn := range []string{"newProject", "loadProjectFrom"} {
		body := funcBody(t, "project.go", `func \(a \*App\) `+fn+`\(`)
		if !strings.Contains(body, "a.setProject(") {
			t.Errorf("%s does not point the session at a file, so the pages keep the last project's folder:\n%s", fn, body)
		}
	}
}

// A project opened under an older name goes on under the new one. The name is
// what the output folder hangs off, so a tom.json left as tom.json would write
// into tom.json.data while everything saved since writes beside a .autocut --
// one rule with two answers, which is the thing this whole change is against.
// It has to happen on the way IN as well as on the way out: Save is not the
// only door.
//
// The old file is left on disk. Nothing here deletes or overwrites a file the
// user has, which is also why a name already in use stops the upgrade instead
// of taking it.
func TestAnOlderProjectGoesOnUnderTheNameTheRuleGivesIt(t *testing.T) {
	ownConfig(t)
	root := t.TempDir()
	a := &App{root: root}

	json := filepath.Join(root, "tom.json")
	if got, want := a.projectName(json), json+projExt; got != want {
		t.Errorf("opening %s continues as %s, want %s", json, got, want)
	}
	// already the new name: nothing to do, and no .autocut.autocut
	named := filepath.Join(root, "tom.json"+projExt)
	if got := a.projectName(named); got != named {
		t.Errorf("opening %s continues as %s", named, got)
	}
	// the new name is taken by a project of its own: it is not written over
	if err := os.WriteFile(named, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := a.projectName(json); got != json {
		t.Errorf("opening %s continues as %s, which is a project that already exists", json, got)
	}

	// The working copy is the one file that gets a name rather than a suffix:
	// root/project.json was never a name anybody chose, and the session it
	// holds is the session, so it comes back as the working copy the launch
	// already looks for.
	if got, want := a.projectName(filepath.Join(root, "project.json")), filepath.Join(root, workName); got != want {
		t.Errorf("the old working copy comes back as %s, want %s", got, want)
	}

	// and the load path is what applies it -- Save is not the only door in
	body := funcBody(t, "project.go", `func \(a \*App\) loadProjectFrom\(`)
	if !strings.Contains(body, "a.setProject(a.projectName(path))") {
		t.Errorf("a loaded project keeps whatever name it was read under:\n%s", body)
	}
	// what it is remembered as has to be the name it is kept under, or the next
	// launch reopens the old file and the upgrade happens again every time
	if !strings.Contains(body, "a.rememberProject(a.projPath)") {
		t.Errorf("the next launch is pointed at the file this one stopped using:\n%s", body)
	}
	// what an older session already wrote is NOT moved: the working copy wrote
	// into the root, which also holds the checkout, and no rename could tell
	// one from the other
	launch := funcBody(t, "main.go", `func \(a \*App\) build\(`)
	for _, no := range []string{"os.Rename", "a.moveOutputs("} {
		if strings.Contains(launch, no) {
			t.Errorf("the launch calls %s to move an old session's outputs, which is a guess", no)
		}
	}
	if !strings.Contains(launch, `case exists(filepath.Join(a.root, "project.json")):`) {
		t.Error("a session saved by an older build no longer opens at all")
	}
}

// Nobody chooses the output folder. It was a button on the Prepare page and a
// line in every project file, which meant two answers to "where does this
// session write" that could disagree -- and when they did, the work went
// somewhere nobody was looking. One answer now: the open project's own name.
func TestNothingChoosesTheOutputFolder(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	sets := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src := readSrc(t, f)
		for _, gone := range []string{"outDirRow", "chooseOutDirDialog", "outLabel", "setOutDir("} {
			if strings.Contains(src, gone) {
				t.Errorf("%s still has %s -- the output folder is derived, not picked", f, gone)
			}
		}
		sets += strings.Count(src, "a.outDir = ")
	}
	// two: the empty session main() starts with, and setProject. A third is a
	// folder that moved without the project file it belongs to.
	if sets != 2 {
		t.Errorf("the output folder is assigned in %d places, want 2 (main and setProject)", sets)
	}
}

// The style is the project's, not the machine's: which kind of video THIS
// session is -- a showcase of towers, a highlight reel, a Short -- is a fact
// about the session, like its notes and its target length. Kept per machine it
// was the last project's answer, so a showcase opened after a highlight reel
// was cut as a highlight reel and the dropdown quietly said so.
//
// applyProject and currentProject both read widgets the rest of a project
// needs, so what runs here is the half that decides the style -- applyStyle,
// which is what a load calls -- and the wiring around it is pinned to the
// source, as every other page-bound path in this package is.
func TestAProjectRemembersItsOwnStyle(t *testing.T) {
	ownConfig(t)
	a := &App{root: t.TempDir()}
	a.loadGlobalPrompts()

	// what a save writes is the wording the Style dropdown is on, which is the
	// cut's: the dropdown lists that job's wordings and every other job
	// follows it (applyStyle), so the six answers are one fact
	src := readSrc(t, "project.go")
	if !strings.Contains(src, `a.promptPickName("cut"),`) {
		t.Error("a save no longer writes the style the dropdown is on")
	}
	if !strings.Contains(src, "Style string `json:\"style,omitempty\"`") {
		t.Error("the project has no style field")
	}
	// ...and a load applies it LAST, after the wordings a pre-merge project
	// may also carry, or those would overwrite it
	i := strings.Index(src, "a.applyPromptStyles(p.PromptStyles, p.PromptPick, p.Prompts)")
	j := strings.Index(src, "a.applyStyle(p.Style)")
	if i < 0 || j < 0 || i > j {
		t.Errorf("applyProject does not apply the style last (wordings %d, style %d)", i, j)
	}
	// only when the project names one: an older file says nothing and the
	// machine's own last answer stands, which is what such a file got anyway
	if !strings.Contains(src, "if p.Style != \"\" {\n\t\ta.applyStyle(p.Style)") {
		t.Error("a project that names no style is not left alone")
	}

	// the load itself, which is applyStyle: it reaches the cut and every job
	// that follows it, over whatever this machine was left on
	a.applyStyle("Highlights")
	a.applyStyle("Showcase")
	if got := a.promptPickName("cut"); got != "Showcase" {
		t.Errorf("after loading a Showcase project the cut is on %q", got)
	}
	if got := a.promptPickName("narrate"); got != "Showcase" {
		t.Errorf("the narration is on %q -- one pick turns every job that has a wording of that name", got)
	}
	if got := a.promptPickName("describe"); got != defStyle {
		t.Errorf("the describer is on %q, want the default -- there is no Showcase describer", got)
	}
	// and back off it again: "this project is General" and "this project
	// predates the field" have to stay different answers, which is why the
	// default is written rather than omitted
	a.applyStyle(defStyle)
	if got := a.promptPickName("cut"); got != defStyle {
		t.Errorf("a General project left the machine on %q", got)
	}

	// the field survives the file, which is the whole point
	blob, err := json.MarshalIndent(Project{Style: "Rating / tier list"}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(blob), `"style": "Rating / tier list"`) {
		t.Errorf("the style is not in the file:\n%s", blob)
	}
	var back Project
	if err := json.Unmarshal(blob, &back); err != nil {
		t.Fatal(err)
	}
	a.applyStyle(back.Style)
	if got := a.promptPickName("cut"); got != "Rating / tier list" {
		t.Errorf("through the file the style came back as %q", got)
	}
	// an empty one is absent from the file rather than written as ""
	if blob, _ := json.Marshal(Project{}); strings.Contains(string(blob), "style") {
		t.Errorf("a project with no style still writes the key: %s", blob)
	}
}
