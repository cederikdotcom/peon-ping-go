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

// actionBarRunning checks if a PeonActionBar window already exists.
func actionBarRunning() bool {
	helper := findHelper()
	if helper == "" {
		return false
	}
	out, err := exec.Command(helper, "actionbar-check").Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "1"
}

// dismissNotifications closes all hanging notification windows.
// Returns the number of helper processes terminated.
func dismissNotifications() int {
	helper := findHelper()
	if helper == "" {
		return 0
	}
	out, err := exec.Command(helper, "dismiss").Output()
	if err != nil {
		return 0
	}
	var n int
	fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &n)
	return n
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

// relaunchFromSource rebuilds peon + helper from the source repo, installs
// them to the hooks directory, and restarts the action bar.
func relaunchFromSource(peonDir string) {
	// Find the source repo (same dir as the running binary's source, or fallback).
	srcDir := os.Getenv("PEON_SRC")
	if srcDir == "" {
		home, _ := os.UserHomeDir()
		srcDir = filepath.Join(home, "workspaces", "peon-ping-go")
	}
	goPath := "/usr/local/go/bin/go"

	fmt.Printf("peon-ping: building from %s ...\n", srcDir)

	// Build Linux binary.
	cmd := exec.Command(goPath, "build", "-o", filepath.Join(peonDir, "peon"), ".")
	cmd.Dir = srcDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "peon-ping: build peon failed: %v\n", err)
		os.Exit(1)
	}

	// Build Windows helper.
	cmd = exec.Command(goPath, "build", "-ldflags", "-H windowsgui", "-o", filepath.Join(peonDir, "peon-helper.exe"), "./helper/")
	cmd.Dir = srcDir
	cmd.Env = append(os.Environ(), "GOOS=windows", "GOARCH=amd64")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "peon-ping: build helper failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("peon-ping: installed to", peonDir)

	// Restart action bar (the new helper kills the old one).
	cachedHelperPath = "" // clear cached path so findHelper picks up new binary
	abFile := toWindowsPath(actionBarPath(peonDir))
	detach("actionbar", abFile)
	fmt.Println("peon-ping: action bar restarted")
}

// toWindowsPath converts a WSL path to a Windows path via wslpath.
func toWindowsPath(linuxPath string) string {
	out, err := exec.Command("wslpath", "-w", linuxPath).Output()
	if err != nil {
		return linuxPath
	}
	return strings.TrimSpace(string(out))
}

// startupVBSPath returns the WSL path to the peon-ping.vbs startup script.
func startupVBSPath() string {
	// Get the Windows Startup folder via cmd.exe.
	out, err := exec.Command("cmd.exe", "/c", "echo", "%APPDATA%").Output()
	if err != nil {
		return ""
	}
	appdata := strings.TrimSpace(string(out))
	winPath := appdata + `\Microsoft\Windows\Start Menu\Programs\Startup\peon-ping.vbs`
	// Convert to WSL path for writing.
	wslOut, err := exec.Command("wslpath", "-u", winPath).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(wslOut))
}

// installStartupShortcut creates a VBS script in the Windows Startup folder
// that silently launches the action bar on login.
func installStartupShortcut(peonDir string) {
	helperWin := toWindowsPath(filepath.Join(peonDir, "peon-helper.exe"))
	stateWin := toWindowsPath(actionBarPath(peonDir))

	vbs := fmt.Sprintf(`Set WshShell = CreateObject("WScript.Shell")
WshShell.Run """%s"" actionbar ""%s""", 0, False
`, helperWin, stateWin)

	dest := startupVBSPath()
	if dest == "" {
		fmt.Fprintln(os.Stderr, "peon-ping: could not find Windows Startup folder")
		os.Exit(1)
	}

	if err := os.WriteFile(dest, []byte(vbs), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "peon-ping: failed to write startup script: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("peon-ping: installed startup script at %s\n", toWindowsPath(dest))
}

// uninstallStartupShortcut removes the VBS startup script.
func uninstallStartupShortcut() {
	dest := startupVBSPath()
	if dest == "" {
		fmt.Fprintln(os.Stderr, "peon-ping: could not find Windows Startup folder")
		os.Exit(1)
	}
	if err := os.Remove(dest); err != nil {
		if os.IsNotExist(err) {
			fmt.Println("peon-ping: no startup script found")
		} else {
			fmt.Fprintf(os.Stderr, "peon-ping: failed to remove: %v\n", err)
			os.Exit(1)
		}
		return
	}
	fmt.Println("peon-ping: startup script removed")
}
