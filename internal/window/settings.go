package window

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/eagtf/duolink/internal/config"
)

// SettingsHooks connect the settings window to the app.
type SettingsHooks struct {
	// Load returns the settings to show, the uplinks connected now (one
	// weight field each), and a note to show above the buttons (e.g. that
	// duolink.json had errors and defaults are shown), or "".
	Load func() (c config.Config, lines []string, note string)
	// Save validates c against the real uplinks, writes duolink.json and
	// applies it. It may take seconds (it restarts duolink); it runs on its
	// own goroutine. A non-nil error is shown and nothing is saved.
	Save func(c config.Config) error
}

const settingsClass = "duolinkSettingsWindow"

// Settings window control IDs.
const (
	idSave = 201 + iota
	idCancel
	idDefaults
	idStickyOn
)

// settingsUI holds the settings window's controls. It lives on the window
// thread only.
type settingsUI struct {
	hwnd        uintptr
	lineLabels  [maxFormLines]uintptr
	lineWeights [maxFormLines]uintptr
	lines       []string

	stickyOn, stickyMin                     uintptr
	interval, downInterval, timeout, misses uintptr
	targets, udpIdle                        uintptr
	exempt, exclude, pin                    uintptr
	message, save                           uintptr
	messageColor                            uintptr
}

// ShowSettings opens the settings window with the current settings.
func (w *Window) ShowSettings() { pPostMessageW.Call(w.hwnd, wmAppSettings, 0, 0) }

// openSettings runs on the window thread: create the window the first time,
// then (re)load the current settings into it and show it.
func (w *Window) openSettings() {
	if w.hooks.Load == nil {
		return
	}
	if w.set == nil {
		if err := w.createSettings(); err != nil {
			fmt.Println("settings window:", err)
			return
		}
	}
	c, lines, note := w.hooks.Load()
	w.fillSettings(formFromConfig(c, lines))
	w.setMessage(note, darkRed)
	pShowWindow.Call(w.set.hwnd, swShowNormal)
	pSetForegroundWindow.Call(w.set.hwnd)
}

