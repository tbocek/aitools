package main

import (
	"bytes"
	"encoding/xml"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diamondburned/gotk4/pkg/gdkpixbuf/v2"
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

// how much of a file an image loader reads before deciding what it is. glycin,
// which is what GNOME hands image files to now, reads about this much; other
// loaders read less. Whatever the number, a drawing that only announces itself
// after a page of prose announces itself to nobody.
const sniffWindow = 256

// Everything above was true, the desktop entry was written, valid, and pointed
// at an icon that opened in every viewer on the machine -- and the shell still
// drew nothing, because it never got as far as the drawing. The file began with
// an xml declaration and a thirty-line note about how the mark was designed,
// which put "<svg" some 1400 bytes in, past the window the loader sniffs to
// decide what kind of file it has. Not a broken svg: a file that is not
// recognised as an image at all, which is a rejection with no error anywhere a
// person would look.
//
// Hence: root element first, prose inside it. That the icon has to survive
// being read by a machine is not obvious from looking at it, so it is checked
// here rather than remembered.
func TestTheIconsAreFilesALoaderWillRecognise(t *testing.T) {
	icons := []string{
		filepath.Join("icons", "hicolor", "scalable", "apps", appID+".svg"),
		filepath.Join("icons", "hicolor", "symbolic", "apps", appID+"-symbolic.svg"),
	}

	for _, p := range icons {
		src, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		// Well-formed, all the way through. This catches the other way a
		// comment breaks the file, which the move above walked straight into:
		// xml forbids a double hyphen inside a comment, so an em dash or a
		// rewritten sentence is the price of writing "-- like this" in one.
		dec := xml.NewDecoder(bytes.NewReader(src))
		for {
			_, err := dec.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Errorf("%s is not well-formed xml, so nothing will draw it: %v", p, err)
				break
			}
		}
		if i := bytes.Index(src, []byte("<svg")); i < 0 || i > sniffWindow {
			t.Errorf("%s does not reach <svg> until byte %d -- a loader sniffs about "+
				"%d bytes to decide what a file is, and gives up on this one. Put the "+
				"root element at the top and any explanation inside it.", p, i, sniffWindow)
		}
	}

	// And the witness: the machine's own image loader, which is the thing that
	// was saying no. It is only asked once it has proved it can read an svg at
	// all -- a build host with no svg loader would otherwise fail this test for
	// something that is not about these files.
	ctrl := filepath.Join(t.TempDir(), "control.svg")
	const plain = `<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" width="16" height="16">
  <rect width="16" height="16" fill="#e2564a"/>
</svg>
`
	if err := os.WriteFile(ctrl, []byte(plain), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := gdkpixbuf.NewPixbufFromFileAtSize(ctrl, 64, 64); err != nil {
		t.Skipf("no svg loader here (%v), so the rules above are all this test can check", err)
	}
	for _, p := range icons {
		if _, err := gdkpixbuf.NewPixbufFromFileAtSize(p, 64, 64); err != nil {
			t.Errorf("%s: the loader on this machine will not read it: %v -- which is "+
				"exactly how the icon goes missing with nothing to show for it", p, err)
		}
	}
}

// Everything above was true and the window still had the generic icon, because
// under GNOME on Wayland none of it reaches the screen: mutter does not
// implement xdg-toplevel-icon, so a window has no icon of its own and the shell
// draws the icon of the .desktop file it matched the window's application id
// to. That file is the icon, and this is what has to hold for it to be found:
// named after the id (the match is on the file name), pointing at a binary that
// exists and a drawing that exists.
func TestTheDesktopEntryIsWhatTheShellCanActuallyFind(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "autocut-gui")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	icon, err := filepath.Abs(filepath.Join("icons", "hicolor", "scalable", "apps", appID+".svg"))
	if err != nil {
		t.Fatal(err)
	}
	entry := desktopEntry(exe, dir, icon)

	// the file name is the id, and the id is the icon name and the svg's name:
	// one string, still (appID). A file called autocut.desktop matches nothing.
	for _, want := range []string{
		"Exec=" + exe,
		"Icon=" + icon,
		"Path=" + dir,
		"StartupWMClass=" + appID,
		"Type=Application",
	} {
		if !strings.Contains(entry, want+"\n") {
			t.Errorf("the entry has no %q line:\n%s", want, entry)
		}
	}

	// Written where the shell reads, under the name that does the matching...
	path := filepath.Join(dir, "applications", appID+".desktop")
	wrote, err := writeDesktop(path, entry)
	if err != nil || !wrote {
		t.Fatalf("writing the entry: wrote=%v err=%v", wrote, err)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != entry {
		t.Fatalf("reading it back: %v", err)
	}
	// ...and not written again when nothing changed. Rewriting it on every
	// start makes the shell reload its application list every time autocut is
	// launched, for a file whose bytes did not move.
	if wrote, err := writeDesktop(path, entry); err != nil || wrote {
		t.Errorf("an unchanged entry was written again (wrote=%v err=%v)", wrote, err)
	}
	// but a moved checkout is a changed entry, and that one must land
	if wrote, err := writeDesktop(path, desktopEntry(exe+"2", dir, icon)); err != nil || !wrote {
		t.Errorf("the entry did not follow the binary (wrote=%v err=%v)", wrote, err)
	}

	// The shell is the one that has to read this, and the format has more rules
	// than it looks like -- required keys, the exact spelling of the Categories
	// it will accept, what needs quoting in Exec. desktop-file-validate is the
	// freedesktop reference for all of it, so when it is on the machine it gets
	// the last word rather than this test's opinion of the format.
	if _, err := exec.LookPath("desktop-file-validate"); err == nil {
		out, err := exec.Command("desktop-file-validate", path).CombinedOutput()
		if err != nil || len(out) > 0 {
			t.Errorf("the shell's own validator rejects the entry:\n%s", out)
		}
	}

	// A `go run` binary lives in the build cache and is deleted on exit; an
	// entry pointing at one launches nothing, and would also overwrite the good
	// entry from the last real build.
	if !builtOnTheFly(filepath.Join(os.TempDir(), "go-build3910245", "b001", "exe", "gui")) {
		t.Error("a go-build binary was taken for an installed one")
	}
	if builtOnTheFly(exe) {
		t.Error("a built binary was taken for a throwaway one")
	}

	// a path with a space in it is one argument, not two
	if arg := desktopArg("/home/a b/gui"); arg != `"/home/a b/gui"` {
		t.Errorf("Exec=%s -- the shell reads that as two words", arg)
	}
	if arg := desktopArg(exe); arg != exe {
		t.Errorf("a plain path came out quoted as %s", arg)
	}

	// and the icon the entry names is the drawing in this checkout, found the
	// same way the theme finds it
	a := &App{root: ".."}
	if got := a.iconFile(); got == "" {
		t.Error("no icon file found for the entry -- Icon= would fall back to a name " +
			"the shell has no file for")
	} else if _, err := os.Stat(got); err != nil {
		t.Errorf("the entry would point at %s: %v", got, err)
	}
}

// The icon is a file somebody made, and not everybody makes svgs. So the three
// formats that are worth having are all accepted, in the order of what they
// survive: a drawing first, then a picture the theme reads, then a picture only
// the shell reads. What this pins is the ranking -- the failure it prevents is
// a jpg dropped into the folder quietly winning over the drawing beside it.
func TestTheIconIsAnSVGOrAPNGOrAJPGInThatOrder(t *testing.T) {
	dir := t.TempDir()
	a := &App{root: dir}
	tree := filepath.Join(dir, "gui", "icons")
	put := func(rel string) string {
		p := filepath.Join(tree, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("drawing"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	svg := put("hicolor/scalable/apps/" + appID + ".svg")
	png := put("hicolor/48x48/apps/" + appID + ".png")
	big := put("hicolor/256x256/apps/" + appID + ".png")
	jpg := put("hicolor/256x256/apps/" + appID + ".jpg")
	loose := put(appID + ".jpeg")

	for _, c := range []struct {
		want string
		drop string // what is taken away before asking again
		why  string
	}{
		{svg, svg, "a drawing loses to a picture beside it"},
		{big, big, "the biggest png is not the one picked -- the shell scales down better than up"},
		{png, png, "the remaining png is not found"},
		{jpg, jpg, "a jpg is not accepted at all"},
		{loose, loose, "a file dropped into icons/ rather than into the tree is not found"},
	} {
		if got := a.iconFile(); got != c.want {
			t.Errorf("%s: the icon is %s, want %s", c.why, got, c.want)
		}
		if err := os.Remove(c.drop); err != nil {
			t.Fatal(err)
		}
	}
	// with that folder empty the search moves on to the next guess at where the
	// tree is, which here is the checkout this test is running in
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if got := a.iconFile(); got != filepath.Join(wd, "icons", "hicolor", "scalable", "apps", appID+".svg") {
		t.Errorf("an emptied folder ends the search at %q rather than moving to the next one", got)
	}

	// and the reason the order can be this generous: the desktop entry takes the
	// file itself, so a format the icon theme cannot load is still drawn by the
	// shell. setupIcons must not give up before writing it.
	src, err := os.ReadFile("icon.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "case file == \"\":") {
		t.Error("setupIcons gives up on a theme lookup alone, so a png or jpg icon " +
			"never reaches the desktop entry that is the only thing drawing it under Wayland")
	}
}
