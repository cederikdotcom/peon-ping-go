package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// ActionBarSession represents a single Claude Code session in the action bar.
type ActionBarSession struct {
	Project   string `json:"project"`
	State     string `json:"state"` // "working", "done", "needs approval", "ready"
	Message   string `json:"message,omitempty"`
	HWND      uint64 `json:"hwnd"`
	UpdatedAt int64  `json:"updated_at"` // unix timestamp
}

// ActionBarState holds all sessions for the action bar to display.
type ActionBarState struct {
	Sessions map[string]ActionBarSession `json:"sessions"` // keyed by session ID
}

func actionBarPath(peonDir string) string {
	return filepath.Join(peonDir, ".actionbar.json")
}

// writeActionBarSession updates a single session in the action bar state file.
// Single writer (main binary), reader tolerates partial reads.
func writeActionBarSession(peonDir, sessionID, project, state, message string, hwnd uint64) {
	if sessionID == "" {
		return
	}

	abPath := actionBarPath(peonDir)

	// Read existing state (best-effort).
	var abs ActionBarState
	if data, err := os.ReadFile(abPath); err == nil {
		json.Unmarshal(data, &abs)
	}
	if abs.Sessions == nil {
		abs.Sessions = make(map[string]ActionBarSession)
	}

	// Prune sessions older than 10 minutes.
	now := time.Now().Unix()
	for id, s := range abs.Sessions {
		if now-s.UpdatedAt > 600 {
			delete(abs.Sessions, id)
		}
	}

	abs.Sessions[sessionID] = ActionBarSession{
		Project:   project,
		State:     state,
		Message:   message,
		HWND:      hwnd,
		UpdatedAt: now,
	}

	data, err := json.Marshal(abs)
	if err != nil {
		return
	}
	os.WriteFile(abPath, data, 0644)
}
