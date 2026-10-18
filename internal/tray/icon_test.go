package tray

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestICOLayout(t *testing.T) {
	ico := makeIcon(colorRunning)
	if binary.LittleEndian.Uint16(ico[2:]) != 1 || binary.LittleEndian.Uint16(ico[4:]) != 2 {
		t.Fatalf("bad ICONDIR: % x", ico[:6])
	}
	for i, want := range []int{16, 32} {
		e := ico[6+16*i:]
		size, off := int(binary.LittleEndian.Uint32(e[8:])), int(binary.LittleEndian.Uint32(e[12:]))
		if int(e[0]) != want || off+size > len(ico) {
			t.Fatalf("entry %d: width %d, offset %d+%d of %d", i, e[0], off, size, len(ico))
		}
		hdr := ico[off:]
		if binary.LittleEndian.Uint32(hdr) != 40 || int(int32(binary.LittleEndian.Uint32(hdr[8:]))) != 2*want {
			t.Errorf("entry %d: bad BITMAPINFOHEADER", i)
		}
	}
}

// Let Windows itself load the icon, as the tray library does (LoadImage
// from a file). Read-only: nothing is shown or changed.
func TestWindowsLoadsIcon(t *testing.T) {
	path := filepath.Join(t.TempDir(), "duolink.ico")
	if err := os.WriteFile(path, makeIcon(colorWarning), 0o644); err != nil {
		t.Fatal(err)
	}
	user32 := windows.NewLazySystemDLL("user32.dll")
	loadImage, destroyIcon := user32.NewProc("LoadImageW"), user32.NewProc("DestroyIcon")
	const imageIcon, lrLoadFromFile, lrDefaultSize = 1, 0x10, 0x40
	p, _ := windows.UTF16PtrFromString(path)
	h, _, err := loadImage.Call(0, uintptr(unsafe.Pointer(p)), imageIcon, 0, 0, lrLoadFromFile|lrDefaultSize)
	if h == 0 {
		t.Fatalf("LoadImage rejected the icon: %v", err)
	}
	destroyIcon.Call(h)
}
