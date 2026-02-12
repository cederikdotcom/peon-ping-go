//go:build windows

package main

import (
	_ "embed"
	"fmt"
	"os"
	"strconv"
	"syscall"
	"time"
	"unsafe"
)

// Embedded icons for each notification state
//
//go:embed icon_complete.png
var iconComplete []byte

//go:embed icon_permission.png
var iconPermission []byte

//go:embed icon_idle.png
var iconIdle []byte

// DLL handles
var (
	winmm      = syscall.NewLazyDLL("winmm.dll")
	user32     = syscall.NewLazyDLL("user32.dll")
	gdi32      = syscall.NewLazyDLL("gdi32.dll")
	kernel32   = syscall.NewLazyDLL("kernel32.dll")
	gdiplus    = syscall.NewLazyDLL("gdiplus.dll")
	shell32    = syscall.NewLazyDLL("shell32.dll")
	ole32      = syscall.NewLazyDLL("ole32.dll")
	shlwapiDLL = syscall.NewLazyDLL("shlwapi.dll")

	// Sound
	mciSendStringW = winmm.NewProc("mciSendStringW")

	// Window management
	registerClassExW    = user32.NewProc("RegisterClassExW")
	createWindowExW     = user32.NewProc("CreateWindowExW")
	defWindowProcW      = user32.NewProc("DefWindowProcW")
	getMessageW         = user32.NewProc("GetMessageW")
	translateMessage    = user32.NewProc("TranslateMessage")
	dispatchMessageW    = user32.NewProc("DispatchMessageW")
	postQuitMessage     = user32.NewProc("PostQuitMessage")
	postMessageW        = user32.NewProc("PostMessageW")
	destroyWindowProc   = user32.NewProc("DestroyWindow")
	setTimerProc        = user32.NewProc("SetTimer")
	getForegroundWindow     = user32.NewProc("GetForegroundWindow")
	setForegroundWindowProc = user32.NewProc("SetForegroundWindow")
	invalidateRectProc      = user32.NewProc("InvalidateRect")
	moveWindowProc          = user32.NewProc("MoveWindow")
	getWindowRect           = user32.NewProc("GetWindowRect")
	loadCursorW             = user32.NewProc("LoadCursorW")
	setWindowPosProc        = user32.NewProc("SetWindowPos")
	setFocusProc            = user32.NewProc("SetFocus")
	getModuleHandleW        = kernel32.NewProc("GetModuleHandleW")

	// DPI
	setProcessDPIAware = user32.NewProc("SetProcessDPIAware")

	// Painting
	beginPaintProc   = user32.NewProc("BeginPaint")
	endPaintProc     = user32.NewProc("EndPaint")
	fillRectProc     = user32.NewProc("FillRect")
	drawTextProc     = user32.NewProc("DrawTextW")
	createSolidBrush = gdi32.NewProc("CreateSolidBrush")
	setTextColorProc = gdi32.NewProc("SetTextColor")
	setBkModeProc    = gdi32.NewProc("SetBkMode")
	createFontW      = gdi32.NewProc("CreateFontW")
	selectObjectProc = gdi32.NewProc("SelectObject")
	deleteObjectProc = gdi32.NewProc("DeleteObject")
	createPenProc    = gdi32.NewProc("CreatePen")
	moveToExProc     = gdi32.NewProc("MoveToEx")
	lineToProc       = gdi32.NewProc("LineTo")

	// GDI+
	gdiplusStartup             = gdiplus.NewProc("GdiplusStartup")
	gdiplusShutdown            = gdiplus.NewProc("GdiplusShutdown")
	gdipCreateBitmapFromStream = gdiplus.NewProc("GdipCreateBitmapFromStream")
	gdipCreateFromHDC          = gdiplus.NewProc("GdipCreateFromHDC")
	gdipDeleteGraphics         = gdiplus.NewProc("GdipDeleteGraphics")
	gdipDrawImageRectI         = gdiplus.NewProc("GdipDrawImageRectI")
	gdipDisposeImage           = gdiplus.NewProc("GdipDisposeImage")
	gdipSetInterpolationMode   = gdiplus.NewProc("GdipSetInterpolationMode")

	// Shell
	shellExecuteW  = shell32.NewProc("ShellExecuteW")
	shBrowseForFolderW = shell32.NewProc("SHBrowseForFolderW")
	shGetPathFromIDListW = shell32.NewProc("SHGetPathFromIDListW")
	coInitializeEx = ole32.NewProc("CoInitializeEx")

	// COM stream
	shCreateMemStream = shlwapiDLL.NewProc("SHCreateMemStream")
)

