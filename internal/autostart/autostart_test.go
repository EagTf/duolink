package autostart

import (
	"encoding/xml"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestTaskXML(t *testing.T) {
	s := taskXML(`PC\someone`, `C:\Tools & Co\duolinkw.exe`, "tray")

	// Well-formed XML (the declaration says UTF-16; the Go string is UTF-8,
	// so parse without the declaration).
	body := s[strings.Index(s, "<Task"):]
	if err := xml.Unmarshal([]byte(body), new(struct{})); err != nil {
		t.Fatalf("not well-formed: %v", err)
	}
	for _, want := range []string{
		`<UserId>PC\someone</UserId>`,
		`<RunLevel>HighestAvailable</RunLevel>`,
		`<LogonType>InteractiveToken</LogonType>`,
		`<DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>`,
		`<StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>`,
		`<ExecutionTimeLimit>PT0S</ExecutionTimeLimit>`,
		`<MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>`,
		`<Command>C:\Tools &amp; Co\duolinkw.exe</Command>`, // escaped
		`<Arguments>tray</Arguments>`,
		`<WorkingDirectory>C:\Tools &amp; Co</WorkingDirectory>`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("task XML lacks %s", want)
		}
	}
}

func TestUTF16File(t *testing.T) {
	b := utf16File("<a>é</a>")
	if b[0] != 0xFF || b[1] != 0xFE {
		t.Fatalf("missing UTF-16 LE BOM: % x", b[:2])
	}
	u := make([]uint16, (len(b)-2)/2)
	for i := range u {
		u[i] = uint16(b[2+2*i]) | uint16(b[3+2*i])<<8
	}
	if got := string(utf16.Decode(u)); got != "<a>é</a>" {
		t.Errorf("round trip = %q", got)
	}
}
