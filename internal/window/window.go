// Package window is duolink's small status window: live per-line stats,
// recent events, and Pause/Start, Open log and Quit buttons. Closing it (X)
// only hides it; the tray app keeps running until Quit.
//
// It's a plain Win32 window (no GUI framework, no cgo) running its own
// message loop on a locked OS thread. Other goroutines talk to it only by
// posting messages, so nothing ever blocks the window or races with it.
package window

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// className identifies duolink's window; a second `duolink tray` finds the
// running one by it.
const className = "duolinkStatusWindow"

// Actions are what the buttons do. They're called on their own goroutine,
// never on the window thread, so they may block (Quit waits for cleanup).
type Actions struct {
	Toggle  func()
	OpenLog func()
	Quit    func()
}

// Window is the status window. Show and Update are safe from any goroutine.
type Window struct {
	act   Actions
	hooks SettingsHooks
	icon  []byte
	hwnd  uintptr

	mu      sync.Mutex
	state   State // latest state to display, read on the window thread
	saveErr error // result of the last settings Save

	set *settingsUI // the settings window, created on first use (window thread only)

	status, text, toggle uintptr // control handles
}

// Control IDs for WM_COMMAND.
const (
	idToggle   = 101
	idLog      = 102
	idQuit     = 103
	idSettings = 104
)

// New creates the window, hidden, and starts its message loop. hooks connect
// the settings window; icon is .ico data for the title bars and taskbar.
func New(act Actions, hooks SettingsHooks, icon []byte) (*Window, error) {
	w := &Window{act: act, hooks: hooks, icon: icon}
	ready := make(chan error, 1)
	go w.loop(icon, ready)
	return w, <-ready
}

// Show brings the window up and to the front.
func (w *Window) Show() { pPostMessageW.Call(w.hwnd, wmAppShow, 0, 0) }

// Update hands the window a new state to display.
func (w *Window) Update(s State) {
	w.mu.Lock()
	w.state = s
	w.mu.Unlock()
	pPostMessageW.Call(w.hwnd, wmAppState, 0, 0)
}

// ShowExisting asks an already running duolink tray to show its window.
// It reports whether one was found. FindWindow looks the window up by its
// class name; AllowSetForegroundWindow lets that other process come to the
// front (Windows otherwise only lets the active process do that).
func ShowExisting() bool {
	h, _, _ := pFindWindowW.Call(ptr(utf16(className)), 0)
	if h == 0 {
		return false
	}
	pAllowSetForegroundWindow.Call(asfwAny)
	pPostMessageW.Call(h, wmAppShow, 0, 0)
	return true
}

// EnableDPIAwareness tells Windows this process scales its own windows
// (system DPI), so text is drawn sharp instead of being bitmap-stretched on
// 125 %/150 % displays. It must run before any window exists, including the
// tray's own.
func EnableDPIAwareness() {
	const dpiAwarenessContextSystemAware = ^uintptr(1) // DPI_AWARENESS_CONTEXT_SYSTEM_AWARE (-2)
	pSetProcessDpiAwarenessContext.Call(dpiAwarenessContextSystemAware)
}