// Win32 constants
const (
	WS_POPUP         = 0x80000000
	WS_VISIBLE       = 0x10000000
	WS_EX_TOPMOST    = 0x00000008
	WS_EX_TOOLWINDOW = 0x00000080
	CS_HREDRAW       = 0x0002
	CS_VREDRAW       = 0x0001
	WM_CLOSE         = 0x0010
	WM_DESTROY       = 0x0002
	WM_PAINT         = 0x000F
	WM_TIMER         = 0x0113
	DT_VCENTER       = 0x0004
	DT_SINGLELINE    = 0x0020
	DT_LEFT          = 0x0000
	TRANSPARENT      = 1
	FW_BOLD          = 700
	DEFAULT_CHARSET  = 1
	PS_SOLID         = 0
	PROCESS_TERMINATE = 0x0001
	WM_LBUTTONDOWN   = 0x0201
	WM_RBUTTONDOWN   = 0x0204
	DT_CENTER        = 0x0001
	DT_END_ELLIPSIS  = 0x00008000
	IDC_ARROW        = 32512
	SWP_NOMOVE       = 0x0002
	SWP_NOSIZE       = 0x0001
	WM_KEYDOWN       = 0x0100
	VK_ESCAPE        = 0x1B
	VK_1             = 0x31
	VK_2             = 0x32
	VK_3             = 0x33
	WM_USER          = 0x0400
)

// WC3 color palette
const (
	colorBgDark       = 0x00100A08 // RGB(8, 10, 16) - very dark blue-black
	colorBorderGold   = 0x001E8CB4 // RGB(180, 140, 30) - bright gold
	colorBorderShadow = 0x00143250 // RGB(80, 50, 20) - dark gold shadow
	colorBorderLight  = 0x0030A0D0 // RGB(208, 160, 48) - highlight gold
	colorTextGold     = 0x0060DCFF // RGB(255, 220, 96) - gold text
	colorTextWhite    = 0x00F0F0F0 // RGB(240, 240, 240) - white text
)

// Win32 structs
type WNDCLASSEX struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSm     uintptr
}

type POINT struct{ X, Y int32 }

type MSG struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      POINT
}

type RECT struct{ Left, Top, Right, Bottom int32 }

type PAINTSTRUCT struct {
	HDC       uintptr
	Erase     int32
	Paint     RECT
	Restore   int32
	IncUpdate int32
	Reserved  [32]byte
}

type GdiplusStartupInput struct {
	GdiplusVersion           uint32
	DebugEventCallback       uintptr
	SuppressBackgroundThread int32
	SuppressExternalCodecs   int32
}

// Notification state (global for WndProc callback)
var (
	notifyTitle   string
	notifyMessage string
	notifyWindows []uintptr
	gdipImages    map[string]uintptr // icon name -> GDI+ image
	activeIcon    string             // which icon to show
	gdipToken     uintptr
)

// Popup dimensions
const (
	popupW    = 420
	popupH    = 86
	borderW   = 3
	iconSize  = 64
	iconPad   = 11 // (popupH - iconSize) / 2
	iconFrame = 2
)

