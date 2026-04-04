package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// LLM message types for the OpenAI API.

type llmMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// SSE chunk from the OpenAI streaming API.
type sseChunk struct {
	Choices []struct {
		Delta struct {
			Content   string     `json:"content"`
			ToolCalls []toolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

// llmRequest sends a request to the LLM and streams the response.
// It returns the full text response, any tool calls, and an error.
func (a *agent) llmRequest(ctx context.Context, conn *LLMConnection, messages []llmMessage) (string, []toolCall, error) {
	reqBody := map[string]any{
		"model":    conn.Model,
		"stream":   true,
		"messages": messages,
		"tools":    llmToolDefinitions(),
	}

	body, _ := json.Marshal(reqBody)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", conn.URL, bytes.NewReader(body))
	if err != nil {
		return "", nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if conn.Token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+conn.Token)
	}

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", nil, fmt.Errorf("LLM returned %d: %s", resp.StatusCode, b)
	}

	var fullText strings.Builder
	var calls []toolCall

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var chunk sseChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if len(chunk.Choices) == 0 {
			continue
		}

		delta := chunk.Choices[0].Delta

		// Accumulate text content.
		if delta.Content != "" {
			fullText.WriteString(delta.Content)
		}

		// Accumulate tool calls (streamed incrementally).
		for _, tc := range delta.ToolCalls {
			if tc.ID != "" {
				// New tool call.
				calls = append(calls, tc)
			} else if len(calls) > 0 {
				// Continuation of last tool call's arguments.
				last := &calls[len(calls)-1]
				last.Function.Arguments += tc.Function.Arguments
			}
		}
	}

	return fullText.String(), calls, nil
}

const maxToolLoopIterations = 10

// runToolLoop runs the agentic tool loop: send to LLM, execute tool calls, repeat.
func (a *agent) runToolLoop(ctx context.Context, sid SessionId, conn *LLMConnection, messages []llmMessage) (string, error) {
	for i := 0; i < maxToolLoopIterations; i++ {
		text, calls, err := a.llmRequest(ctx, conn, messages)
		if err != nil {
			return "", err
		}

		// Stream text to Zed.
		if text != "" {
			a.sendUpdate(ctx, sid, AgentMessageChunk(TextBlock(text)))
		}

		// No tool calls — we're done.
		if len(calls) == 0 {
			return text, nil
		}

		// Add assistant message with tool calls to history.
		messages = append(messages, llmMessage{
			Role:      "assistant",
			Content:   text,
			ToolCalls: calls,
		})

		// Execute each tool call.
		for _, tc := range calls {
			result := a.executeTool(ctx, sid, tc)
			messages = append(messages, llmMessage{
				Role:       "tool",
				Content:    result,
				ToolCallID: tc.ID,
			})
		}
	}
	return "", fmt.Errorf("tool loop exceeded %d iterations", maxToolLoopIterations)
}

