//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

// JSON types mirroring actionbar.go in the main package.
type abSessionJSON struct {
	Project               string          `json:"project"`
	State                 string          `json:"state"`
	Message               string          `json:"message"`
	HWND                  uint64          `json:"hwnd"`
	UpdatedAt             int64           `json:"updated_at"`
	ToolName              string          `json:"tool_name,omitempty"`
	ToolInput             json.RawMessage `json:"tool_input,omitempty"`
	PermissionSuggestions json.RawMessage `json:"permission_suggestions,omitempty"`
}

type abStateJSON struct {
	Sessions map[string]abSessionJSON `json:"sessions"`
}

// Permission response file (written by this action bar).
type abPermRsp struct {
	Behavior         string `json:"behavior"`
	ApplySuggestions bool   `json:"apply_suggestions,omitempty"`
}

// Rendering slot.
type abSlot struct {
	SessionID  string
	Project    string
	State      string
	Message    string
	HWND       uintptr
	HasPending bool   // session state is "needs approval" with tool details
	ToolName   string // from req file
	ToolDesc   string // Claude's description/reason
	ToolDetail string // command, file path, etc.
}

// Action bar globals (for WndProc callback).
var (
	abStateFile      string
	abPeonDir        string // directory containing state + req/rsp files
	abSlots          []abSlot
	abHwnd           uintptr
	abSelectedSlot   int    // -1 = no selection, 0+ = selected slot index
	abSelectedSessID string // session ID of selected slot (survives slot rebuilds)
	abPendingSlots   []abSlot // written by bg goroutine, read by main thread on WM_USER
	abBgRunning      int32    // atomic: 1 if bg goroutine is active
	abInputText      string   // current text input buffer for send message
	abInputActive    bool     // true when text input is visible (non-pending selected slot)
)

// Action bar dimensions.
const (
	abBarH      = 90
	abOptionsH  = 200 // height of options panel above the bar
	abOptionsW  = 600 // fixed width of the options panel
	abSlotW     = 80
	abIconSz    = 48
	abMaxSlots  = 7
	abMinW      = 200
	abFrameW    = 3 // gold frame thickness around pending icons
)

// WoW-style colors (BGR format).
const (
	colorSlotDimBg    = 0x000A0604  // very dark dimmed background
	colorSlotDimFrame = 0x00302820  // muted frame for working slots
	colorGoldRing     = 0x0030C8FF  // bright gold ring RGB(255,200,48)
	colorGoldRingOuter = 0x001090D0 // darker gold outer ring
	colorTextDim      = 0x00707060  // dimmed text for working slots
	colorOptionsBg    = 0x00180E08  // slightly lighter bg for options panel
	colorOptionsKey   = 0x0060DCFF  // gold for key labels
)

func init() {
	abSelectedSlot = -1
}

// abStateIcon maps route status to an embedded icon name.
func abStateIcon(state string) string {
	switch state {
	case "done":
		return "complete"
	case "needs approval", "has question":
		return "permission"
	default: // "working", "ready"
		return "idle"
	}
}

// abNeedsAttention returns true if the state requires user attention (gold ring).
func abNeedsAttention(state string) bool {
	return state == "needs approval" || state == "has question"
}

// abToolInfo extracts description and detail from tool_input JSON.
func abToolInfo(toolName string, raw json.RawMessage) (desc, detail string) {
	if len(raw) == 0 {
		return "", ""
	}
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", ""
	}

	// Extract description (Claude's reason for the action).
	if v, ok := m["description"]; ok {
		desc = fmt.Sprintf("%v", v)
	}

	// Extract the primary detail field.
	switch toolName {
	case "Bash":
		if v, ok := m["command"]; ok {
			detail = fmt.Sprintf("%v", v)
		}
	case "Edit":
		if v, ok := m["file_path"]; ok {
			detail = fmt.Sprintf("%v", v)
		}
		if v, ok := m["old_string"]; ok {
			s := fmt.Sprintf("%v", v)
			if len(s) > 120 {
				s = s[:117] + "..."
			}
			detail += "\n" + s
		}
	case "ExitPlanMode":
		if prompts, ok := m["allowedPrompts"]; ok {
			if arr, ok := prompts.([]interface{}); ok {
				var parts []string
				for _, p := range arr {
					if pm, ok := p.(map[string]interface{}); ok {
						if pr, ok := pm["prompt"]; ok {
							parts = append(parts, fmt.Sprintf("%v", pr))
						}
					}
				}
				detail = "Prompts: " + strings.Join(parts, ", ")
			}
		}
	default:
		for _, key := range []string{"command", "file_path", "pattern", "url", "query", "skill"} {
			if v, ok := m[key]; ok {
				detail = fmt.Sprintf("%v", v)
				break
			}
		}
	}
	return desc, detail
}

// abTriggerRead kicks off a background goroutine to read state files.
// The goroutine posts WM_USER to the window when done.
func abTriggerRead() {
	if !atomic.CompareAndSwapInt32(&abBgRunning, 0, 1) {
		return // already running
	}
	go func() {
		defer atomic.StoreInt32(&abBgRunning, 0)
		slots := abDoRead()
		abPendingSlots = slots
		if abHwnd != 0 {
			postMessageW.Call(abHwnd, WM_USER, 0, 0)
		}
	}()
}

