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
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

type Project struct {
	Videos          []string `json:"videos"` // ordered, relative to root
	Audios          []string `json:"audios"`
	Interval        float64  `json:"interval"` // seconds between frames; 0 = every frame
	FrameScale      string   `json:"frame_scale,omitempty"`
	OutDir          string   `json:"out_dir,omitempty"`
	DescribeHints   string   `json:"describe_hints,omitempty"`
	TranscriptHints string   `json:"transcript_hints,omitempty"`
	CutHints        string   `json:"cut_hints,omitempty"`
	NarrateHints    string   `json:"narrate_hints,omitempty"`
	Pitch           float64  `json:"pitch,omitempty"`

	Produce *prodSettings `json:"produce,omitempty"`
}

func (a *App) currentProject() Project {
	out := a.outDir
	if rel, err := filepath.Rel(a.root, out); err == nil && !strings.HasPrefix(rel, "..") {
		out = rel // keep project files relocatable when outputs live inside root
	}
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
			if rel, err := filepath.Rel(a.root, st.OutFile); err == nil && !strings.HasPrefix(rel, "..") {
				st.OutFile = rel
			}
		}
		prod = &st
	}
	return Project{
		Videos:          a.vidList.selected(),
		Audios:          a.audList.selected(),
		Interval:        a.frameInterval(),
		FrameScale:      scaleName,
		OutDir:          out,
		DescribeHints:   a.describeHints(),
		TranscriptHints: a.transcriptHints(),
		CutHints:        a.cutHints(),
		NarrateHints:    a.narrateHints(),
		Pitch:           a.pitchValue(),
		Produce:         prod,
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
	// a project entry whose file vanished (renamed, moved) must be LOUD:
	// a silently-unchecked source once cost half a debugging session
	for _, list := range [][]string{p.Videos, p.Audios} {
		for _, rel := range list {
			if !exists(filepath.Join(a.root, rel)) {
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
	if a.s2hints != nil {
		a.s2hints.Buffer().SetText(p.DescribeHints)
	}
	if a.s3hints != nil {
		a.s3hints.Buffer().SetText(p.TranscriptHints)
	}
	if a.ed != nil && a.ed.hints != nil {
		a.ed.hints.Buffer().SetText(p.CutHints)
	}
	if a.narr5 != nil && a.narr5.hints != nil {
		a.narr5.hints.Buffer().SetText(p.NarrateHints)
	}
	if p.Pitch > 0 && a.voice5 != nil && a.voice5.out != nil {
		a.voice5.out.SetValue(p.Pitch)
	}
	a.applyProdSettings(p.Produce)
	out := p.OutDir
	if out == "" {
		out = a.root
	} else if !filepath.IsAbs(out) {
		out = filepath.Join(a.root, out)
	}
	a.setOutDir(out)
	a.setStatus("project loaded: " + path)
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
		check := gtk.NewCheckButtonWithLabel(s.items[i].name)
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
