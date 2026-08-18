package main

// The window icon, and why it needs code at all.
//
// icons/ has held a drawn icon for a while and nothing ever showed it. Two
// things were missing, and each on its own is enough to leave a window with the
// generic placeholder:
//
//   - Nobody told GTK the folder exists. An icon theme searches the XDG icon
//     directories, and a hicolor tree sitting beside the binary is not one of
//     them until AddSearchPath says so. Installing it system-wide would also
//     work, but then the icon only appears on a machine where somebody ran an
//     install step, which is exactly the kind of "works here" this app has been
//     moving away from.
//
//   - Nobody named it. GTK does not go looking for an icon that matches the
//     binary: a window shows whatever icon NAME it was given, so the files were
//     findable-in-principle and asked for by nothing.
//
// Hence appID as the icon name and as the file name. It is the freedesktop
// convention -- the icon is named after the application id, which is also what
// a .desktop file would be called -- and it means the one name in this file is
// the same string the compositor already knows the window by, rather than a
// second name that has to be kept in step with it.
//
// On X11 the icon is set on the window directly and SetIconName is the whole
// story. On Wayland it is not, and this is the part that had the icon still
// missing after all of the above: a window's own icon travels over the
// xdg-toplevel-icon protocol, GTK 4.22 sends it, and mutter does not implement
// it -- there is not one mention of the protocol in libmutter or gnome-shell as
// of GNOME 50. So under GNOME on Wayland a window HAS no icon of its own, and
// the only thing the shell will draw is the icon of the .desktop file it
// matched the window's application id to.
//
// Which is why writing that file is done here rather than left to an install
// step. The alternative is a program that looks broken until somebody knows to
// run a script, and the script and the program then each hold half of the same
// three facts -- id, binary, icon -- and drift. The entry is written from the
// running binary's own path, so it is right by construction and rewrites itself
// when the checkout moves.

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// appID is the application id, the icon name and the base name of both svgs.
// One string: see above.
const appID = "li.jos.autocut"

// iconDirs is where the hicolor tree might be, best guess first: beside the
// binary (an installed or built copy: <somewhere>/gui/autocut-gui with
// <somewhere>/gui/icons next to it), then under the autocut root, which is
// where it is when the binary was moved but the checkout was not, and last the
// working directory, which is what `go run .` gives us.
func (a *App) iconDirs() []string {
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Join(filepath.Dir(exe), "icons"))
	}
	dirs = append(dirs, filepath.Join(a.root, "gui", "icons"))
	if wd, err := os.Getwd(); err == nil {
		dirs = append(dirs, filepath.Join(wd, "icons"))
	}
	return dirs
}

// setupIcons points the display's icon theme at the icons this build ships and
// gives the window their name. Called from build, after the window exists --
// the theme is per-display, and there is no display until then.
//
// Best-effort, like the settings file: a missing icon is a placeholder in the
// title bar and nothing else. It does say so in the log, once, because the
// failure is otherwise invisible in exactly the way this whole file exists to
// fix -- the icon is there, it is simply never asked for, and nothing anywhere
// mentions it.
func (a *App) setupIcons() {
	theme := gtk.IconThemeGetForDisplay(gtk.BaseWidget(a.win).Display())
	if theme == nil {
		return
	}
	for _, dir := range a.iconDirs() {
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			theme.AddSearchPath(dir)
		}
	}
	switch file := a.iconFile(); {
	case theme.HasIcon(appID):
		// the default covers windows built later (the settings dialog), the
		// window's own is what the compositor is told about this one
		gtk.WindowSetDefaultIconName(appID)
		a.win.SetIconName(appID)
	case file == "":
		a.logf("icon: no %q in the icon theme -- looked in %v", appID, a.iconDirs())
		return
	default:
		// A picture the theme will not load -- a jpg, or a png dropped next to
		// the tree instead of into a size folder. The entry below points at the
		// file itself and the shell draws it from there, which under GNOME on
		// Wayland is the only half that ever reaches the screen anyway; what is
		// lost is the title bar on X11. Worth saying once, since the icon then
		// appears in one place and not the other for a reason nothing shows.
		a.logf("icon: the icon theme has no %q, so %s is drawn from the desktop entry "+
			"and not by the theme -- an svg in hicolor/scalable/apps, or a png in "+
			"hicolor/<size>/apps, is read by both.", appID, file)
	}
	a.installDesktop()
}

// iconExts is what the icon may be, best first, and the order is a ranking of
// what each format survives rather than a preference:
//
//	.svg   drawn at whatever size the moment asks for, and the only one of the
//	       three that is right in a title bar and in an app grid at once
//	.png   the other format an icon theme reads. A picture, so it is the size it
//	       is; put it under hicolor/<size>/apps/ and the size is declared
//	.jpg   read by the shell out of the desktop entry, and NOT by the icon theme
//	       -- GTK's theme loader does not do jpeg. See setupIcons: it still gets
//	       drawn where it matters, and leaves the title bar generic on X11.
//
// Photographic compression on a small drawing is also exactly the wrong codec,
// which is the other reason jpg is last rather than absent.
var iconExts = []string{".svg", ".png", ".jpg", ".jpeg"}

