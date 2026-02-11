//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// detach launches the Windows helper exe fully detached from the process tree.
// Uses Setsid + explicit /dev/null FDs so the hook runner doesn't wait for it.
func detach(args ...string) {
	helper := findHelper()
	if helper == "" {
		return
	}

	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return
	}
	defer devNull.Close()

	cmd := exec.Command(helper, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdin = devNull
	cmd.Stdout = devNull
	cmd.Stderr = devNull
	cmd.Start()
}

// playSound plays a WAV file via the Windows helper.
func playSound(file string, volume float64) {
	wpath := toWindowsPath(file)
	detach("play", wpath, fmt.Sprintf("%g", volume))
}

// sendNotification shows a colored popup via the Windows helper.
func sendNotification(msg, title, color string) {
	detach("notify", msg, color)
}

// playSoundAndNotify plays sound + shows notification in a single helper process.
func playSoundAndNotify(file string, volume float64, msg, title, color string) {
	wpath := toWindowsPath(file)
	detach("both", wpath, fmt.Sprintf("%g", volume), msg, color)
}

// findHelper locates peon-helper.exe next to the running binary.
func findHelper() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	helper := filepath.Join(filepath.Dir(exe), "peon-helper.exe")
	if _, err := os.Stat(helper); err != nil {
		return ""
	}
	return helper
}

// toWindowsPath converts a WSL path to a Windows path via wslpath.
func toWindowsPath(linuxPath string) string {
	out, err := exec.Command("wslpath", "-w", linuxPath).Output()
	if err != nil {
		return linuxPath
	}
	return strings.TrimSpace(string(out))
}