func (w *Window) createSettings() error {
	instance, err := moduleHandle()
	if err != nil {
		return err
	}
	cursor, _, _ := pLoadCursorW.Call(0, idcArrow)
	wc := wndClassEx{
		wndProc:    windows.NewCallback(w.settingsProc),
		instance:   instance,
		cursor:     windows.Handle(cursor),
		background: windows.Handle(colorBtnFace + 1),
		className:  utf16(settingsClass),
	}
	wc.size = uint32(unsafe.Sizeof(wc))
	if r, _, err := pRegisterClassExW.Call(ptr(&wc)); r == 0 {
		return fmt.Errorf("RegisterClassEx: %w", err)
	}

	dpi, _, _ := pGetDpiForSystem.Call()
	s := func(v int) uintptr { return uintptr(v * int(dpi) / 96) }
	const clientW, clientH = 608, 636
	style := uintptr(wsCaption | wsSysMenu | wsMinimizeBox)
	r := rect{0, 0, int32(s(clientW)), int32(s(clientH))}
	pAdjustWindowRectEx.Call(ptr(&r), style, 0, 0)
	// Owned by the status window: stays in front of it, one taskbar entry.
	hwnd, _, err := pCreateWindowExW.Call(0, ptr(utf16(settingsClass)), ptr(utf16("duolink settings")), style,
		cwUseDefault, cwUseDefault, uintptr(r.right-r.left), uintptr(r.bottom-r.top), w.hwnd, 0, uintptr(instance), 0)
	if hwnd == 0 {
		return fmt.Errorf("CreateWindowEx: %w", err)
	}
	setIcon(hwnd, w.icon)
	u := &settingsUI{hwnd: hwnd}

	ui := font("Segoe UI", 9, fwNormal, dpi)
	bold := font("Segoe UI", 9, fwSemiBold, dpi)
	mono := font("Consolas", 10, fwNormal, dpi)
	ctl := func(class, text string, style uintptr, exStyle uintptr, x, y, cw, ch int, id uintptr, f uintptr) uintptr {
		h, _, _ := pCreateWindowExW.Call(exStyle, ptr(utf16(class)), ptr(utf16(text)),
			wsChild|wsVisible|style, s(x), s(y), s(cw), s(ch), hwnd, id, uintptr(instance), 0)
		pSendMessageW.Call(h, wmSetFont, f, 1)
		return h
	}
	label := func(text string, x, y, cw, ch int) uintptr { return ctl("STATIC", text, 0, 0, x, y, cw, ch, 0, ui) }
	heading := func(text string, y int) { ctl("STATIC", text, 0, 0, 16, y, 576, 18, 0, bold) }
	number := func(x, y int) uintptr {
		return ctl("EDIT", "", esNumber|esAutoHScroll|wsTabStop, wsExClientEdge, x, y, 48, 22, 0, ui)
	}
	text := func(x, y, cw int) uintptr {
		return ctl("EDIT", "", esAutoHScroll|wsTabStop, wsExClientEdge, x, y, cw, 22, 0, ui)
	}
	multi := func(x, y int) uintptr {
		return ctl("EDIT", "", esMultiline|esAutoVScroll|esWantReturn|wsVScroll|wsTabStop, wsExClientEdge, x, y, 184, 104, 0, mono)
	}

	heading("Lines", 12)
	label("Each line's share of new connections. 0 = don't use that line. All at 1 = share equally.", 16, 31, 576, 16)
	for i := range u.lineWeights {
		x, y := 24+(i%2)*292, 52+(i/2)*28
		u.lineLabels[i] = label("", x, y+3, 170, 18)
		u.lineWeights[i] = number(x+176, y)
	}

	heading("Stickiness", 116)
	u.stickyOn = ctl("BUTTON", "Keep each destination on the same line for", bsAutoCheckBox|wsTabStop, 0, 24, 136, 270, 22, idStickyOn, ui)
	u.stickyMin = number(298, 136)
	label("minutes after its last connection", 352, 139, 240, 18)
	label("So websites and game servers see one public IP. \"Balance every connection\" apps ignore this.", 24, 162, 568, 16)

	heading("Health checks", 190)
	label("Check each line every", 24, 213, 150, 18)
	u.interval = number(176, 210)
	label("seconds", 230, 213, 60, 18)
	label("While DOWN, check every", 300, 213, 150, 18)
	u.downInterval = number(452, 210)
	label("seconds", 506, 213, 96, 18)
	label("Timeout", 24, 241, 150, 18)
	u.timeout = number(176, 238)
	label("seconds", 230, 241, 60, 18)
	label("DOWN after", 300, 241, 150, 18)
	u.misses = number(452, 238)
	label("misses in a row", 506, 241, 96, 18)
	label("Targets", 24, 269, 150, 18)
	u.targets = text(176, 266, 416)
	label("A check is a connection to these ip:port addresses through each line; one answer is enough.", 24, 292, 568, 16)

	heading("Connections", 320)
	label("A UDP flow ends after", 24, 343, 150, 18)
	u.udpIdle = number(176, 340)
	label("seconds without packets", 230, 343, 200, 18)

	heading("Per-app rules: one program per line, like steam.exe", 374)
	label("Balance every connection", 16, 396, 184, 16)
	label("Never balance (Windows' line)", 212, 396, 184, 16)
	label("Pin to a line: game.exe = eth", 408, 396, 184, 16)
	u.exempt = multi(16, 414)
	u.exclude = multi(212, 414)
	u.pin = multi(408, 414)

	u.message = ctl("STATIC", "", 0, 0, 16, 528, 576, 54, 0, ui)
	ctl("BUTTON", "Reset to defaults", bsPushButton|wsTabStop, 0, 16, 594, 130, 30, idDefaults, ui)
	ctl("BUTTON", "Cancel", bsPushButton|wsTabStop, 0, 346, 594, 110, 30, idCancel, ui)
	u.save = ctl("BUTTON", "Save and apply", bsPushButton|wsTabStop, 0, 464, 594, 128, 30, idSave, ui)
	w.set = u
	return nil
}

