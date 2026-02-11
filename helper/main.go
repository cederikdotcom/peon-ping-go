//go:build windows

package main

import (
	"fmt"
	"os"
	"strconv"
	"syscall"
	"unsafe"
)

// DLL handles
var (
	winmm    = syscall.NewLazyDLL("winmm.dll")
	user32   = syscall.NewLazyDLL("user32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	// Sound
	mciSendStringW = winmm.NewProc("mciSendStringW")

	// Window management
	registerClassExW     = user32.NewProc("RegisterClassExW")
	createWindowExW      = user32.NewProc("CreateWindowExW")
	showWindowProc       = user32.NewProc("ShowWindow")
	defWindowProcW       = user32.NewProc("DefWindowProcW")
	getMessageW          = user32.NewProc("GetMessageW")
	translateMessage     = user32.NewProc("TranslateMessage")
	dispatchMessageW     = user32.NewProc("DispatchMessageW")
	postQuitMessage      = user32.NewProc("PostQuitMessage")
	destroyWindowProc    = user32.NewProc("DestroyWindow")
	setTimerProc         = user32.NewProc("SetTimer")
	enumDisplayMonitors  = user32.NewProc("EnumDisplayMonitors")
	getMonitorInfoW      = user32.NewProc("GetMonitorInfoW")
	getModuleHandleW     = kernel32.NewProc("GetModuleHandleW")

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
)

// Win32 constants
const (
	WS_POPUP         = 0x80000000
	WS_VISIBLE       = 0x10000000
	WS_EX_TOPMOST    = 0x00000008
	WS_EX_TOOLWINDOW = 0x00000080
	CS_HREDRAW       = 0x0002
	CS_VREDRAW       = 0x0001
	WM_CREATE        = 0x0001
	WM_DESTROY       = 0x0002
	WM_PAINT         = 0x000F
	WM_TIMER         = 0x0113
	SW_SHOW          = 5
	DT_CENTER        = 0x0001
	DT_VCENTER       = 0x0004
	DT_SINGLELINE   = 0x0020
	TRANSPARENT      = 1
	FW_BOLD          = 700
	DEFAULT_CHARSET  = 1
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

type MONITORINFO struct {
	Size    uint32
	Monitor RECT
	Work    RECT
	Flags   uint32
}

// Notification state (global for WndProc callback)
var (
	notifyBgColor uint32
	notifyText    string
	notifyWindows []uintptr
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: peon-helper.exe <play|notify|both> ...")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "play":
		// play <file> <volume>
		if len(os.Args) < 4 {
			os.Exit(1)
		}
		vol, _ := strconv.ParseFloat(os.Args[3], 64)
		playWav(os.Args[2], vol)

	case "notify":
		// notify <message> <color>
		if len(os.Args) < 4 {
			os.Exit(1)
		}
		showNotification(os.Args[2], os.Args[3])

	case "both":
		// both <file> <volume> <message> <color>
		if len(os.Args) < 6 {
			os.Exit(1)
		}
		vol, _ := strconv.ParseFloat(os.Args[3], 64)
		// Start sound async, then show notification (blocks ~4s), then cleanup
		mci(`open "` + os.Args[2] + `" type waveaudio alias peon`)
		mci(fmt.Sprintf("setaudio peon volume to %d", int(vol*1000)))
		mci("play peon")
		showNotification(os.Args[4], os.Args[5])
		mci("close peon")
	}
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

// --- Notification popup via Win32 ---

func showNotification(msg string, color string) {
	notifyText = msg
	notifyBgColor = colorToColorRef(color)

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

	// Get all monitor work areas
	areas := getMonitorWorkAreas()

	const popupW, popupH = 500, 80
	for _, area := range areas {
		x := int(area.Left) + (int(area.Right-area.Left)-popupW)/2
		y := int(area.Top) + 40

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

	// Auto-close after 4 seconds
	if len(notifyWindows) > 0 {
		setTimerProc.Call(notifyWindows[0], 1, 4000, 0)
	}

	// Message loop
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

		// Fill background
		brush, _, _ := createSolidBrush.Call(uintptr(notifyBgColor))
		fillRectProc.Call(hdc, uintptr(unsafe.Pointer(&ps.Paint)), brush)
		deleteObjectProc.Call(brush)

		// Set up text drawing
		setBkModeProc.Call(hdc, TRANSPARENT)
		setTextColorProc.Call(hdc, 0x00FFFFFF) // white

		fontName, _ := syscall.UTF16PtrFromString("Segoe UI")
		fontHeight := int32(-24) // ~16pt at 96dpi
		font, _, _ := createFontW.Call(
			uintptr(fontHeight),
			0, 0, 0,
			FW_BOLD, 0, 0, 0,
			DEFAULT_CHARSET, 0, 0, 0, 0,
			uintptr(unsafe.Pointer(fontName)),
		)
		old, _, _ := selectObjectProc.Call(hdc, font)

		text, _ := syscall.UTF16PtrFromString(notifyText)
		rc := RECT{0, 0, 500, 80}
		drawTextProc.Call(
			hdc,
			uintptr(unsafe.Pointer(text)),
			^uintptr(0), // -1 (null-terminated)
			uintptr(unsafe.Pointer(&rc)),
			DT_CENTER|DT_VCENTER|DT_SINGLELINE,
		)

		selectObjectProc.Call(hdc, old)
		deleteObjectProc.Call(font)
		endPaintProc.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		return 0

	case WM_DESTROY:
		postQuitMessage.Call(0)
		return 0
	}

	ret, _, _ := defWindowProcW.Call(hwnd, uintptr(msg), wParam, lParam)
	return ret
}

func getMonitorWorkAreas() []RECT {
	var areas []RECT
	cb := syscall.NewCallback(func(hMon, hdc uintptr, lprc *RECT, lParam uintptr) uintptr {
		var mi MONITORINFO
		mi.Size = uint32(unsafe.Sizeof(mi))
		getMonitorInfoW.Call(hMon, uintptr(unsafe.Pointer(&mi)))
		areas = append(areas, mi.Work)
		return 1 // continue enumeration
	})
	enumDisplayMonitors.Call(0, 0, cb, 0)
	return areas
}

// colorToColorRef converts a color name to Windows COLORREF (0x00BBGGRR).
func colorToColorRef(color string) uint32 {
	switch color {
	case "blue":
		return 180<<16 | 80<<8 | 30 // RGB(30, 80, 180)
	case "yellow":
		return 0<<16 | 160<<8 | 200 // RGB(200, 160, 0)
	default: // red
		return 0<<16 | 0<<8 | 180 // RGB(180, 0, 0)
	}
}
