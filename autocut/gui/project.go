package main

// Project state: which sources are in, in what order, and the step settings.
// Saved as plain JSON with root-relative paths, so a project file survives
// moving the autocut directory. root/project.json is the working copy --
// loaded on startup, rewritten whenever a step runs; Save/Load dialogs are
// for keeping named variants.

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotk4/pkg/pango"
)

type Project struct {
	Videos     []string `json:"videos"` // ordered, relative to root
	Audios     []string `json:"audios"`
	Interval   float64  `json:"interval"` // seconds between frames; 0 = every frame
	FrameScale string   `json:"frame_scale,omitempty"`
	InDir      string   `json:"in_dir,omitempty"`
	OutDir     string   `json:"out_dir,omitempty"`
	// describe_hints and transcript_hints are read, never written: the two
	// pages they belonged to had a notes box beside the system prompt, which
	// the runner glued onto the prompt just before sending. One box now, so a
	// project written by an older build has its notes folded into the prompt on
	// load (migrateHints) and the keys go away with the next save.
	DescribeHints   string `json:"describe_hints,omitempty"`
	TranscriptHints string `json:"transcript_hints,omitempty"`
	CutHints        string `json:"cut_hints,omitempty"`
	NarrateHints    string `json:"narrate_hints,omitempty"`
	// "pitch" used to live here too -- the output-pitch slider. It is gone; a
	// project written back then still loads, the key is simply ignored.

	// only the system prompts the user changed; see prompts.go for why an
	// untouched one is absent rather than copied
	Prompts map[string]string `json:"prompts,omitempty"`

	Produce *prodSettings `json:"produce,omitempty"`
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
	return Project{
		Videos:       a.vidList.selected(),
		Audios:       a.audList.selected(),
		Interval:     a.frameInterval(),
		FrameScale:   scaleName,
		InDir:        a.relToRoot(a.inDir),
		OutDir:       a.relToRoot(a.outDir),
		CutHints:     a.cutHints(),
		NarrateHints: a.narrateHints(),
		Prompts:      a.currentPrompts(),
		Produce:      prod,
	}
}

func (a *App) saveProjectTo(path string) {
	p := a.currentProject()
	b, _ := json.MarshalIndent(p, "", "  ")
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		a.logf("save project: %v", err)
		return
	}
	a.setStatus("project saved: " + path)
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
	// the input folder first: the source lists have to be looking at the right
	// directory before a selection can be applied to them
	a.setInDir(a.fromRoot(p.InDir))
	// a project entry whose file vanished (renamed, moved) must be LOUD:
	// a silently-unchecked source once cost half a debugging session
	for _, list := range [][]string{p.Videos, p.Audios} {
		for _, rel := range list {
			if !exists(a.srcPath(rel)) {
				a.logf("WARNING: project references %s, which no longer exists -- it was dropped from the selection (renamed?)", rel)
			}
		}
	}
	a.vidList.setSelection(p.Videos)
	a.audList.setSelection(p.Audios)
	a.setFrameInterval(p.Interval)
	if p.FrameScale != "" {
		a.setFrameScale(p.FrameScale)
	}
	if a.ed != nil && a.ed.hints != nil {
		a.ed.hints.Buffer().SetText(p.CutHints)
	}
	if a.narr5 != nil && a.narr5.hints != nil {
		a.narr5.hints.Buffer().SetText(p.NarrateHints)
	}
	a.applyPrompts(p.Prompts)
	a.migrateHints(p)
	a.applyProdSettings(p.Produce)
	a.setOutDir(a.fromRoot(p.OutDir))
	a.setStatus("project loaded: " + path)
}

// migrateHints folds a pre-merge project's notes into the prompts they used to
// be appended to. The runners did that concatenation at request time, using
// exactly these lead-ins, so folding them in preserves what the model saw;
// dropping them would silently change what a reloaded project sends. Must run
// after applyPrompts, which is what puts the prompt in the box in the first
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
}

