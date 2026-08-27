package main

// Project state: which sources are in, in what order, and the step settings.
// Saved as plain JSON with root-relative paths, so a project file survives
// moving the autocut directory. root/project.json is the working copy --
// always written, whatever else is open; Save/Load dialogs are for keeping
// named variants, and whichever file is open when the window closes is the one
// the next launch reopens (settings.go).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

type Project struct {
	// the session's files in order, each with what it is for; paths relative to
	// root where they can be
	Sources []ProjectSource `json:"sources,omitempty"`
	// videos/audios are read, never written: membership used to be a folder and
	// a checkbox, one list per folder. A project written back then loads as
	// sources -- its videos as footage, its first recording as narrator 1 --
	// and the keys go away with the next save. See projectSources.
	Videos     []string `json:"videos,omitempty"`
	Audios     []string `json:"audios,omitempty"`
	Interval   float64  `json:"interval"` // seconds between frames; 0 = every frame
	FrameScale string   `json:"frame_scale,omitempty"`
	// what the ASR model is told this session is spoken in. It was a setting --
	// one language for the machine, however many languages its sessions were in
	// -- and it is a property of the footage, so it belongs to the project that
	// names that footage. Absent means defLanguage, the same deal a prompt gets.
	Language string `json:"language,omitempty"`
	VidDir   string `json:"vid_dir,omitempty"` // where the choosers open, each
	AudDir   string `json:"aud_dir,omitempty"` // relative to root when it can be
	// in_dir is read, never written: there was one input folder, which had to
	// hold input_video/ and input_audio/. A project written back then names it
	// here, and those two subfolders are where the two folders above start.
	InDir  string `json:"in_dir,omitempty"`
	OutDir string `json:"out_dir,omitempty"`
	// these four are read, never written: the pages they belonged to had a notes
	// box beside the system prompt, which the runner glued onto the prompt just
	// before sending. One box per step now, so a project written by an older
	// build has its notes folded into the prompt on load (migrateHints) and the
	// keys go away with the next save.
	DescribeHints   string `json:"describe_hints,omitempty"`
	TranscriptHints string `json:"transcript_hints,omitempty"`
	CutHints        string `json:"cut_hints,omitempty"`
	NarrateHints    string `json:"narrate_hints,omitempty"`
	// "pitch" used to live here too -- the output-pitch slider. It is gone; a
	// project written back then still loads, the key is simply ignored.

	// what the editor typed about this session on the Describe page. Stored in
	// full, unlike a prompt: there is no built-in wording for it to differ from
	// (see context.go).
	Context string `json:"context,omitempty"`

	// Read, never written. Prompts is how a project stored an edited system
	// prompt before a job could have more than one wording: one string per job
	// and no name for it. Loading folds each into the default style for that job,
	// which is where it came from, and the next save writes it back under
	// prompt_styles instead.
	Prompts map[string]string `json:"prompts,omitempty"`

	// the wordings this project has something of its own to say about -- one it
	// edited, one it added -- keyed by job then listed by name, and which name
	// each job is set to. Both hold only what differs from what the build ships;
	// see prompts.go for why an untouched prompt is absent rather than copied.
	PromptStyles map[string][]promptStyle `json:"prompt_styles,omitempty"`
	PromptPick   map[string]string        `json:"prompt_pick,omitempty"`

	Produce *prodSettings `json:"produce,omitempty"`
	// the thumbnail and the upload text. Absent until the Publish page has
	// something on it, so an older project -- or a session that stops at the
	// rendered video -- stays as short a file as it was.
	Publish *pubSettings `json:"publish,omitempty"`
}

// ProjectSource is one source as a project stores it. The roles are omitted
// when unset so the common row -- a recording nobody narrates as -- is one
// line, and so a project file stays something you can read and fix by hand.
type ProjectSource struct {
	Path     string `json:"path"`
	Footage  bool   `json:"footage,omitempty"`
	Narrator int    `json:"narrator,omitempty"`
}

