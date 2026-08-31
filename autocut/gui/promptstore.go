package main

// Where an edited prompt is kept: ~/.config/autocut/prompts/, one text file
// per wording, beside the settings that are also this machine's (settings.go).
//
//	prompts/cut/Highlights.txt
//	prompts/narrate/Default.txt
//
// It used to be the project file, one prompt per key inside project.json, and
// that was wrong about what a prompt is. A prompt is not a fact about this
// session the way the sources and the context are -- it is how you like to be
// edited for, and it is the same in January's raid as in March's. Kept in the
// project, every new session started from the shipped wording again, and the
// paragraph you had tuned over four videos was in a file you had stopped
// opening.
//
// One file per wording rather than one blob, because a prompt is prose: it is
// read in a diff, greped for a phrase, and occasionally fixed in an editor
// while the app is closed. None of that survives being a JSON string with \n
// in it. The picked wording is not here -- it is one short name per job and it
// lives in llm.conf with the other short answers (rememberedBody).
//
// Only what differs is written. The built-ins are in the binary, so an
// untouched job has no file at all, which is what makes Reset a delete and
// what lets a newer build's wording reach a machine that never edited it.

import (
	"os"
	"path/filepath"
	"strings"
)

const promptExt = ".txt"

// promptsDir is the folder, or "" when there is nowhere to put it. Same answer
// as configDir, and for the same reason: nowhere to write is not an error, it
// is a launch where the prompts are whatever the binary ships.
func promptsDir() string {
	d := configDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, "prompts")
}

// promptFileName is a wording's name as a file name. A name is whatever was
// typed into "Name this wording", and one of the shipped ones -- "Rating /
// tier list" -- already holds the one character a file name cannot: a slash
// would be read as a folder, and a name of ".." as a folder that is not even
// under ours.
//
// The mapping is therefore lossy on purpose, and promptStyleName is what
// undoes it: a file is matched back against the built-ins by the name it
// WOULD have, so "Rating - tier list.txt" is read as the shipped wording it
// belongs to rather than as a fourth style with a nearly identical name.
// Two invented names that flatten to the same file would share it; they would
// also be two names nobody could tell apart in the dropdown.
func promptFileName(name string) string {
	f := strings.TrimSpace(strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == 0 {
			return '-'
		}
		return r
	}, name))
	if f == "" || f == "." || f == ".." {
		return "" // nothing writable: the caller skips it
	}
	return f
}

// promptStyleName is the name a file holds a wording for: the built-in it
// matches, or the file's own name for a wording this machine invented.
func promptStyleName(key, file string) string {
	for _, s := range promptDefFor(key).builtins() {
		if promptFileName(s.Name) == file {
			return s.Name
		}
	}
	return file
}

// loadGlobalPrompts is the startup read: every wording on disk, plus which one
// each job is set to. Called once, before the first project is opened, so that
// what the boxes show is this machine's answer rather than the last project's.
func (a *App) loadGlobalPrompts() {
	sty, disk := map[string][]promptStyle{}, map[string]string{}
	for _, d := range promptDefs {
		ents, err := os.ReadDir(filepath.Join(promptsDir(), d.key))
		if err != nil {
			continue // no folder for this job: it uses what the binary ships
		}
		for _, e := range ents {
			if e.IsDir() || !strings.HasSuffix(e.Name(), promptExt) {
				continue
			}
			b, err := os.ReadFile(filepath.Join(promptsDir(), d.key, e.Name()))
			if err != nil {
				a.logf("!!! could not read the %s prompt %s: %v", d.key, e.Name(), err)
				continue
			}
			// an empty file is a wording that says nothing, which would send
			// the model no system prompt at all -- treat it as absent
			text := strings.TrimSpace(string(b))
			if text == "" {
				continue
			}
			name := promptStyleName(d.key, strings.TrimSuffix(e.Name(), promptExt))
			sty[d.key] = append(sty[d.key], promptStyle{Name: name, Text: text})
			disk[d.key+"\x00"+name] = text
		}
	}
	pick := a.readGlobal().PromptPick
	a.promptMu.Lock()
	a.promptSty = sty
	a.promptPick = map[string]string{}
	for k, n := range pick {
		a.promptPick[k] = n
	}
	a.promptMu.Unlock()
	a.promptDisk = disk
	for _, d := range promptDefs {
		a.showPromptStyle(d.key, a.promptPickName(d.key))
	}
}