// loop creates the window and runs its message loop. A Win32 window belongs
// to the thread that created it, so this goroutine stays on one OS thread.
func (w *Window) loop(icon []byte, ready chan<- error) {
	runtime.LockOSThread()
	enableVisualStyles()

	instance, err := moduleHandle()
	if err != nil {
		ready <- err
		return
	}
	cursor, _, _ := pLoadCursorW.Call(0, idcArrow)
	wc := wndClassEx{
		wndProc:    windows.NewCallback(w.wndProc),
		instance:   instance,
		cursor:     windows.Handle(cursor),
		background: windows.Handle(colorBtnFace + 1), // the standard dialog colour
		className:  utf16(className),
	}
	wc.size = uint32(unsafe.Sizeof(wc))
	if r, _, err := pRegisterClassExW.Call(ptr(&wc)); r == 0 {
		ready <- fmt.Errorf("RegisterClassEx: %w", err)
		return
	}

	// Layout in 96-DPI units, scaled to the system DPI.
	dpi, _, _ := pGetDpiForSystem.Call()
	s := func(v int) int32 { return int32(v * int(dpi) / 96) }
	const clientW, clientH = 720, 420
	style := uintptr(wsCaption | wsSysMenu | wsMinimizeBox)
	r := rect{0, 0, s(clientW), s(clientH)}
	pAdjustWindowRectEx.Call(ptr(&r), style, 0, 0)
	hwnd, _, err := pCreateWindowExW.Call(0, ptr(utf16(className)), ptr(utf16("duolink")), style,
		cwUseDefault, cwUseDefault, uintptr(r.right-r.left), uintptr(r.bottom-r.top), 0, 0, uintptr(instance), 0)
	if hwnd == 0 {
		ready <- fmt.Errorf("CreateWindowEx: %w", err)
		return
	}
	w.hwnd = hwnd
	setIcon(hwnd, icon)

	child := func(class, text string, style uintptr, exStyle uintptr, x, y, cw, ch int, id uintptr) uintptr {
		h, _, _ := pCreateWindowExW.Call(exStyle, ptr(utf16(class)), ptr(utf16(text)),
			wsChild|wsVisible|style, uintptr(s(x)), uintptr(s(y)), uintptr(s(cw)), uintptr(s(ch)), hwnd, id, uintptr(instance), 0)
		return h
	}
	ui := font("Segoe UI", 9, fwNormal, dpi)
	bold := font("Segoe UI", 12, fwSemiBold, dpi)
	mono := font("Consolas", 10, fwNormal, dpi)

	w.status = child("STATIC", "Starting…", 0, 0, 14, 12, 692, 26, 0)
	w.text = child("EDIT", "", esMultiline|esReadOnly|esAutoVScroll|wsVScroll, wsExClientEdge, 14, 46, 692, 318, 0)
	w.toggle = child("BUTTON", "Pause", bsPushButton|wsTabStop, 0, 14, 376, 100, 30, idToggle)
	logBtn := child("BUTTON", "Open log", bsPushButton|wsTabStop, 0, 122, 376, 100, 30, idLog)
	setBtn := child("BUTTON", "Settings", bsPushButton|wsTabStop, 0, 230, 376, 100, 30, idSettings)
	quitBtn := child("BUTTON", "Quit", bsPushButton|wsTabStop, 0, 606, 376, 100, 30, idQuit)
	for h, f := range map[uintptr]uintptr{w.status: bold, w.text: mono, w.toggle: ui, logBtn: ui, setBtn: ui, quitBtn: ui} {
		pSendMessageW.Call(h, wmSetFont, f, 1)
	}
	ready <- nil

	var m msg
	for {
		r, _, _ := pGetMessageW.Call(ptr(&m), 0, 0, 0)
		if int32(r) <= 0 { // WM_QUIT or error
			return
		}
		// IsDialogMessage gives the controls Tab and Enter/Space handling,
		// in whichever of our windows the message is for.
		if d, _, _ := pIsDialogMessageW.Call(hwnd, ptr(&m)); d != 0 {
			continue
		}
		if w.set != nil {
			if d, _, _ := pIsDialogMessageW.Call(w.set.hwnd, ptr(&m)); d != 0 {
				continue
			}
		}
		pTranslateMessage.Call(ptr(&m))
		pDispatchMessageW.Call(ptr(&m))
	}
}

