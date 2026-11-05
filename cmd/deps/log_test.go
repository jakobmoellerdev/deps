package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestTextLogRoundsDurations(t *testing.T) {
	defer slog.SetDefault(slog.Default())
	for _, tc := range []struct {
		format string
		d      time.Duration
		want   string
	}{
		{"text", 12345678 * time.Nanosecond, "duration=12.3ms"},
		{"text", 1234567890 * time.Nanosecond, "duration=1.235s"},
		{"json", 12345678 * time.Nanosecond, `"duration":12345678`},
	} {
		var b bytes.Buffer
		if err := setupLogger(logOpts{level: "info", format: tc.format}, &b); err != nil {
			t.Fatal(err)
		}
		slog.Info("x", "duration", tc.d)
		if !strings.Contains(b.String(), tc.want) {
			t.Errorf("%s %v: %q lacks %q", tc.format, tc.d, b.String(), tc.want)
		}
	}
}
