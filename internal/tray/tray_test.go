package tray

import (
	"errors"
	"strings"
	"testing"
)

func TestAfterFailure(t *testing.T) {
	err := errors.New("need at least 2 usable uplinks, found 1\nmore detail")
	cases := []struct {
		name           string
		want, canRetry bool
		status         string // substring
		retry          bool
	}{
		{"paused while starting", false, true, "Paused", false},
		{"paused, not retryable", false, false, "Paused", false},
		{"not ready yet", true, true, "(retrying)", true},
		{"failed after changing routes", true, false, "Start tries again", false},
	}
	for _, c := range cases {
		status, retry := afterFailure(c.want, c.canRetry, err)
		if retry != c.retry || !strings.Contains(status, c.status) {
			t.Errorf("%s: got %q retry=%v, want %q retry=%v", c.name, status, retry, c.status, c.retry)
		}
		if strings.Contains(status, "more detail") {
			t.Errorf("%s: status should show only the first line: %q", c.name, status)
		}
	}
}