// abDoRead performs all file I/O (runs in background goroutine).
// Returns nil on failure so abApplyPending knows to keep current state.
func abDoRead() []abSlot {
	data, err := os.ReadFile(abStateFile)
	if err != nil {
		return nil
	}

	var state abStateJSON
	if err := json.Unmarshal(data, &state); err != nil {
		return nil
	}

	type kv struct {
		id string
		s  abSessionJSON
	}
	now := time.Now().Unix()
	var items []kv
	for id, s := range state.Sessions {
		if now-s.UpdatedAt > 600 { // 10 min safety net
			continue
		}
		items = append(items, kv{id, s})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].id < items[j].id
	})

	slots := make([]abSlot, 0, len(items))
	for _, item := range items {
		if len(slots) >= abMaxSlots {
			break
		}
		slot := abSlot{
			SessionID: item.id,
			Project:   item.s.Project,
			State:     item.s.State,
			Message:   item.s.Message,
			HWND:      uintptr(item.s.HWND),
		}

		// For "needs approval" state, check heartbeat to see if the hook process
		// is still alive. If stale/missing, the permission was handled in-terminal
		// — override the state to "working" visually.
		if item.s.State == "needs approval" {
			hbPath := filepath.Join(abPeonDir, ".actionbar-hb-"+item.id)
			hbFresh := false
			if info, err := os.Stat(hbPath); err == nil {
				hbFresh = time.Since(info.ModTime()) < 3*time.Second
			}
			if hbFresh && item.s.ToolName != "" {
				slot.HasPending = true
				slot.ToolName = item.s.ToolName
				slot.ToolDesc, slot.ToolDetail = abToolInfo(item.s.ToolName, item.s.ToolInput)
			} else if !hbFresh {
				// Hook process is dead — permission was handled in-terminal.
				slot.State = "working"
			}
		}

		slots = append(slots, slot)
	}

	return slots
}

// abSlotsEqual checks if two slot slices are equivalent (to avoid needless repaints).
func abSlotsEqual(a, b []abSlot) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].SessionID != b[i].SessionID || a[i].State != b[i].State ||
			a[i].Message != b[i].Message || a[i].HasPending != b[i].HasPending ||
			a[i].Project != b[i].Project || a[i].ToolName != b[i].ToolName {
			return false
		}
	}
	return true
}

// abApplyPending applies the result from the background read to the main thread state.
func abApplyPending() {
	if abPendingSlots == nil {
		return
	}
	newSlots := abPendingSlots
	abPendingSlots = nil

	// Skip repaint if nothing changed.
	if abSlotsEqual(abSlots, newSlots) {
		return
	}

	abSlots = newSlots

	// Restore selection by session ID.
	abSelectedSlot = -1
	if abSelectedSessID != "" {
		for i, s := range abSlots {
			if s.SessionID == abSelectedSessID {
				abSelectedSlot = i
				break
			}
		}
		if abSelectedSlot < 0 {
			abSelectedSessID = ""
			abInputText = ""
			abInputActive = false
		} else {
			// Update input active state based on whether slot has pending permission.
			abInputActive = !abSlots[abSelectedSlot].HasPending
		}
	}

	abResizeWindow()
	if abHwnd != 0 {
		invalidateRectProc.Call(abHwnd, 0, 1)
	}
}

// primaryMonitor returns the monitor containing (0,0), which is the
// Windows primary monitor. Falls back to first monitor.
func primaryMonitor() RECT {
	monitors := getMonitors()
	for _, mon := range monitors {
		if mon.Left <= 0 && mon.Right > 0 && mon.Top <= 0 && mon.Bottom > 0 {
			return mon
		}
	}
	if len(monitors) > 0 {
		return monitors[0]
	}
	return RECT{0, 0, 1920, 1080}
}

func abBarWidth() int {
	// Slots + "+" button + borders. Never changes with panel state.
	w := (len(abSlots)+1)*abSlotW + 2*borderW
	if w < abMinW {
		w = abMinW
	}
	return w
}

func abWindowWidth() int {
	barW := abBarWidth()
	if abSelectedSlot >= 0 && abOptionsW > barW {
		return abOptionsW
	}
	return barW
}

func abTotalHeight() int {
	if abSelectedSlot >= 0 {
		return abBarH + abOptionsH
	}
	return abBarH
}

func abResizeWindow() {
	if abHwnd == 0 {
		return
	}
	mon := primaryMonitor()
	winW := abWindowWidth()
	totalH := abTotalHeight()
	screenW := int(mon.Right - mon.Left)
	x := int(mon.Left) + (screenW-winW)/2
	y := int(mon.Bottom) - totalH - 50
	moveWindowProc.Call(abHwnd, uintptr(x), uintptr(y), uintptr(winW), uintptr(totalH), 1)
}