// relToRoot stores a folder the way a project file wants it: relative when it
// lives inside root, so moving the autocut directory moves it too; absolute
// when it does not, because then there is nothing to be relative to.
func (a *App) relToRoot(dir string) string {
	if rel, err := filepath.Rel(a.root, dir); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return dir
}

// fromRoot is its inverse, with an empty value meaning "the root itself".
func (a *App) fromRoot(rel string) string {
	switch {
	case rel == "":
		return a.root
	case filepath.IsAbs(rel):
		return rel
	default:
		return filepath.Join(a.root, rel)
	}
}

func (a *App) currentProject() Project {
	scaleName, _ := a.frameScale()
	var prod *prodSettings
	if a.prod != nil {
		st := a.prodSettings()
		// an untouched destination is left unset so it keeps tracking the
		// output folder; a chosen one is stored relative when it lives under
		// root, for the same reason as OutDir -- surviving a move
		switch {
		case a.prod.outAuto:
			st.OutFile = ""
		default:
			st.OutFile = a.relToRoot(st.OutFile)
		}
		prod = &st
	}
	var srcs []ProjectSource
	for _, it := range a.srcList.items {
		srcs = append(srcs, ProjectSource{
			Path: a.relToRoot(it.path), Footage: it.footage, Narrator: it.narrator,
		})
	}
	return Project{
		Sources:      srcs,
		Interval:     a.frameInterval(),
		FrameScale:   scaleName,
		Language:     a.projectLanguage(),
		VidDir:       a.relToRoot(a.vidDir),
		AudDir:       a.relToRoot(a.audDir),
		OutDir:       a.relToRoot(a.outDir),
		Context:      a.sessionCtx(),
		PromptStyles: a.currentPromptStyles(),
		PromptPick:   a.currentPromptPick(),
		Produce:      prod,
		Publish:      a.currentPublish(),
	}
}

// projectSources is a stored project as a list of sources, migrating one
// written before the two lists became one.
//
// The migration has to preserve what the old project meant, which was: these
// videos are the footage, these recordings are the voices, and the first
// recording is the one Narrate clones. That last part was a convention nothing
// stated -- ownVoiceFile took audios[0] -- so it becomes the narrator 1 tag,
// which is the same choice said out loud.
//
// Legacy paths are resolved through the folder, not through root: a project
// from before the folders were settable stores input_video/x.mkv, which is
// relative to a root that may no longer be this one.
func (a *App) projectSources(p Project) []sourceItem {
	if len(p.Sources) > 0 {
		var out []sourceItem
		for _, s := range p.Sources {
			out = append(out, sourceItem{
				path: a.fromRoot(s.Path), footage: s.Footage, narrator: s.Narrator,
			})
		}
		return out
	}
	var out []sourceItem
	for _, l := range []struct {
		dir     string
		files   []string
		footage bool
	}{{a.vidDir, p.Videos, true}, {a.audDir, p.Audios, false}} {
		for i, f := range l.files {
			path := a.fromRoot(f)
			if inDir := filepath.Join(l.dir, filepath.Base(f)); !exists(path) && exists(inDir) {
				path = inDir
			}
			it := sourceItem{path: path, footage: l.footage}
			if !l.footage && i == 0 {
				it.narrator = 1 // what "my own voice" used to mean
			}
			out = append(out, it)
		}
	}
	return out
}

// ---- saving, and then saving itself -----------------------------------------

// projectJSON is the project as it would be written right now. It is also how
// the autosave decides there is anything to write: the bytes are the state, so
// comparing them catches every field of every page without a single page
// having to remember to say it changed. Nothing else could -- the settings are
// spread over five pages, two text buffers, a list and a dozen widgets, and a
// notify-me hook on each of them is a hook somebody will forget on the next
// one, silently, with the symptom appearing a session later as lost work.
func (a *App) projectJSON() []byte {
	b, err := json.MarshalIndent(a.currentProject(), "", "  ")
	if err != nil {
		return nil // cannot happen with these types; not worth a wrong write
	}
	return append(b, '\n')
}

