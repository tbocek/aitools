package main

// Adding a source: in place, or into the project.
//
// A project is a folder now (projExt), and the point of that is that it can be
// copied, backed up or zipped as one thing. A session whose sources are
// somewhere else -- on the card they were recorded on, in a downloads folder,
// on a drive that will be unplugged -- is not one thing, and nothing about the
// project says so until a source is gone and the row goes red.
//
// So the choice is asked once, when the file is added, and both answers are
// legitimate. Reference is right for the footage you are cutting on the
// machine that recorded it: 20 GB of capture does not want a second copy.
// Copy is right for anything that has to survive being handed over.
//
// The copy goes to <project>/sources/. Not a settings folder, not the root:
// inside the project, because that is the whole reason to press it.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// sourcesDir is where copied sources land.
func (a *App) sourcesDir() string { return filepath.Join(a.outDir, "sources") }

// inProject is whether a file is already inside the project folder -- one
// already there is added as it is, whatever the answer, because copying a file
// onto itself is not a thing to offer.
func (a *App) inProject(path string) bool {
	if a.outDir == "" {
		return false
	}
	rel, err := filepath.Rel(a.outDir, path)
	return err == nil && !strings.HasPrefix(rel, "..")
}

// askImport is the question every Add asks: copy these into the project, or
// point at them where they are. Files already inside the project skip it.
func (a *App) askImport(paths []string) {
	var outside []string
	for _, p := range paths {
		if !a.inProject(p) {
			outside = append(outside, p)
		}
	}
	if len(outside) == 0 {
		a.addSources(paths...)
		return
	}
	total := int64(0)
	for _, p := range outside {
		if fi, err := os.Stat(p); err == nil {
			total += fi.Size()
		}
	}
	a.askCopy(fmt.Sprintf("Copy %s into the project?", plural(len(paths), "file")),
		fmt.Sprintf("%s of footage.\n\nCopied, the project folder holds everything it needs "+
			"and can be moved or zipped as one thing. Referenced, nothing is duplicated and "+
			"the session breaks if the files move.", humanSize(total)),
		func(copyIn bool) {
			if !copyIn {
				a.addSources(paths...)
				return
			}
			a.copySources(paths)
		})
}

// askCopy is the two-answer question, with Cancel. Neither answer is
// destructive, so neither button is red: this is a fork in the road, not a
// warning.
func (a *App) askCopy(question, detail string, ok func(copyIn bool)) {
	win := gtk.NewWindow()
	win.SetTransientFor(&a.win.Window)
	win.SetModal(true)
	win.SetTitle(question)
	win.SetDefaultSize(460, -1)

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
	ref := gtk.NewButtonWithLabel("Reference in place")
	ref.ConnectClicked(func() {
		win.Close()
		ok(false)
	})
	cp := gtk.NewButtonWithLabel("Copy in")
	cp.AddCSSClass("suggested-action")
	cp.ConnectClicked(func() {
		win.Close()
		ok(true)
	})
	btns := gtk.NewBox(gtk.OrientationHorizontal, 8)
	btns.SetHAlign(gtk.AlignEnd)
	btns.SetMarginTop(8)
	btns.Append(cancel)
	btns.Append(ref)
	btns.Append(cp)

	box := gtk.NewBox(gtk.OrientationVertical, 8)
	box.SetMarginTop(16)
	box.SetMarginBottom(16)
	box.SetMarginStart(16)
	box.SetMarginEnd(16)
	box.Append(q)
	box.Append(d)
	box.Append(btns)
	win.SetChild(box)
	cp.GrabFocus()
	win.SetVisible(true)
}

// copySources copies what is outside the project into <project>/sources/ and
// adds the copies, on a goroutine because a card full of capture is gigabytes
// and the window has to stay alive.
//
// A file already inside the project is added where it is. A name already taken
// in sources/ by a file of the same size is taken to be the same file and is
// not copied again -- adding the same card twice is a thing people do, and the
// second add should cost nothing.
func (a *App) copySources(paths []string) {
	if a.running {
		a.setStatus("a run is already active — stop it first (⏹)")
		return
	}
	dir := a.sourcesDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		a.logf("!!! copy sources: %v", err)
		a.setStatus("could not make the project's sources folder — see log")
		return
	}
	a.running = true
	a.updateRunControls()
	a.logExp.SetExpanded(true)
	a.logf(">>> copying %s into %s", plural(len(paths), "source"), dir)
	go func() {
		var out []string
		for i, p := range paths {
			if a.inProject(p) {
				out = append(out, p)
				continue
			}
			a.progIdle(float64(i)/float64(len(paths)), "copying %s", filepath.Base(p))
			to, err := copyInto(dir, p)
			if err != nil {
				a.logfIdle("!!! copying %s: %v", filepath.Base(p), err)
				continue
			}
			out = append(out, to)
		}
		glib.IdleAdd(func() {
			a.running = false
			a.updateRunControls()
			a.progress.SetFraction(0)
			a.addSources(out...)
			a.saveProjectNow()
		})
	}()
}

// progIdle moves the run bar from a worker goroutine.
func (a *App) progIdle(f float64, format string, args ...any) {
	s := fmt.Sprintf(format, args...)
	glib.IdleAdd(func() {
		a.progress.SetFraction(f)
		a.setStatus(s)
	})
}

// copyFile writes src to the exact path out, through the same .part-and-rename
// copyInto uses: a copy interrupted by a pulled drive or a full disk must not
// look like a finished file.
func copyFile(src, out string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	part := out + ".part"
	f, err := os.Create(part)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, in); err != nil {
		f.Close()
		os.Remove(part)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(part)
		return err
	}
	return os.Rename(part, out)
}

// copyInto copies one file into dir and answers with its new path. The copy is
// written to a .part and renamed, so an interrupted copy cannot be mistaken
// for a source: a half file that plays for ten seconds and stops is the worst
// way to find out a drive was pulled.
func copyInto(dir, src string) (string, error) {
	fi, err := os.Stat(src)
	if err != nil {
		return "", err
	}
	dst := filepath.Join(dir, filepath.Base(src))
	if d, err := os.Stat(dst); err == nil && d.Size() == fi.Size() {
		return dst, nil // already here, same size: the same file
	}
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	part := dst + ".part"
	out, err := os.Create(part)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(part)
		return "", err
	}
	if err := out.Close(); err != nil {
		os.Remove(part)
		return "", err
	}
	return dst, os.Rename(part, dst)
}