func main() {
	setProcessDPIAware.Call()
	initGDIPlus()

	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: peon-helper.exe <play|notify|both|hwnd|dismiss|actionbar> ...")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "dismiss":
		n := dismissAllNotifications()
		fmt.Println(n)

	case "hwnd":
		hwnd, _, _ := getForegroundWindow.Call()
		fmt.Println(hwnd)

	case "play":
		// play <file> <volume>
		if len(os.Args) < 4 {
			os.Exit(1)
		}
		vol, _ := strconv.ParseFloat(os.Args[3], 64)
		playWav(os.Args[2], vol)

	case "notify":
		// notify <title> <message> <icon> [hwnd]
		if len(os.Args) < 5 {
			os.Exit(1)
		}
		targetHwnd := parseHwndArg(5)
		showNotification(os.Args[2], os.Args[3], os.Args[4], targetHwnd)

	case "both":
		// both <file> <volume> <title> <message> <icon> [hwnd]
		if len(os.Args) < 7 {
			os.Exit(1)
		}
		vol, _ := strconv.ParseFloat(os.Args[3], 64)
		targetHwnd := parseHwndArg(7)
		mci(`open "` + os.Args[2] + `" type waveaudio alias peon`)
		mci(fmt.Sprintf("setaudio peon volume to %d", int(vol*1000)))
		mci("play peon")
		showNotification(os.Args[4], os.Args[5], os.Args[6], targetHwnd)
		mci("close peon")

	case "actionbar-check":
		// Check if a PeonActionBar window exists. Prints "1" or "0".
		className, _ := syscall.UTF16PtrFromString("PeonActionBar")
		found, _, _ := findWindowExW.Call(0, 0, uintptr(unsafe.Pointer(className)), 0)
		if found != 0 {
			fmt.Println("1")
		} else {
			fmt.Println("0")
		}

	case "actionbar":
		// actionbar <state-file>
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "Usage: peon-helper.exe actionbar <state-file>")
			os.Exit(1)
		}
		runActionBar(os.Args[2])
	}

	shutdownGDIPlus()
}

func parseHwndArg(idx int) uintptr {
	if idx < len(os.Args) {
		v, err := strconv.ParseUint(os.Args[idx], 10, 64)
		if err == nil {
			return uintptr(v)
		}
	}
	return 0
}

// --- GDI+ icon loading ---

func loadGDIPImage(data []byte) uintptr {
	if len(data) == 0 {
		return 0
	}
	stream, _, _ := shCreateMemStream.Call(
		uintptr(unsafe.Pointer(&data[0])),
		uintptr(len(data)),
	)
	if stream == 0 {
		return 0
	}

	var img uintptr
	gdipCreateBitmapFromStream.Call(stream, uintptr(unsafe.Pointer(&img)))

	// Release IStream
	type vtbl struct{ QI, AddRef, Release uintptr }
	vp := *(*uintptr)(unsafe.Pointer(stream))
	v := (*vtbl)(unsafe.Pointer(vp))
	syscall.SyscallN(v.Release, stream)

	return img
}

func initGDIPlus() {
	input := GdiplusStartupInput{GdiplusVersion: 1}
	gdiplusStartup.Call(
		uintptr(unsafe.Pointer(&gdipToken)),
		uintptr(unsafe.Pointer(&input)),
		0,
	)

	gdipImages = map[string]uintptr{
		"complete":   loadGDIPImage(iconComplete),
		"permission": loadGDIPImage(iconPermission),
		"idle":       loadGDIPImage(iconIdle),
	}
}

func shutdownGDIPlus() {
	for _, img := range gdipImages {
		if img != 0 {
			gdipDisposeImage.Call(img)
		}
	}
	if gdipToken != 0 {
		gdiplusShutdown.Call(gdipToken)
	}
}

