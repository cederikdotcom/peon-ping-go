package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Action bar state machine:
//
//   SessionStart ──────► "ready"
//   UserPromptSubmit ──► "working"
//   Stop ──────────────► "done"
//   PermissionRequest ─► "needs approval" (with tool details + heartbeat)
//   Notification(idle) ► "has question"
//   SessionEnd ────────► removed
//
// The "needs approval" state is exclusively managed by handlePermissionRequest
// (via updateActionBarPermission/clearActionBarPermission). Other hooks skip
// action bar writes for permission events to avoid dual-write races.
//
// The helper detects stale heartbeats to visually override "needs approval"
// to "working" when permissions are handled in-terminal.

// ActionBarSession represents a single Claude Code session in the action bar.
type ActionBarSession struct {
	Project              string          `json:"project"`
	State                string          `json:"state"` // "working", "done", "needs approval", "ready"
	Message              string          `json:"message,omitempty"`
	HWND                 uint64          `json:"hwnd"`
	UpdatedAt            int64           `json:"updated_at"` // unix timestamp
	ToolName             string          `json:"tool_name,omitempty"`
	ToolInput            json.RawMessage `json:"tool_input,omitempty"`
	PermissionSuggestions json.RawMessage `json:"permission_suggestions,omitempty"`
}

// ActionBarState holds all sessions for the action bar to display.
type ActionBarState struct {
	Sessions map[string]ActionBarSession `json:"sessions"` // keyed by session ID
}

func actionBarPath(peonDir string) string {
	return filepath.Join(peonDir, ".actionbar.json")
}

// modifyActionBar performs an atomic read-modify-write of the action bar state
// file under an exclusive flock to prevent lost updates from concurrent sessions.
func modifyActionBar(peonDir string, fn func(abs *ActionBarState)) {
	lockPath := filepath.Join(peonDir, ".actionbar.lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return
	}
	defer func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return
	}

	abPath := actionBarPath(peonDir)
	var abs ActionBarState
	if data, err := os.ReadFile(abPath); err == nil {
		json.Unmarshal(data, &abs)
	}
	if abs.Sessions == nil {
		abs.Sessions = make(map[string]ActionBarSession)
	}

	fn(&abs)

	data, err := json.Marshal(abs)
	if err != nil {
		return
	}
	atomicWriteFile(abPath, data)
}

// writeActionBarSession updates a single session in the action bar state file.
func writeActionBarSession(peonDir, sessionID, project, state, message string, hwnd uint64) {
	if sessionID == "" {
		return
	}
	modifyActionBar(peonDir, func(abs *ActionBarState) {
		// Prune sessions older than 10 minutes (safety net).
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
	})
}

// removeActionBarSession removes a session from the action bar state file.
func removeActionBarSession(peonDir, sessionID string) {
	if sessionID == "" {
		return
	}
	modifyActionBar(peonDir, func(abs *ActionBarState) {
		delete(abs.Sessions, sessionID)
	})
}

// updateActionBarPermission sets a session to "needs approval" with tool details.
// This is the single source of truth — the helper reads tool info from here.
func updateActionBarPermission(peonDir, sessionID, toolName string, toolInput, permSuggestions json.RawMessage) {
	if sessionID == "" {
		return
	}
	modifyActionBar(peonDir, func(abs *ActionBarState) {
		s, ok := abs.Sessions[sessionID]
		if !ok {
			return
		}
		s.State = "needs approval"
		s.ToolName = toolName
		s.ToolInput = toolInput
		s.PermissionSuggestions = permSuggestions
		s.UpdatedAt = time.Now().Unix()
		abs.Sessions[sessionID] = s
	})
}

// clearActionBarPermission resets a session from "needs approval" to "working",
// clearing tool details. Called when a permission is resolved or the hook exits.
func clearActionBarPermission(peonDir, sessionID string) {
	if sessionID == "" {
		return
	}
	modifyActionBar(peonDir, func(abs *ActionBarState) {
		s, ok := abs.Sessions[sessionID]
		if !ok {
			return
		}
		s.State = "working"
		s.ToolName = ""
		s.ToolInput = nil
		s.PermissionSuggestions = nil
		s.UpdatedAt = time.Now().Unix()
		abs.Sessions[sessionID] = s
	})
}

// atomicWriteFile writes data to a temp file then renames it into place,
// preventing readers from seeing partial/corrupt content.
func atomicWriteFile(path string, data []byte) {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return
	}
	os.Rename(tmp, path)
}
