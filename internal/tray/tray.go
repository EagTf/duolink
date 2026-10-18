// Package tray is duolink's system tray app: an icon whose colour shows the
// state, live speeds in the tooltip and menu, Pause/Start, "Start with
// Windows", the log, and Quit (which cleans up like Ctrl+C in the CLI).
package tray

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"fyne.io/systray"
	"golang.org/x/sys/windows"

	"github.com/eagtf/duolink/internal/autostart"
	"github.com/eagtf/duolink/internal/config"
	"github.com/eagtf/duolink/internal/engine"
)

// Options for the tray.
type Options struct {
	ConfigPath    string // duolink.json; re-read on every Start
	LogPath       string // shown by "Open log"
	AutostartExe  string // what the logon task runs (the windowed duolinkw.exe)
	AutostartArgs string
}

const (
	retryEvery = 10 * time.Second // retry interval while Start keeps failing
	maxLines   = 4                // menu rows reserved for uplinks
)

type app struct {
	o Options

	mu      sync.Mutex
	want    bool           // the user wants it running (Start), or not (Pause)
	eng     *engine.Engine // non-nil while running
	stopped bool           // Quit was chosen

	kick    chan struct{} // wake the controller now
	lastErr string        // last start error, so repeated retries log it once (guarded by mu)
	startMu sync.Mutex    // held while a start is in progress, so Quit can wait for it

	icons                     map[string][]byte
	mStatus, mTotal           *systray.MenuItem
	mLines                    [maxLines]*systray.MenuItem
	mToggle, mAuto, mLog, mQt *systray.MenuItem
}

// Run shows the tray icon and runs duolink until Quit. It blocks.
func Run(o Options) {
	a := &app{
		o:    o,
		want: true, // starting the tray means "run"
		kick: make(chan struct{}, 1),
		icons: map[string][]byte{
			"running": makeIcon(colorRunning),
			"warning": makeIcon(colorWarning),
			"stopped": makeIcon(colorStopped),
		},
	}
	systray.Run(a.onReady, func() {})
}

func (a *app) onReady() {
	systray.SetIcon(a.icons["stopped"])
	systray.SetTooltip("duolink: starting")

	a.mStatus = systray.AddMenuItem("Starting…", "")
	a.mStatus.Disable()
	for i := range a.mLines {
		a.mLines[i] = systray.AddMenuItem("", "")
		a.mLines[i].Disable()
		a.mLines[i].Hide()
	}
	a.mTotal = systray.AddMenuItem("", "")
	a.mTotal.Disable()
	a.mTotal.Hide()
	systray.AddSeparator()
	a.mToggle = systray.AddMenuItem("Pause", "Stop balancing and restore normal routing")
	a.mAuto = systray.AddMenuItemCheckbox("Start with Windows", "Start duolink in the tray at logon (elevated)", autostart.Enabled())
	a.mLog = systray.AddMenuItem("Open log", "")
	systray.AddSeparator()
	a.mQt = systray.AddMenuItem("Quit", "Stop duolink, restore normal routing and close")

	go a.controller()
	go a.clicks()
}

// controller keeps the engine in the state the user wants: it starts it when
// wanted and not running, retrying every retryEvery until it works.
func (a *app) controller() {
	for {
		a.mu.Lock()
		if a.stopped {
			a.mu.Unlock()
			return
		}
		need := a.want && a.eng == nil
		a.mu.Unlock()
		if need {
			a.startMu.Lock()
			a.start()
			a.startMu.Unlock()
		}
		select {
		case <-a.kick:
		case <-time.After(retryEvery):
		}
	}
}

func (a *app) start() {
	cfg, _, err := config.Load(a.o.ConfigPath)
	if err != nil {
		// Nothing changed yet, so retrying is free; it also picks up a fixed file.
		a.failed(fmt.Errorf("config error: %w", err), true)
		return
	}
	a.mStatus.SetTitle("Starting…")
	a.mu.Lock()
	quiet := a.lastErr != "" // a retry of the same failure stays quiet
	a.mu.Unlock()
	if !quiet {
		fmt.Printf("\n=== %s starting\n", time.Now().Format("2006-01-02 15:04:05"))
	}
	eng, err := engine.Start(engine.Options{Config: cfg, OnStats: a.onStats})
	if err != nil {
		a.failed(err, engine.Retryable(err))
		return
	}
	a.mu.Lock()
	a.lastErr = ""
	if !a.want || a.stopped { // paused or quit while starting
		a.mu.Unlock()
		eng.Stop()
		return
	}
	a.eng = eng
	a.mu.Unlock()
	if quiet { // the "starting" header was skipped; say when it finally came up
		fmt.Printf("=== %s started after retrying\n", time.Now().Format("2006-01-02 15:04:05"))
	}
	fmt.Println("running")
	a.mStatus.SetTitle("Running")
	systray.SetIcon(a.icons["running"])
	systray.SetTooltip("duolink: running")
}

// failed handles a failed start: it shows why, and stops the automatic
// retries unless retrying is safe. While retries keep failing with the same
// error, it is logged only once.
func (a *app) failed(err error, canRetry bool) {
	a.mu.Lock()
	status, retry := afterFailure(a.want, canRetry, err)
	if !retry {
		a.want = false // the controller stops; the user's Start tries again
	}
	if err.Error() == a.lastErr {
		err = nil // already logged
	} else {
		a.lastErr = err.Error()
	}
	a.mu.Unlock()
	if !retry {
		a.mToggle.SetTitle("Start")
	}
	a.showStopped(status, err)
}