func drawIcon(hdc uintptr, img uintptr, destX, destY, destSize int) {
	if img == 0 {
		return
	}
	var graphics uintptr
	gdipCreateFromHDC.Call(hdc, uintptr(unsafe.Pointer(&graphics)))
	if graphics == 0 {
		return
	}
	gdipSetInterpolationMode.Call(graphics, 7) // HighQualityBicubic
	gdipDrawImageRectI.Call(
		graphics, img,
		uintptr(destX), uintptr(destY),
		uintptr(destSize), uintptr(destSize),
	)
	gdipDeleteGraphics.Call(graphics)
}

// --- Sound via MCI ---

func mci(cmd string) {
	p, _ := syscall.UTF16PtrFromString(cmd)
	mciSendStringW.Call(uintptr(unsafe.Pointer(p)), 0, 0, 0)
}

func playWav(file string, volume float64) {
	vol := int(volume * 1000)
	if vol > 1000 {
		vol = 1000
	}
	mci(`open "` + file + `" type waveaudio alias peon`)
	mci(fmt.Sprintf("setaudio peon volume to %d", vol))
	mci("play peon wait")
	mci("close peon")
}

// --- Monitor enumeration ---

var (
	enumDisplayMonitors = user32.NewProc("EnumDisplayMonitors")
	findWindowExW            = user32.NewProc("FindWindowExW")
	getWindowThreadProcessId = user32.NewProc("GetWindowThreadProcessId")
	openProcess              = kernel32.NewProc("OpenProcess")
	terminateProcess         = kernel32.NewProc("TerminateProcess")
	closeHandleProc          = kernel32.NewProc("CloseHandle")
)

// monitorRects collects all monitor rectangles during enumeration.
var monitorRects []RECT

func enumMonitorCallback(hMonitor, hdc uintptr, lpRect *RECT, lParam uintptr) uintptr {
	if lpRect != nil {
		monitorRects = append(monitorRects, *lpRect)
	}
	return 1 // continue enumeration
}

func getMonitors() []RECT {
	monitorRects = nil
	cb := syscall.NewCallback(enumMonitorCallback)
	enumDisplayMonitors.Call(0, 0, cb, 0)
	return monitorRects
}

// countExistingPopups counts how many PeonNotify windows already exist,
// so new popups can stack below them.
func countExistingPopups() int {
	className, _ := syscall.UTF16PtrFromString("PeonNotify")
	count := 0
	var prev uintptr
	for {
		found, _, _ := findWindowExW.Call(0, prev, uintptr(unsafe.Pointer(className)), 0)
		if found == 0 {
			break
		}
		count++
		prev = found
	}
	return count
}

// dismissAllNotifications finds all PeonNotify windows and terminates their
// owning processes. This is more reliable than posting WM_CLOSE, which won't
// work if the owning process's message loop is hung.
func dismissAllNotifications() int {
	className, _ := syscall.UTF16PtrFromString("PeonNotify")
	myPID := uint32(os.Getpid())

	// Collect unique PIDs that own PeonNotify windows.
	pids := make(map[uint32]bool)
	var prev uintptr
	for {
		found, _, _ := findWindowExW.Call(0, prev, uintptr(unsafe.Pointer(className)), 0)
		if found == 0 {
			break
		}
		var pid uint32
		getWindowThreadProcessId.Call(found, uintptr(unsafe.Pointer(&pid)))
		if pid != 0 && pid != myPID {
			pids[pid] = true
		}
		prev = found
	}

	// Terminate each owning process. Windows destroys all its windows.
	for pid := range pids {
		h, _, _ := openProcess.Call(PROCESS_TERMINATE, 0, uintptr(pid))
		if h != 0 {
			terminateProcess.Call(h, 0)
			closeHandleProc.Call(h)
		}
	}
	return len(pids)
}

// --- Notification popup via Win32 ---

