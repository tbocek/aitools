package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var skipDirs = map[string]bool{
	".git": true, ".codehalter": true, "node_modules": true,
	"__pycache__": true, ".venv": true, "vendor": true,
	".idea": true, ".vscode": true, "target": true, "dist": true, "build": true,
}

func init() {
	RegisterTool(Tool{Def: map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        "list_files",
			"description": "List files in the project directory. Returns a newline-separated list of relative paths.",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string", "description": "Subdirectory to list (relative to project root, empty for root)"},
				},
			},
		},
	}, Execute: func(ctx context.Context, a *agent, sid SessionId, args map[string]string) string {
		sess := a.getSession(sid)
		if sess == nil {
			return "error: no session"
		}
		root := sess.Cwd
		dir := root
		if subdir := args["path"]; subdir != "" {
			resolved, err := a.resolvePath(sid, subdir)
			if err != nil {
				return "error: " + err.Error()
			}
			dir = resolved
		}

		tcId := a.StartToolCall(ctx, sid, "Listing files", "search", nil)

		var files []string
		filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if skipDirs[d.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			files = append(files, rel)
			return nil
		})

		result := strings.Join(files, "\n")
		a.CompleteToolCall(ctx, sid, tcId, []ToolCallContent{TextContent(fmt.Sprintf("%d files", len(files)))})
		return result
	}})

	RegisterTool(Tool{Def: map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        "read_file",
			"description": "Read the contents of a file from the project",
			"parameters": map[string]any{
				"type":     "object",
				"required": []string{"path"},
				"properties": map[string]any{
					"path": map[string]any{"type": "string", "description": "Path to the file (absolute or relative to project root)"},
				},
			},
		},
	}, Execute: func(ctx context.Context, a *agent, sid SessionId, args map[string]string) string {
		path, err := a.resolvePath(sid, args["path"])
		if err != nil {
			return "error: " + err.Error()
		}
		tcId := a.StartToolCall(ctx, sid, "Reading "+path, "read", []ToolCallLocation{{Path: path}})

		content, err := fsRead(a.conn.RPC(), ctx, sid, path)
		if err != nil {
			a.FailToolCall(ctx, sid, tcId, err.Error())
			return "error: " + err.Error()
		}

		a.CompleteToolCall(ctx, sid, tcId, []ToolCallContent{TextContent(content)})
		return content
	}})

	RegisterTool(Tool{Def: map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        "write_file",
			"description": "Write content to a file in the project. The user will be asked to approve the change.",
			"parameters": map[string]any{
				"type":     "object",
				"required": []string{"path", "content"},
				"properties": map[string]any{
					"path":    map[string]any{"type": "string", "description": "Path to the file (absolute or relative to project root)"},
					"content": map[string]any{"type": "string", "description": "The new file content"},
				},
			},
		},
	}, Execute: func(ctx context.Context, a *agent, sid SessionId, args map[string]string) string {
		path, err := a.resolvePath(sid, args["path"])
		if err != nil {
			return "error: " + err.Error()
		}
		newContent := args["content"]
		tcId := a.StartToolCall(ctx, sid, "Editing "+path, "edit", []ToolCallLocation{{Path: path}})

		oldContent, _ := fsRead(a.conn.RPC(), ctx, sid, path)

		a.CompleteToolCall(ctx, sid, tcId, []ToolCallContent{DiffContent(path, &oldContent, newContent)})

		// Check if writes are pre-approved.
		a.mu.Lock()
		allowed := a.allowWrites
		a.mu.Unlock()

		if allowed == "" {
			choice, err := a.conn.AskWritePermission(ctx, sid, tcId)
			if err != nil {
				return "error asking user: " + err.Error()
			}
			switch choice {
			case "reject":
				return "user rejected the changes"
			case "allow_turn":
				a.mu.Lock()
				a.allowWrites = "turn"
				a.mu.Unlock()
			}
		}

		if err := fsWrite(a.conn.RPC(), ctx, sid, path, newContent); err != nil {
			return "error writing file: " + err.Error()
		}
		return "file written successfully"
	}})
}

func fsRead(c *Connection, ctx context.Context, sid SessionId, path string) (string, error) {
	resp, err := SendRequest[struct {
		Content string `json:"content"`
	}](c, ctx, "fs/read_text_file", struct {
		SessionId SessionId `json:"sessionId"`
		Path      string    `json:"path"`
	}{SessionId: sid, Path: path})
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}

func fsWrite(c *Connection, ctx context.Context, sid SessionId, path, content string) error {
	_, err := SendRequest[struct{}](c, ctx, "fs/write_text_file", struct {
		SessionId SessionId `json:"sessionId"`
		Path      string    `json:"path"`
		Content   string    `json:"content"`
	}{SessionId: sid, Path: path, Content: content})
	return err
}
