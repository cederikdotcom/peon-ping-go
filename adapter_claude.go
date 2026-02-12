package main

import "encoding/json"

// claudePayload represents Claude Code's hook JSON input.
type claudePayload struct {
	HookEventName    string `json:"hook_event_name"`
	NotificationType string `json:"notification_type"`
	Message          string `json:"message"`
	Title            string `json:"title"`
	CWD              string `json:"cwd"`
	SessionID        string `json:"session_id"`
	PermissionMode   string `json:"permission_mode"`
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
	case "Notification":
		switch p.NotificationType {
		case "permission_prompt":
			e.Type = "permission_needed"
		case "idle_prompt":
			e.Type = "idle"
		default:
			// Unknown notification type — no-op
			e.Type = ""
		}
	default:
		e.Type = ""
	}

	return e, nil
}
