//go:build windows

package main

import (
	"encoding/json"
	"os"
	"sort"
	"syscall"
	"time"
	"unsafe"
)

// JSON types mirroring actionbar.go in the main package.
type abSessionJSON struct {
	Project   string `json:"project"`
	State     string `json:"state"`
	HWND      uint64 `json:"hwnd"`
	UpdatedAt int64  `json:"updated_at"`
}

type abStateJSON struct {
	Sessions map[string]abSessionJSON `json:"sessions"`
}

// Rendering slot.
type abSlot struct {
	Project string
	State   string
	HWND    uintptr
}

// Action bar globals (for WndProc callback).
var (
	abStateFile string
	abSlots     []abSlot
	abHwnd      uintptr
	abLastJSON  string
)

// Action bar dimensions.
const (
	abBarH     = 90
	abSlotW    = 80
	abIconSz   = 48
	abMaxSlots = 6
	abMinW     = 200
)

// abStateIcon maps route status to an embedded icon name.
func abStateIcon(state string) string {
	switch state {
	case "done":
		return "complete"
	case "needs approval":
		return "permission"
	default: // "working", "ready"
		return "idle"
	}
}

// abStateColor maps route status to a top-bar color (BGR).
func abStateColor(state string) uintptr {
	switch state {
	case "done":
		return 0x00FF8C50 // blue  RGB(80,140,255)
	case "needs approval":
		return 0x005050FF // red   RGB(255,80,80)
	case "working":
		return 0x0050DC50 // green RGB(80,220,80)
	default: // "ready"
		return colorTextGold // yellow/gold
	}
}

// abReadState re-reads the state file and updates abSlots.
func abReadState() {
	data, err := os.ReadFile(abStateFile)
	if err != nil {
		return
	}

	content := string(data)
	if content == abLastJSON {
		return // unchanged
	}
	abLastJSON = content

	var state abStateJSON
	if err := json.Unmarshal(data, &state); err != nil {
		return // tolerate partial reads
	}

	type kv struct {
		id string
		s  abSessionJSON
	}
	now := time.Now().Unix()
	var items []kv
	for id, s := range state.Sessions {
		if now-s.UpdatedAt > 600 { // skip stale (>10 min)
			continue
		}
		items = append(items, kv{id, s})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].s.UpdatedAt < items[j].s.UpdatedAt
	})

	slots := make([]abSlot, 0, len(items))
	for _, item := range items {
		if len(slots) >= abMaxSlots {
			break
		}
		slots = append(slots, abSlot{
			Project: item.s.Project,
			State:   item.s.State,
			HWND:    uintptr(item.s.HWND),
		})
	}
	abSlots = slots

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
	w := len(abSlots)*abSlotW + 2*borderW
	if w < abMinW {
		w = abMinW
	}
	return w
}

