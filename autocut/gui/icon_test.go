package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The icon was drawn, committed, and never appeared: nothing added its folder
// to the icon theme's search path and nothing named it, so a perfectly good svg
// sat in the repo being findable by no one. Neither half fails loudly -- a
// window with the fallback icon looks like a window, and the only symptom is
// that the drawing you made is not on it.
//
// What can be checked without a display is that the two halves still agree: the
// files are where the search path points, named what the window asks for. The
// lookup itself is GTK's and needs a display, which a test does not have; the
// running program says so in the log instead (setupIcons).
func TestTheIconIsNamedWhatTheWindowAsksFor(t *testing.T) {
	for _, want := range []string{
		filepath.Join("icons", "hicolor", "scalable", "apps", appID+".svg"),
		filepath.Join("icons", "hicolor", "symbolic", "apps", appID+"-symbolic.svg"),
	} {
		fi, err := os.Stat(want)
		if err != nil {
			t.Errorf("%s is not there -- the window asks the theme for %q, and that "+
				"name is the file name: %v", want, appID, err)
			continue
		}
		if fi.Size() < 200 {
			t.Errorf("%s is %d bytes -- too small to be the drawing", want, fi.Size())
		}
	}

	// hicolor is the fallback theme every desktop has, and the layout is the
	// spec's: a search path holds <theme>/<size>/<context>/. Getting this wrong
	// is the same invisible failure as not adding the path at all.
	a := &App{root: t.TempDir()}
	for _, dir := range a.iconDirs() {
		if base := filepath.Base(dir); base != "icons" {
			t.Errorf("icon search path %s does not end in icons/ -- GTK looks for "+
				"hicolor/ INSIDE what it is handed", dir)
		}
	}

	// and it is actually called: setupIcons doing the right thing in a file
	// nobody invokes is where this started
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "a.setupIcons()") {
		t.Error("the window is built without calling setupIcons -- the icon goes back to being decoration in a folder")
	}
	// the id the compositor knows the window by IS the icon name; two spellings
	// of it is how they come apart
	if !strings.Contains(string(src), "gtk.NewApplication(appID,") {
		t.Error("the application id is spelled out again instead of using appID")
	}
}
