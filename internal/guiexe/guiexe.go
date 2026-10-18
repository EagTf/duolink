// Package guiexe makes a "windowed" copy of the running console exe.
//
// Windows decides from a field in the exe's PE header (Subsystem) whether a
// program gets a console window: 3 = console, 2 = windowed (GUI). duolink.exe
// is a console program so the CLI works; for the tray we copy it to
// duolinkw.exe and change only that field, so the same code starts without
// any console window, also at logon.
package guiexe

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	subsystemGUI     = 2 // IMAGE_SUBSYSTEM_WINDOWS_GUI
	subsystemConsole = 3 // IMAGE_SUBSYSTEM_WINDOWS_CUI
)

// subsystemOffset finds the Subsystem field in a PE image:
// DOS header → e_lfanew (offset 0x3C) → "PE\0\0" signature → 20-byte COFF
// header → optional header, where Subsystem sits at offset 68 for both
// PE32 and PE32+.
func subsystemOffset(img []byte) (int, error) {
	if len(img) < 0x40 || img[0] != 'M' || img[1] != 'Z' {
		return 0, errors.New("not an exe (no MZ header)")
	}
	pe := int(binary.LittleEndian.Uint32(img[0x3C:]))
	if pe < 0 || pe+24+70 > len(img) || !bytes.Equal(img[pe:pe+4], []byte("PE\x00\x00")) {
		return 0, errors.New("not a PE image")
	}
	opt := pe + 24
	if magic := binary.LittleEndian.Uint16(img[opt:]); magic != 0x10b && magic != 0x20b {
		return 0, fmt.Errorf("unknown optional header magic %#x", magic)
	}
	return opt + 68, nil
}

// IsGUI reports whether the exe at path is a windowed (GUI subsystem) program.
func IsGUI(path string) (bool, error) {
	img, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	off, err := subsystemOffset(img)
	if err != nil {
		return false, err
	}
	return binary.LittleEndian.Uint16(img[off:]) == subsystemGUI, nil
}

// WindowedPath is where the windowed copy of exe lives: duolink.exe →
// duolinkw.exe (the same "w" convention as python.exe / pythonw.exe).
func WindowedPath(exe string) string {
	ext := filepath.Ext(exe)
	return strings.TrimSuffix(exe, ext) + "w" + ext
}

// Update makes dst a windowed copy of src, if it isn't one already. It
// returns whether it wrote a new copy.
//
// If dst is running (the tray is open), it can't be overwritten, but Windows
// does allow renaming a running exe. So the old file is renamed to
// dst + ".old" first and removed on a later update; the running tray keeps
// working, and the next start uses the new copy.
func Update(src, dst string) (bool, error) {
	img, err := os.ReadFile(src)
	if err != nil {
		return false, err
	}
	off, err := subsystemOffset(img)
	if err != nil {
		return false, fmt.Errorf("%s: %w", src, err)
	}
	binary.LittleEndian.PutUint16(img[off:], subsystemGUI)

	if cur, err := os.ReadFile(dst); err == nil && bytes.Equal(cur, img) {
		return false, nil // already up to date
	}
	old := dst + ".old"
	os.Remove(old) // left over from an earlier update; fails harmlessly if still running

	tmp := dst + ".new"
	if err := os.WriteFile(tmp, img, 0o755); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, dst); err != nil {
		// dst is probably running: move it aside, then try again.
		if err2 := os.Rename(dst, old); err2 != nil {
			os.Remove(tmp)
			return false, fmt.Errorf("replace %s: %w (is it running from somewhere that can't be renamed?)", dst, err)
		}
		if err := os.Rename(tmp, dst); err != nil {
			os.Remove(tmp)
			return false, err
		}
	}
	return true, nil
}