func (w *Window) wndProc(hwnd, message, wParam, lParam uintptr) uintptr {
	switch message {
	case wmClose:
		// X only hides the window; the tray app keeps running until Quit.
		pShowWindow.Call(hwnd, swHide)
		return 0
	case wmAppShow:
		pShowWindow.Call(hwnd, swShowNormal)
		pSetForegroundWindow.Call(hwnd)
		return 0
	case wmAppSettings:
		w.openSettings()
		return 0
	case wmAppSaved:
		w.saved()
		return 0
	case wmAppState:
		w.mu.Lock()
		s := w.state
		w.mu.Unlock()
		pSetWindowTextW.Call(w.status, ptr(utf16(s.Status)))
		pSetWindowTextW.Call(w.text, ptr(utf16(Format(s))))
		pSetWindowTextW.Call(w.toggle, ptr(utf16(s.ToggleLabel)))
		return 0
	case wmCtlColorStatic:
		// A read-only edit control asks its parent for colours like a label
		// does and would get the grey dialog background; the stats table
		// reads better on white.
		if lParam == w.text {
			pSetBkColor.Call(wParam, white)
			b, _, _ := pGetStockObject.Call(whiteBrush)
			return b
		}
	case wmCommand:
		if wParam>>16 == bnClicked {
			var f func()
			switch wParam & 0xFFFF {
			case idToggle:
				f = w.act.Toggle
			case idLog:
				f = w.act.OpenLog
			case idQuit:
				f = w.act.Quit
			case idSettings:
				w.openSettings() // window thread already; nothing blocks
			}
			if f != nil {
				go f() // never block the window thread
			}
		}
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(hwnd, message, wParam, lParam)
	return r
}

func moduleHandle() (windows.Handle, error) {
	var h windows.Handle
	err := windows.GetModuleHandleEx(0, nil, &h)
	return h, err
}

// font creates a GDI font of pt points at the given DPI.
func font(face string, pt, weight int, dpi uintptr) uintptr {
	height := -int32(pt * int(dpi) / 72)
	f, _, _ := pCreateFontW.Call(uintptr(height), 0, 0, 0, uintptr(weight), 0, 0, 0,
		defaultCharset, 0, 0, clearTypeQuality, 0, ptr(utf16(face)))
	return f
}

// setIcon gives the window the tray's icon (title bar and taskbar). LoadImage
// reads .ico only from a file, so the bytes go to a temp file first.
func setIcon(hwnd uintptr, icon []byte) {
	if len(icon) == 0 {
		return
	}
	p := filepath.Join(os.TempDir(), "duolink-window.ico")
	if err := os.WriteFile(p, icon, 0o644); err != nil {
		return
	}
	for _, which := range []struct{ kind, metric uintptr }{{iconBig, smCxIcon}, {iconSmall, smCxSmIcon}} {
		size, _, _ := pGetSystemMetrics.Call(which.metric)
		h, _, _ := pLoadImageW.Call(0, ptr(utf16(p)), imageIcon, size, size, lrLoadFromFile)
		if h != 0 {
			pSendMessageW.Call(hwnd, wmSetIcon, which.kind, h)
		}
	}
}

// enableVisualStyles activates, for this thread, a manifest that asks for
// Common Controls 6, so buttons get the current Windows look instead of the
// Windows 95 one. An exe normally embeds this manifest; we don't have a
// resource compiler in the build, so CreateActCtx loads it from a file.
func enableVisualStyles() error {
	const manifest = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<assembly xmlns="urn:schemas-microsoft-com:asm.v1" manifestVersion="1.0">
  <dependency>
    <dependentAssembly>
      <assemblyIdentity type="win32" name="Microsoft.Windows.Common-Controls" version="6.0.0.0"
        processorArchitecture="*" publicKeyToken="6595b64144ccf1df" language="*"/>
    </dependentAssembly>
  </dependency>
</assembly>
`
	p := filepath.Join(os.TempDir(), "duolink-controls.manifest")
	if err := os.WriteFile(p, []byte(manifest), 0o644); err != nil {
		return err
	}
	ctx := actCtx{source: utf16(p)}
	ctx.size = uint32(unsafe.Sizeof(ctx))
	h, _, err := pCreateActCtxW.Call(ptr(&ctx))
	if h == 0 || h == ^uintptr(0) { // INVALID_HANDLE_VALUE
		return fmt.Errorf("CreateActCtx: %w", err)
	}
	var cookie uintptr
	if r, _, err := pActivateActCtx.Call(h, ptr(&cookie)); r == 0 {
		return fmt.Errorf("ActivateActCtx: %w", err)
	}
	return nil
}
