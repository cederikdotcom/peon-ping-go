//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

// JSON types mirroring actionbar.go in the main package.
type abSessionJSON struct {
	Project   string `json:"project"`
	State     string `json:"state"`
	Message   string `json:"message"`
	HWND      uint64 `json:"hwnd"`
	UpdatedAt int64  `json:"updated_at"`
}

type abStateJSON struct {
	Sessions map[string]abSessionJSON `json:"sessions"`
}

// Permission request file (written by peon main binary).
type abPermReq struct {
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
	CreatedAt int64           `json:"created_at"`
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
	HasPending bool   // has a .actionbar-req-{session}.json
	ToolName   string // from req file
	ToolDetail string // summary of tool_input
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
)

// Action bar dimensions.
const (
	abBarH      = 90
	abOptionsH  = 80 // height of options panel above the bar
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
	case "needs approval":
		return "permission"
	default: // "working", "ready"
		return "idle"
	}
}

// abToolDetail extracts a short summary from tool_input JSON.
func abToolDetail(toolName string, raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	// Try common fields in priority order.
	for _, key := range []string{"command", "file_path", "pattern", "url", "query"} {
		if v, ok := m[key]; ok {
			s := fmt.Sprintf("%v", v)
			if len(s) > 60 {
				s = s[:57] + "..."
			}
			return s
		}
	}
	return ""
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
		if now-s.UpdatedAt > 600 {
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

		// Check for req file — this is the sole source of truth for "needs action".
		reqPath := filepath.Join(abPeonDir, ".actionbar-req-"+item.id+".json")
		if _, err := os.Stat(reqPath); err == nil {
			slot.HasPending = true
		}

		slots = append(slots, slot)
	}
	return slots
}