// flushPrompts writes what changed since the last flush. Called from the same
// tick as the project (startAutosave), and for the same reason: every
// keystroke in the box goes through setPrompt, and a file written per
// keystroke would be a hundred writes for one sentence.
//
// promptDisk is what is believed to be on disk, so a session that edits
// nothing writes nothing. A write that fails is logged and then treated as
// done: retrying it every two seconds would put the same line in the log for
// the rest of the evening, and the next edit to that wording tries again
// anyway, which is the moment a retry is worth something.
//
// GUI thread only, like flushProject -- promptDisk has no lock and needs none.
func (a *App) flushPrompts() {
	dir := promptsDir()
	if dir == "" {
		return
	}
	cur := map[string]string{}
	a.promptMu.Lock()
	for k, list := range a.promptSty {
		for _, s := range list {
			if promptFileName(s.Name) != "" {
				cur[k+"\x00"+s.Name] = s.Text
			}
		}
	}
	a.promptMu.Unlock()

	for id, text := range cur {
		if a.promptDisk[id] == text {
			continue
		}
		key, name, _ := strings.Cut(id, "\x00")
		p := filepath.Join(dir, key)
		if err := os.MkdirAll(p, 0o700); err != nil {
			a.logf("!!! could not keep the %s prompt: %v", key, err)
			continue
		}
		if err := os.WriteFile(filepath.Join(p, promptFileName(name)+promptExt),
			[]byte(text+"\n"), 0o600); err != nil {
			a.logf("!!! could not keep the %s prompt: %v", key, err)
		}
	}
	// gone from memory means gone from disk: that is what Reset is, and
	// leaving the file would put the wording back on the next launch
	for id := range a.promptDisk {
		if _, ok := cur[id]; ok {
			continue
		}
		key, name, _ := strings.Cut(id, "\x00")
		if err := os.Remove(filepath.Join(dir, key, promptFileName(name)+promptExt)); err != nil && !os.IsNotExist(err) {
			a.logf("!!! could not drop the %s prompt: %v", key, err)
		}
	}
	a.promptDisk = cur
}

// promptStored is whether this machine has anything of its own for a job: a
// wording it edited or invented, or a shipped wording other than the default
// picked. It is the question applyPromptStyles asks before letting a project's
// wording in, and it is deliberately wider than promptOwned's: an edited
// "Highlights" counts even while "General" is the one picked, because it is
// still work somebody did here.
func (a *App) promptStored(key string) bool {
	if a.promptPickName(key) != promptDefFor(key).styleName() {
		return true
	}
	a.promptMu.Lock()
	defer a.promptMu.Unlock()
	return len(a.promptSty[key]) > 0
}

// rememberPromptPick records which wording a job is set to. Only when it is
// not the shipped default: a machine that never touched the dropdown says
// nothing about it, and follows whatever a later build ships as the default.
func (a *App) rememberPromptPick(key, name string) {
	g := a.readGlobal()
	_, had := g.PromptPick[key]
	switch {
	case name == promptDefFor(key).styleName():
		if !had {
			return
		}
		delete(g.PromptPick, key)
	case g.PromptPick[key] == name:
		return
	default:
		if g.PromptPick == nil {
			g.PromptPick = map[string]string{}
		}
		g.PromptPick[key] = name
	}
	if err := a.writeGlobal(g); err != nil {
		a.logf("settings: %v", err)
	}
}
