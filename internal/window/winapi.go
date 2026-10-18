package window

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// The few Win32 calls a small native window needs. x/sys/windows doesn't
// wrap them, so they're loaded from user32/gdi32/kernel32 here.
var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	pRegisterClassExW              = user32.NewProc("RegisterClassExW")
	pCreateWindowExW               = user32.NewProc("CreateWindowExW")
	pDefWindowProcW                = user32.NewProc("DefWindowProcW")
	pGetMessageW                   = user32.NewProc("GetMessageW")
	pTranslateMessage              = user32.NewProc("TranslateMessage")
	pDispatchMessageW              = user32.NewProc("DispatchMessageW")
	pIsDialogMessageW              = user32.NewProc("IsDialogMessageW")
	pPostMessageW                  = user32.NewProc("PostMessageW")
	pSendMessageW                  = user32.NewProc("SendMessageW")
	pShowWindow                    = user32.NewProc("ShowWindow")
	pSetForegroundWindow           = user32.NewProc("SetForegroundWindow")
	pIsWindowVisible               = user32.NewProc("IsWindowVisible")
	pAllowSetForegroundWindow      = user32.NewProc("AllowSetForegroundWindow")
	pSetWindowTextW                = user32.NewProc("SetWindowTextW")
	pFindWindowW                   = user32.NewProc("FindWindowW")
	pLoadCursorW                   = user32.NewProc("LoadCursorW")
	pLoadImageW                    = user32.NewProc("LoadImageW")
	pAdjustWindowRectEx            = user32.NewProc("AdjustWindowRectEx")
	pGetSystemMetrics              = user32.NewProc("GetSystemMetrics")
	pGetDpiForSystem               = user32.NewProc("GetDpiForSystem")
	pSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	pCreateFontW                   = gdi32.NewProc("CreateFontW")
	pGetStockObject                = gdi32.NewProc("GetStockObject")
	pSetBkColor                    = gdi32.NewProc("SetBkColor")
	pCreateActCtxW                 = kernel32.NewProc("CreateActCtxW")
	pActivateActCtx                = kernel32.NewProc("ActivateActCtx")
)

const (
	wsCaption      = 0x00C00000
	wsSysMenu      = 0x00080000
	wsMinimizeBox  = 0x00020000
	wsChild        = 0x40000000
	wsVisible      = 0x10000000
	wsVScroll      = 0x00200000
	wsTabStop      = 0x00010000
	wsExClientEdge = 0x00000200

	esMultiline   = 0x0004
	esAutoVScroll = 0x0040
	esReadOnly    = 0x0800
	bsPushButton  = 0x0000

	wmDestroy        = 0x0002
	wmClose          = 0x0010
	wmSetFont        = 0x0030
	wmSetIcon        = 0x0080
	wmCommand        = 0x0111
	wmCtlColorStatic = 0x0138
	wmApp            = 0x8000
	wmAppShow        = wmApp + 1 // bring the window up (also sent by a second `duolink tray`)
	wmAppState       = wmApp + 2 // new State to display

	bnClicked = 0

	swHide       = 0
	swShowNormal = 1

	cwUseDefault = 0x80000000
	colorBtnFace = 15
	idcArrow     = 32512

	imageIcon      = 1
	lrLoadFromFile = 0x10
	iconSmall      = 0
	iconBig        = 1
	smCxIcon       = 11
	smCxSmIcon     = 49

	fwNormal         = 400
	fwSemiBold       = 600
	defaultCharset   = 1
	clearTypeQuality = 5
	whiteBrush       = 0
	white            = 0x00FFFFFF

	asfwAny = ^uintptr(0) // ASFW_ANY: let any process take the foreground
)

type wndClassEx struct {
	size       uint32
	style      uint32
	wndProc    uintptr
	clsExtra   int32
	wndExtra   int32
	instance   windows.Handle
	icon       windows.Handle
	cursor     windows.Handle
	background windows.Handle
	menuName   *uint16
	className  *uint16
	iconSm     windows.Handle
}

type msg struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      struct{ x, y int32 }
	private uint32
}

type rect struct{ left, top, right, bottom int32 }

// actCtx is ACTCTXW, for activating a manifest that requests Common
// Controls 6 (the current Windows look for buttons).
type actCtx struct {
	size                  uint32
	flags                 uint32
	source                *uint16
	processorArchitecture uint16
	langID                uint16
	assemblyDirectory     *uint16
	resourceName          *uint16
	applicationName       *uint16
	module                uintptr
}

func utf16(s string) *uint16 {
	p, _ := windows.UTF16PtrFromString(s)
	return p
}

func ptr[T any](p *T) uintptr { return uintptr(unsafe.Pointer(p)) }
