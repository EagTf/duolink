package window

import (
	"testing"
	"time"
)

// Creates the real window (it starts hidden, so nothing appears on screen)
// and checks a second `duolink tray` could find it by its class name.
func TestNewCreatesHiddenFindableWindow(t *testing.T) {
	w, err := New(Actions{}, makeTestIcon())
	if err != nil {
		t.Fatal(err)
	}
	if w.hwnd == 0 || w.status == 0 || w.text == 0 || w.toggle == 0 {
		t.Fatalf("missing handles: %+v", w)
	}
	found, _, _ := pFindWindowW.Call(ptr(utf16(className)), 0)
	if found != w.hwnd {
		t.Errorf("FindWindow by class = %#x, want %#x", found, w.hwnd)
	}
	// Updates are processed on the window thread without blocking the caller.
	w.Update(State{Status: "Running", ToggleLabel: "Pause", Events: []string{"started"}})
	time.Sleep(100 * time.Millisecond)
	if visible, _, _ := pIsWindowVisible.Call(w.hwnd); visible != 0 {
		t.Error("the window should stay hidden until shown")
	}
}

// A minimal valid .ico (1x1, 32-bit) so setIcon has something to load.
func makeTestIcon() []byte {
	return []byte{
		0, 0, 1, 0, 1, 0, // ICONDIR: 1 image
		1, 1, 0, 0, 1, 0, 32, 0, 48, 0, 0, 0, 22, 0, 0, 0, // entry: 1x1, 32bpp, 48 bytes at 22
		40, 0, 0, 0, 1, 0, 0, 0, 2, 0, 0, 0, 1, 0, 32, 0, // BITMAPINFOHEADER...
		0, 0, 0, 0, 8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 255, 255, // one BGRA pixel (red)
		0, 0, 0, 0, // AND mask row
	}
}
