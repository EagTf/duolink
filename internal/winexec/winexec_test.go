package winexec

import (
	"strings"
	"testing"
)

// Output is still captured without a console window.
func TestCommandCapturesOutput(t *testing.T) {
	out, err := Command("cmd", "/c", "echo duolink-test").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "duolink-test") {
		t.Fatalf("output %q, err %v", out, err)
	}
}