func (a *App) writeProject(path string, b []byte) error {
	return os.WriteFile(path, b, 0o644)
}

// projectFiles is where a save goes. root/project.json is the working copy the
// next launch opens, and is always written; a file named through Save Project
// (or opened through Load Project) is written as well, because the moment the
// user names a file, that file is the project as far as they are concerned.
func (a *App) projectFiles() []string {
	work := filepath.Join(a.root, "project.json")
	if a.projPath == "" || a.projPath == work {
		return []string{work}
	}
	return []string{work, a.projPath}
}

// projNameChars is how much of the project's name the header bar shows before
// it ellipsizes. Wide enough for the names people actually type ("before the
// recut.json"), narrow enough that it cannot walk the centered tabs sideways
// on a small window.
const projNameChars = 28

// projLabelText is what the header bar says about a project path: the file's
// name, and the whole path as the tooltip behind it. The name alone, not the
// path -- every project in a session usually lives in the same folder, so the
// leading directories are the part that is identical between them and the name
// is the part that is not, and an ellipsized path would cut away exactly the
// half worth reading.
func projLabelText(path string) (name, tip string) {
	if strings.TrimSpace(path) == "" {
		return "no project file", "this session has not been saved to a project file yet"
	}
	return filepath.Base(path), path
}

// showProject puts the open project's name in the header bar. Called from both
// places that assign projPath, so the bar can never name a file the autosave
// has stopped following.
func (a *App) showProject() {
	if a.projLabel == nil {
		return // headless (tests)
	}
	name, tip := projLabelText(a.projPath)
	a.projLabel.SetText(name)
	a.projLabel.SetTooltipText(tip)
}

// saveProjectNow writes every target and remembers what it wrote. Quiet: it is
// what the ticker and the step runners call, and a status line per autosave
// would push the message the user is actually waiting on off the bar.
func (a *App) saveProjectNow() {
	b := a.projectJSON()
	if b == nil {
		return
	}
	// before the writes, not after: a target that cannot be written must not be
	// retried every tick for the rest of the session, and the log line below
	// says it once per change either way.
	a.projSaved = b
	for _, p := range a.projectFiles() {
		if err := a.writeProject(p, b); err != nil {
			a.logf("save project: %v", err)
		}
	}
}

// saveProjectTo is the explicit save behind the button: it names the file the
// autosave follows from here on, and says so.
func (a *App) saveProjectTo(path string) {
	a.projPath = path
	a.showProject()
	a.rememberProject(path)
	a.saveProjectNow()
	a.setStatus("project saved: " + path)
}

// autosaveTick is how often the project is compared with what is on disk.
// Long enough that typing a prompt is not a write per keystroke, short enough
// that closing the window after a change loses nothing anyone would notice.
const autosaveTick = 2000

// startAutosave keeps the file up to date without a button. Save Project stays
// -- it is how a variant gets a name -- but it stopped being the thing that
// decides whether tonight's work exists in the morning.
//
// A ticker rather than a debounce on every setter, for the reason projectJSON
// gives. The tick costs one marshal of a few kB, and closing the window
// flushes whatever the last tick has not seen yet.
func (a *App) startAutosave() {
	a.projSaved = a.projectJSON() // what the startup load already put in memory
	glib.TimeoutAdd(autosaveTick, func() bool {
		a.flushProject()
		return true
	})
	// the tick leaves a gap of up to autosaveTick between a change and the
	// file, and the change most likely to land in it is the LAST one -- you
	// click it, you see it happen, you close the window. That gap is what made
	// a narrator tag come back after it was taken off: the tick that caught
	// the tagging ran, the one that would have caught the untagging never did.
	if a.win != nil {
		a.win.ConnectCloseRequest(func() bool {
			a.flushProject()
			return false // false lets the window close; this only writes
		})
	}
}

// flushProject writes the project if it differs from what is on disk. The
// compare is what makes a tick free in practice: a session spends most of its
// time not changing anything, and an unchanged project writes nothing at all.
func (a *App) flushProject() {
	if b := a.projectJSON(); b != nil && !bytes.Equal(b, a.projSaved) {
		a.saveProjectNow()
	}
}

