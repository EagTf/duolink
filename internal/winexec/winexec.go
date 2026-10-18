// Package winexec runs Windows console tools (netsh, schtasks, route,
// powershell) without flashing a console window.
package winexec

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// Command is exec.Command for console tools whose output we capture.
//
// A console program started from a process without a console (the tray's
// windowed duolinkw.exe) gets a brand-new console window, which would flash
// up for every netsh call. CREATE_NO_WINDOW starts it without one; its
// output still arrives through the pipes exec sets up. From the CLI this
// changes nothing visible either.
func Command(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW,
	}
	return cmd
}
