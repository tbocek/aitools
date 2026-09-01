package main

// Project state: which sources are in, in what order, and the step settings.
// Saved as plain JSON with root-relative paths, so a project file survives
// moving the autocut directory. There is always an open file -- root/session.autocut
// until Save names another one -- and it is the only file a save writes; Save/Load
// dialogs are for keeping named variants, and whichever file is open when the
// window closes is the one the next launch reopens (settings.go).

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
	InDir string `json:"in_dir,omitempty"`
	// out_dir is read, never written: the output folder used to be chosen and
	// stored, and is now derived from the project file's own name (see projExt).
	// It is read only to say, once, where an older project's work was left.
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

	// All three are read, never written. The prompts are the machine's now --
	// text files under ~/.config/autocut/prompts, see promptstore.go -- because
	// how you like to be edited for is the same in January's raid as in
	// March's, and kept in the project it started from the shipped wording
	// again every time a session began.
	//
	// A project that still carries them is one written before that, and what it
	// carries is adopted on load, once, for whichever jobs this machine has
	// nothing of its own for (applyPromptStyles). Nothing is deleted from the
	// file: it is left exactly as it was, so an older build opening it finds
	// its wordings where it left them.
	//
	// Prompts is the oldest of the three, from before a job could have more
	// than one wording: one string per job and no name for it. It belongs to
	// whichever style was the default at the time, which is the default now.
	Prompts      map[string]string        `json:"prompts,omitempty"`
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
	// the wish, not the result: it is saved because a project closed before ▶
	// has to open with the same rows still flagged, and it is cleared by the
	// run that grants it, so a finished project stores nothing here
	SepVoice bool `json:"sepvoice,omitempty"`
	// which audio tracks of a multi-track file this session uses (a:N indices).
	// Omitted for the ordinary answer -- the first track alone -- so a project
	// written before multi-track files were reachable already says the right
	// thing by saying nothing, and no migration was needed (wantTracks).
	Tracks []int `json:"tracks,omitempty"`
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

// A project is a file you can double-click, and the folder it writes into is
// its own path with .data on the end: /mnt/rec/tom.json.autocut writes into
// /mnt/rec/tom.json.autocut.data. Nothing chooses that folder and nothing
// stores it. Stored, it was a second answer to "where does this session write"
// that could disagree with the first -- a project copied to another machine,
// or a folder emptied between sessions, and the work went somewhere nobody was
// looking. Derived, the two travel together and cannot come apart, and the
// answer to "where are my transcripts" is the project's own name.
//
// Which is also why a session nobody has saved still has a file: the working
// copy is root/session.autocut, so the rule is the same everywhere and there
// is no unsaved special case. Save then renames the pair.
const projExt = ".autocut"

// workName is the working copy inside the autocut root: what the first launch
// starts as, and what New Project goes back to.
const workName = "session" + projExt

// dataDir is a project's output folder. One line, so that every step, every
// page and the Save that moves the folder all mean the same folder.
func dataDir(proj string) string { return proj + ".data" }

// projectName is the file an opened project is kept as from here on, which is
// not always the file it was read from. A project saved before projects were
// files the desktop could open is a .json, and the name is what the output
// folder hangs off -- so leaving it as it was would mean tom.json writing into
// tom.json.data while everything saved since writes beside a .autocut. One
// rule, so opening tom.json continues as tom.json.autocut.
//
// The old file is not touched and not deleted: it stays on disk as the last
// thing that build wrote, and the session simply goes on under the new name.
// Unless that name is taken -- then the .json keeps its own, because quietly
// autosaving over a project the user already has is not an upgrade.
//
// root/project.json is the exception, and not really one: it was never a name
// anybody chose, it was the working copy, and the working copy has a name of
// its own now.
func (a *App) projectName(path string) string {
	if path == filepath.Join(a.root, "project.json") {
		return filepath.Join(a.root, workName)
	}
	up := withProjExt(path)
	switch {
	case up == path:
		return path
	case exists(up):
		a.logf("!!! %s is still open under its old name: %s is already there, and this "+
			"session would have written over it", filepath.Base(path), filepath.Base(up))
		return path
	}
	a.logf(">>> %s is open as %s from here on; %s is left on disk as it is",
		filepath.Base(path), filepath.Base(up), filepath.Base(path))
	return up
}

