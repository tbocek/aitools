package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// agent implements acp.Agent.
type agent struct {
	conn     *AgentSideConnection
	mu       sync.Mutex
	cancel   context.CancelFunc
	sessions map[SessionId]*Session
	mode           string
	settings       Settings
	allowWrites    string // "", "turn"
	runners        []taskRunner
	pendingRefs    []CodeRef
}

var _ Agent = (*agent)(nil)

func cwdOrDefault(cwd string) string {
	if cwd != "" {
		return cwd
	}
	d, _ := os.Getwd()
	return d
}

func (a *agent) getSession(id SessionId) *Session {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sessions[id]
}

func (a *agent) putSession(s *Session) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessions[s.ID] = s
}

func (a *agent) sendUpdate(ctx context.Context, sid SessionId, u SessionUpdate) {
	_ = a.conn.SessionUpdate(ctx, SessionNotification{SessionId: sid, Update: u})
}

func (a *agent) Initialize(_ context.Context, req InitializeRequest) (InitializeResponse, error) {
	return InitializeResponse{
		ProtocolVersion: ProtocolVersionNumber,
		AgentCapabilities: AgentCapabilities{
			LoadSession:        true,
			PromptCapabilities: PromptCapabilities{},
			SessionCapabilities: &SessionCapabilities{
				List: &SessionListCapabilities{},
			},
		},
		AgentInfo: &Implementation{
			Name:    "llama-acp",
			Version: "0.1.0",
		},
		AuthMethods: []string{},
	}, nil
}

func (a *agent) Authenticate(_ context.Context, _ AuthenticateRequest) (AuthenticateResponse, error) {
	// No auth needed for local llama.cpp.
	return AuthenticateResponse{}, nil
}

func (a *agent) NewSession(_ context.Context, req NewSessionRequest) (NewSessionResponse, error) {
	cwd := cwdOrDefault(req.Cwd)
	s, err := newSession(cwd)
	if err != nil {
		return NewSessionResponse{}, err
	}
	a.putSession(s)
	if settings, err := loadSettings(cwd); err == nil {
		a.settings = settings
	}
	a.discoverRunners(cwd)
	return NewSessionResponse{SessionId: s.ID, Modes: modeState(a.mode)}, nil
}

func (a *agent) LoadSession(ctx context.Context, req LoadSessionRequest) (LoadSessionResponse, error) {
	cwd := cwdOrDefault(req.Cwd)
	s, err := loadSession(cwd, req.SessionId)
	if err != nil {
		return LoadSessionResponse{}, fmt.Errorf("loading session: %w", err)
	}
	a.putSession(s)
	if settings, err := loadSettings(cwd); err == nil {
		a.settings = settings
	}
	a.discoverRunners(cwd)

	// Replay conversation history. Alternate chunk types create message
	// boundaries in Zed; insert empty spacers between consecutive same-type messages.
	lastRole := ""
	for _, m := range s.Messages {
		if m.Role == lastRole {
			if m.Role == "user" {
				a.sendUpdate(ctx, req.SessionId, AgentMessageChunk(TextBlock("")))
			} else {
				a.sendUpdate(ctx, req.SessionId, UserMessageChunk(TextBlock("")))
			}
		}
		if m.Role == "user" {
			a.sendUpdate(ctx, req.SessionId, UserMessageChunk(TextBlock(m.Content)))
		} else {
			a.sendUpdate(ctx, req.SessionId, AgentMessageChunk(TextBlock(m.Content)))
		}
		lastRole = m.Role
	}

	return LoadSessionResponse{Modes: modeState(a.mode)}, nil
}

func (a *agent) ListSessions(_ context.Context, req ListSessionsRequest) (ListSessionsResponse, error) {
	sessions, err := listSessions(cwdOrDefault(req.Cwd))
	if err != nil {
		return ListSessionsResponse{Sessions: []SessionInfo{}}, err
	}
	if sessions == nil {
		sessions = []SessionInfo{}
	}
	return ListSessionsResponse{Sessions: sessions}, nil
}

var availableModes = []SessionMode{
	{Id: "discussion", Name: "Discussion", Description: "Discuss and explore ideas"},
	{Id: "execution", Name: "Execution", Description: "Execute tasks and make changes"},
}

