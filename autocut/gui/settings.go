package main

// What autocut remembers ACROSS sessions -- and, since it turned out to be the
// same question, where this machine's settings live.
//
// Everything else the program knows is in the project file, which is the right
// place for it: a project is a session, and it travels with the folder it
// describes. What cannot live there is anything whose answer is "this
// machine": which server writes the cuts, which ffmpeg to shell out to, and --
// the one that could never be in the project, by construction -- WHICH project
// file was open, since reading that from the project would require already
// knowing it.
//
// Those were two files in two places. llm.conf sat beside the videos, written
// by the gear dialog; settings.json sat under ~/.config, holding one line of
// bookkeeping. Both were wrong about where they were: a session folder gets
// copied and zipped and handed around, so the API key travelled with it and a
// remembered path in there would reopen somebody else's variant on the first
// launch after the copy.
//
// They are one file now, in the place the second one already had right:
//
//	~/.config/autocut/llm.conf        the settings, and what is remembered
//	~/.config/autocut/prompts/        the prompts edited here (prompts.go)
//
// XDG_CONFIG_HOME when it is set, which is also how the tests get at it. Still
// bash-sourceable, still chmod 600 -- see writeGlobal.
//
// Losing the file costs nothing that cannot be got back: the endpoints are
// retyped in the gear dialog, and a forgotten project falls back to the
// working copy, the way it did before any of this existed. Every read and
// write of the remembered half is therefore best-effort and never fails a
// launch.

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// configDir is ~/.config/autocut, or "" when there is nowhere to put it -- no
// HOME, no XDG_CONFIG_HOME. That is not an error worth reporting: it means the
// feature is off for this launch, and every caller falls back.
func configDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "autocut")
}

// uiSettings is the old settings.json, which held exactly this. Read, never
// written: readGlobal takes the map from here when the conf file has nothing
// to say about projects, which is once, on the launch after the merge.
type uiSettings struct {
	Projects map[string]string `json:"projects,omitempty"`
}

func settingsPath() string {
	dir := configDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "settings.json")
}

// loadSettings reads the old file, or hands back an empty one. A settings file
// that has been corrupted (edited by hand, half-written by a kill -9) is
// treated the same as a missing one: the whole content is a convenience, so
// refusing to start over it would be the wrong trade.
func loadSettings() uiSettings {
	var s uiSettings
	p := settingsPath()
	if p == "" {
		return s
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return s
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return uiSettings{}
	}
	return s
}

// lastProject is the project file to open on startup, or "" for none. A
// remembered file that has since been renamed or deleted is not an error and
// not a dialog -- it is simply not there, and the working copy is what opens
// instead. The entry is left alone rather than pruned: an external drive that
// is not mounted this morning is the same shape as a deletion, and forgetting
// the name would make the difference permanent.
func (a *App) lastProject() string {
	p := a.readGlobal().Projects[a.root]
	if p == "" || !exists(p) {
		return ""
	}
	return p
}

// rememberProject records what the header bar now names. Called from the two
// places that assign projPath, so what the next launch opens is what this one
// last had open -- including the working copy itself, which is a decision
// ("go back to the unnamed session") and not an absence.
func (a *App) rememberProject(path string) {
	g := a.readGlobal()
	if g.Projects[a.root] == path {
		return // the startup load re-remembering what it just read
	}
	if g.Projects == nil {
		g.Projects = map[string]string{}
	}
	g.Projects[a.root] = path
	if err := a.writeGlobal(g); err != nil {
		// worth a line, not worth interrupting: the session is unaffected, the
		// only casualty is which file the NEXT launch opens
		a.logf("settings: %v", err)
	}
}