// withProjExt is what Save actually writes, whatever was typed in the name box.
// The extension is not decoration: it is what the desktop matches to open a
// file with Autocut (icon.go), and it is what .data hangs off, so "tom" and
// "tom.autocut" would otherwise be two projects sharing neither name nor work.
func withProjExt(p string) string {
	if strings.EqualFold(filepath.Ext(p), projExt) {
		return p
	}
	return p + projExt
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
			SepVoice: it.sepVoice, Tracks: it.tracks,
		})
	}
	return Project{
		Sources:    srcs,
		Interval:   a.frameInterval(),
		FrameScale: scaleName,
		Language:   a.projectLanguage(),
		VidDir:     a.relToRoot(a.vidDir),
		AudDir:     a.relToRoot(a.audDir),
		Context:    a.sessionCtx(),
		Produce:    prod,
		Publish:    a.currentPublish(),
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
				sepVoice: s.SepVoice, tracks: s.Tracks,
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

// projNameChars is how much of the project's name the header bar shows before
// it ellipsizes. Wide enough for the names people actually type ("before the
// recut.json"), narrow enough that it cannot walk the centered tabs sideways
// on a small window.
const projNameChars = 28

// projLabelText is what the header bar can say about a project path: the file's
// name, the whole path, and the tooltip behind whichever of the two is on
// screen. Which one is shown is a question of width and belongs to fitHeader;
// this is only the wording, including the one for a session that has never been
// saved -- a blank space where a file name goes reads as a bug rather than as
// "nothing has been written yet". That case has no path to offer, so it gives
// the same words twice and the bar is free to pick either.
func projLabelText(p string) (name, full, tip string) {
	if strings.TrimSpace(p) == "" {
		none := "no project file"
		return none, none, "this session has not been saved to a project file yet"
	}
	return filepath.Base(p), p, p
}

// showProject puts the open project in the header bar. Called from both places
// that assign projPath, so the bar can never name a file the autosave has
// stopped following. The name first and then the fit, because the fit is what
// decides between the name and the path and it declines to answer at all
// before the bar has been laid out -- at which point the name is the right
// thing to be showing.
func (a *App) showProject() {
	if a.projLabel == nil {
		return // headless (tests)
	}
	name, _, tip := projLabelText(a.projPath)
	a.projLabel.SetText(name)
	a.projLabel.SetTooltipText(tip)
	a.fitHeader()
}

// setProject points the session at a project file: the file the autosave
// follows, and the folder beside it that every step writes into. The two are
// one decision (see projExt), so they are made in one place, and everything on
// screen that was read out of the old folder is re-read here.
func (a *App) setProject(path string) {
	a.projPath = path
	a.outDir = dataDir(path)
	a.showProject()
	a.followOutDir()
	// the voice belongs to the project, not to the session: drop the cached id,
	// pitch and hand-picked takes so all three are re-read from the folder we
	// just moved to
	a.voiceMu.Lock()
	a.voiceSel = ""
	a.takesRead, a.takesMap = false, nil
	a.pitchRead = false
	a.voiceMu.Unlock()
	if a.voicePick != nil {
		a.voicePick.syncSelection()
	}
	// The narration lives in the output folder, and this is the only thing that
	// re-reads it: the page is built once at startup, against the empty
	// session's folder, so without this a saved narration would never load for
	// any project at all -- it looked like narration was never saved.
	if a.narr != nil {
		a.narr.load()
		a.narr.rebuildRows()
	}
	// every page shows state derived from the output folder -- refresh all.
	// The Cut page included: its tracks were just drawn from the folder of the
	// project we are leaving.
	a.prep.refresh()
	a.updateProduceInfo()
	a.pub.refresh()
	a.refreshCut()
	a.updateGates()
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
	// One file: the open one. It used to be two -- the named file and a working
	// copy the next launch opened -- and the working copy is now simply the
	// project that is open when nobody has named one, so there is nothing left
	// to keep in step with anything.
	if err := a.writeProject(a.projPath, b); err != nil {
		a.logf("save project: %v", err)
	}
}

// saveProjectTo is the explicit save behind the button: it names the file the
// autosave follows from here on, and says so.
//
// The work follows the name. Transcripts, frames and renders live in a folder
// derived from the file name (see projExt), so a Save As without the move would
// leave a project whose every output is filed under the name it used to have,
// and which reads, on every page, as one that has never been run.
func (a *App) saveProjectTo(path string) {
	path = withProjExt(path)
	if path != a.projPath {
		a.moveOutputs(a.outDir, dataDir(path))
		a.setProject(path)
	}
	a.rememberProject(path)
	a.saveProjectNow()
	a.setStatus("project saved")
}

// moveOutputs takes one project's output folder to another's name. A rename is
// instant on one filesystem and cannot half-happen, which is why it is a rename
// and not a copy: nobody presses Save expecting gigabytes of frames to be
// duplicated. It is also the whole check -- a rename onto a folder that already
// has work in it fails, and onto one that is empty succeeds, which is exactly
// the answer wanted in both cases. When it will not go through, across
// filesystems or into a name in use, the files stay where they are and the log
// says where, because a Save that silently moved work would be worse than one
// that did not.
//
// Silent when there is nothing to move, which is the ordinary case: a project
// saved before it has been run has an empty folder or none at all.
func (a *App) moveOutputs(from, to string) {
	n, _ := countOutputs(from)
	if n == 0 {
		return
	}
	if err := os.Rename(from, to); err != nil {
		a.logf("!!! could not move the output folder to %s: %v -- the %d file(s) are still in %s",
			to, err, n, from)
		return
	}
	a.logf(">>> moved the output folder to %s", to)
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
		a.flushPrompts() // the prompts are the machine's, not the project's, and
		return true      // are written on the same tick for the same reason
	})
	// the tick leaves a gap of up to autosaveTick between a change and the
	// file, and the change most likely to land in it is the LAST one -- you
	// click it, you see it happen, you close the window. That gap is what made
	// a narrator tag come back after it was taken off: the tick that caught
	// the tagging ran, the one that would have caught the untagging never did.
	if a.win != nil {
		a.win.ConnectCloseRequest(func() bool {
			a.narr.flushSave() // ...and the same for the last line typed
			a.flushProject()
			a.flushPrompts()
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
	// After applyProject, exactly where the output folder used to be set: what
	// is open is what the autosave keeps, and every page that reads the folder
	// is now reading this project's own rather than the last one's. Under the
	// name it is kept as, which for an older project is not the one it was read
	// from (projectName).
	a.setProject(a.projectName(path))
	if p.OutDir != "" && a.fromRoot(p.OutDir) != a.outDir {
		a.logf("!!! this project used to write into %s and now writes into %s -- "+
			"the old folder is untouched", a.fromRoot(p.OutDir), a.outDir)
	}
	a.rememberProject(a.projPath)
	a.projSaved = a.projectJSON()
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
			a.logf("!!! %s is not there any more -- dropped from the session", it.path)
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
	// Every page except the ones drawn from the output folder -- the Cut page,
	// the counts, the narration. The stored out_dir is deliberately not read:
	// the folder is derived from the file's own name (see projExt), and the
	// caller sets both with setProject, which is what redraws those pages.
}

// blankProject is what New Project starts from: an empty session, and the
// defaults that are the program's rather than the zero value's.
//
// Interval and Produce are stated because their zero values are real settings
// and the wrong ones -- 0 seconds means EVERY frame, which is gigabytes nobody
// asked for, and a zeroed produce block is a 0-CRF, 0-fps render. Everything
// else is legitimately empty: no sources, nothing typed, no prompt edited and
// no thumbnail. Not the output folder, which is not a setting any more: it
// follows whichever file the emptied session goes back to being.
func blankProject() Project {
	prod := defaultProdSettings()
	return Project{
		Interval:   frameStops[defFrameStop],
		FrameScale: scalePresets[0].Name,
		Produce:    &prod,
	}
}

// newProject empties the session. The named project FILE is not deleted and not
// written over here: what the autosave follows afterwards is the working copy,
// so a named project that was open stays on disk exactly as it was, and going
// back to it is Load, not undo. Its output folder is left alone too -- it is
// that file's folder now, not this session's.
func (a *App) newProject() {
	a.applyProject(blankProject())
	a.setProject(filepath.Join(a.root, workName))
	// the working copy is now the open project, and that is a decision, not an
	// absence: without this the next launch reopens the named project this was
	// meant to get away from (see rememberProject)
	a.rememberProject(a.projPath)
	a.saveProjectNow()
	a.setStatus("new project — the session is empty")
	a.logf(">>> new project -- the session is empty; outputs on disk are untouched")
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
	if filepath.Base(a.projPath) != workName {
		detail += "\n\n" + filepath.Base(a.projPath) + " stays on disk as it is, " +
			"with everything it has written -- this session simply stops being it."
	} else {
		detail += "\n\nThis session has never been saved under a name of its own, " +
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
			a.logf(">>> moved this project's notes into the %q prompt", key)
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

// projFilter is the one filter both project dialogs use: .autocut files, plus
// the .json a project was before it was a file the desktop could open.
func projFilter() *gtk.FileFilter {
	filt := gtk.NewFileFilter()
	filt.SetName("Autocut projects")
	filt.AddSuffix(strings.TrimPrefix(projExt, "."))
	filt.AddSuffix("json")
	return filt
}

// saveProjectDialog names the project. It opens beside the open project rather
// than in the root, because Save As on /mnt/rec/tom.json.autocut is nearly
// always another name in /mnt/rec -- that is where the footage is.
//
// Not during a run: the save moves the output folder the run is writing into.
func (a *App) saveProjectDialog() {
	if a.running {
		a.setStatus("stop the run first — saving under a new name moves the folder it is writing into")
		return
	}
	d := gtk.NewFileDialog()
	d.SetInitialFolder(gio.NewFileForPath(filepath.Dir(a.projPath)))
	d.SetInitialName(filepath.Base(a.projPath))
	filters := gio.NewListStore(gtk.GTypeFileFilter)
	filters.Append(projFilter().Object)
	d.SetFilters(filters)
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
	d.SetInitialFolder(gio.NewFileForPath(filepath.Dir(a.projPath)))
	filters := gio.NewListStore(gtk.GTypeFileFilter)
	filters.Append(projFilter().Object)
	d.SetFilters(filters)
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
// how long ago the newest was written. Recursive, because Prepare's output is
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

// countOutputs is the same walk with the two halves kept apart. Prepare
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
// count. Prepare writes one frame per second of footage; a log carrying four
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