func (a *App) loadProjectFrom(path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		a.logf("load project: %v", err)
		return
	}
	var p Project
	if err := json.Unmarshal(b, &p); err != nil {
		a.logf("load project: %v", err)
		return
	}
	a.applyProject(p)
	// what is open is what the autosave keeps: opening a variant and then
	// editing it must not quietly write the edits into the working copy alone
	a.projPath = path
	a.showProject()
	a.rememberProject(path)
	a.projSaved = a.projectJSON()
	a.setStatus("project loaded: " + path)
}

// applyProject puts a project on screen -- every page of it. Split out of
// loadProjectFrom because New Project needs exactly this and nothing else:
// handed a blank project it walks the same list and each page comes back to its
// default, which is a reset that cannot forget a page. A newProject() that
// cleared what it could remember to clear would leave the last session's
// narration settings, or its thumbnail, in a project claiming to be new -- the
// invisible kind of bug applyPromptStyles's comment is about.
func (a *App) applyProject(p Project) {
	// the folders first: they are where the file choosers open, and where a
	// pre-merge project's half-relative source names are resolved from
	vid, aud := srcDirs(p)
	a.vidDir, a.audDir = a.fromRoot(vid), a.fromRoot(aud)
	items := a.projectSources(p)
	// a project entry whose file vanished (renamed, moved) must be LOUD: a
	// silently-dropped source once cost half a debugging session
	for _, it := range items {
		if !exists(it.path) {
			a.logf("WARNING: project references %s, which is not there any more -- it was dropped from the session (renamed? moved?)", it.path)
		}
	}
	a.srcList.load(items)
	a.srcList.prune()
	a.setFrameInterval(p.Interval)
	if p.FrameScale != "" {
		a.setFrameScale(p.FrameScale)
	}
	a.applyLanguage(p.Language)
	a.applyPromptStyles(p.PromptStyles, p.PromptPick, p.Prompts)
	a.applySessionCtx(p.Context)
	a.migrateHints(p)
	a.applyProdSettings(p.Produce)
	a.applyPublish(p.Publish)
	a.setOutDir(a.fromRoot(p.OutDir))
	// the Cut page is drawn from the OTHER project's folder until something
	// says so; the sources on its tracks have just been replaced wholesale
	a.refreshCut()
}

// blankProject is what New Project starts from: an empty session, and the
// defaults that are the program's rather than the zero value's.
//
// Interval and Produce are stated because their zero values are real settings
// and the wrong ones -- 0 seconds means EVERY frame, which is gigabytes nobody
// asked for, and a zeroed produce block is a 0-CRF, 0-fps render. Everything
// else is legitimately empty: no sources, nothing typed, no prompt edited, no
// thumbnail, and the output folder back to the autocut root.
func blankProject() Project {
	prod := defaultProdSettings()
	return Project{
		Interval:   frameStops[defFrameStop],
		FrameScale: scalePresets[0].Name,
		Produce:    &prod,
	}
}

// newProject empties the session. The project FILE is not deleted and not
// written over here: what the autosave follows afterwards is the working copy
// again (projPath ""), so a named project that was open stays on disk exactly
// as it was, and going back to it is Load, not undo.
func (a *App) newProject() {
	a.applyProject(blankProject())
	a.projPath = ""
	a.showProject()
	// the working copy is now the open project, and that is a decision, not an
	// absence: without this the next launch reopens the named project this was
	// meant to get away from (see rememberProject)
	a.rememberProject(filepath.Join(a.root, "project.json"))
	a.saveProjectNow()
	a.setStatus("new project — the session is empty")
	a.logf(">>> new project: the session was emptied; outputs already on disk are untouched")
}

