package main

import (
	"bufio"
	"encoding/json"
	"os"
)

// claudePayload represents Claude Code's hook JSON input.
type claudePayload struct {
	HookEventName    string `json:"hook_event_name"`
	NotificationType string `json:"notification_type"`
	Message          string `json:"message"`
	Title            string `json:"title"`
	CWD              string `json:"cwd"`
	SessionID        string `json:"session_id"`
	PermissionMode   string `json:"permission_mode"`
	TranscriptPath   string `json:"transcript_path"`
}

// ClaudeAdapter maps Claude Code hook JSON to internal Events.
type ClaudeAdapter struct{}

func (ClaudeAdapter) Parse(raw json.RawMessage) (Event, error) {
	var p claudePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return Event{}, err
	}

	e := Event{
		CWD:       p.CWD,
		SessionID: p.SessionID,
		AgentMode: p.PermissionMode == "delegate",
		Message:   p.Message,
	}

	switch p.HookEventName {
	case "SessionStart":
		e.Type = "session_start"
	case "UserPromptSubmit":
		e.Type = "prompt_submit"
	case "Stop":
		e.Type = "task_complete"
		// Extract Claude's last text output from the transcript.
		if p.TranscriptPath != "" {
			if msg := lastAssistantText(p.TranscriptPath); msg != "" {
				e.Message = msg
			}
		}
	case "SessionEnd":
		e.Type = "session_end"
	case "Notification":
		switch p.NotificationType {
		case "permission_prompt":
			e.Type = "permission_needed"
		case "idle_prompt":
			e.Type = "idle"
		default:
			e.Type = ""
		}
	default:
		e.Type = ""
	}

	return e, nil
}

// lastAssistantText reads a transcript JSONL file and returns the text from
// the last assistant message that contains text content (Claude's final output).
func lastAssistantText(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	// Collect all assistant messages with text, keep only the last one.
	var allTexts []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()

		var entry struct {
			Type    string `json:"type"`
			Message struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &entry) != nil || entry.Type != "assistant" {
			continue
		}
		// Collect all text blocks from this assistant message.
		var msgText string
		for _, c := range entry.Message.Content {
			if c.Type == "text" && c.Text != "" {
				msgText = c.Text
			}
		}
		if msgText != "" {
			allTexts = append(allTexts, msgText)
		}
	}

	if len(allTexts) == 0 {
		return ""
	}
	lastText := allTexts[len(allTexts)-1]

	// Truncate to a reasonable length for the action bar.
	if len(lastText) > 500 {
		lastText = lastText[:497] + "..."
	}
	return lastText
}