// killExistingActionBar terminates any previously running action bar process.
func killExistingActionBar() {
	className, _ := syscall.UTF16PtrFromString("PeonActionBar")
	myPID := uint32(os.Getpid())
	var prev uintptr
	for {
		found, _, _ := findWindowExW.Call(0, prev, uintptr(unsafe.Pointer(className)), 0)
		if found == 0 {
			break
		}
		var pid uint32
		getWindowThreadProcessId.Call(found, uintptr(unsafe.Pointer(&pid)))
		if pid != 0 && pid != myPID {
			h, _, _ := openProcess.Call(PROCESS_TERMINATE, 0, uintptr(pid))
			if h != 0 {
				terminateProcess.Call(h, 0)
				closeHandleProc.Call(h)
			}
		}
		prev = found
	}
}

// abLaunchNewSession opens a new Windows Terminal tab with claude.
func abLaunchNewSession() {
	go func() {
		verb, _ := syscall.UTF16PtrFromString("open")
		exe, _ := syscall.UTF16PtrFromString("wt.exe")
		params, _ := syscall.UTF16PtrFromString("new-tab wsl.exe -- claude")
		shellExecuteW.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(exe)), uintptr(unsafe.Pointer(params)), 0, 1)
	}()
}

// abSendMessage copies text to clipboard, focuses the terminal window, pastes, and submits.
// Runs in a goroutine to avoid blocking the message loop.
func abSendMessage(text string, targetHwnd uintptr) {
	// Convert text to UTF-16 for clipboard.
	utf16Text, _ := syscall.UTF16FromString(text)
	byteSize := len(utf16Text) * 2

	// Copy to clipboard.
	openClipboardProc.Call(abHwnd)
	emptyClipboardProc.Call()
	hMem, _, _ := globalAllocProc.Call(GMEM_MOVEABLE, uintptr(byteSize))
	if hMem != 0 {
		ptr, _, _ := globalLockProc.Call(hMem)
		if ptr != 0 {
			for i, c := range utf16Text {
				*(*uint16)(unsafe.Pointer(ptr + uintptr(i*2))) = c
			}
			globalUnlockProc.Call(hMem)
			setClipboardDataProc.Call(CF_UNICODETEXT, hMem)
		}
	}
	closeClipboardProc.Call()

	// Focus the terminal window.
	if targetHwnd != 0 {
		setForegroundWindowProc.Call(targetHwnd)
	}
	time.Sleep(50 * time.Millisecond)

	// Ctrl+V to paste.
	keybdEventProc.Call(VK_CONTROL, 0, 0, 0)
	keybdEventProc.Call(VK_V, 0, 0, 0)
	keybdEventProc.Call(VK_V, 0, KEYEVENTF_KEYUP, 0)
	keybdEventProc.Call(VK_CONTROL, 0, KEYEVENTF_KEYUP, 0)
	time.Sleep(50 * time.Millisecond)

	// Enter to submit.
	keybdEventProc.Call(VK_RETURN, 0, 0, 0)
	keybdEventProc.Call(VK_RETURN, 0, KEYEVENTF_KEYUP, 0)

	// Re-topmost the action bar.
	time.Sleep(100 * time.Millisecond)
	if abHwnd != 0 {
		setWindowPosProc.Call(abHwnd, ^uintptr(0), 0, 0, 0, 0, SWP_NOMOVE|SWP_NOSIZE)
	}
}

// abWriteResponse writes a permission response file and deselects the slot.
func abWriteResponse(behavior string, applySuggestions bool) {
	if abSelectedSlot < 0 || abSelectedSlot >= len(abSlots) {
		return
	}
	slot := abSlots[abSelectedSlot]
	if slot.State != "needs approval" {
		return
	}

	rsp := abPermRsp{
		Behavior:         behavior,
		ApplySuggestions: applySuggestions,
	}
	data, err := json.Marshal(rsp)
	if err != nil {
		return
	}

	rspPath := filepath.Join(abPeonDir, ".actionbar-rsp-"+slot.SessionID+".json")
	targetHwnd := slot.HWND

	// Deselect and mark slot as no longer pending (immediate visual feedback).
	abSlots[abSelectedSlot].HasPending = false
	abSelectedSlot = -1
	abSelectedSessID = ""
	abInputText = ""
	abInputActive = false
	abResizeWindow()
	invalidateRectProc.Call(abHwnd, 0, 1)

	// Remove heartbeat so the next poll doesn't restore HasPending.
	hbPath := filepath.Join(abPeonDir, ".actionbar-hb-"+slot.SessionID)
	os.Remove(hbPath)

	// Write response file and focus terminal in background.
	// The write must complete before focusing so the hook process picks it up.
	go func() {
		if err := os.WriteFile(rspPath, data, 0644); err != nil {
			time.Sleep(100 * time.Millisecond)
			os.WriteFile(rspPath, data, 0644)
		}
		time.Sleep(50 * time.Millisecond)
		if targetHwnd != 0 {
			setForegroundWindowProc.Call(targetHwnd)
		}
	}()
}

