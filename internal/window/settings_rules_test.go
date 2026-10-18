package window

import (
	"strings"
	"testing"
	"unsafe"

	"github.com/eagtf/duolink/internal/config"
)

func TestRowsFromConfig(t *testing.T) {
	pc := config.ProcessConfig{
		StickyExempt: []string{"steam.exe"},
		Exclude:      []string{"backup.exe"},
		Pin:          map[string]string{"discord.exe": "eth", "game.exe": "phone"},
	}
	rows := rowsFromConfig(pc, lines)
	want := []ruleRow{
		{Exe: "backup.exe", Kind: ruleExclude},
		{Exe: "discord.exe", Kind: rulePin, Line: "Ethernet"}, // "eth" shown as the line it matches
		{Exe: "game.exe", Kind: rulePin, Line: "phone"},       // no such line now: kept as written
		{Exe: "steam.exe", Kind: ruleExempt},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows = %+v", rows)
	}
	for i := range want {
		if rows[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, rows[i], want[i])
		}
	}
	// Back to the config's three lists.
	back := rowsToConfig(rows)
	if back.Pin["discord.exe"] != "Ethernet" || back.Pin["game.exe"] != "phone" ||
		back.StickyExempt[0] != "steam.exe" || back.Exclude[0] != "backup.exe" {
		t.Errorf("rowsToConfig = %+v", back)
	}
}

func TestRuleOptions(t *testing.T) {
	rows := []ruleRow{{Exe: "game.exe", Kind: rulePin, Line: "phone"}}
	opts := ruleOptions(lines, rows)
	var labels []string
	for _, o := range opts {
		labels = append(labels, o.Label)
	}
	got := strings.Join(labels, " | ")
	want := "Balance every connection | Never balance (Windows' line) | Pin to Ethernet | Pin to Wi-Fi | Pin to phone (not connected)"
	if got != want {
		t.Errorf("options:\n got %s\nwant %s", got, want)
	}
	if i := optionIndex(opts, ruleRow{Kind: rulePin, Line: "Wi-Fi"}); i != 3 {
		t.Errorf("optionIndex(pin Wi-Fi) = %d", i)
	}
	if i := optionIndex(opts, ruleRow{Kind: rulePin, Line: "phone"}); i != 4 {
		t.Errorf("optionIndex(pin phone) = %d", i)
	}
}

func TestUpsertRule(t *testing.T) {
	rows := []ruleRow{{Exe: "steam.exe", Kind: ruleExempt}, {Exe: "zoom.exe", Kind: ruleExclude}}
	rows, at := upsertRule(rows, ruleRow{Exe: "Discord.exe", Kind: rulePin, Line: "Ethernet"})
	if len(rows) != 3 || rows[at].Exe != "Discord.exe" || at != 0 {
		t.Fatalf("add: %+v at %d", rows, at)
	}
	// Same program, any case: replaced, never two rules for one program.
	rows, at = upsertRule(rows, ruleRow{Exe: "STEAM.EXE", Kind: rulePin, Line: "Wi-Fi"})
	if len(rows) != 3 || rows[at] != (ruleRow{Exe: "STEAM.EXE", Kind: rulePin, Line: "Wi-Fi"}) {
		t.Errorf("update: %+v at %d", rows, at)
	}
	// Whatever the list holds is valid config: no program in two lists.
	c := config.Default()
	c.Processes = rowsToConfig(rows)
	if err := c.Validate(); err != nil {
		t.Errorf("rules from the list don't validate: %v", err)
	}
}

func TestExeName(t *testing.T) {
	for in, want := range map[string]string{
		"game.exe":                       "game.exe",
		`  "C:\Games\Apex\r5apex.exe"  `: "r5apex.exe", // a path from the picker or pasted
		"Discord.EXE":                    "Discord.EXE",
	} {
		if got, err := exeName(in); err != nil || got != want {
			t.Errorf("exeName(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "   ", "game", "notes.txt"} {
		if _, err := exeName(bad); err == nil {
			t.Errorf("exeName(%q): expected an error", bad)
		}
	}
}

// OPENFILENAMEW is 152 bytes on 64-bit Windows; a wrong layout would make
// GetOpenFileNameW fail or read garbage.
func TestOpenFileNameSize(t *testing.T) {
	if got := unsafe.Sizeof(openFileName{}); got != 152 {
		t.Errorf("sizeof(openFileName) = %d, want 152", got)
	}
}
