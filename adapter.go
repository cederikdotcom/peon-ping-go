package main

import "encoding/json"

// Adapter parses harness-specific JSON into an internal Event.
type Adapter interface {
	Parse(raw json.RawMessage) (Event, error)
}

// genericPayload is the fallback format any harness can send directly.
type genericPayload struct {
	Type      string `json:"type"`
	CWD       string `json:"cwd"`
	SessionID string `json:"session_id"`
	AgentMode bool   `json:"agent_mode"`
}

// GenericAdapter handles the internal event format sent directly.
type GenericAdapter struct{}

func (GenericAdapter) Parse(raw json.RawMessage) (Event, error) {
	var p genericPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return Event{}, err
	}
	return Event{
		Type:      p.Type,
		CWD:       p.CWD,
		SessionID: p.SessionID,
		AgentMode: p.AgentMode,
	}, nil
}

// probeFields peeks at JSON to detect which harness sent it.
type probeFields struct {
	HookEventName string `json:"hook_event_name"`
	Type          string `json:"type"`
}

// detectAdapter auto-detects the harness from JSON field presence.
// Can be overridden by --harness flag or PEON_HARNESS env var.
func detectAdapter(raw json.RawMessage, forceHarness string) Adapter {
	if forceHarness != "" {
		switch forceHarness {
		case "claude":
			return ClaudeAdapter{}
		case "generic":
			return GenericAdapter{}
		default:
			return GenericAdapter{}
		}
	}

	var probe probeFields
	_ = json.Unmarshal(raw, &probe)

	// Claude Code sends hook_event_name or notification_type
	if probe.HookEventName != "" {
		return ClaudeAdapter{}
	}

	// Generic fallback: expects {"type": "..."}
	return GenericAdapter{}
}