func runActionBar(stateFile string) {
	// CRITICAL: Lock the main goroutine to its OS thread. Win32 windows
	// and message loops are thread-affine; without this, Go's scheduler
	// can move us to a different thread and the message pump stops working.
	runtime.LockOSThread()

	abStateFile = stateFile
	abPeonDir = filepath.Dir(stateFile)
	killExistingActionBar()

	// Initial read (blocking, before window is created).
	abSlots = abDoRead()

	className, _ := syscall.UTF16PtrFromString("PeonActionBar")
	hInst, _, _ := getModuleHandleW.Call(0)

	cursor, _, _ := loadCursorW.Call(0, IDC_ARROW)
	wc := WNDCLASSEX{
		Size:      uint32(unsafe.Sizeof(WNDCLASSEX{})),
		Style:     CS_HREDRAW | CS_VREDRAW,
		WndProc:   syscall.NewCallback(abWndProc),
		Instance:  hInst,
		Cursor:    cursor,
		ClassName: className,
	}
	registerClassExW.Call(uintptr(unsafe.Pointer(&wc)))

	// Position at bottom center of primary monitor.
	mon := primaryMonitor()
	winW := abWindowWidth()
	totalH := abTotalHeight()
	screenW := int(mon.Right - mon.Left)
	x := int(mon.Left) + (screenW-winW)/2
	y := int(mon.Bottom) - totalH - 50

	hwnd, _, _ := createWindowExW.Call(
		WS_EX_TOPMOST|WS_EX_TOOLWINDOW,
		uintptr(unsafe.Pointer(className)),
		0,
		WS_POPUP|WS_VISIBLE,
		uintptr(x), uintptr(y), uintptr(winW), uintptr(totalH),
		0, 0, hInst, 0,
	)
	abHwnd = hwnd

	// Poll every 500ms for responsive state updates.
	setTimerProc.Call(hwnd, 1, 500, 0)

	var m MSG
	for {
		ret, _, _ := getMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if ret == 0 || int32(ret) == -1 {
			break
		}
		translateMessage.Call(uintptr(unsafe.Pointer(&m)))
		dispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func abWndProc(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case WM_TIMER:
		abTriggerRead()
		return 0

	case WM_USER:
		abApplyPending()
		return 0

	case WM_PAINT:
		var ps PAINTSTRUCT
		hdc, _, _ := beginPaintProc.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		// Double buffer: paint to off-screen bitmap, then blit.
		w := int32(abWindowWidth())
		h := int32(abTotalHeight())
		memDC, _, _ := createCompatibleDC.Call(hdc)
		memBmp, _, _ := createCompatibleBitmap.Call(hdc, uintptr(w), uintptr(h))
		oldBmp, _, _ := selectObjectProc.Call(memDC, memBmp)
		abPaint(memDC)
		bitBltProc.Call(hdc, 0, 0, uintptr(w), uintptr(h), memDC, 0, 0, SRCCOPY)
		selectObjectProc.Call(memDC, oldBmp)
		deleteObjectProc.Call(memBmp)
		deleteDCProc.Call(memDC)
		endPaintProc.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		return 0

	case WM_LBUTTONDOWN:
		x := int(int16(lParam & 0xFFFF))
		y := int(int16((lParam >> 16) & 0xFFFF))
		totalH := abTotalHeight()
		barTop := totalH - abBarH

		if y >= barTop {
			// Click in slot area (bar is centered within window).
			winW := abWindowWidth()
			barW := abBarWidth()
			barLeft := (winW - barW) / 2
			localX := x - barLeft - borderW
			if localX >= 0 {
				slotIdx := localX / abSlotW
				if slotIdx == len(abSlots) {
					// "+" button clicked — launch new session.
					abLaunchNewSession()
				} else if slotIdx >= 0 && slotIdx < len(abSlots) {
					// Toggle detail panel for any slot.
					if abSelectedSlot == slotIdx {
						abSelectedSlot = -1
						abSelectedSessID = ""
						abInputText = ""
						abInputActive = false
					} else {
						abSelectedSlot = slotIdx
						abSelectedSessID = abSlots[slotIdx].SessionID
						abInputText = ""
						abInputActive = !abSlots[slotIdx].HasPending
					}
					abResizeWindow()
					invalidateRectProc.Call(abHwnd, 0, 1)
					if abSelectedSlot >= 0 {
						setForegroundWindowProc.Call(abHwnd)
						setFocusProc.Call(abHwnd)
					}
				}
			}
		}
		// Click in options area: no-op (keyboard only for actions).
		return 0

	case WM_KEYDOWN:
		if abSelectedSlot >= 0 {
			slot := abSlots[abSelectedSlot]
			if slot.State == "needs approval" {
				switch wParam {
				case VK_1:
					abWriteResponse("allow", false)
				case VK_2:
					abWriteResponse("allow", true)
				case VK_3:
					abWriteResponse("deny", false)
				}
			}
			if wParam == VK_ESCAPE {
				abSelectedSlot = -1
				abSelectedSessID = ""
				abInputText = ""
				abInputActive = false
				abResizeWindow()
				invalidateRectProc.Call(abHwnd, 0, 1)
			}
		}
		return 0

	case WM_CHAR:
		if abInputActive && abSelectedSlot >= 0 {
			ch := rune(wParam)
			switch {
			case ch == 0x0D: // Enter — send message
				if abInputText != "" {
					slot := abSlots[abSelectedSlot]
					text := abInputText
					targetHwnd := slot.HWND
					abInputText = ""
					abInputActive = false
					abSelectedSlot = -1
					abSelectedSessID = ""
					abResizeWindow()
					invalidateRectProc.Call(abHwnd, 0, 1)
					go abSendMessage(text, targetHwnd)
				}
			case ch == 0x08: // Backspace
				if len(abInputText) > 0 {
					// Remove last rune.
					runes := []rune(abInputText)
					abInputText = string(runes[:len(runes)-1])
					invalidateRectProc.Call(abHwnd, 0, 1)
				}
			case ch >= 0x20: // Printable character
				abInputText += string(ch)
				invalidateRectProc.Call(abHwnd, 0, 1)
			}
		}
		return 0

	case WM_RBUTTONDOWN:
		destroyWindowProc.Call(hwnd)
		return 0

	case WM_DESTROY:
		postQuitMessage.Call(0)
		return 0
	}

	ret, _, _ := defWindowProcW.Call(hwnd, uintptr(msg), wParam, lParam)
	return ret
}

func abPaint(hdc uintptr) {
	winW := int32(abWindowWidth())
	barW := int32(abBarWidth())
	totalH := int32(abTotalHeight())
	barTop := totalH - int32(abBarH)

	// Full background fill (transparent black for the whole window).
	fillRect(hdc, RECT{0, 0, winW, totalH}, colorBgDark)

	// --- Options panel (if a slot is selected) ---
	if abSelectedSlot >= 0 && abSelectedSlot < len(abSlots) {
		abPaintOptions(hdc, winW, barTop)
	}

	// --- Bar area (centered within the window) ---
	barLeft := (winW - barW) / 2
	barRight := barLeft + barW
	barBottom := totalH

	// Beveled gold border around the bar.
	fillRect(hdc, RECT{barLeft, barTop, barRight, barTop + borderW}, colorBorderLight)
	fillRect(hdc, RECT{barLeft, barTop, barLeft + borderW, barBottom}, colorBorderLight)
	fillRect(hdc, RECT{barLeft, barBottom - borderW, barRight, barBottom}, colorBorderShadow)
	fillRect(hdc, RECT{barRight - borderW, barTop, barRight, barBottom}, colorBorderShadow)

	// Inner highlight.
	drawLine(hdc, barLeft+borderW, barTop+borderW, barRight-borderW, barTop+borderW, colorBorderGold)
	drawLine(hdc, barLeft+borderW, barTop+borderW, barLeft+borderW, barBottom-borderW, colorBorderGold)
	drawLine(hdc, barLeft+borderW, barBottom-borderW-1, barRight-borderW, barBottom-borderW-1, colorBorderShadow)
	drawLine(hdc, barRight-borderW-1, barTop+borderW, barRight-borderW-1, barBottom-borderW, colorBorderShadow)

	// Render session slots.
	setBkModeProc.Call(hdc, TRANSPARENT)
	fontName, _ := syscall.UTF16PtrFromString("Segoe UI")

	for i, slot := range abSlots {
		sx := barLeft + int32(borderW+i*abSlotW)
		slotTop := barTop + borderW + 1

		isSelected := i == abSelectedSlot

		attention := slot.HasPending || abNeedsAttention(slot.State)
		if attention {
			// Gold ring — action needed.
			ringColor := uintptr(colorGoldRing)
			if isSelected {
				ringColor = colorBorderLight
			}
			fillRect(hdc, RECT{sx, slotTop, sx + int32(abSlotW), slotTop + abFrameW}, ringColor)
			fillRect(hdc, RECT{sx, slotTop, sx + abFrameW, barBottom - borderW}, ringColor)
			fillRect(hdc, RECT{sx, barBottom - borderW - abFrameW, sx + int32(abSlotW), barBottom - borderW}, ringColor)
			fillRect(hdc, RECT{sx + int32(abSlotW) - abFrameW, slotTop, sx + int32(abSlotW), barBottom - borderW}, ringColor)
			drawLine(hdc, sx+abFrameW, slotTop+abFrameW, sx+int32(abSlotW)-abFrameW, slotTop+abFrameW, colorGoldRingOuter)
		} else {
			// Dimmed/idle.
			fillRect(hdc, RECT{sx, slotTop, sx + int32(abSlotW), barBottom - borderW}, colorSlotDimBg)
		}

		// Icon (48x48, centered in slot).
		// Working = night elf, idle/pending = peon.
		iconX := int(sx) + (abSlotW-abIconSz)/2
		iconY := int(slotTop) + 10
		iconKey := "idle"
		if slot.State == "working" {
			iconKey = "complete"
		}
		if img, ok := gdipImages[iconKey]; ok {
			drawIcon(hdc, img, iconX, iconY, abIconSz)
		}

		// Slot number in top-left corner.
		numSize := int32(-11)
		numFont, _, _ := createFontW.Call(
			uintptr(numSize),
			0, 0, 0,
			FW_BOLD, 0, 0, 0,
			DEFAULT_CHARSET, 0, 0, 0, 0,
			uintptr(unsafe.Pointer(fontName)),
		)
		oldFont, _, _ := selectObjectProc.Call(hdc, numFont)
		if attention {
			setTextColorProc.Call(hdc, colorTextGold)
		} else {
			setTextColorProc.Call(hdc, colorTextDim)
		}
		numStr, _ := syscall.UTF16PtrFromString(fmt.Sprintf("%d", i+1))
		numRC := RECT{sx + abFrameW + 2, slotTop + abFrameW + 1, sx + 20, slotTop + 16}
		drawTextProc.Call(hdc, uintptr(unsafe.Pointer(numStr)), ^uintptr(0), uintptr(unsafe.Pointer(&numRC)), DT_LEFT|DT_SINGLELINE)
		selectObjectProc.Call(hdc, oldFont)
		deleteObjectProc.Call(numFont)

		// Project name below icon.
		fontSize := int32(-12)
		textFont, _, _ := createFontW.Call(
			uintptr(fontSize),
			0, 0, 0,
			0, 0, 0, 0,
			DEFAULT_CHARSET, 0, 0, 0, 0,
			uintptr(unsafe.Pointer(fontName)),
		)
		oldFont2, _, _ := selectObjectProc.Call(hdc, textFont)
		if attention {
			setTextColorProc.Call(hdc, colorTextGold)
		} else {
			setTextColorProc.Call(hdc, colorTextDim)
		}

		text, _ := syscall.UTF16PtrFromString(slot.Project)
		textRC := RECT{sx + 2, int32(iconY + abIconSz + 2), sx + int32(abSlotW) - 2, barBottom - borderW}
		drawTextProc.Call(
			hdc,
			uintptr(unsafe.Pointer(text)),
			^uintptr(0),
			uintptr(unsafe.Pointer(&textRC)),
			DT_CENTER|DT_SINGLELINE|DT_END_ELLIPSIS,
		)
		selectObjectProc.Call(hdc, oldFont2)
		deleteObjectProc.Call(textFont)

		// Slot separator.
		if i > 0 {
			drawLine(hdc, sx, slotTop, sx, barBottom-borderW, colorBorderShadow)
		}
	}

	// "+" button after the last slot.
	{
		plusX := barLeft + int32(borderW+len(abSlots)*abSlotW)
		slotTop := barTop + borderW + 1

		// Separator before "+".
		if len(abSlots) > 0 {
			drawLine(hdc, plusX, slotTop, plusX, barBottom-borderW, colorBorderShadow)
		}

		// Draw "+" text centered in the slot.
		plusSize := int32(-28)
		plusFont, _, _ := createFontW.Call(
			uintptr(plusSize),
			0, 0, 0,
			0, 0, 0, 0,
			DEFAULT_CHARSET, 0, 0, 0, 0,
			uintptr(unsafe.Pointer(fontName)),
		)
		oldPlusFont, _, _ := selectObjectProc.Call(hdc, plusFont)
		setTextColorProc.Call(hdc, colorBorderGold)

		plusStr, _ := syscall.UTF16PtrFromString("+")
		plusRC := RECT{plusX, slotTop, plusX + int32(abSlotW), barBottom - borderW}
		drawTextProc.Call(hdc, uintptr(unsafe.Pointer(plusStr)), ^uintptr(0), uintptr(unsafe.Pointer(&plusRC)), DT_CENTER|DT_VCENTER|DT_SINGLELINE)
		selectObjectProc.Call(hdc, oldPlusFont)
		deleteObjectProc.Call(plusFont)
	}
}

// abPaintOptions renders the options panel above the bar.
func abPaintOptions(hdc uintptr, w, barTop int32) {
	slot := abSlots[abSelectedSlot]
	optTop := int32(0)
	optBottom := barTop

	// Options background (slightly different from bar).
	fillRect(hdc, RECT{0, optTop, w, optBottom}, colorOptionsBg)

	// Border around options panel.
	fillRect(hdc, RECT{0, optTop, w, optTop + borderW}, colorBorderLight)
	fillRect(hdc, RECT{0, optTop, borderW, optBottom}, colorBorderLight)
	fillRect(hdc, RECT{w - borderW, optTop, w, optBottom}, colorBorderShadow)

	// Inner highlight.
	drawLine(hdc, borderW, optTop+borderW, w-borderW, optTop+borderW, colorBorderGold)

	setBkModeProc.Call(hdc, TRANSPARENT)
	fontName, _ := syscall.UTF16PtrFromString("Segoe UI")
	monoName, _ := syscall.UTF16PtrFromString("Consolas")
	pad := int32(borderW + 8)
	y := optTop + borderW + 6

	if slot.HasPending {
		// --- Permission request detail ---

		// Line 1: Project + Tool name (gold, bold).
		headerText := slot.Project + " — " + slot.ToolName
		headerSize := int32(-16)
		headerFont, _, _ := createFontW.Call(
			uintptr(headerSize), 0, 0, 0, FW_BOLD, 0, 0, 0,
			DEFAULT_CHARSET, 0, 0, 0, 0,
			uintptr(unsafe.Pointer(fontName)),
		)
		oldFont, _, _ := selectObjectProc.Call(hdc, headerFont)
		setTextColorProc.Call(hdc, colorTextGold)
		headerStr, _ := syscall.UTF16PtrFromString(headerText)
		headerRC := RECT{pad, y, w - pad, y + 22}
		drawTextProc.Call(hdc, uintptr(unsafe.Pointer(headerStr)), ^uintptr(0), uintptr(unsafe.Pointer(&headerRC)), DT_LEFT|DT_SINGLELINE|DT_END_ELLIPSIS)
		selectObjectProc.Call(hdc, oldFont)
		deleteObjectProc.Call(headerFont)
		y += 24

		// Line 2: Description (white, normal) — Claude's reason.
		if slot.ToolDesc != "" {
			descSize := int32(-14)
			descFont, _, _ := createFontW.Call(
				uintptr(descSize), 0, 0, 0, 0, 0, 0, 0,
				DEFAULT_CHARSET, 0, 0, 0, 0,
				uintptr(unsafe.Pointer(fontName)),
			)
			oldDescFont, _, _ := selectObjectProc.Call(hdc, descFont)
			setTextColorProc.Call(hdc, colorTextWhite)
			descStr, _ := syscall.UTF16PtrFromString(slot.ToolDesc)
			descRC := RECT{pad, y, w - pad, y + 40}
			drawTextProc.Call(hdc, uintptr(unsafe.Pointer(descStr)), ^uintptr(0), uintptr(unsafe.Pointer(&descRC)), DT_LEFT|DT_WORDBREAK|DT_END_ELLIPSIS)
			selectObjectProc.Call(hdc, oldDescFont)
			deleteObjectProc.Call(descFont)
			y += 22
		}

		// Separator line between description and detail.
		if slot.ToolDetail != "" {
			drawLine(hdc, pad, y, w-pad, y, colorBorderShadow)
			y += 4
		}

		// Lines 3+: Tool detail (monospace, dimmer, with word wrap).
		if slot.ToolDetail != "" {
			detailSize := int32(-13)
			detailFont, _, _ := createFontW.Call(
				uintptr(detailSize), 0, 0, 0, 0, 0, 0, 0,
				DEFAULT_CHARSET, 0, 0, 0, 0,
				uintptr(unsafe.Pointer(monoName)),
			)
			oldDetailFont, _, _ := selectObjectProc.Call(hdc, detailFont)
			setTextColorProc.Call(hdc, 0x00B0B0A0) // light gray-green
			detailStr, _ := syscall.UTF16PtrFromString(slot.ToolDetail)
			detailRC := RECT{pad, y, w - pad, optBottom - 30}
			drawTextProc.Call(hdc, uintptr(unsafe.Pointer(detailStr)), ^uintptr(0), uintptr(unsafe.Pointer(&detailRC)), DT_LEFT|DT_WORDBREAK|DT_END_ELLIPSIS)
			selectObjectProc.Call(hdc, oldDetailFont)
			deleteObjectProc.Call(detailFont)
		}

		// Action buttons line at the bottom of the options panel (gold text).
		actionsY := optBottom - 26
		optSize := int32(-14)
		optFont, _, _ := createFontW.Call(
			uintptr(optSize), 0, 0, 0, FW_BOLD, 0, 0, 0,
			DEFAULT_CHARSET, 0, 0, 0, 0,
			uintptr(unsafe.Pointer(fontName)),
		)
		oldOptFont, _, _ := selectObjectProc.Call(hdc, optFont)
		setTextColorProc.Call(hdc, colorOptionsKey)
		actionsStr, _ := syscall.UTF16PtrFromString("[1] Allow    [2] Always Allow    [3] Deny")
		actRC := RECT{pad, actionsY, w - pad, actionsY + 20}
		drawTextProc.Call(hdc, uintptr(unsafe.Pointer(actionsStr)), ^uintptr(0), uintptr(unsafe.Pointer(&actRC)), DT_LEFT|DT_SINGLELINE)
		selectObjectProc.Call(hdc, oldOptFont)
		deleteObjectProc.Call(optFont)
	} else {
		// --- Session info (no pending permission) ---

		// Reserve space for the text input at the bottom.
		inputH := int32(30)
		inputPadY := int32(6)
		contentBottom := optBottom - inputH - inputPadY*2

		// Line 1: Project name (gold, bold).
		headerSize := int32(-18)
		headerFont, _, _ := createFontW.Call(
			uintptr(headerSize), 0, 0, 0, FW_BOLD, 0, 0, 0,
			DEFAULT_CHARSET, 0, 0, 0, 0,
			uintptr(unsafe.Pointer(fontName)),
		)
		oldFont, _, _ := selectObjectProc.Call(hdc, headerFont)
		setTextColorProc.Call(hdc, colorTextGold)
		headerStr, _ := syscall.UTF16PtrFromString(slot.Project)
		headerRC := RECT{pad, y, w - pad, y + 24}
		drawTextProc.Call(hdc, uintptr(unsafe.Pointer(headerStr)), ^uintptr(0), uintptr(unsafe.Pointer(&headerRC)), DT_LEFT|DT_SINGLELINE|DT_END_ELLIPSIS)
		selectObjectProc.Call(hdc, oldFont)
		deleteObjectProc.Call(headerFont)
		y += 28

		// Line 2: Status (white).
		stateText := "Status: " + slot.State
		stateSize := int32(-15)
		stateFont, _, _ := createFontW.Call(
			uintptr(stateSize), 0, 0, 0, 0, 0, 0, 0,
			DEFAULT_CHARSET, 0, 0, 0, 0,
			uintptr(unsafe.Pointer(fontName)),
		)
		oldStateFont, _, _ := selectObjectProc.Call(hdc, stateFont)
		setTextColorProc.Call(hdc, colorTextWhite)
		stateStr, _ := syscall.UTF16PtrFromString(stateText)
		stateRC := RECT{pad, y, w - pad, y + 20}
		drawTextProc.Call(hdc, uintptr(unsafe.Pointer(stateStr)), ^uintptr(0), uintptr(unsafe.Pointer(&stateRC)), DT_LEFT|DT_SINGLELINE)
		selectObjectProc.Call(hdc, oldStateFont)
		deleteObjectProc.Call(stateFont)
		y += 24

		// Lines 3+: Last message (dimmer, with word wrap).
		if slot.Message != "" {
			drawLine(hdc, pad, y, w-pad, y, colorBorderShadow)
			y += 4

			msgSize := int32(-13)
			msgFont, _, _ := createFontW.Call(
				uintptr(msgSize), 0, 0, 0, 0, 0, 0, 0,
				DEFAULT_CHARSET, 0, 0, 0, 0,
				uintptr(unsafe.Pointer(monoName)),
			)
			oldMsgFont, _, _ := selectObjectProc.Call(hdc, msgFont)
			setTextColorProc.Call(hdc, 0x00B0B0A0) // light gray-green
			msgStr, _ := syscall.UTF16PtrFromString(slot.Message)
			msgRC := RECT{pad, y, w - pad, contentBottom}
			drawTextProc.Call(hdc, uintptr(unsafe.Pointer(msgStr)), ^uintptr(0), uintptr(unsafe.Pointer(&msgRC)), DT_LEFT|DT_WORDBREAK|DT_END_ELLIPSIS)
			selectObjectProc.Call(hdc, oldMsgFont)
			deleteObjectProc.Call(msgFont)
		}

		// --- Text input field at the bottom ---
		inputLeft := pad
		inputRight := w - pad
		inputTop := optBottom - inputH - inputPadY
		inputBottom := optBottom - inputPadY

		// Input field background (dark).
		fillRect(hdc, RECT{inputLeft, inputTop, inputRight, inputBottom}, colorSlotDimBg)

		// Gold border around input.
		drawLine(hdc, inputLeft, inputTop, inputRight, inputTop, colorBorderGold)
		drawLine(hdc, inputLeft, inputBottom, inputRight, inputBottom, colorBorderGold)
		drawLine(hdc, inputLeft, inputTop, inputLeft, inputBottom, colorBorderGold)
		drawLine(hdc, inputRight-1, inputTop, inputRight-1, inputBottom, colorBorderGold)

		// Input text (or placeholder).
		inputFontSize := int32(-14)
		inputFont, _, _ := createFontW.Call(
			uintptr(inputFontSize), 0, 0, 0, 0, 0, 0, 0,
			DEFAULT_CHARSET, 0, 0, 0, 0,
			uintptr(unsafe.Pointer(monoName)),
		)
		oldInputFont, _, _ := selectObjectProc.Call(hdc, inputFont)
		textRC := RECT{inputLeft + 6, inputTop + 2, inputRight - 6, inputBottom - 2}
		if abInputText == "" {
			setTextColorProc.Call(hdc, colorTextDim)
			placeholder, _ := syscall.UTF16PtrFromString("Send message...")
			drawTextProc.Call(hdc, uintptr(unsafe.Pointer(placeholder)), ^uintptr(0), uintptr(unsafe.Pointer(&textRC)), DT_LEFT|DT_VCENTER|DT_SINGLELINE)
		} else {
			setTextColorProc.Call(hdc, colorTextWhite)
			// Show text with blinking cursor.
			cursorChar := "|"
			if (time.Now().UnixMilli()/500)%2 == 0 {
				cursorChar = ""
			}
			inputStr, _ := syscall.UTF16PtrFromString(abInputText + cursorChar)
			drawTextProc.Call(hdc, uintptr(unsafe.Pointer(inputStr)), ^uintptr(0), uintptr(unsafe.Pointer(&textRC)), DT_LEFT|DT_VCENTER|DT_SINGLELINE|DT_END_ELLIPSIS)
		}
		selectObjectProc.Call(hdc, oldInputFont)
		deleteObjectProc.Call(inputFont)
	}
}
