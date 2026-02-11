//go:build linux

package main

import (
	"fmt"
	"os/exec"
	"strings"
)

// playSound plays a WAV file via powershell.exe MediaPlayer (WSL).
// Fire-and-forget: starts the process and returns immediately.
func playSound(file string, volume float64) {
	wpath := toWindowsPath(file)
	// Convert backslashes to forward slashes for file:/// URI.
	wpath = strings.ReplaceAll(wpath, "\\", "/")

	ps := fmt.Sprintf(`
Add-Type -AssemblyName PresentationCore
$p = New-Object System.Windows.Media.MediaPlayer
$p.Open([Uri]::new('file:///%s'))
$p.Volume = %g
Start-Sleep -Milliseconds 200
$p.Play()
Start-Sleep -Seconds 3
$p.Close()
`, wpath, volume)

	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", ps)
	cmd.Start()
}

// sendNotification shows a colored popup on all Windows screens via powershell.exe.
// Fire-and-forget.
func sendNotification(msg, title, color string) {
	var r, g, b int
	switch color {
	case "blue":
		r, g, b = 30, 80, 180
	case "yellow":
		r, g, b = 200, 160, 0
	default: // red
		r, g, b = 180, 0, 0
	}

	ps := fmt.Sprintf(`
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
foreach ($screen in [System.Windows.Forms.Screen]::AllScreens) {
  $form = New-Object System.Windows.Forms.Form
  $form.FormBorderStyle = 'None'
  $form.BackColor = [System.Drawing.Color]::FromArgb(%d, %d, %d)
  $form.Size = New-Object System.Drawing.Size(500, 80)
  $form.TopMost = $true
  $form.ShowInTaskbar = $false
  $form.StartPosition = 'Manual'
  $form.Location = New-Object System.Drawing.Point(
    ($screen.WorkingArea.X + ($screen.WorkingArea.Width - 500) / 2),
    ($screen.WorkingArea.Y + 40)
  )
  $label = New-Object System.Windows.Forms.Label
  $label.Text = '%s'
  $label.ForeColor = [System.Drawing.Color]::White
  $label.Font = New-Object System.Drawing.Font('Segoe UI', 16, [System.Drawing.FontStyle]::Bold)
  $label.TextAlign = 'MiddleCenter'
  $label.Dock = 'Fill'
  $form.Controls.Add($label)
  $form.Show()
}
Start-Sleep -Seconds 4
[System.Windows.Forms.Application]::Exit()
`, r, g, b, escapePSString(msg))

	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", ps)
	cmd.Start()
}

// playSoundAndNotify combines sound + notification into a single powershell.exe invocation.
func playSoundAndNotify(file string, volume float64, msg, title, color string) {
	wpath := toWindowsPath(file)
	wpath = strings.ReplaceAll(wpath, "\\", "/")

	var r, g, b int
	switch color {
	case "blue":
		r, g, b = 30, 80, 180
	case "yellow":
		r, g, b = 200, 160, 0
	default:
		r, g, b = 180, 0, 0
	}

	ps := fmt.Sprintf(`
Add-Type -AssemblyName PresentationCore
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
$p = New-Object System.Windows.Media.MediaPlayer
$p.Open([Uri]::new('file:///%s'))
$p.Volume = %g
Start-Sleep -Milliseconds 200
$p.Play()
foreach ($screen in [System.Windows.Forms.Screen]::AllScreens) {
  $form = New-Object System.Windows.Forms.Form
  $form.FormBorderStyle = 'None'
  $form.BackColor = [System.Drawing.Color]::FromArgb(%d, %d, %d)
  $form.Size = New-Object System.Drawing.Size(500, 80)
  $form.TopMost = $true
  $form.ShowInTaskbar = $false
  $form.StartPosition = 'Manual'
  $form.Location = New-Object System.Drawing.Point(
    ($screen.WorkingArea.X + ($screen.WorkingArea.Width - 500) / 2),
    ($screen.WorkingArea.Y + 40)
  )
  $label = New-Object System.Windows.Forms.Label
  $label.Text = '%s'
  $label.ForeColor = [System.Drawing.Color]::White
  $label.Font = New-Object System.Drawing.Font('Segoe UI', 16, [System.Drawing.FontStyle]::Bold)
  $label.TextAlign = 'MiddleCenter'
  $label.Dock = 'Fill'
  $form.Controls.Add($label)
  $form.Show()
}
Start-Sleep -Seconds 4
$p.Close()
[System.Windows.Forms.Application]::Exit()
`, wpath, volume, r, g, b, escapePSString(msg))

	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", ps)
	cmd.Start()
}

// toWindowsPath converts a WSL path to a Windows path via wslpath.
func toWindowsPath(linuxPath string) string {
	out, err := exec.Command("wslpath", "-w", linuxPath).Output()
	if err != nil {
		return linuxPath
	}
	return strings.TrimSpace(string(out))
}

// escapePSString escapes single quotes for PowerShell string literals.
func escapePSString(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}
