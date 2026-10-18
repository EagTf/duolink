package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"

	"github.com/eagtf/duolink/internal/autostart"
	"github.com/eagtf/duolink/internal/config"
	"github.com/eagtf/duolink/internal/engine"
	"github.com/eagtf/duolink/internal/guiexe"
	"github.com/eagtf/duolink/internal/tray"
)

const maxLogSize = 1 << 20 // duolink.log starts over beyond 1 MiB

// cmdTray has two jobs, depending on which exe runs it:
//   - duolink.exe (console, from a terminal): refresh duolinkw.exe, the
//     windowed copy, start it, and return. The terminal stays free.
//   - duolinkw.exe (windowed: no console, also at logon): run the tray.
func cmdTray(args []string) error {
	fs := flag.NewFlagSet("tray", flag.ExitOnError)
	cfgPath := fs.String("config", config.DefaultPath(), "config file")
	fs.Parse(args)

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	gui, err := guiexe.IsGUI(exe)
	if err != nil {
		return err
	}
	if !gui {
		return launchTray(exe, args)
	}
	runTray(exe, *cfgPath, args)
	return nil
}

func launchTray(exe string, args []string) error {
	if !engine.IsAdmin() {
		return errors.New("start the tray from an administrator terminal (or use `duolink autostart on` to start it at logon)")
	}
	w, err := refreshWindowed(exe)
	if err != nil {
		return err
	}
	cmd := exec.Command(w, append([]string{"tray"}, args...)...)
	if err := cmd.Start(); err != nil {
		return err
	}
	cmd.Process.Release()
	fmt.Println("duolink is starting in the tray. Log:", logPath(w))
	return nil
}

// refreshWindowed makes sure duolinkw.exe next to exe is the windowed copy of
// this build, and returns its path.
func refreshWindowed(exe string) (string, error) {
	w := guiexe.WindowedPath(exe)
	wrote, err := guiexe.Update(exe, w)
	if err != nil {
		return "", fmt.Errorf("make %s: %w", filepath.Base(w), err)
	}
	if wrote {
		fmt.Printf("updated %s (the windowed copy of %s the tray runs as)\n", filepath.Base(w), filepath.Base(exe))
	}
	return w, nil
}

func runTray(exe, cfgPath string, args []string) {
	openLog(logPath(exe))
	if !engine.IsAdmin() {
		// Started without elevation (e.g. double-clicked): ask for it.
		if err := elevate(exe, append([]string{"tray"}, args...)); err != nil {
			tray.Fatal("duolink needs administrator rights to change routes.\n\n" +
				"Start it with `duolink tray` from an administrator terminal, or turn on \"Start with Windows\".")
		}
		return
	}
	if ok, err := tray.SingleInstance(); !ok {
		if err != nil {
			fmt.Println("error:", err)
		}
		tray.Fatal("duolink is already running in the tray.")
		return
	}
	tray.Run(tray.Options{
		ConfigPath:    cfgPath,
		LogPath:       logPath(exe),
		AutostartExe:  exe,
		AutostartArgs: argLine(append([]string{"tray"}, args...)),
	})
}

// elevate restarts exe with args through the UAC prompt. ShellExecute with
// the "runas" verb is how Windows asks for "Run as administrator".
func elevate(exe string, args []string) error {
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exe)
	params, _ := windows.UTF16PtrFromString(argLine(args))
	dir, _ := windows.UTF16PtrFromString(filepath.Dir(exe))
	return windows.ShellExecute(0, verb, file, params, dir, windows.SW_SHOWNORMAL)
}

func argLine(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = syscall.EscapeArg(a)
	}
	return strings.Join(q, " ")
}

func logPath(exe string) string {
	return filepath.Join(filepath.Dir(exe), "duolink.log")
}

// openLog sends everything duolink prints (engine steps, netsh commands,
// health changes, errors) to duolink.log: the windowed exe has no console.
func openLog(path string) {
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if fi, err := os.Stat(path); err == nil && fi.Size() > maxLogSize {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		return
	}
	os.Stdout, os.Stderr = f, f
	log.SetOutput(f)
	fmt.Printf("\n===== %s duolink tray (pid %d)\n", time.Now().Format("2006-01-02 15:04:05"), os.Getpid())
}

// cmdAutostart turns the logon task on or off, or shows it.
func cmdAutostart(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: duolink autostart on|off|status")
	}
	switch args[0] {
	case "status":
		if autostart.Enabled() {
			fmt.Printf("on: the tray starts at logon (task %q in Task Scheduler)\n", autostart.TaskName)
		} else {
			fmt.Println("off")
		}
		return nil
	case "on", "off":
	default:
		return errors.New("usage: duolink autostart on|off|status")
	}
	if !engine.IsAdmin() {
		return errors.New("duolink autostart on/off must be run as administrator (the task runs elevated)")
	}
	if args[0] == "off" {
		if err := autostart.Disable(); err != nil {
			return err
		}
		fmt.Println("autostart off")
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if gui, _ := guiexe.IsGUI(exe); !gui {
		if exe, err = refreshWindowed(exe); err != nil {
			return err
		}
	}
	if err := autostart.Enable(exe, "tray"); err != nil {
		return err
	}
	fmt.Println("autostart on: the tray starts at logon, elevated, without a console window")
	return nil
}
