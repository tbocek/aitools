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

// Step 1's outputs are frames in per-recording subfolders, so the count has to
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
		if back[i] != it {
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
		if got[i] != want[i] {
			t.Errorf("source %d migrated to %+v, want %+v", i, got[i], want[i])
		}
	}
}

// ---- autosave ---------------------------------------------------------------

// Where a save lands. The working copy is what the next launch opens, so it is
// written whatever else happens; a file the user named through Save Project is
// written too, because from the moment they name it, that file is the project
// as far as they are concerned. The failure this pins is the quiet one: edits
// after a Save As going only into root/project.json, so the named file the user
// believes they are working in is a session behind.
func TestASaveGoesToTheWorkingCopyAndTheNamedFile(t *testing.T) {
	root := t.TempDir()
	a := &App{root: root}
	work := filepath.Join(root, "project.json")

	if got := a.projectFiles(); len(got) != 1 || got[0] != work {
		t.Errorf("with nothing named, a save goes to %v, want just the working copy", got)
	}
	a.projPath = work // the startup load names the working copy itself
	if got := a.projectFiles(); len(got) != 1 || got[0] != work {
		t.Errorf("after loading the working copy, a save goes to %v -- it must not be written twice", got)
	}
	named := filepath.Join(root, "before-the-recut.json")
	a.projPath = named
	got := a.projectFiles()
	if len(got) != 2 || got[0] != work || got[1] != named {
		t.Errorf("after Save As, a save goes to %v, want both %s and %s", got, work, named)
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
	for _, want := range []string{
		"glib.TimeoutAdd(autosaveTick, func() bool {\n\t\ta.flushProject()",
		"a.win.ConnectCloseRequest(func() bool {\n\t\t\ta.flushProject()",
		"return false // false lets the window close; this only writes",
	} {
		if !strings.Contains(string(src), want) {
			t.Errorf("project.go no longer holds:\n%s", want)
		}
	}
}

// Narrate writes step4/ and nothing else, and no test writes into the live
// session. Both halves of "there is a step5/ folder and I never opened
// Produce": the renumbering that made Narrate step 4 left the old name in
// places that read either way, and the render smoke test rendered a smoke.mp4
// straight into the user's own out/test/step5 on every `go test ./...`,
// clearing its clips on the way past.
func TestOnlyProduceWritesTheProduceFolder(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f == "step5.go" || f == "main.go" || strings.HasSuffix(f, "_test.go") {
			continue // main.go defines it; step5.go is the step that owns it
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "produceDir()") {
			t.Errorf("%s names produceDir() -- a step5/ folder now appears for a user who never opened Produce", f)
		}
		if strings.Contains(string(b), `"step5"`) && f != "pipeline.go" {
			t.Errorf("%s names the step5 folder literally", f)
		}
	}
	// ...the narration's own files are all under step4...
	b, err := os.ReadFile("step4.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "step5") {
		t.Error("step4.go still mentions step5 -- narration used to be written there")
	}
	// ...and the one test that renders for real reads the session but writes
	// into its own folder. A test that leaves files in someone's project is a
	// bug report from a user who did nothing wrong.
	b, err = os.ReadFile("step5_render_test.go")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(b), "\tredirectOutput(t, a"); n < 2 {
		t.Errorf("%d of the render tests redirect their output -- the rest write into the live out/test", n)
	}
}

// ---- the project name in the header bar --------------------------------------

// What the title bar says about the open project. The name and not the path:
// variants of a session live beside each other in one folder, so the leading
// directories are the identical half and the file name is the half that tells
// them apart. The unnamed case has to say something too -- a blank space where
// a file name goes reads as a bug, not as "nothing has been saved yet".
func TestTheHeaderBarNamesTheOpenProject(t *testing.T) {
	name, tip := projLabelText("/home/tom/cuts/before-the-recut.json")
	if name != "before-the-recut.json" {
		t.Errorf("the bar shows %q, want the file name alone", name)
	}
	if tip != "/home/tom/cuts/before-the-recut.json" {
		t.Errorf("the tooltip shows %q, want the whole path -- it is the only place the path is readable", tip)
	}
	name, tip = projLabelText("  ")
	if !strings.Contains(name, "no project") {
		t.Errorf("with nothing saved the bar shows %q, want it to say so in words", name)
	}
	if tip == "" {
		t.Error("with nothing saved the tooltip is empty -- hovering must still answer")
	}
}

// The wiring: the label is ellipsized and capped (a project buried in a long
// name must not walk the centered tabs sideways), and both places that assign
// projPath refresh it. The second half is the one that rots quietly -- a Save
// As that renames the file the autosave follows while the bar keeps showing
// the old name is worse than showing no name at all.
func TestTheProjectNameFollowsSaveAndLoad(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"a.projLabel.SetEllipsize(pango.EllipsizeEnd)",
		"a.projLabel.SetMaxWidthChars(projNameChars)",
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
	for _, fn := range []string{"saveProjectTo", "loadProjectFrom"} {
		body := regexp.MustCompile(`(?s)func \(a \*App\) ` + fn + `\(path string\) \{.*?\n}\n`).Find(p)
		if body == nil {
			t.Fatalf("%s is gone", fn)
		}
		if !strings.Contains(string(body), "a.showProject()") {
			t.Errorf("%s renames the project without telling the header bar", fn)
		}
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
	conf, err := os.ReadFile(a.confPath())
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
		"a.applyPromptStyles(", "a.applySessionCtx(", "a.applyProdSettings(", "a.applyPublish(", "a.setOutDir("} {
		if !strings.Contains(string(load), want) {
			t.Errorf("applyProject no longer calls %s -- New Project would leave that page as it was", want)
		}
	}
}
