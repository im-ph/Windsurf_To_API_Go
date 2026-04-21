// Package logx is a leveled logger with a ring buffer for the dashboard and
// a fan-out channel for live SSE subscribers. Mirrors dashboard/logger.js —
// JSONL persistence and SSE wiring land in a later phase (they depend on the
// dashboard HTTP surface).
package logx

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Level int8

const (
	LDebug Level = iota
	LInfo
	LWarn
	LError
)

func parseLevel(s string) Level {
	switch strings.ToLower(s) {
	case "debug":
		return LDebug
	case "warn":
		return LWarn
	case "error":
		return LError
	default:
		return LInfo
	}
}

func (l Level) String() string {
	switch l {
	case LDebug:
		return "debug"
	case LWarn:
		return "warn"
	case LError:
		return "error"
	default:
		return "info"
	}
}

// Entry is the structured record kept in the ring buffer and emitted over SSE.
type Entry struct {
	Ts    int64          `json:"ts"`
	Level string         `json:"level"`
	Msg   string         `json:"msg"`
	Ctx   map[string]any `json:"ctx,omitempty"`
}

const ringCap = 1000

var (
	mu          sync.Mutex
	minLevel    Level = LInfo
	ring              = make([]Entry, 0, ringCap)
	subscribers       = map[chan Entry]struct{}{}

	// JSONL persistence — app + error streams rotated by UTC date.
	logDir     = "logs"
	appFile    *os.File
	errFile    *os.File
	streamDate string
)

// SetLogDir is called once at startup by main before emit() ever runs.
// Rejects paths that contain NUL bytes or `..` traversal markers — the only
// caller today is main.go with a fixed "logs" literal, but surface-level
// validation keeps future code from passing in user-controlled paths by
// accident.
func SetLogDir(dir string) {
	if dir == "" {
		dir = "logs"
	}
	if strings.ContainsRune(dir, 0) || strings.Contains(dir, "..") {
		// Silently fall back to "logs" — logger is best-effort; never
		// crash just because a caller passed in something suspicious.
		dir = "logs"
	}
	logDir = dir
	_ = os.MkdirAll(logDir, 0o755)
}

func rotateIfNeeded() {
	now := time.Now().UTC()
	date := fmt.Sprintf("%04d-%02d-%02d", now.Year(), now.Month(), now.Day())
	if date == streamDate && appFile != nil {
		return
	}
	if appFile != nil {
		_ = appFile.Close()
	}
	if errFile != nil {
		_ = errFile.Close()
	}
	_ = os.MkdirAll(logDir, 0o755)
	if f, err := os.OpenFile(filepath.Join(logDir, "app-"+date+".jsonl"), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644); err == nil {
		appFile = f
	}
	if f, err := os.OpenFile(filepath.Join(logDir, "error-"+date+".jsonl"), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644); err == nil {
		errFile = f
	}
	streamDate = date
}

// SetLevel changes the minimum log level at runtime.
func SetLevel(s string) { minLevel = parseLevel(s) }

// Subscribe returns a channel that receives every new log entry until the
// returned cancel func is called. Buffered so a slow reader can miss entries
// rather than stall the producer.
func Subscribe() (<-chan Entry, func()) {
	ch := make(chan Entry, 64)
	mu.Lock()
	subscribers[ch] = struct{}{}
	mu.Unlock()
	cancel := func() {
		mu.Lock()
		if _, ok := subscribers[ch]; ok {
			delete(subscribers, ch)
			close(ch)
		}
		mu.Unlock()
	}
	return ch, cancel
}

// Recent returns up to n most recent entries.
func Recent(n int) []Entry {
	mu.Lock()
	defer mu.Unlock()
	if n <= 0 || n > len(ring) {
		n = len(ring)
	}
	out := make([]Entry, n)
	copy(out, ring[len(ring)-n:])
	return out
}

func emit(level Level, msg string, ctx map[string]any) {
	if level < minLevel {
		return
	}
	e := Entry{
		Ts:    time.Now().UnixMilli(),
		Level: level.String(),
		Msg:   msg,
		Ctx:   ctx,
	}
	mu.Lock()
	if len(ring) == ringCap {
		copy(ring, ring[1:])
		ring = ring[:ringCap-1]
	}
	ring = append(ring, e)
	for ch := range subscribers {
		select {
		case ch <- e:
		default: // drop — slow consumer
		}
	}
	// JSONL persistence under the same lock — keeps rotation atomic.
	rotateIfNeeded()
	if appFile != nil {
		line, _ := json.Marshal(&e)
		line = append(line, '\n')
		_, _ = appFile.Write(line)
		if level == LError || level == LWarn {
			if errFile != nil {
				_, _ = errFile.Write(line)
			}
		}
	}
	mu.Unlock()

	// Console mirror so pm2/systemd logs keep their usual content.
	tag := fmt.Sprintf("[%s]", strings.ToUpper(e.Level))
	if e.Ctx != nil {
		b, _ := json.Marshal(e.Ctx)
		fmt.Fprintln(os.Stderr, tag, msg, string(b))
	} else {
		fmt.Fprintln(os.Stderr, tag, msg)
	}
}

// Debug / Info / Warn / Error accept printf-style format with an optional
// trailing map[string]any as structured context.
func Debug(format string, args ...any) { level(LDebug, format, args) }
func Info(format string, args ...any)  { level(LInfo, format, args) }
func Warn(format string, args ...any)  { level(LWarn, format, args) }
func Error(format string, args ...any) { level(LError, format, args) }

func level(l Level, format string, args []any) {
	var ctx map[string]any
	if n := len(args); n > 0 {
		if c, ok := args[n-1].(map[string]any); ok {
			ctx = c
			args = args[:n-1]
		}
	}
	emit(l, fmt.Sprintf(format, args...), ctx)
}