func showNotification(title, msg, icon string, _ uintptr) {
	// Watchdog: force-exit if the Win32 message loop hangs for any reason
	// (e.g. SetTimer fails silently, message loop deadlocks).
	go func() {
		time.Sleep(6 * time.Second)
		os.Exit(0)
	}()

	notifyTitle = title
	notifyMessage = msg
	activeIcon = icon

	className, _ := syscall.UTF16PtrFromString("PeonNotify")
	hInst, _, _ := getModuleHandleW.Call(0)

	wc := WNDCLASSEX{
		Size:      uint32(unsafe.Sizeof(WNDCLASSEX{})),
		Style:     CS_HREDRAW | CS_VREDRAW,
		WndProc:   syscall.NewCallback(notifyWndProc),
		Instance:  hInst,
		ClassName: className,
	}
	registerClassExW.Call(uintptr(unsafe.Pointer(&wc)))

	// Create a popup on every monitor
	monitors := getMonitors()

	// Stack below any existing popups (divide by monitor count since each
	// notification creates one window per monitor)
	existing := countExistingPopups()
	monCount := len(monitors)
	if monCount < 1 {
		monCount = 1
	}
	stackOffset := (existing / monCount) * (popupH + 6)
	for _, mon := range monitors {
		monW := int(mon.Right - mon.Left)
		x := int(mon.Left) + (monW-popupW)/2
		y := int(mon.Top) + 2 + stackOffset

		hwnd, _, _ := createWindowExW.Call(
			WS_EX_TOPMOST|WS_EX_TOOLWINDOW,
			uintptr(unsafe.Pointer(className)),
			0,
			WS_POPUP|WS_VISIBLE,
			uintptr(x), uintptr(y), popupW, popupH,
			0, 0, hInst, 0,
		)
		notifyWindows = append(notifyWindows, hwnd)
	}

	// Set timer on first window to auto-close all after 4 seconds
	if len(notifyWindows) > 0 {
		setTimerProc.Call(notifyWindows[0], 1, 4000, 0)
	}

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

// fillRect is a helper that creates a solid brush, fills, and cleans up.
func fillRect(hdc uintptr, r RECT, color uintptr) {
	brush, _, _ := createSolidBrush.Call(color)
	fillRectProc.Call(hdc, uintptr(unsafe.Pointer(&r)), brush)
	deleteObjectProc.Call(brush)
}

// drawLine draws a single-pixel line.
func drawLine(hdc uintptr, x1, y1, x2, y2 int32, color uintptr) {
	pen, _, _ := createPenProc.Call(PS_SOLID, 1, color)
	old, _, _ := selectObjectProc.Call(hdc, pen)
	moveToExProc.Call(hdc, uintptr(x1), uintptr(y1), 0)
	lineToProc.Call(hdc, uintptr(x2), uintptr(y2))
	selectObjectProc.Call(hdc, old)
	deleteObjectProc.Call(pen)
}

func notifyWndProc(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case WM_TIMER:
		for _, w := range notifyWindows {
			destroyWindowProc.Call(w)
		}
		return 0

	case WM_PAINT:
		var ps PAINTSTRUCT
		hdc, _, _ := beginPaintProc.Call(hwnd, uintptr(unsafe.Pointer(&ps)))

		w := int32(popupW)
		h := int32(popupH)

		// Dark background fill
		fillRect(hdc, RECT{0, 0, w, h}, colorBgDark)

		// Outer gold border (3px beveled)
		// Top edge - bright
		fillRect(hdc, RECT{0, 0, w, borderW}, colorBorderLight)
		// Left edge - bright
		fillRect(hdc, RECT{0, 0, borderW, h}, colorBorderLight)
		// Bottom edge - dark shadow
		fillRect(hdc, RECT{0, h - borderW, w, h}, colorBorderShadow)
		// Right edge - dark shadow
		fillRect(hdc, RECT{w - borderW, 0, w, h}, colorBorderShadow)

		// Inner highlight line (1px)
		drawLine(hdc, borderW, borderW, w-borderW, borderW, colorBorderGold)           // top
		drawLine(hdc, borderW, borderW, borderW, h-borderW, colorBorderGold)           // left
		drawLine(hdc, borderW, h-borderW-1, w-borderW, h-borderW-1, colorBorderShadow) // bottom
		drawLine(hdc, w-borderW-1, borderW, w-borderW-1, h-borderW, colorBorderShadow) // right

		// Icon area: gold frame around the icon
		ix := int32(iconPad)
		iy := int32(iconPad)
		is := int32(iconSize)
		fr := int32(iconFrame)

		// Icon frame (gold border around icon)
		fillRect(hdc, RECT{ix - fr, iy - fr, ix + is + fr, iy - fr + fr}, colorBorderGold)     // top
		fillRect(hdc, RECT{ix - fr, iy + is, ix + is + fr, iy + is + fr}, colorBorderGold)      // bottom
		fillRect(hdc, RECT{ix - fr, iy - fr, ix - fr + fr, iy + is + fr}, colorBorderGold)      // left
		fillRect(hdc, RECT{ix + is, iy - fr, ix + is + fr, iy + is + fr}, colorBorderGold)      // right

		// Draw the icon
		if img, ok := gdipImages[activeIcon]; ok {
			drawIcon(hdc, img, int(ix), int(iy), int(is))
		} else if img, ok := gdipImages["complete"]; ok {
			drawIcon(hdc, img, int(ix), int(iy), int(is))
		}

		// Text area
		setBkModeProc.Call(hdc, TRANSPARENT)
		textLeft := ix + is + int32(iconPad)
		fontName, _ := syscall.UTF16PtrFromString("Segoe UI")

		// Title (large, gold, bold)
		titleHeight := int32(-20)
		titleFont, _, _ := createFontW.Call(
			uintptr(titleHeight),
			0, 0, 0,
			FW_BOLD, 0, 0, 0,
			DEFAULT_CHARSET, 0, 0, 0, 0,
			uintptr(unsafe.Pointer(fontName)),
		)
		oldFont, _, _ := selectObjectProc.Call(hdc, titleFont)
		setTextColorProc.Call(hdc, colorTextGold)

		titleText, _ := syscall.UTF16PtrFromString(notifyTitle)
		titleRC := RECT{textLeft, borderW + 10, w - borderW - 8, h/2 + 4}
		drawTextProc.Call(
			hdc,
			uintptr(unsafe.Pointer(titleText)),
			^uintptr(0),
			uintptr(unsafe.Pointer(&titleRC)),
			DT_LEFT|DT_SINGLELINE,
		)
		selectObjectProc.Call(hdc, oldFont)
		deleteObjectProc.Call(titleFont)

		// Message (smaller, lighter)
		msgHeight := int32(-15)
		msgFont, _, _ := createFontW.Call(
			uintptr(msgHeight),
			0, 0, 0,
			0, 0, 0, 0, // normal weight
			DEFAULT_CHARSET, 0, 0, 0, 0,
			uintptr(unsafe.Pointer(fontName)),
		)
		oldFont2, _, _ := selectObjectProc.Call(hdc, msgFont)
		setTextColorProc.Call(hdc, colorTextWhite)

		msgText, _ := syscall.UTF16PtrFromString(notifyMessage)
		msgRC := RECT{textLeft, h/2 - 2, w - borderW - 8, h - borderW - 8}
		drawTextProc.Call(
			hdc,
			uintptr(unsafe.Pointer(msgText)),
			^uintptr(0),
			uintptr(unsafe.Pointer(&msgRC)),
			DT_LEFT|DT_SINGLELINE,
		)
		selectObjectProc.Call(hdc, oldFont2)
		deleteObjectProc.Call(msgFont)
		endPaintProc.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		return 0

	case WM_DESTROY:
		postQuitMessage.Call(0)
		return 0
	}

	ret, _, _ := defWindowProcW.Call(hwnd, uintptr(msg), wParam, lParam)
	return ret
}

