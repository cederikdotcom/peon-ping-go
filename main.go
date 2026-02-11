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

	// Detect harness and parse event.
	forceHarness := os.Getenv("PEON_HARNESS")
	adapter := detectAdapter(json.RawMessage(input), forceHarness)
	event, err := adapter.Parse(json.RawMessage(input))
	if err != nil || event.Type == "" {
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
		checkForUpdate(peonDir)
		showUpdateNotice(peonDir)
		if paused {
			fmt.Fprintf(os.Stderr, "peon-ping: sounds paused — run 'peon --resume' or '/peon-ping-toggle' to unpause\n")
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

	// Play sound and/or notify.
	if !paused {
		notifyMsg := ""
		if route.Notify {
			notifyMsg = fmt.Sprintf("%s  —  %s", project, route.NotifyMsg)
		}

		tabTitle := fmt.Sprintf("%s%s: %s", route.Marker, project, route.Status)

		if soundFile != "" && fileExists(soundFile) && route.Notify {
			// Combined sound + notification in one powershell call.
			playSoundAndNotify(soundFile, cfg.Volume, notifyMsg, tabTitle, route.NotifyColor)
		} else if soundFile != "" && fileExists(soundFile) {
			playSound(soundFile, cfg.Volume)
		} else if route.Notify {
			sendNotification(notifyMsg, tabTitle, route.NotifyColor)
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