// newProjectDialog asks first. The session is not a file until Save names one,
// so New on a session nobody saved throws away work that exists nowhere else --
// and it is one click from Load and Save in the header bar, which are the two
// buttons a hand reaching for it is aiming between.
//
// Nothing to lose, no question: an empty session being emptied is not a
// decision worth interrupting anyone for.
func (a *App) newProjectDialog() {
	if a.running {
		a.setStatus("stop the run first — a new project would pull its inputs out from under it")
		return
	}
	if len(a.srcList.items) == 0 && a.sessionCtx() == "" {
		a.newProject()
		return
	}
	detail := "The sources, the session context and every prompt edit go back to empty. " +
		"Files already written to the output folder are left alone."
	if a.projPath != "" {
		detail += "\n\n" + filepath.Base(a.projPath) + " stays on disk as it is -- " +
			"this session simply stops being it."
	} else {
		detail += "\n\nThis session has never been saved to a named project file, " +
			"so there is nothing to come back to."
	}
	a.confirm("Start a new project?", detail, "Start new", a.newProject)
}

// confirm is a modal yes/no. Hand-rolled on a plain window for the reason the
// settings dialog is: GtkAlertDialog's constructor is variadic and does not
// survive the binding, and this needs no more than the two buttons anyway.
// Destructive-action styling, because that is what the left button means.
func (a *App) confirm(question, detail, okLabel string, ok func()) {
	win := gtk.NewWindow()
	win.SetTransientFor(&a.win.Window)
	win.SetModal(true)
	win.SetTitle(question)
	win.SetDefaultSize(420, -1)

	q := gtk.NewLabel(question)
	q.SetXAlign(0)
	q.SetWrap(true)
	q.AddCSSClass("heading")
	d := gtk.NewLabel(detail)
	d.SetXAlign(0)
	d.SetWrap(true)
	d.AddCSSClass("dim-label")

	cancel := gtk.NewButtonWithLabel("Cancel")
	cancel.ConnectClicked(func() { win.Close() })
	go1 := gtk.NewButtonWithLabel(okLabel)
	go1.AddCSSClass("destructive-action")
	go1.ConnectClicked(func() {
		win.Close()
		ok()
	})
	btns := gtk.NewBox(gtk.OrientationHorizontal, 8)
	btns.SetHAlign(gtk.AlignEnd)
	btns.SetMarginTop(8)
	btns.Append(cancel)
	btns.Append(go1)

	box := gtk.NewBox(gtk.OrientationVertical, 8)
	box.SetMarginTop(16)
	box.SetMarginBottom(16)
	box.SetMarginStart(16)
	box.SetMarginEnd(16)
	box.Append(q)
	box.Append(d)
	box.Append(btns)
	win.SetChild(box)
	// Escape is Cancel, as it is in every other dialog; the default is Cancel
	// too, so a blind Enter on a destructive question does nothing
	cancel.GrabFocus()
	win.SetVisible(true)
}

// migrateHints folds a pre-merge project's notes into the prompts they used to
// be appended to. The runners did that concatenation at request time, using
// exactly these lead-ins, so folding them in preserves what the model saw;
// dropping them would silently change what a reloaded project sends. Must run
// after applyPromptStyles, which is what puts the prompt in the box in the first
// place.
func (a *App) migrateHints(p Project) {
	fold := func(key, notes, lead string) {
		notes = strings.TrimSpace(notes)
		if notes == "" {
			return
		}
		cur := a.prompt(key)
		if strings.Contains(cur, notes) {
			return // already folded in, by this load or an earlier one
		}
		merged := cur + "\n" + lead + "\n" + notes
		a.setPrompt(key, merged)
		if tv := a.promptViews[key]; tv != nil {
			tv.Buffer().SetText(merged)
		}
		if a.log != nil { // not during tests, which have no window
			a.logf("moved this project's notes into the %q prompt -- they are one box now", key)
		}
	}
	fold("describe", p.DescribeHints, "Editor's notes about this footage -- trust them:")
	fold("fix", p.TranscriptHints, "Editor's notes -- trust them:")
	fold("cut", p.CutHints, "Editor's notes about this session -- trust them and let them guide what matters:")
	fold("narrate", p.NarrateHints, "Editor's goals and context -- honor them:")
}

