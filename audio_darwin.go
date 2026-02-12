//go:build darwin

package main

import (
	"fmt"
	"os/exec"
)

// captureWindowHandle is a no-op on macOS (window targeting not needed).
func captureWindowHandle() uint64 { return 0 }

// playSound plays a WAV file via afplay (macOS).
// Fire-and-forget.
func playSound(file string, volume float64) {
	cmd := exec.Command("afplay", "-v", fmt.Sprintf("%g", volume), file)
	cmd.Start()
}

// sendNotification shows a macOS notification via osascript.
// Fire-and-forget. hwnd is ignored on macOS.
func sendNotification(title, msg, icon string, hwnd uint64) {
	script := fmt.Sprintf(`display notification %q with title %q`, msg, title)
	cmd := exec.Command("osascript", "-e", script)
	cmd.Start()
}

// playSoundAndNotify plays sound and sends notification separately on macOS
// (no benefit to combining since afplay and osascript are both fast).
func playSoundAndNotify(file string, volume float64, title, msg, icon string, hwnd uint64) {
	playSound(file, volume)
	sendNotification(title, msg, icon, hwnd)
}

// dismissNotifications is a no-op on macOS.
func dismissNotifications() int { return 0 }

// actionBarRunning is a no-op on macOS (action bar is Windows-only).
func actionBarRunning() bool { return false }

// toWindowsPath is a no-op on macOS.
func toWindowsPath(p string) string { return p }

// detach is a no-op on macOS.
func detach(args ...string) {}
