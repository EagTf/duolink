package guiexe

import (
	"bytes"
	"debug/pe"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// The test binary itself is a real console PE image, so these tests patch a
// copy of it and check the result with the standard library's PE parser.

func testExe(t *testing.T) string {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return exe
}

func subsystem(t *testing.T, path string) uint16 {
	t.Helper()
	f, err := pe.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	switch h := f.OptionalHeader.(type) {
	case *pe.OptionalHeader64:
		return h.Subsystem
	case *pe.OptionalHeader32:
		return h.Subsystem
	}
	t.Fatal("no optional header")
	return 0
}

func TestUpdateMakesWindowedCopy(t *testing.T) {
	src := testExe(t)
	if got := subsystem(t, src); got != subsystemConsole {
		t.Fatalf("test binary subsystem = %d, want console (%d)", got, subsystemConsole)
	}
	dst := filepath.Join(t.TempDir(), "duolinkw.exe")

	wrote, err := Update(src, dst)
	if err != nil || !wrote {
		t.Fatalf("first Update: wrote=%v err=%v", wrote, err)
	}
	if got := subsystem(t, dst); got != subsystemGUI {
		t.Errorf("copy subsystem = %d, want GUI (%d)", got, subsystemGUI)
	}
	if gui, err := IsGUI(dst); err != nil || !gui {
		t.Errorf("IsGUI(copy) = %v, %v", gui, err)
	}
	if gui, err := IsGUI(src); err != nil || gui {
		t.Errorf("IsGUI(original) = %v, %v", gui, err)
	}

	// Only the subsystem field differs.
	a, _ := os.ReadFile(src)
	b, _ := os.ReadFile(dst)
	if len(a) != len(b) {
		t.Fatalf("sizes differ: %d vs %d", len(a), len(b))
	}
	diff := 0
	for i := range a {
		if a[i] != b[i] {
			diff++
		}
	}
	if diff != 1 {
		t.Errorf("%d bytes differ, want exactly 1", diff)
	}

	// A second Update with the same source changes nothing.
	if wrote, err := Update(src, dst); err != nil || wrote {
		t.Errorf("second Update: wrote=%v err=%v, want no write", wrote, err)
	}
}

func TestUpdateReplacesStaleCopy(t *testing.T) {
	src := testExe(t)
	dst := filepath.Join(t.TempDir(), "duolinkw.exe")
	if err := os.WriteFile(dst, []byte("an older build"), 0o755); err != nil {
		t.Fatal(err)
	}
	if wrote, err := Update(src, dst); err != nil || !wrote {
		t.Fatalf("Update over stale copy: wrote=%v err=%v", wrote, err)
	}
	if gui, err := IsGUI(dst); err != nil || !gui {
		t.Errorf("IsGUI = %v, %v", gui, err)
	}
}

func TestNotAnExe(t *testing.T) {
	if _, err := subsystemOffset([]byte("hello, this is not a PE file at all, just text......................")); err == nil {
		t.Error("expected an error for non-PE data")
	}
	if _, err := subsystemOffset(bytes.Repeat([]byte{'M', 'Z'}, 40)); err == nil {
		t.Error("expected an error for MZ without a PE header")
	}
}

func TestWindowedPath(t *testing.T) {
	if got := WindowedPath(`C:\tools\duolink.exe`); got != `C:\tools\duolinkw.exe` {
		t.Errorf("WindowedPath = %q", got)
	}
}

// TestHelperSleep is not a real test: TestUpdateWhileRunning starts the
// windowed copy of this test binary with it, to have a running copy.
func TestHelperSleep(t *testing.T) {
	if os.Getenv("GUIEXE_SLEEP") != "1" {
		t.Skip("helper for TestUpdateWhileRunning")
	}
	time.Sleep(10 * time.Second)
}

func TestUpdateWhileRunning(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "duolinkw.exe")
	if _, err := Update(testExe(t), dst); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(dst, "-test.run=^TestHelperSleep$")
	cmd.Env = append(os.Environ(), "GUIEXE_SLEEP=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	time.Sleep(300 * time.Millisecond)

	// A "new build": same program plus trailing bytes (still a valid exe).
	img, _ := os.ReadFile(testExe(t))
	src := filepath.Join(dir, "duolink.exe")
	if err := os.WriteFile(src, append(img, "newer build"...), 0o755); err != nil {
		t.Fatal(err)
	}
	if wrote, err := Update(src, dst); err != nil || !wrote {
		t.Fatalf("Update while running: wrote=%v err=%v", wrote, err)
	}
	if _, err := os.Stat(dst + ".old"); err != nil {
		t.Errorf("running copy should have been moved to .old: %v", err)
	}
	if gui, err := IsGUI(dst); err != nil || !gui {
		t.Errorf("new copy IsGUI = %v, %v", gui, err)
	}
	if b, _ := os.ReadFile(dst); !bytes.HasSuffix(b, []byte("newer build")) {
		t.Error("dst is not the new build")
	}
}