// abApplyPending applies the result from the background read to the main thread state.
func abApplyPending() {
	if abPendingSlots == nil {
		return
	}
	abSlots = abPendingSlots
	abPendingSlots = nil

	// Restore selection by session ID.
	abSelectedSlot = -1
	if abSelectedSessID != "" {
		for i, s := range abSlots {
			if s.SessionID == abSelectedSessID {
				if s.HasPending {
					abSelectedSlot = i
				} else {
					abSelectedSessID = ""
				}
				break
			}
		}
		if abSelectedSlot < 0 {
			abSelectedSessID = ""
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
	// Slots + "+" button + borders.
	w := (len(abSlots)+1)*abSlotW + 2*borderW
	if w < abMinW {
		w = abMinW
	}
	return w
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
	barW := abBarWidth()
	totalH := abTotalHeight()
	screenW := int(mon.Right - mon.Left)
	x := int(mon.Left) + (screenW-barW)/2
	y := int(mon.Bottom) - totalH - 50
	moveWindowProc.Call(abHwnd, uintptr(x), uintptr(y), uintptr(barW), uintptr(totalH), 1)
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

// abLoadReqDetails reads the req file to populate tool name and detail.
func abLoadReqDetails(slot *abSlot) {
	reqPath := filepath.Join(abPeonDir, ".actionbar-req-"+slot.SessionID+".json")
	data, err := os.ReadFile(reqPath)
	if err != nil {
		return
	}
	var req abPermReq
	if json.Unmarshal(data, &req) == nil {
		slot.ToolName = req.ToolName
		slot.ToolDetail = abToolDetail(req.ToolName, req.ToolInput)
	}
}

// BROWSEINFOW for SHBrowseForFolderW.
type BROWSEINFOW struct {
	Owner        uintptr
	Root         uintptr
	DisplayName  *uint16
	Title        *uint16
	Flags        uint32
	Callback     uintptr
	LParam       uintptr
	Image        int32
}

const (
	BIF_RETURNONLYFSDIRS = 0x0001
	BIF_NEWDIALOGSTYLE   = 0x0040
	MAX_PATH             = 260
)

// abLaunchNewSession opens a folder picker and launches claude in the selected folder.
func abLaunchNewSession() {
	go func() {
		// Initialize COM for this goroutine.
		coInitializeEx.Call(0, 0)

		title, _ := syscall.UTF16PtrFromString("Select project folder")
		var displayName [MAX_PATH]uint16

		bi := BROWSEINFOW{
			Owner:       abHwnd,
			DisplayName: &displayName[0],
			Title:       title,
			Flags:       BIF_RETURNONLYFSDIRS | BIF_NEWDIALOGSTYLE,
		}

		pidl, _, _ := shBrowseForFolderW.Call(uintptr(unsafe.Pointer(&bi)))
		if pidl == 0 {
			return // cancelled
		}

		var pathBuf [MAX_PATH]uint16
		shGetPathFromIDListW.Call(pidl, uintptr(unsafe.Pointer(&pathBuf[0])))
		winPath := syscall.UTF16ToString(pathBuf[:])
		if winPath == "" {
			return
		}

		// Launch Windows Terminal with wsl + claude in the selected folder.
		// wt.exe new-tab wsl.exe --cd <winPath> -- claude
		args := fmt.Sprintf(`new-tab wsl.exe --cd "%s" -- claude`, winPath)
		verb, _ := syscall.UTF16PtrFromString("open")
		exe, _ := syscall.UTF16PtrFromString("wt.exe")
		params, _ := syscall.UTF16PtrFromString(args)
		shellExecuteW.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(exe)), uintptr(unsafe.Pointer(params)), 0, 1)
	}()
}

// abWriteResponse writes a permission response file and deselects the slot.
func abWriteResponse(behavior string, applySuggestions bool) {
	if abSelectedSlot < 0 || abSelectedSlot >= len(abSlots) {
		return
	}
	slot := abSlots[abSelectedSlot]
	if !slot.HasPending {
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
	os.WriteFile(rspPath, data, 0644)

	// Deselect and mark slot as no longer pending (immediate visual feedback).
	abSlots[abSelectedSlot].HasPending = false
	abSelectedSlot = -1
	abSelectedSessID = ""
	abResizeWindow()
	invalidateRectProc.Call(abHwnd, 0, 1)
}

func runActionBar(stateFile string) {
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
	barW := abBarWidth()
	totalH := abTotalHeight()
	screenW := int(mon.Right - mon.Left)
	x := int(mon.Left) + (screenW-barW)/2
	y := int(mon.Bottom) - totalH - 50

	hwnd, _, _ := createWindowExW.Call(
		WS_EX_TOPMOST|WS_EX_TOOLWINDOW,
		uintptr(unsafe.Pointer(className)),
		0,
		WS_POPUP|WS_VISIBLE,
		uintptr(x), uintptr(y), uintptr(barW), uintptr(totalH),
		0, 0, hInst, 0,
	)
	abHwnd = hwnd

	// Poll every 2 seconds (UNC file I/O can be slow).
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
		abTriggerRead()
		return 0

	case WM_USER:
		abApplyPending()
		return 0

	case WM_PAINT:
		var ps PAINTSTRUCT
		hdc, _, _ := beginPaintProc.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		abPaint(hdc)
		endPaintProc.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		return 0

	case WM_LBUTTONDOWN:
		x := int(int16(lParam & 0xFFFF))
		y := int(int16((lParam >> 16) & 0xFFFF))
		totalH := abTotalHeight()
		barTop := totalH - abBarH

		if y >= barTop {
			// Click in slot area.
			localX := x - borderW
			if localX >= 0 {
				slotIdx := localX / abSlotW
				if slotIdx == len(abSlots) {
					// "+" button clicked — launch new session.
					abLaunchNewSession()
				} else if slotIdx >= 0 && slotIdx < len(abSlots) {
					slot := &abSlots[slotIdx]
					if slot.HasPending {
						// Lazy-load tool details from req file on click.
						if slot.ToolName == "" {
							abLoadReqDetails(slot)
						}
						// Toggle options panel.
						if abSelectedSlot == slotIdx {
							abSelectedSlot = -1
							abSelectedSessID = ""
						} else {
							abSelectedSlot = slotIdx
							abSelectedSessID = slot.SessionID
						}
						abResizeWindow()
						invalidateRectProc.Call(abHwnd, 0, 1)
						if abSelectedSlot >= 0 {
							setForegroundWindowProc.Call(abHwnd)
							setFocusProc.Call(abHwnd)
						}
					} else {
						// No req file: focus its terminal window.
						target := slot.HWND
						if target != 0 {
							setForegroundWindowProc.Call(target)
							setWindowPosProc.Call(abHwnd, ^uintptr(0), 0, 0, 0, 0, SWP_NOMOVE|SWP_NOSIZE)
						}
					}
				}
			}
		}
		// Click in options area: no-op (keyboard only for actions).
		return 0

	case WM_KEYDOWN:
		if abSelectedSlot >= 0 {
			switch wParam {
			case VK_1:
				abWriteResponse("allow", false)
			case VK_2:
				abWriteResponse("allow", true)
			case VK_3:
				abWriteResponse("deny", false)
			case VK_ESCAPE:
				abSelectedSlot = -1
				abSelectedSessID = ""
				abResizeWindow()
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
	w := int32(abBarWidth())
	totalH := int32(abTotalHeight())
	barTop := totalH - int32(abBarH)

	// Full background fill.
	fillRect(hdc, RECT{0, 0, w, totalH}, colorBgDark)

	// --- Options panel (if a slot is selected) ---
	if abSelectedSlot >= 0 && abSelectedSlot < len(abSlots) {
		abPaintOptions(hdc, w, barTop)
	}

	// --- Bar area ---
	barBottom := totalH

	// Beveled gold border around the bar.
	fillRect(hdc, RECT{0, barTop, w, barTop + borderW}, colorBorderLight)
	fillRect(hdc, RECT{0, barTop, borderW, barBottom}, colorBorderLight)
	fillRect(hdc, RECT{0, barBottom - borderW, w, barBottom}, colorBorderShadow)
	fillRect(hdc, RECT{w - borderW, barTop, w, barBottom}, colorBorderShadow)

	// Inner highlight.
	drawLine(hdc, borderW, barTop+borderW, w-borderW, barTop+borderW, colorBorderGold)
	drawLine(hdc, borderW, barTop+borderW, borderW, barBottom-borderW, colorBorderGold)
	drawLine(hdc, borderW, barBottom-borderW-1, w-borderW, barBottom-borderW-1, colorBorderShadow)
	drawLine(hdc, w-borderW-1, barTop+borderW, w-borderW-1, barBottom-borderW, colorBorderShadow)

	// Render session slots.
	setBkModeProc.Call(hdc, TRANSPARENT)
	fontName, _ := syscall.UTF16PtrFromString("Segoe UI")

	for i, slot := range abSlots {
		sx := int32(borderW + i*abSlotW)
		slotTop := barTop + borderW + 1

		isSelected := i == abSelectedSlot

		// Req file = needs action. No req file = idle.
		if slot.HasPending {
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
			// No req file — dimmed/idle.
			fillRect(hdc, RECT{sx, slotTop, sx + int32(abSlotW), barBottom - borderW}, colorSlotDimBg)
		}

		// Icon (48x48, centered in slot).
		iconX := int(sx) + (abSlotW-abIconSz)/2
		iconY := int(slotTop) + 10
		if slot.HasPending {
			if img, ok := gdipImages["permission"]; ok {
				drawIcon(hdc, img, iconX, iconY, abIconSz)
			}
		} else {
			if img, ok := gdipImages["idle"]; ok {
				drawIcon(hdc, img, iconX, iconY, abIconSz)
			}
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
		if slot.HasPending {
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
		if slot.HasPending {
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
		plusX := int32(borderW + len(abSlots)*abSlotW)
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
	pad := int32(borderW + 8)

	// Tool name + detail line (white, bold).
	toolText := slot.ToolName
	if slot.ToolDetail != "" {
		toolText += ": " + slot.ToolDetail
	}
	// Truncate if too long.
	if len(toolText) > 70 {
		toolText = toolText[:67] + "..."
	}

	toolSize := int32(-15)
	toolFont, _, _ := createFontW.Call(
		uintptr(toolSize),
		0, 0, 0,
		FW_BOLD, 0, 0, 0,
		DEFAULT_CHARSET, 0, 0, 0, 0,
		uintptr(unsafe.Pointer(fontName)),
	)
	oldFont, _, _ := selectObjectProc.Call(hdc, toolFont)
	setTextColorProc.Call(hdc, colorTextWhite)

	toolStr, _ := syscall.UTF16PtrFromString(toolText)
	toolRC := RECT{pad, optTop + borderW + 6, w - pad, optTop + borderW + 28}
	drawTextProc.Call(hdc, uintptr(unsafe.Pointer(toolStr)), ^uintptr(0), uintptr(unsafe.Pointer(&toolRC)), DT_LEFT|DT_SINGLELINE|DT_END_ELLIPSIS)
	selectObjectProc.Call(hdc, oldFont)
	deleteObjectProc.Call(toolFont)

	// Message line if available.
	msgY := optTop + borderW + 28
	if slot.Message != "" {
		msgSize := int32(-12)
		msgFont, _, _ := createFontW.Call(
			uintptr(msgSize),
			0, 0, 0,
			0, 0, 0, 0,
			DEFAULT_CHARSET, 0, 0, 0, 0,
			uintptr(unsafe.Pointer(fontName)),
		)
		oldMsgFont, _, _ := selectObjectProc.Call(hdc, msgFont)
		setTextColorProc.Call(hdc, colorTextDim)
		msgStr, _ := syscall.UTF16PtrFromString(slot.Message)
		msgRC := RECT{pad, msgY, w - pad, msgY + 16}
		drawTextProc.Call(hdc, uintptr(unsafe.Pointer(msgStr)), ^uintptr(0), uintptr(unsafe.Pointer(&msgRC)), DT_LEFT|DT_SINGLELINE|DT_END_ELLIPSIS)
		selectObjectProc.Call(hdc, oldMsgFont)
		deleteObjectProc.Call(msgFont)
		msgY += 16
	}

	// Action buttons line (gold text).
	actionsY := msgY + 4
	optSize := int32(-14)
	optFont, _, _ := createFontW.Call(
		uintptr(optSize),
		0, 0, 0,
		FW_BOLD, 0, 0, 0,
		DEFAULT_CHARSET, 0, 0, 0, 0,
		uintptr(unsafe.Pointer(fontName)),
	)
	oldOptFont, _, _ := selectObjectProc.Call(hdc, optFont)
	setTextColorProc.Call(hdc, colorOptionsKey)

	// Build actions string with brackets.
	var actions []string
	actions = append(actions, "[1] Allow")
	actions = append(actions, "[2] Always Allow")
	actions = append(actions, "[3] Deny")
	actionsStr, _ := syscall.UTF16PtrFromString(strings.Join(actions, "    "))
	actRC := RECT{pad, actionsY, w - pad, actionsY + 20}
	drawTextProc.Call(hdc, uintptr(unsafe.Pointer(actionsStr)), ^uintptr(0), uintptr(unsafe.Pointer(&actRC)), DT_LEFT|DT_SINGLELINE)
	selectObjectProc.Call(hdc, oldOptFont)
	deleteObjectProc.Call(optFont)
}
