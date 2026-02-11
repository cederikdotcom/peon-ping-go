//go:build linux

package main

import (
	"crypto/sha256"
	"encoding/hex"
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

// captureWindowHandle calls the helper synchronously to get the foreground HWND.
// Should be called on session_start when the terminal is focused.
func captureWindowHandle() uint64 {
	helper := findHelper()
	if helper == "" {
		return 0
	}
	out, err := exec.Command(helper, "hwnd").Output()
	if err != nil {
		return 0
	}
	var hwnd uint64
	fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &hwnd)
	return hwnd
}

// playSound plays a WAV file via the Windows helper.
func playSound(file string, volume float64) {
	wpath := toWindowsPath(file)
	detach("play", wpath, fmt.Sprintf("%g", volume))
}

// sendNotification shows a colored popup via the Windows helper.
func sendNotification(title, msg, icon string, hwnd uint64) {
	args := []string{"notify", title, msg, icon}
	if hwnd != 0 {
		args = append(args, fmt.Sprintf("%d", hwnd))
	}
	detach(args...)
}

// playSoundAndNotify plays sound + shows notification in a single helper process.
func playSoundAndNotify(file string, volume float64, title, msg, icon string, hwnd uint64) {
	args := []string{"both", toWindowsPath(file), fmt.Sprintf("%g", volume), title, msg, icon}
	if hwnd != 0 {
		args = append(args, fmt.Sprintf("%d", hwnd))
	}
	detach(args...)
}

// cachedHelperPath is resolved once per invocation.
var cachedHelperPath string

// findHelper locates peon-helper.exe next to the running binary.
// To work around WSL/Windows executable image caching, the helper is copied
// to /tmp with a content-hash filename so each new build gets a fresh image.
func findHelper() string {
	if cachedHelperPath != "" {
		return cachedHelperPath
	}

	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	src := filepath.Join(filepath.Dir(exe), "peon-helper.exe")
	data, err := os.ReadFile(src)
	if err != nil {
		return ""
	}

	// Short content hash for unique filename
	hash := sha256.Sum256(data)
	tag := hex.EncodeToString(hash[:4]) // 8 hex chars
	dst := filepath.Join(os.TempDir(), "peon-helper-"+tag+".exe")

	if _, err := os.Stat(dst); err != nil {
		os.WriteFile(dst, data, 0755)
	}

	cachedHelperPath = dst
	return dst
}

// toWindowsPath converts a WSL path to a Windows path via wslpath.
func toWindowsPath(linuxPath string) string {
	out, err := exec.Command("wslpath", "-w", linuxPath).Output()
	if err != nil {
		return linuxPath
	}
	return strings.TrimSpace(string(out))
}
