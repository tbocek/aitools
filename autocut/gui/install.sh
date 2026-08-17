#!/bin/sh
# Desktop integration, for this user only. Optional: the window carries its own
# icon without any of this (icon.go), and the app runs fine from the terminal.
#
# What it buys is the part a running program cannot do for itself -- the shell
# matches a window to a .desktop file by its application id, and that file is
# what puts autocut in the app grid, in the dash, and in the Alt-Tab list under
# its own name instead of the binary's.
#
# Undo: rm the two paths this prints.
set -eu

id=li.jos.autocut
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
apps=${XDG_DATA_HOME:-$HOME/.local/share}/applications
icons=${XDG_DATA_HOME:-$HOME/.local/share}/icons/hicolor

# the icons, at the sizes the theme expects to find them under. Copied rather
# than symlinked: a checkout that moves would otherwise leave the shell with a
# dangling icon and no way to say so.
for d in scalable/apps symbolic/apps; do
	mkdir -p "$icons/$d"
	cp "$here/icons/hicolor/$d/"* "$icons/$d/"
done

mkdir -p "$apps"
cat >"$apps/$id.desktop" <<EOF
[Desktop Entry]
Type=Application
Name=autocut
Comment=Workflow console for the autocut pipeline
Exec=$here/autocut-gui
Path=$(dirname -- "$here")
Icon=$id
Terminal=false
Categories=AudioVideo;Video;AudioVideoEditing;
StartupNotify=true
StartupWMClass=$id
EOF

# without this the grid can take until the next login to notice
command -v update-desktop-database >/dev/null 2>&1 &&
	update-desktop-database "$apps" 2>/dev/null || true
command -v gtk4-update-icon-cache >/dev/null 2>&1 &&
	gtk4-update-icon-cache -q -t -f "${icons%/hicolor}/hicolor" 2>/dev/null || true

echo "installed:"
echo "  $apps/$id.desktop"
echo "  $icons/{scalable,symbolic}/apps/$id*.svg"