func (w *Window) fillSettings(f form) {
	u := w.set
	u.lines = f.Lines
	for i := range u.lineWeights {
		show := uintptr(swHide)
		if i < len(f.Lines) {
			pSetWindowTextW.Call(u.lineLabels[i], ptr(utf16(f.Lines[i]+" weight")))
			pSetWindowTextW.Call(u.lineWeights[i], ptr(utf16(f.Weights[i])))
			show = swShowNormal
		}
		pShowWindow.Call(u.lineLabels[i], show)
		pShowWindow.Call(u.lineWeights[i], show)
	}
	check := uintptr(0)
	if f.StickyOn {
		check = 1
	}
	pSendMessageW.Call(u.stickyOn, bmSetCheck, check, 0)
	for h, v := range map[uintptr]string{
		u.stickyMin: f.StickyMin, u.interval: f.Interval, u.downInterval: f.DownInterval,
		u.timeout: f.Timeout, u.misses: f.Failures, u.targets: f.Targets, u.udpIdle: f.UDPIdle,
		u.exempt: f.Exempt, u.exclude: f.Exclude, u.pin: f.Pin,
	} {
		pSetWindowTextW.Call(h, ptr(utf16(v)))
	}
}

func (w *Window) readSettings() form {
	u := w.set
	f := form{Lines: u.lines}
	for i := range u.lines {
		f.Weights = append(f.Weights, getText(u.lineWeights[i]))
	}
	on, _, _ := pSendMessageW.Call(u.stickyOn, bmGetCheck, 0, 0)
	f.StickyOn = on == 1
	f.StickyMin = getText(u.stickyMin)
	f.Interval = getText(u.interval)
	f.DownInterval = getText(u.downInterval)
	f.Timeout = getText(u.timeout)
	f.Failures = getText(u.misses)
	f.Targets = getText(u.targets)
	f.UDPIdle = getText(u.udpIdle)
	f.Exempt = getText(u.exempt)
	f.Exclude = getText(u.exclude)
	f.Pin = getText(u.pin)
	return f
}

// setMessage shows a note, error (dark red) or success (dark green) above
// the buttons.
func (w *Window) setMessage(text string, color uintptr) {
	w.set.messageColor = color
	pSetWindowTextW.Call(w.set.message, ptr(utf16(text)))
}

func (w *Window) settingsProc(hwnd, message, wParam, lParam uintptr) uintptr {
	switch message {
	case wmClose:
		pShowWindow.Call(hwnd, swHide) // like Cancel: unsaved edits are dropped on next open
		return 0
	case wmCtlColorStatic:
		if w.set != nil && lParam == w.set.message {
			pSetTextColor.Call(wParam, w.set.messageColor)
			pSetBkMode.Call(wParam, transparent)
			b, _, _ := pGetSysColorBrush.Call(colorBtnFace)
			return b
		}
	case wmCommand:
		if wParam>>16 != bnClicked {
			break
		}
		switch wParam & 0xFFFF {
		case idCancel:
			pShowWindow.Call(hwnd, swHide)
		case idDefaults:
			w.fillSettings(formFromConfig(config.Default(), w.set.lines))
			w.setMessage("Defaults filled in. Nothing is saved until you click Save and apply.", darkGreen)
		case idSave:
			w.saveSettings()
		}
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(hwnd, message, wParam, lParam)
	return r
}

// saveSettings checks the form here (fast), then hands the config to the
// app's Save on its own goroutine (it may restart duolink), and shows the
// result when it's done (wmAppSaved).
func (w *Window) saveSettings() {
	c, err := w.readSettings().toConfig()
	if err != nil {
		w.setMessage("Not saved:\r\n"+err.Error(), darkRed)
		return
	}
	pEnableWindow.Call(w.set.save, 0)
	w.setMessage("Saving…", darkGreen)
	go func() {
		err := w.hooks.Save(c)
		w.mu.Lock()
		w.saveErr = err
		w.mu.Unlock()
		pPostMessageW.Call(w.hwnd, wmAppSaved, 0, 0)
	}()
}

// saved runs on the window thread when a Save finished.
func (w *Window) saved() {
	w.mu.Lock()
	err := w.saveErr
	w.mu.Unlock()
	pEnableWindow.Call(w.set.save, 1)
	if err != nil {
		w.setMessage("Not saved:\r\n"+friendly(err).Error(), darkRed)
		return
	}
	w.setMessage("Saved to duolink.json. duolink restarts with the new settings (if it was running).", darkGreen)
}

func getText(h uintptr) string {
	n, _, _ := pGetWindowTextLengthW.Call(h)
	buf := make([]uint16, n+1)
	pGetWindowTextW.Call(h, ptr(&buf[0]), n+1)
	return windows.UTF16ToString(buf)
}
