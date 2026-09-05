package main

// Where an edited prompt is kept: ~/.config/autocut/prompts/, one text file
// per job, beside the settings that are also this machine's (settings.go).
//
//	prompts/cut.txt
//	prompts/narrate.txt
//
// It used to be the project file, one prompt per key inside project.json, and
// that was wrong about what a prompt is. A prompt is not a fact about this
// session the way the sources and the context are -- it is how you like to be
// edited for, and it is the same in January's raid as in March's. Kept in the
// project, every new session started from the shipped wording again, and the
// paragraph you had tuned over four videos was in a file you had stopped
// opening.
//
// A file rather than a blob, because a prompt is prose: it is read in a diff,
// greped for a phrase, and occasionally fixed in an editor while the app is
// closed. None of that survives being a JSON string with \n in it.
//
// Only what differs is written. The built-ins are in the binary, so an
// untouched job has no file at all, which is what makes Reset a delete and
// what lets a newer build's wording reach a machine that never edited it.

import (
	"os"
	"path/filepath"
	"strings"
)

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

const promptExt = ".txt"

// promptPath is where a job's wording is kept, or "" when there is nowhere to
// put it. One file per job: prompts/cut.txt.
//
// It was a folder per job with a file per WORDING in it -- prompts/cut/
// Highlights.txt -- back when a Style dropdown turned every prompt at once.
// The styles are gone (prompts.go), so the name in the path had nothing left
// to vary, and a folder holding one file called General.txt is a folder
// holding one file.
func promptPath(key string) string {
	d := promptsDir()
	if d == "" || key == "" {
		return ""
	}
	return filepath.Join(d, key+promptExt)
}

// oldPromptNames are the file names the folder-per-job era wrote a job's
// DEFAULT wording under. Anything else in that folder was a wording for a
// style, and the styles no longer exist -- the file is left where it is, and
// what is worth keeping out of it goes in the box or the user context by hand.
var oldPromptNames = []string{"General" + promptExt, "Default" + promptExt}

// loadGlobalPrompts is the startup read: whatever this machine has of its own
// for each job. Called once, before the first project is opened, so that what
// the boxes show is this machine's answer rather than the last project's.
func (a *App) loadGlobalPrompts() {
	txt, disk := map[string]string{}, map[string]string{}
	for _, d := range promptDefs {
		text, from := a.readPrompt(d.key)
		if text == "" {
			continue
		}
		txt[d.key] = text
		if from == promptPath(d.key) {
			// adopted from the old folder instead: leaving promptDisk empty
			// for it is what makes the next flush write it where it belongs
			disk[d.key] = text
		}
	}
	a.promptMu.Lock()
	a.promptTxt = txt
	a.promptMu.Unlock()
	a.promptDisk = disk
	for _, d := range promptDefs {
		a.showPrompt(d.key)
	}
}

// readPrompt is one job's stored wording and the file it came from: its own
// file, or -- for a machine that last ran a build with styles -- the default
// wording out of the old folder.
//
// An empty file is a wording that says nothing, which would send the model no
// system prompt at all: treated as absent, here as before.
func (a *App) readPrompt(key string) (text, from string) {
	try := []string{promptPath(key)}
	if d := promptsDir(); d != "" {
		for _, n := range oldPromptNames {
			try = append(try, filepath.Join(d, key, n))
		}
	}
	for _, p := range try {
		if p == "" {
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			if !os.IsNotExist(err) {
				a.logf("!!! could not read the %s prompt: %v", key, err)
			}
			continue
		}
		if s := strings.TrimSpace(string(b)); s != "" {
			return s, p
		}
	}
	return "", ""
}

// flushPrompts writes what changed since the last flush. Called from the same
// tick as the project (startAutosave), and for the same reason: every
// keystroke in the box goes through setPrompt, and a file written per
// keystroke would be a hundred writes for one sentence.
//
// promptDisk is what is believed to be on disk, so a session that edits
// nothing writes nothing. A write that fails is logged and then treated as
// done: retrying it every two seconds would put the same line in the log for
// the rest of the evening, and the next edit tries again anyway, which is the
// moment a retry is worth something.
//
// A job back on its shipped wording has its file REMOVED, not written empty:
// that is what Reset is, and leaving the file would put the edit back on the
// next launch.
//
// GUI thread only, like flushProject -- promptDisk has no lock and needs none.
func (a *App) flushPrompts() {
	if promptsDir() == "" {
		return
	}
	cur := map[string]string{}
	for _, d := range promptDefs {
		if a.promptOwned(d.key) {
			cur[d.key] = a.prompt(d.key)
		}
	}
	for key, text := range cur {
		if a.promptDisk[key] == text {
			continue
		}
		if err := os.MkdirAll(promptsDir(), 0o700); err != nil {
			a.logf("!!! could not keep the %s prompt: %v", key, err)
			continue
		}
		if err := os.WriteFile(promptPath(key), []byte(text+"\n"), 0o600); err != nil {
			a.logf("!!! could not keep the %s prompt: %v", key, err)
		}
	}
	for key := range a.promptDisk {
		if _, ok := cur[key]; ok {
			continue
		}
		if err := os.Remove(promptPath(key)); err != nil && !os.IsNotExist(err) {
			a.logf("!!! could not drop the %s prompt: %v", key, err)
		}
	}
	a.promptDisk = cur
}
