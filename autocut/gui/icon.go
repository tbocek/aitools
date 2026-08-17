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
// On Wayland the icon travels over xdg-toplevel-icon; on X11 it is set on the
// window directly. Both come out of SetIconName, so neither is a special case
// here. What is NOT covered is the shell's app grid and dash: those match a
// window's app id against an installed .desktop file, which is a packaging
// step, not something a running program can do for itself (install.sh).

import (
	"os"
	"path/filepath"

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
	if !theme.HasIcon(appID) {
		a.logf("icon: no %q in the icon theme -- looked in %v", appID, a.iconDirs())
		return
	}
	// the default covers windows built later (the settings dialog), the window's
	// own is what the compositor is told about this one
	gtk.WindowSetDefaultIconName(appID)
	a.win.SetIconName(appID)
}
