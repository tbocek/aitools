package main

// What autocut remembers ACROSS sessions, as opposed to inside one.
//
// Everything else the program knows is in the project file, which is the right
// place for it: a project is a session, and it travels with the folder it
// describes. The one fact that cannot live there is WHICH project file was
// open -- reading it from the project would require already knowing it.
//
// So: ~/.config/autocut/settings.json (XDG_CONFIG_HOME when it is set, which is
// also how the tests get at it). Deliberately not a dotfile beside the videos,
// because a session folder gets copied and zipped and handed around, and a
// remembered path travelling with it would reopen somebody else's variant on
// the first launch after the copy.
//
// Losing this file costs nothing: Autocut falls back to the working copy, the
// working copy it opened before this existed. Every read and write here is
// therefore best-effort and never fails a launch.

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type uiSettings struct {
	// The named project last opened or saved, keyed by the autocut root it
	// belongs to. Keyed rather than one path, because every path INSIDE a
	// project file is relative to its root (see relToRoot): reopening last
	// night's project from a different session folder would resolve its sources
	// against the wrong directory and drop every one of them with a warning.
	//
	// Nothing prunes this map. Entries are one short string each, and a root
	// that comes back after a month is exactly the case worth remembering.
	Projects map[string]string `json:"projects,omitempty"`
}

// settingsPath is the file, or "" when there is nowhere to put it -- no HOME,
// no XDG_CONFIG_HOME. That is not an error worth reporting: it means the
// feature is off for this launch, and the caller falls back.
func settingsPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "autocut", "settings.json")
}

// loadSettings reads the file, or hands back an empty one. A settings file that
// has been corrupted (edited by hand, half-written by a kill -9) is treated the
// same as a missing one: the whole content is a convenience, so refusing to
// start over it would be the wrong trade.
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

func (s uiSettings) save() error {
	p := settingsPath()
	if p == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, append(b, '\n'), 0o644)
}

// lastProject is the project file to open on startup, or "" for none. A
// remembered file that has since been renamed or deleted is not an error and
// not a dialog -- it is simply not there, and the working copy is what opens
// instead. The entry is left alone rather than pruned: an external drive that
// is not mounted this morning is the same shape as a deletion, and forgetting
// the name would make the difference permanent.
func (a *App) lastProject() string {
	p := loadSettings().Projects[a.root]
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
	s := loadSettings()
	if s.Projects[a.root] == path {
		return // the startup load re-remembering what it just read
	}
	if s.Projects == nil {
		s.Projects = map[string]string{}
	}
	s.Projects[a.root] = path
	if err := s.save(); err != nil {
		// worth a line, not worth interrupting: the session is unaffected, the
		// only casualty is which file the NEXT launch opens
		a.logf("settings: %v", err)
	}
}