func modeState(current string) *SessionModeState {
	return &SessionModeState{
		CurrentModeId:  current,
		AvailableModes: availableModes,
	}
}

func (a *agent) SetSessionConfigOption(_ context.Context, req SetSessionConfigOptionRequest) (SetSessionConfigOptionResponse, error) {
	return SetSessionConfigOptionResponse{ConfigOptions: []SessionConfigOption{}}, nil
}

func (a *agent) SetSessionMode(_ context.Context, req SetSessionModeRequest) (SetSessionModeResponse, error) {
	a.mu.Lock()
	a.mode = string(req.ModeId)
	a.mu.Unlock()
	return SetSessionModeResponse{}, nil
}

func (a *agent) SetSessionModel(_ context.Context, req SetSessionModelRequest) (SetSessionModelResponse, error) {
	return SetSessionModelResponse{}, nil
}

func (a *agent) Cancel(_ context.Context, _ CancelNotification) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		a.cancel()
	}
}

func (a *agent) systemPrompt(sid SessionId) (string, error) {
	sess := a.getSession(sid)
	if sess == nil {
		return "", fmt.Errorf("no session found")
	}

	content, err := os.ReadFile(filepath.Join(sess.Cwd, "AGENT.md"))
	if err != nil {
		return "", fmt.Errorf("AGENT.md not found in project root — create one with your system prompt")
	}

	a.mu.Lock()
	mode := a.mode
	a.mu.Unlock()

	var b strings.Builder
	b.Write(content)
	b.WriteString("\n\n")
	b.WriteString("Project directory: " + sess.Cwd + "\n")
	b.WriteString("Current mode: " + mode + "\n\n")

	files := listProjectFiles(sess.Cwd)
	if len(files) > 0 {
		b.WriteString("Project structure:\n")
		for _, f := range files {
			b.WriteString("  " + f + "\n")
		}
	}

	return b.String(), nil
}

func (a *agent) replyError(ctx context.Context, sid SessionId, msg string) PromptResponse {
	a.sendUpdate(ctx, sid, AgentMessageChunk(TextBlock("❌ Error: "+msg)))
	return PromptResponse{StopReason: StopReasonEndTurn}
}

func (a *agent) Prompt(ctx context.Context, req PromptRequest) (PromptResponse, error) {
	ctx, cancel := context.WithCancel(ctx)
	a.mu.Lock()
	a.cancel = cancel
	a.allowWrites = ""
	a.mu.Unlock()
	defer cancel()

	// Extract user text.
	var userText string
	for _, block := range req.Content {
		if block.Text != nil {
			userText += block.Text.Text
		}
	}

	// Store user message.
	sess := a.getSession(req.SessionId)
	isFirstMessage := sess != nil && len(sess.Messages) == 0 && len(sess.History) == 0
	if sess != nil {
		sess.AddUser(userText)
		_ = sess.Save()
	}

	conn := a.settings.LLM("fast")
	if conn == nil {
		return a.replyError(ctx, req.SessionId, "no 'fast' connection in .codehalter/settings.toml"), nil
	}

	// Generate title for new sessions.
	if isFirstMessage {
		go a.generateTitle(ctx, sess, userText)
	}

	// Build context for the first message.
	content := userText
	if isFirstMessage {
		sysPrompt, err := a.systemPrompt(req.SessionId)
		if err != nil {
			return a.replyError(ctx, req.SessionId, err.Error()), nil
		}
		content = sysPrompt + "\n---\n" + userText
	}

	// Build message history for the LLM.
	var messages []llmMessage
	if sess != nil {
		messages = a.buildLLMHistory(sess, userText)
	}
	messages = append(messages, llmMessage{Role: "user", Content: content})

	response, err := a.runToolLoop(ctx, req.SessionId, conn, messages)
	if err != nil {
		return a.replyError(ctx, req.SessionId, err.Error()), nil
	}

	if sess != nil && response != "" {
		sess.AddAssistant(response)
		_ = sess.Save()
		a.compressHistory(ctx, sess)
	}

	return PromptResponse{StopReason: StopReasonEndTurn}, nil
}

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	a := &agent{sessions: make(map[SessionId]*Session), mode: "discussion"}
	conn := NewAgentSideConnection(a, os.Stdout, os.Stdin, log)
	a.conn = conn

	log.Info("waiting for connection")
	<-conn.Done()
	log.Info("connection closed")
}
