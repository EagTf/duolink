// Package autostart registers duolink's tray to start at logon, elevated,
// with a Windows Task Scheduler task.
//
// A logon task with "run with highest privileges" is the standard way to
// start an admin tool at logon without a UAC prompt every time; the Run key
// in the registry can't elevate.
package autostart

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"

	"os/user"
	"path/filepath"
	"strings"
	"unicode/utf16"

	"github.com/eagtf/duolink/internal/winexec"
)

// TaskName is the name in Task Scheduler (taskschd.msc → Task Scheduler Library).
const TaskName = "duolink"

// taskXML builds the task definition. Plain `schtasks /create /sc onlogon`
// flags can't set what a long-running tray needs, so the XML spells it out:
//   - LogonTrigger for this user only, and an interactive logon token, so the
//     tray icon appears on this user's desktop.
//   - RunLevel HighestAvailable: elevated without a UAC prompt.
//   - DisallowStartIfOnBatteries / StopIfGoingOnBatteries false: the defaults
//     would not start it on a laptop on battery, or kill it on unplugging.
//   - ExecutionTimeLimit PT0S: no limit (the default kills tasks after 3 days).
//   - MultipleInstancesPolicy IgnoreNew: never a second copy.
//   - Priority 5: normal process priority (task default 7 is below normal,
//     which would slow down forwarding).
func taskXML(userID, exe, args string) string {
	esc := func(s string) string {
		var b bytes.Buffer
		xml.EscapeText(&b, []byte(s))
		return b.String()
	}
	return `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>Starts duolink in the system tray at logon.</Description>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
      <UserId>` + esc(userID) + `</UserId>
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <UserId>` + esc(userID) + `</UserId>
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>HighestAvailable</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>false</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <IdleSettings>
      <StopOnIdleEnd>false</StopOnIdleEnd>
      <RestartOnIdle>false</RestartOnIdle>
    </IdleSettings>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>false</Hidden>
    <RunOnlyIfIdle>false</RunOnlyIfIdle>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Priority>5</Priority>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>` + esc(exe) + `</Command>
      <Arguments>` + esc(args) + `</Arguments>
      <WorkingDirectory>` + esc(filepath.Dir(exe)) + `</WorkingDirectory>
    </Exec>
  </Actions>
</Task>
`
}

// utf16File encodes s as UTF-16 LE with a byte-order mark, the encoding
// schtasks expects for /xml files.
func utf16File(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 2, 2+2*len(u))
	b[0], b[1] = 0xFF, 0xFE
	for _, c := range u {
		b = append(b, byte(c), byte(c>>8))
	}
	return b
}

// schtasks runs one schtasks command and echoes it, like the netsh calls, so
// the change is visible and can be repeated or undone by hand.
func schtasks(args ...string) (string, error) {
	fmt.Println("  + schtasks", strings.Join(args, " "))
	out, err := winexec.Command("schtasks", args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("schtasks %s: %v: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// Enable creates (or replaces) the logon task that runs `exe args`.
// Creating a task with highest privileges needs an elevated process.
func Enable(exe, args string) error {
	u, err := user.Current()
	if err != nil {
		return err
	}
	f, err := os.CreateTemp("", "duolink-task-*.xml")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(utf16File(taskXML(u.Username, exe, args)))
	f.Close()
	if err != nil {
		return err
	}
	// /f replaces an existing task of the same name.
	_, err = schtasks("/create", "/tn", TaskName, "/xml", f.Name(), "/f")
	return err
}

// Disable deletes the logon task. A task that doesn't exist is not an error.
func Disable() error {
	if !Enabled() {
		return nil
	}
	_, err := schtasks("/delete", "/tn", TaskName, "/f")
	return err
}

// Enabled reports whether the logon task exists. `schtasks /query /tn` only
// reads; it fails when the task doesn't exist.
func Enabled() bool {
	return winexec.Command("schtasks", "/query", "/tn", TaskName).Run() == nil
}