// iconFile is the icon as a path, for whoever needs a file rather than a theme
// name -- which is the shell, reading a .desktop entry written by a checkout it
// knows nothing about. One folder at a time, best format within it: the folders
// are three guesses at the same tree, so the nearest one that has an icon at all
// is the tree this build means, whatever it keeps its icon as.
func (a *App) iconFile() string {
	for _, dir := range a.iconDirs() {
		for _, ext := range iconExts {
			if p := iconIn(dir, ext); p != "" {
				return p
			}
		}
	}
	return ""
}

// iconIn is the icon of one kind inside one icons/ folder: the theme's own
// place for it first -- scalable for a drawing, the largest pixel size for a
// picture, since the shell scales down better than it scales up -- and then the
// folder itself, for a file somebody simply dropped in beside the tree.
func iconIn(dir, ext string) string {
	if ext == ".svg" {
		if p := filepath.Join(dir, "hicolor", "scalable", "apps", appID+ext); exists(p) {
			return p
		}
	}
	best, px := "", -1
	found, _ := filepath.Glob(filepath.Join(dir, "hicolor", "*", "apps", appID+ext))
	for _, p := range found {
		if n := sizeDir(filepath.Base(filepath.Dir(filepath.Dir(p)))); n > px {
			best, px = p, n
		}
	}
	if best != "" {
		return best
	}
	if p := filepath.Join(dir, appID+ext); exists(p) {
		return p
	}
	return ""
}

// sizeDir reads the pixels out of a theme's size folder: "256x256" is 256, and
// "scalable" or "symbolic" is not a pixel size at all, which is 0 here -- last
// among sizes, still ahead of nothing.
func sizeDir(name string) int {
	n, _, ok := strings.Cut(name, "x")
	if !ok {
		return 0
	}
	px, err := strconv.Atoi(n)
	if err != nil {
		return 0
	}
	return px
}

// dataHome is XDG_DATA_HOME or the ~/.local/share the spec says to assume.
func dataHome() string {
	if d := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(d) {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "share")
}

// builtOnTheFly reports whether this binary is one the go tool made to run
// once: `go run .` and `go test` both build into a cache directory that is
// deleted on exit, and an entry pointing there launches nothing. Such a run
// gets no desktop entry rather than a broken one -- and the entry from the last
// real build is left alone rather than overwritten with a path that will not
// exist in a minute.
func builtOnTheFly(exe string) bool {
	return strings.Contains(exe, "/go-build")
}

// desktopEntry is what the shell reads. Icon takes an absolute path on purpose:
// the spec allows it, and the alternative -- a name, resolved out of a copy of
// the svg installed into the icon theme -- is a second copy of the drawing that
// goes stale on its own schedule. Exec, Path and Icon all point into this
// checkout, so they are wrong together or right together, never half.
//
// StartupWMClass is for X11, where the match is on WM_CLASS rather than on the
// application id; GTK sets that from the id too, so it is the same string
// again. On Wayland the file NAME is what does the matching, and that is why it
// has to be the id and not "autocut".
func desktopEntry(exe, dir, icon string) string {
	if icon == "" {
		icon = appID
	}
	return fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=autocut
Comment=Workflow console for the autocut pipeline
Exec=%s
Path=%s
Icon=%s
Terminal=false
Categories=AudioVideo;Video;AudioVideoEditing;
StartupNotify=true
StartupWMClass=%s
`, desktopArg(exe), dir, icon, appID)
}

// desktopArg quotes a program path the way the spec's Exec key wants it. Paths
// with spaces in them are the normal case for a checkout under a directory
// somebody named, and an unquoted one silently becomes two arguments.
func desktopArg(s string) string {
	if !strings.ContainsAny(s, ` "'\`+"\t`$") {
		return s
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", `$`, `\$`)
	return `"` + r.Replace(s) + `"`
}

// writeDesktop puts the entry where the shell looks, and says whether it had
// to. Unchanged is the common case -- every start after the first -- and it
// must not touch the file, or the shell reloads its application list on every
// launch of this program.
func writeDesktop(path, entry string) (bool, error) {
	if old, err := os.ReadFile(path); err == nil && string(old) == entry {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	if err := os.WriteFile(path, []byte(entry), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// installDesktop registers this build with the desktop, which under GNOME on
// Wayland is the only way the icon is ever drawn (see the top of this file).
//
// Best-effort and quiet when there is nothing to do: it is a side effect on the
// user's home directory, so the one time it happens it says so in the log, with
// the path, because a file written into someone's home by a program they only
// meant to run should not be a surprise found later.
func (a *App) installDesktop() {
	data := dataHome()
	if data == "" {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	if builtOnTheFly(exe) {
		return
	}
	path := filepath.Join(data, "applications", appID+".desktop")
	wrote, err := writeDesktop(path, desktopEntry(exe, a.root, a.iconFile()))
	if err != nil {
		a.logf("icon: could not write %s: %v -- the window will show the "+
			"generic icon until it exists", path, err)
		return
	}
	if wrote {
		a.logf("icon: wrote %s, so the shell can put the autocut icon on this "+
			"window and in the app grid. Delete that file to undo it. "+
			"Already-open windows keep the old icon until autocut is restarted.", path)
	}
}