func abResizeWindow() {
	if abHwnd == 0 {
		return
	}
	mon := primaryMonitor()
	barW := abBarWidth()
	screenW := int(mon.Right - mon.Left)
	x := int(mon.Left) + (screenW-barW)/2
	y := int(mon.Bottom) - abBarH - 50
	moveWindowProc.Call(abHwnd, uintptr(x), uintptr(y), uintptr(barW), uintptr(abBarH), 1)
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

func runActionBar(stateFile string) {
	abStateFile = stateFile
	killExistingActionBar()

	// Initial read.
	abReadState()

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
	barW := abBarWidth()
	screenW := int(mon.Right - mon.Left)
	x := int(mon.Left) + (screenW-barW)/2
	y := int(mon.Bottom) - abBarH - 50

	hwnd, _, _ := createWindowExW.Call(
		WS_EX_TOPMOST|WS_EX_TOOLWINDOW,
		uintptr(unsafe.Pointer(className)),
		0,
		WS_POPUP|WS_VISIBLE,
		uintptr(x), uintptr(y), uintptr(barW), uintptr(abBarH),
		0, 0, hInst, 0,
	)
	abHwnd = hwnd

	// Poll every 2 seconds.
	setTimerProc.Call(hwnd, 1, 2000, 0)

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
		abReadState()
		return 0

	case WM_PAINT:
		var ps PAINTSTRUCT
		hdc, _, _ := beginPaintProc.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		abPaint(hdc)
		endPaintProc.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		return 0

	case WM_LBUTTONDOWN:
		x := int(int16(lParam & 0xFFFF))
		if x >= borderW {
			slotIdx := (x - borderW) / abSlotW
			if slotIdx >= 0 && slotIdx < len(abSlots) {
				target := abSlots[slotIdx].HWND
				if target != 0 {
					setForegroundWindowProc.Call(target)
					// Re-assert topmost so the bar stays visible above the terminal.
					setWindowPosProc.Call(abHwnd, ^uintptr(0), 0, 0, 0, 0, SWP_NOMOVE|SWP_NOSIZE)
				}
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
	w := int32(abBarWidth())
	h := int32(abBarH)

	// Dark background.
	fillRect(hdc, RECT{0, 0, w, h}, colorBgDark)

	// Beveled gold border (same as notifications).
	fillRect(hdc, RECT{0, 0, w, borderW}, colorBorderLight)
	fillRect(hdc, RECT{0, 0, borderW, h}, colorBorderLight)
	fillRect(hdc, RECT{0, h - borderW, w, h}, colorBorderShadow)
	fillRect(hdc, RECT{w - borderW, 0, w, h}, colorBorderShadow)

	// Inner highlight.
	drawLine(hdc, borderW, borderW, w-borderW, borderW, colorBorderGold)
	drawLine(hdc, borderW, borderW, borderW, h-borderW, colorBorderGold)
	drawLine(hdc, borderW, h-borderW-1, w-borderW, h-borderW-1, colorBorderShadow)
	drawLine(hdc, w-borderW-1, borderW, w-borderW-1, h-borderW, colorBorderShadow)

	// Render session slots.
	setBkModeProc.Call(hdc, TRANSPARENT)
	fontName, _ := syscall.UTF16PtrFromString("Segoe UI")

	for i, slot := range abSlots {
		sx := int32(borderW + i*abSlotW)

		// State-colored top bar (3px).
		fillRect(hdc, RECT{sx, borderW + 1, sx + int32(abSlotW), borderW + 4}, abStateColor(slot.State))

		// Icon (48x48, centered in slot).
		iconX := int(sx) + (abSlotW-abIconSz)/2
		iconY := borderW + 7 // below state bar + gap
		if img, ok := gdipImages[abStateIcon(slot.State)]; ok {
			drawIcon(hdc, img, iconX, iconY, abIconSz)
		}

		// Project name below icon.
		fontSize := int32(-12)
		textFont, _, _ := createFontW.Call(
			uintptr(fontSize),
			0, 0, 0,
			0, 0, 0, 0,
			DEFAULT_CHARSET, 0, 0, 0, 0,
			uintptr(unsafe.Pointer(fontName)),
		)
		oldFont, _, _ := selectObjectProc.Call(hdc, textFont)
		setTextColorProc.Call(hdc, colorTextGold)

		text, _ := syscall.UTF16PtrFromString(slot.Project)
		textRC := RECT{sx + 2, int32(iconY + abIconSz + 2), sx + int32(abSlotW) - 2, h - borderW}
		drawTextProc.Call(
			hdc,
			uintptr(unsafe.Pointer(text)),
			^uintptr(0),
			uintptr(unsafe.Pointer(&textRC)),
			DT_CENTER|DT_SINGLELINE|DT_END_ELLIPSIS,
		)
		selectObjectProc.Call(hdc, oldFont)
		deleteObjectProc.Call(textFont)

		// Slot separator.
		if i > 0 {
			drawLine(hdc, sx, borderW+1, sx, h-borderW, colorBorderShadow)
		}
	}
}
