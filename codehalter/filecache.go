package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

const (
	maxPreviewLines = 50
	maxPreviewBytes = maxPreviewLines * 200
	chunkSize       = 10

	fileSummaryPrompt = "Summarize each file below. Call the file_summary tool with one entry per file. Max 20 words per summary. Focus on what the code does, not the file type.\n\n"
)

type FileCache struct {
	Files map[string]FileCacheEntry `toml:"files"`
}

type FileCacheEntry struct {
	Hash    string `toml:"hash"`
	Size    int64  `toml:"size"`
	Summary string `toml:"summary"`
}

func cachePath(cwd string) string {
	return filepath.Join(cwd, sessionDir, "cache.toml")
}

func loadFileCache(cwd string) FileCache {
	var c FileCache
	_, _ = toml.DecodeFile(cachePath(cwd), &c)
	if c.Files == nil {
		c.Files = make(map[string]FileCacheEntry)
	}
	return c
}

func (c *FileCache) Save(cwd string) error {
	f, err := os.Create(cachePath(cwd))
	if err != nil {
		return err
	}
	defer f.Close()
	return toml.NewEncoder(f).Encode(c)
}

// updateFileCache checks all project files against the cache and returns
// which files need re-summarization. Updates hashes for changed files.
func updateFileCache(cwd string, cache *FileCache) []string {
	files := listProjectFiles(cwd)
	var stale []string

	current := make(map[string]bool)

	for _, rel := range files {
		current[rel] = true
		abs := filepath.Join(cwd, rel)
		hash := hashFileQuick(abs)

		var size int64
		if info, err := os.Stat(abs); err == nil {
			size = info.Size()
		}

		entry, exists := cache.Files[rel]
		if !exists || entry.Hash != hash {
			cache.Files[rel] = FileCacheEntry{Hash: hash, Size: size}
			stale = append(stale, rel)
		} else if entry.Size != size {
			entry.Size = size
			cache.Files[rel] = entry
		}
	}

	for rel := range cache.Files {
		if !current[rel] {
			delete(cache.Files, rel)
		}
	}

	return stale
}

func hashFileQuick(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	h := sha256.New()
	scanner := bufio.NewScanner(f)
	lines := 0
	for scanner.Scan() {
		h.Write(scanner.Bytes())
		h.Write([]byte{'\n'})
		lines++
		if lines >= maxPreviewLines {
			break
		}
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func isBinaryFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	for _, b := range buf[:n] {
		if b == 0 {
			return true
		}
	}
	return false
}

func readPreview(cwd, rel string) string {
	path := filepath.Join(cwd, rel)
	if isBinaryFile(path) {
		return ""
	}

	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	var b strings.Builder
	scanner := bufio.NewScanner(f)
	lines := 0
	for scanner.Scan() {
		b.WriteString(scanner.Text())
		b.WriteByte('\n')
		lines++
		if lines >= maxPreviewLines {
			break
		}
	}
	if b.Len() > maxPreviewBytes {
		return ""
	}
	return b.String()
}

// summarizeStaleFiles summarizes stale files in chunks and updates the cache.
func (a *agent) summarizeStaleFiles(ctx context.Context, cwd string, cache *FileCache, staleFiles []string) error {
	conn := a.settings.LLM("fast")
	if conn == nil {
		return fmt.Errorf("no 'fast' LLM connection configured")
	}

	// Filter to text files only, mark binary.
	var toSummarize []string
	for _, rel := range staleFiles {
		preview := readPreview(cwd, rel)
		if preview == "" {
			if entry, ok := cache.Files[rel]; ok {
				entry.Summary = "(binary file)"
				cache.Files[rel] = entry
			}
			continue
		}
		toSummarize = append(toSummarize, rel)
	}

	if len(toSummarize) == 0 {
		return cache.Save(cwd)
	}

	// Process in chunks.
	for i := 0; i < len(toSummarize); i += chunkSize {
		end := i + chunkSize
		if end > len(toSummarize) {
			end = len(toSummarize)
		}
		chunk := toSummarize[i:end]

		if err := a.summarizeChunk(ctx, cwd, cache, conn, chunk); err != nil {
			return err
		}
	}

	return cache.Save(cwd)
}

var fileSummaryTool = []map[string]any{{
	"type": "function",
	"function": map[string]any{
		"name":        "file_summary",
		"description": "Store file summaries",
		"parameters": map[string]any{
			"type":     "object",
			"required": []string{"summaries"},
			"properties": map[string]any{
				"summaries": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type":     "object",
						"required": []string{"file", "summary"},
						"properties": map[string]any{
							"file":    map[string]any{"type": "string"},
							"summary": map[string]any{"type": "string"},
						},
					},
				},
			},
		},
	},
}}

func (a *agent) summarizeChunk(ctx context.Context, cwd string, cache *FileCache, conn *LLMConnection, files []string) error {
	var prompt strings.Builder
	prompt.WriteString(fileSummaryPrompt)

	for _, rel := range files {
		preview := readPreview(cwd, rel)
		fmt.Fprintf(&prompt, "=== %s ===\n%s\n\n", rel, preview)
	}

	messages := []llmMessage{{Role: "user", Content: prompt.String()}}
	_, calls, err := a.llmWithTools(ctx, conn, messages, fileSummaryTool)
	if err != nil {
		return fmt.Errorf("indexing failed: %w", err)
	}

	// Parse the tool call arguments.
	for _, tc := range calls {
		if tc.Function.Name != "file_summary" {
			continue
		}
		var result struct {
			Summaries []struct {
				File    string `json:"file"`
				Summary string `json:"summary"`
			} `json:"summaries"`
		}
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &result); err != nil {
			continue
		}
		for _, s := range result.Summaries {
			if entry, ok := cache.Files[s.File]; ok {
				entry.Summary = s.Summary
				cache.Files[s.File] = entry
			}
		}
	}

	return nil
}


// buildProjectContext returns the project structure with summaries,
// suitable for prepending to a prompt. Not stored in history.
func buildProjectContext(cwd string, cache *FileCache) string {
	files := listProjectFiles(cwd)
	if len(files) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("[Project structure — not part of conversation history]\n")
	for _, rel := range files {
		if entry, ok := cache.Files[rel]; ok && entry.Summary != "" {
			fmt.Fprintf(&b, "  %s — %s\n", rel, entry.Summary)
		} else {
			b.WriteString("  " + rel + "\n")
		}
	}
	return b.String()
}
