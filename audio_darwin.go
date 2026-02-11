//go:build darwin

package main

import (
	"fmt"
	"os/exec"
)

// playSound plays a WAV file via afplay (macOS).
// Fire-and-forget.
func playSound(file string, volume float64) {
	cmd := exec.Command("afplay", "-v", fmt.Sprintf("%g", volume), file)
	cmd.Start()
}

// sendNotification shows a macOS notification via osascript.
// Fire-and-forget.
func sendNotification(msg, title, color string) {
	script := fmt.Sprintf(`display notification %q with title %q`, msg, title)
	cmd := exec.Command("osascript", "-e", script)
	cmd.Start()
}

// playSoundAndNotify plays sound and sends notification separately on macOS
// (no benefit to combining since afplay and osascript are both fast).
func playSoundAndNotify(file string, volume float64, msg, title, color string) {
	playSound(file, volume)
	sendNotification(msg, title, color)
}