// srcDirs says where a project's two source folders are, still relative to root
// where it stored them that way. A project written before the split names one
// folder that had to hold input_video/ and input_audio/, and those two
// subfolders are exactly the folders it meant -- so an old project keeps
// pointing at the same files instead of opening on two empty lists.
func srcDirs(p Project) (vid, aud string) {
	vid, aud = p.VidDir, p.AudDir
	if vid == "" {
		vid = filepath.Join(p.InDir, "input_video")
	}
	if aud == "" {
		aud = filepath.Join(p.InDir, "input_audio")
	}
	return vid, aud
}

// addFilesDialog adds files to the session, several at a time -- a card holds
// one take per angle and picking them one dialog at a time is not a workflow.
func (a *App) addFilesDialog() {
	d := gtk.NewFileDialog()
	d.SetTitle("Add sources")
	d.SetInitialFolder(gio.NewFileForPath(a.vidDir))
	filt := gtk.NewFileFilter()
	filt.SetName("Audio and video")
	for e := range mediaExt {
		filt.AddSuffix(strings.TrimPrefix(e, "."))
	}
	filters := gio.NewListStore(gtk.GTypeFileFilter)
	filters.Append(filt.Object)
	d.SetFilters(filters)
	d.OpenMultiple(context.Background(), &a.win.Window, func(res gio.AsyncResulter) {
		lm, err := d.OpenMultipleFinish(res)
		if err != nil || lm == nil {
			return // dismissed
		}
		var paths []string
		for i := uint(0); i < lm.NItems(); i++ {
			if obj := lm.Item(i); obj != nil {
				paths = append(paths, (&gio.File{Object: obj}).Path())
			}
		}
		a.addSources(paths...)
	})
}

// addFolderDialog adds everything playable in a folder: how a session arrives
// off a card, and what the two folder pickers on this page used to be for.
func (a *App) addFolderDialog() {
	d := gtk.NewFileDialog()
	d.SetTitle("Add every recording in a folder")
	d.SetInitialFolder(gio.NewFileForPath(a.vidDir))
	d.SelectFolder(context.Background(), &a.win.Window, func(res gio.AsyncResulter) {
		f, err := d.SelectFolderFinish(res)
		if err != nil || f == nil {
			return // dismissed
		}
		dir := f.Path()
		if len(listMedia(dir)) == 0 {
			a.logf("nothing added: %s holds no video or audio files", dir)
			a.setStatus("nothing playable in that folder")
			return
		}
		var paths []string
		for _, n := range listMedia(dir) {
			paths = append(paths, filepath.Join(dir, n))
		}
		a.addSources(paths...)
	})
}

// addSources puts files in the list and remembers where they came from, so the
// next chooser opens where the last one did. The two folders are no longer what
// the list is made of -- they are only where it starts looking.
func (a *App) addSources(paths ...string) {
	n := a.srcList.add(paths...)
	for _, p := range paths {
		if isVideo(p) {
			a.vidDir = filepath.Dir(p)
		} else {
			a.audDir = filepath.Dir(p)
		}
	}
	switch {
	case n == 0:
		a.setStatus("already in the session — nothing added")
	case n == len(paths):
		a.setStatus(fmt.Sprintf("added %d source(s)", n))
	default:
		a.setStatus(fmt.Sprintf("added %d of %d — the rest were already in", n, len(paths)))
	}
}

func (a *App) chooseOutDirDialog() {
	d := gtk.NewFileDialog()
	d.SetInitialFolder(gio.NewFileForPath(a.outDir))
	d.SelectFolder(context.Background(), &a.win.Window, func(res gio.AsyncResulter) {
		f, err := d.SelectFolderFinish(res)
		if err != nil || f == nil {
			return
		}
		a.setOutDir(f.Path())
	})
}

func (a *App) saveProjectDialog() {
	d := gtk.NewFileDialog()
	d.SetInitialFolder(gio.NewFileForPath(a.root))
	d.SetInitialName("project.json")
	d.Save(context.Background(), &a.win.Window, func(res gio.AsyncResulter) {
		f, err := d.SaveFinish(res)
		if err != nil || f == nil {
			return // dismissed
		}
		a.saveProjectTo(f.Path())
	})
}

