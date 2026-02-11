package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const updateRepo = "cederikdotcom/peon-ping-go"

// checkForUpdate checks GitHub for a newer version (once per day, non-blocking).
// Should be called only on session_start events.
func checkForUpdate(peonDir string) {
	go func() {
		checkFile := filepath.Join(peonDir, ".last_update_check")
		now := time.Now().Unix()

		// Read last check time.
		if data, err := os.ReadFile(checkFile); err == nil {
			if last, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64); err == nil {
				if now-last < 86400 {
					return
				}
			}
		}

		// Write current timestamp.
		os.WriteFile(checkFile, []byte(strconv.FormatInt(now, 10)), 0644)

		localVersion := ""
		if data, err := os.ReadFile(filepath.Join(peonDir, "VERSION")); err == nil {
			localVersion = strings.TrimSpace(string(data))
		}

		client := http.Client{Timeout: 5 * time.Second}
		resp, err := client.Get("https://raw.githubusercontent.com/" + updateRepo + "/main/VERSION")
		if err != nil {
			return
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return
		}
		remoteVersion := strings.TrimSpace(string(body))

		updateFile := filepath.Join(peonDir, ".update_available")
		if remoteVersion != "" && localVersion != "" && remoteVersion != localVersion {
			os.WriteFile(updateFile, []byte(remoteVersion), 0644)
		} else {
			os.Remove(updateFile)
		}
	}()
}

// showUpdateNotice prints an update notice if one is pending.
func showUpdateNotice(peonDir string) {
	updateFile := filepath.Join(peonDir, ".update_available")
	data, err := os.ReadFile(updateFile)
	if err != nil {
		return
	}
	newVer := strings.TrimSpace(string(data))
	if newVer == "" {
		return
	}
	curVer := ""
	if d, err := os.ReadFile(filepath.Join(peonDir, "VERSION")); err == nil {
		curVer = strings.TrimSpace(string(d))
	}
	if curVer == "" {
		curVer = "?"
	}
	fmt.Fprintf(os.Stderr, "peon-ping update available: %s → %s — run: curl -fsSL https://raw.githubusercontent.com/%s/main/install.sh | bash\n",
		curVer, newVer, updateRepo)
}
