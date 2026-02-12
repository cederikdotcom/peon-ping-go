package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// permissionRspFile is the response file written by the action bar helper.
type permissionRspFile struct {
	Behavior         string `json:"behavior"` // "allow" or "deny"
	ApplySuggestions bool   `json:"apply_suggestions,omitempty"`
}

// hookOutput is the JSON structure Claude Code expects on stdout from a PermissionRequest hook.
type hookOutput struct {
	HookSpecificOutput hookSpecificOutput `json:"hookSpecificOutput"`
}

type hookSpecificOutput struct {
	HookEventName string           `json:"hookEventName"`
	Decision      hookDecision     `json:"decision"`
}

type hookDecision struct {
	Behavior           string          `json:"behavior"`
	UpdatedPermissions json.RawMessage `json:"updatedPermissions,omitempty"`
}

// handlePermissionRequest handles PermissionRequest hook events by updating
// the action bar state and polling for a response file. Falls back to terminal
// dialog on timeout.
func handlePermissionRequest(peonDir string, raw []byte) {
	var payload struct {
		SessionID             string          `json:"session_id"`
		ToolName              string          `json:"tool_name"`
		ToolInput             json.RawMessage `json:"tool_input"`
		PermissionSuggestions json.RawMessage `json:"permission_suggestions"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil || payload.SessionID == "" {
		os.Exit(0)
	}

	rspPath := filepath.Join(peonDir, ".actionbar-rsp-"+payload.SessionID+".json")
	heartbeatPath := filepath.Join(peonDir, ".actionbar-hb-"+payload.SessionID)

	// Clean up any stale response file.
	os.Remove(rspPath)

	// Update action bar state with "needs approval" + tool details (single source of truth).
	updateActionBarPermission(peonDir, payload.SessionID, payload.ToolName, payload.ToolInput, payload.PermissionSuggestions)

	// Create heartbeat file so the helper knows we're alive and polling.
	// On exit (clean or killed), clear the permission state back to "working".
	os.WriteFile(heartbeatPath, nil, 0644)
	defer func() {
		os.Remove(heartbeatPath)
		clearActionBarPermission(peonDir, payload.SessionID)
	}()

	// Poll for response file (500ms intervals, up to 5 minutes).
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)

		// Touch heartbeat so the helper knows we're still alive.
		now := time.Now()
		os.Chtimes(heartbeatPath, now, now)

		rspData, err := os.ReadFile(rspPath)
		if err != nil {
			continue // not yet written
		}

		var rsp permissionRspFile
		if err := json.Unmarshal(rspData, &rsp); err != nil {
			continue
		}

		// Clean up response file and update action bar to "working".
		os.Remove(rspPath)
		clearActionBarPermission(peonDir, payload.SessionID)

		// Build and output the hook response.
		decision := hookDecision{
			Behavior: rsp.Behavior,
		}
		if rsp.ApplySuggestions && len(payload.PermissionSuggestions) > 0 {
			decision.UpdatedPermissions = payload.PermissionSuggestions
		}

		out := hookOutput{
			HookSpecificOutput: hookSpecificOutput{
				HookEventName: "PermissionRequest",
				Decision:      decision,
			},
		}
		outData, _ := json.Marshal(out)
		fmt.Println(string(outData))
		os.Exit(0)
	}

	// Timeout: exit 0 to fall through to terminal dialog.
	os.Exit(0)
}

const version = "2.0.0"

func main() {
	peonDir := os.Getenv("CLAUDE_PEON_DIR")
	if peonDir == "" {
		home, _ := os.UserHomeDir()
		peonDir = filepath.Join(home, ".claude", "hooks", "peon-ping")
	}

	// CLI subcommands (must come before stdin read which would block).
	if len(os.Args) > 1 {
		runCLI(peonDir, os.Args[1:])
		// runCLI exits for known commands; if it returns, fall through to hook mode.
	}

	// Hook mode: read JSON from stdin.
	input, err := io.ReadAll(os.Stdin)
	if err != nil || len(input) == 0 {
		os.Exit(0)
	}

	// Debug dump: write raw stdin JSON for Stop events to inspect full payload.
	{
		var probe struct {
			HookEventName string `json:"hook_event_name"`
		}
		json.Unmarshal(input, &probe)
		if probe.HookEventName == "Stop" {
			ts := time.Now().UnixMilli()
			debugPath := filepath.Join(peonDir, fmt.Sprintf(".hook-debug-%d.json", ts))
			os.WriteFile(debugPath, input, 0644)
		}
	}

	// Early intercept: PermissionRequest hook gets special blocking handling.
	var probe struct {
		HookEventName string `json:"hook_event_name"`
	}
	json.Unmarshal(input, &probe)
	if probe.HookEventName == "PermissionRequest" {
		handlePermissionRequest(peonDir, input)
		// handlePermissionRequest always calls os.Exit; this is unreachable.
	}

	// Detect harness and parse event.
	forceHarness := os.Getenv("PEON_HARNESS")
	adapter := detectAdapter(json.RawMessage(input), forceHarness)
	event, err := adapter.Parse(json.RawMessage(input))
	if err != nil || event.Type == "" {
		os.Exit(0)
	}

	// Session end: remove from action bar and exit.
	if event.Type == "session_end" {
		removeActionBarSession(peonDir, event.SessionID)
		os.Exit(0)
	}

	// Load config.
	cfg := loadConfig(peonDir)
	if !cfg.Enabled {
		os.Exit(0)
	}

	// Check paused.
	pausedFile := filepath.Join(peonDir, ".paused")
	paused := fileExists(pausedFile)

	// Load state with flock.
	ls, err := loadStateLocked(peonDir)
	if err != nil {
		// Can't lock state; play without state tracking.
		os.Exit(0)
	}

	// Check agent suppression (needs original Claude payload for permission_mode).
	permissionMode := ""
	if ca, ok := adapter.(ClaudeAdapter); ok {
		_ = ca // we already parsed; get permission_mode from re-parsing
		var cp claudePayload
		json.Unmarshal(input, &cp)
		permissionMode = cp.PermissionMode
	}
	if event.AgentMode || (event.SessionID != "" && checkAgent(ls.State, event.SessionID, permissionMode)) {
		ls.saveStateUnlock(peonDir)
		os.Exit(0)
	}

	// Derive project name from CWD.
	project := filepath.Base(event.CWD)
	if project == "" || project == "." || project == "/" {
		project = "claude"
	}
	project = sanitizeProject(project)

	// Session start extras.
	if event.Type == "session_start" {
		// Capture the terminal window handle for popup targeting.
		if event.SessionID != "" {
			hwnd := captureWindowHandle()
			if hwnd != 0 {
				if ls.State.WindowHandles == nil {
					ls.State.WindowHandles = make(map[string]uint64)
				}
				ls.State.WindowHandles[event.SessionID] = hwnd
			}
		}
		checkForUpdate(peonDir)
		showUpdateNotice(peonDir)
		if paused {
			fmt.Fprintf(os.Stderr, "peon-ping: sounds paused — run 'peon --resume' or '/peon-ping-toggle' to unpause\n")
		}
	}

	// Look up the saved window handle for this session, fall back to default.
	var targetHwnd uint64
	if ls.State.WindowHandles != nil {
		if event.SessionID != "" {
			targetHwnd = ls.State.WindowHandles[event.SessionID]
		}
		if targetHwnd == 0 {
			targetHwnd = ls.State.WindowHandles["_default"]
		}
	}

	// Route the event.
	route := routeEvent(event)

	// Annoyed check for prompt_submit.
	if event.Type == "prompt_submit" {
		if catEnabled(cfg, "annoyed") {
			now := float64(time.Now().UnixMicro()) / 1e6
			if checkAnnoyed(ls.State, cfg.AnnoyedThreshold, cfg.AnnoyedWindowSeconds, now) {
				route.Category = "annoyed"
			}
		} else {
			// Still track timestamps even if category disabled.
			now := float64(time.Now().UnixMicro()) / 1e6
			checkAnnoyed(ls.State, cfg.AnnoyedThreshold, cfg.AnnoyedWindowSeconds, now)
		}
	}

	// Check if category is enabled.
	if route.Category != "" && !catEnabled(cfg, route.Category) {
		route.Category = ""
	}

	// Pick sound (mutates state).
	var soundFile string
	if route.Category != "" && !paused {
		soundFile = pickSound(peonDir, cfg.ActivePack, route.Category, ls.State)
	}

	// Save state and release lock.
	ls.saveStateUnlock(peonDir)

	// Set tab title.
	if route.Status != "" {
		title := fmt.Sprintf("%s%s: %s", route.Marker, project, route.Status)
		fmt.Printf("\033]0;%s\007", title)
	}

	// Update action bar state.
	if event.SessionID != "" && route.Status != "" {
		writeActionBarSession(peonDir, event.SessionID, project, route.Status, event.Message, targetHwnd)
	}

	// Play sound and/or notify.
	if !paused {
		if soundFile != "" && fileExists(soundFile) && route.Notify {
			playSoundAndNotify(soundFile, cfg.Volume, project, route.NotifyMsg, route.NotifyIcon, targetHwnd)
		} else if soundFile != "" && fileExists(soundFile) {
			playSound(soundFile, cfg.Volume)
		} else if route.Notify {
			sendNotification(project, route.NotifyMsg, route.NotifyIcon, targetHwnd)
		}
	}

	os.Exit(0)
}

// catEnabled checks if a sound category is enabled in config.
func catEnabled(cfg Config, category string) bool {
	enabled, ok := cfg.Categories[category]
	if !ok {
		return true // default to enabled if not specified
	}
	return enabled
}

// fileExists returns true if the path exists.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// sanitizeProject removes unsafe characters from the project name.
var projectSanitizer = regexp.MustCompile(`[^a-zA-Z0-9 ._-]`)

func sanitizeProject(name string) string {
	return strings.TrimSpace(projectSanitizer.ReplaceAllString(name, ""))
}