func (a *App) loadProjectDialog() {
	d := gtk.NewFileDialog()
	d.SetInitialFolder(gio.NewFileForPath(a.root))
	d.Open(context.Background(), &a.win.Window, func(res gio.AsyncResulter) {
		f, err := d.OpenFinish(res)
		if err != nil || f == nil {
			return
		}
		a.loadProjectFrom(f.Path())
	})
}

// openFolder shows a directory in the user's file manager -- via the desktop
// portal (works on any DE, unlike bare xdg-open from an app context), falling
// back to xdg-open. The directory is created first: launching a missing path
// silently does nothing, which reads as a dead button.
func (a *App) openFolder(dir string) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		a.logf("open folder: %v", err)
		return
	}
	l := gtk.NewFileLauncher(gio.NewFileForPath(dir))
	l.Launch(context.Background(), &a.win.Window, func(res gio.AsyncResulter) {
		if err := l.LaunchFinish(res); err != nil {
			a.logf("portal launch failed (%v), trying xdg-open", err)
			if err := exec.Command("xdg-open", dir).Start(); err != nil {
				a.logf("xdg-open: %v", err)
			}
		}
	})
}

// ---- output inspection ------------------------------------------------------

// summarizeOutputs is the one-line answer: how many files are under dir, and
// how long ago the newest was written. Recursive, because preprocessing's output is
// mostly frames in subdirectories -- a top-level count would say "3 entries"
// about four thousand files. The age is what tells a finished step from a
// stale one at a glance.
func summarizeOutputs(dir string) string {
	n, newest := countOutputs(dir)
	if n == 0 {
		return "nothing yet"
	}
	return fmt.Sprintf("%d files, newest %s", n, humanAgo(newest))
}

// countOutputs is the same walk with the two halves kept apart. Preprocessing
// shows three of these at once and puts the age on hover: three sentences of
// the form above, side by side, is a paragraph across the bottom of a page.
func countOutputs(dir string) (n int, newest time.Time) {
	filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil // an unreadable subtree is not worth a whole error line here
		}
		n++
		if fi, err := d.Info(); err == nil && fi.ModTime().After(newest) {
			newest = fi.ModTime()
		}
		return nil
	})
	return n, newest
}

func humanAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	case d < 48*time.Hour:
		return "yesterday"
	default:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	}
}

// logListMax is how many files a directory gets to name before it becomes a
// count. Preprocessing writes one frame per second of footage; a log carrying four
// thousand f000123.jpg lines is a log nobody scrolls, and the run's own
// messages would be buried in it.
const logListMax = 12

// logOutputs names what a step actually wrote, in the log, and returns the file
// count for the status line. The page shows the count; the names live here,
// because that is the place you can scroll back through after a run.
// GUI thread only -- callers are completion handlers.
func (a *App) logOutputs(label, dir string) int {
	n := a.logTree(dir, label) // label leads every path, so the log says which step
	if n == 0 {
		a.logf("    %s/: nothing written", label)
	}
	return n
}

func (a *App) logTree(dir, rel string) int {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	var files []os.DirEntry
	for _, e := range ents {
		if e.IsDir() {
			n += a.logTree(filepath.Join(dir, e.Name()), filepath.Join(rel, e.Name()))
			continue
		}
		files = append(files, e)
	}
	if len(files) > logListMax {
		a.logf("    %s/ — %d files (%s … %s)", rel, len(files),
			files[0].Name(), files[len(files)-1].Name())
	} else {
		for _, e := range files {
			size := ""
			if fi, err := e.Info(); err == nil {
				size = "  (" + humanSize(fi.Size()) + ")"
			}
			a.logf("    %s%s", filepath.Join(rel, e.Name()), size)
		}
	}
	return n + len(files)
}

func humanSize(n int64) string {
	switch {
	case n > 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n > 1<<10:
		return fmt.Sprintf("%.0f kB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