func (a *App) chooseInDirDialog() {
	d := gtk.NewFileDialog()
	d.SetInitialFolder(gio.NewFileForPath(a.inDir))
	d.SelectFolder(context.Background(), &a.win.Window, func(res gio.AsyncResulter) {
		f, err := d.SelectFolderFinish(res)
		if err != nil || f == nil {
			return
		}
		a.setInDir(f.Path())
	})
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

// ---- an orderable multi-select file list -----------------------------------

// sourceList shows every media file in a directory as a checkbox row, sorted
// by name at first; ↑/↓ reorder rows, and the checked rows top-to-bottom ARE
// the selection. The list is rebuilt from the slice on every change -- at a
// handful of files that is simpler and safer than in-place row surgery.
type sourceList struct {
	root, dir string
	items     []sourceItem
	box       *gtk.ListBox
	onChange  func()
}

type sourceItem struct {
	name string
	sel  bool
}

func newSourceList(root, dir string, onChange func()) *sourceList {
	s := &sourceList{root: root, dir: dir, onChange: onChange}
	s.box = gtk.NewListBox()
	s.box.SetSelectionMode(gtk.SelectionNone)
	s.box.AddCSSClass("boxed-list")
	s.rescan()
	return s
}

// setRoot moves the list to another input folder, forgetting what was checked
// in the old one -- see setInDir for why nothing is carried across.
func (s *sourceList) setRoot(root string) {
	s.root = root
	s.items = nil
	s.rescan()
	if s.onChange != nil {
		s.onChange() // nothing is checked in the new folder; say so
	}
}

// rescan merges the directory contents into the existing order: known files
// keep their position and checkmark, new files append sorted by name.
func (s *sourceList) rescan() {
	have := map[string]bool{}
	var kept []sourceItem
	for _, it := range s.items {
		if exists(filepath.Join(s.root, s.dir, it.name)) {
			kept = append(kept, it)
			have[it.name] = true
		}
	}
	fresh := listMedia(filepath.Join(s.root, s.dir))
	sort.Strings(fresh)
	for _, f := range fresh {
		if !have[f] {
			kept = append(kept, sourceItem{name: f})
		}
	}
	s.items = kept
	s.render()
}

// setSelection applies a project: listed files first, checked, in project
// order; everything else follows unchecked, sorted by name.
func (s *sourceList) setSelection(relPaths []string) {
	rank := map[string]int{}
	for i, p := range relPaths {
		rank[filepath.Base(p)] = i + 1
	}
	for i := range s.items {
		s.items[i].sel = rank[s.items[i].name] > 0
	}
	sort.SliceStable(s.items, func(i, j int) bool {
		ri, rj := rank[s.items[i].name], rank[s.items[j].name]
		switch {
		case ri > 0 && rj > 0:
			return ri < rj
		case ri != rj:
			return ri > 0 // selected before unselected
		default:
			return s.items[i].name < s.items[j].name
		}
	})
	s.render()
	// a loaded project changes the checkmarks as surely as a click does, and
	// what hangs off onChange -- the voice step's "my own voice" row -- is wrong
	// until it hears about it
	if s.onChange != nil {
		s.onChange()
	}
}

// selected returns the checked files, in list order, relative to root.
func (s *sourceList) selected() []string {
	var out []string
	for _, it := range s.items {
		if it.sel {
			out = append(out, filepath.Join(s.dir, it.name))
		}
	}
	return out
}

func (s *sourceList) render() {
	for {
		row := s.box.RowAtIndex(0)
		if row == nil {
			break
		}
		s.box.Remove(row)
	}
	for i := range s.items {
		i := i
		// an ellipsizing label rather than the check button's own: a recorder
		// filename is 50 characters, and a row that insists on all of them sets
		// a floor the divider cannot be dragged past
		lbl := gtk.NewLabel(s.items[i].name)
		lbl.SetXAlign(0)
		lbl.SetHExpand(true)
		lbl.SetEllipsize(pango.EllipsizeMiddle)
		check := gtk.NewCheckButton()
		check.SetChild(lbl)
		check.SetTooltipText(s.items[i].name)
		check.SetActive(s.items[i].sel)
		check.SetHExpand(true)
		check.ConnectToggled(func() {
			s.items[i].sel = check.Active()
			if s.onChange != nil {
				s.onChange()
			}
		})
		up := gtk.NewButtonFromIconName("go-up-symbolic")
		up.AddCSSClass("flat")
		up.SetSensitive(i > 0)
		up.ConnectClicked(func() { s.move(i, -1) })
		down := gtk.NewButtonFromIconName("go-down-symbolic")
		down.AddCSSClass("flat")
		down.SetSensitive(i < len(s.items)-1)
		down.ConnectClicked(func() { s.move(i, +1) })

		row := gtk.NewBox(gtk.OrientationHorizontal, 4)
		row.SetMarginStart(6)
		row.SetMarginEnd(2)
		row.Append(check)
		row.Append(up)
		row.Append(down)
		s.box.Append(row)
	}
}

func (s *sourceList) move(i, d int) {
	j := i + d
	if j < 0 || j >= len(s.items) {
		return
	}
	s.items[i], s.items[j] = s.items[j], s.items[i]
	s.render()
	if s.onChange != nil {
		s.onChange()
	}
}

// ---- output inspection ------------------------------------------------------

// summarizeOutputs is the one-line answer: how many files are under dir, and
// how long ago the newest was written. Recursive, because step 1's output is
// mostly frames in subdirectories -- a top-level count would say "3 entries"
// about four thousand files. The age is what tells a finished step from a
// stale one at a glance.
func summarizeOutputs(dir string) string {
	n := 0
	var newest time.Time
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
	if n == 0 {
		return "nothing yet"
	}
	return fmt.Sprintf("%d files, newest %s", n, humanAgo(newest))
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

// describeOutputs summarizes what a directory holds, one line per entry.
func describeOutputs(dir string) string {
	ents, err := os.ReadDir(dir)
	if err != nil || len(ents) == 0 {
		return "(nothing yet)"
	}
	var out string
	for _, e := range ents {
		p := filepath.Join(dir, e.Name())
		if e.IsDir() {
			sub, _ := os.ReadDir(p)
			out += fmt.Sprintf("%s/  (%d files)\n", e.Name(), len(sub))
		} else {
			if fi, err := e.Info(); err == nil {
				out += fmt.Sprintf("%s  (%s)\n", e.Name(), humanSize(fi.Size()))
			}
		}
	}
	return out
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
