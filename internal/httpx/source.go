package httpx

import (
	"fmt"
	"log/slog"
	"sync"
)

// noted holds the source notes logged in this process.
var noted sync.Map

// NoteSource logs at info level, once per process for the same msg and attributes, where deps
// takes credentials or settings from: `credentials host=github.com source="gh auth token"`,
// `read config file=/home/u/.npmrc datasource=npm`. The attributes never carry a secret.
func NoteSource(msg string, attrs ...any) {
	if _, dup := noted.LoadOrStore(fmt.Sprint(append([]any{msg}, attrs...)...), true); dup {
		return
	}
	slog.Info(msg, attrs...)
}