// afterFailure decides what a failed start means for the tray: the status
// line, and whether the controller keeps retrying.
//   - Paused meanwhile: say Paused; nothing retries.
//   - Failed before changing anything (fewer than 2 lines, another duolink
//     running, a config error): retry every retryEvery; this is what makes
//     the tray come up by itself once Wi-Fi connects after logon.
//   - Failed after the adapter and routes were set up (self-check, say):
//     stop. Retrying would add and remove routes every few seconds and make
//     the connection blip each time.
func afterFailure(want, canRetry bool, err error) (status string, retry bool) {
	switch {
	case !want:
		return "Paused", false
	case canRetry:
		return "Not running: " + firstLine(err) + " (retrying)", true
	}
	return "Not running: " + firstLine(err) + " (see log; Start tries again)", false
}

// stop stops the engine if it runs. Routes go first, so normal internet is
// back before this returns.
func (a *app) stop() {
	a.mu.Lock()
	eng := a.eng
	a.eng = nil
	a.mu.Unlock()
	if eng != nil {
		a.mStatus.SetTitle("Stopping…")
		fmt.Printf("\n=== %s stopping\n", time.Now().Format("2006-01-02 15:04:05"))
		eng.Stop()
	}
}

func (a *app) clicks() {
	for {
		select {
		case <-a.mToggle.ClickedCh:
			a.mu.Lock()
			a.want = !a.want
			want := a.want
			if want {
				a.lastErr = "" // an explicit Start is always logged in full
			}
			a.mu.Unlock()
			if want {
				a.mToggle.SetTitle("Pause")
				a.wake()
			} else {
				a.mToggle.SetTitle("Start")
				a.stop()
				a.showStopped("Paused", nil)
			}
		case <-a.mAuto.ClickedCh:
			a.toggleAutostart()
		case <-a.mLog.ClickedCh:
			exec.Command("notepad.exe", a.o.LogPath).Start()
		case <-a.mQt.ClickedCh:
			a.mu.Lock()
			a.stopped, a.want = true, false
			a.mu.Unlock()
			// A start in progress finishes first (and then stops itself, since
			// want is false), so Quit never leaves a half-set-up engine behind.
			a.startMu.Lock()
			a.stop()
			fmt.Println("quit")
			systray.Quit()
			return
		}
	}
}

func (a *app) toggleAutostart() {
	var err error
	if autostart.Enabled() {
		err = autostart.Disable()
	} else {
		err = autostart.Enable(a.o.AutostartExe, a.o.AutostartArgs)
	}
	if err != nil {
		fmt.Println("autostart:", err)
		messageBox("Couldn't change \"Start with Windows\":\n\n"+err.Error(), windows.MB_ICONWARNING)
	}
	if autostart.Enabled() {
		a.mAuto.Check()
	} else {
		a.mAuto.Uncheck()
	}
}

func (a *app) wake() {
	select {
	case a.kick <- struct{}{}:
	default:
	}
}

// onStats updates the menu, tooltip and icon from an engine sample.
func (a *app) onStats(s engine.Stats) {
	a.mu.Lock()
	running := a.eng != nil
	a.mu.Unlock()
	if !running {
		return // a late sample from an engine that is stopping
	}
	var states []string
	for i := range a.mLines {
		if i >= len(s.Lines) {
			a.mLines[i].Hide()
			continue
		}
		l := s.Lines[i]
		a.mLines[i].SetTitle(fmt.Sprintf("%s: %s   ↓ %.1f  ↑ %.1f Mbit/s", l.Name, l.State, l.DownMbps, l.UpMbps))
		a.mLines[i].Show()
		states = append(states, l.Name+" "+l.State)
	}
	a.mTotal.SetTitle(fmt.Sprintf("Total   ↓ %.1f  ↑ %.1f Mbit/s", s.DownMbps, s.UpMbps))
	a.mTotal.Show()

	icon, status := "running", "Running"
	if s.AnyDown() {
		icon, status = "warning", "Running: a line is DOWN"
	}
	a.mStatus.SetTitle(status)
	systray.SetIcon(a.icons[icon])
	// Windows cuts tray tooltips at 127 characters.
	tip := fmt.Sprintf("duolink ↓ %.1f ↑ %.1f Mbit/s\n%s", s.DownMbps, s.UpMbps, strings.Join(states, ", "))
	if r := []rune(tip); len(r) > 127 {
		tip = string(r[:127])
	}
	systray.SetTooltip(tip)
}

func (a *app) showStopped(status string, err error) {
	if err != nil {
		fmt.Println("error:", err)
	}
	a.mStatus.SetTitle(status)
	for _, m := range a.mLines {
		m.Hide()
	}
	a.mTotal.Hide()
	systray.SetIcon(a.icons["stopped"])
	systray.SetTooltip("duolink: " + strings.ToLower(status[:1]) + status[1:])
}

func firstLine(err error) string {
	s := err.Error()
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

// SingleInstance takes the "tray is running" mutex for this user session.
// It returns false if another tray already holds it. Windows releases the
// mutex when the process exits.
func SingleInstance() (bool, error) {
	name, _ := windows.UTF16PtrFromString(`Local\duolink-tray`)
	_, err := windows.CreateMutex(nil, false, name)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return false, nil
	}
	return err == nil, err
}

// MessageBox shows a simple dialog; the tray has no console to print to.
func messageBox(text string, flags uint32) {
	t, _ := windows.UTF16PtrFromString(text)
	c, _ := windows.UTF16PtrFromString("duolink")
	windows.MessageBox(0, t, c, flags|windows.MB_OK|windows.MB_SETFOREGROUND)
}

// Fatal shows an error dialog, for problems before the tray icon exists.
func Fatal(text string) { messageBox(text, windows.MB_ICONERROR) }
