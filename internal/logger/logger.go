package logger

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"sync"
)

var (
	once      sync.Once
	logBuffer *lineBuffer
	logFile   *os.File
)

// InitLogger initializes the global logger to write JSON to stdout and a file.
func InitLogger(filePath string) error {
	var err error
	once.Do(func() {
		logBuffer = newLineBuffer(200)
		var out io.Writer = os.Stdout
		if filePath != "" {
			logFile, err = os.OpenFile(filePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
			if err != nil {
				return
			}
			out = io.MultiWriter(os.Stdout, logFile)
		}
		writer := &teeWriter{out: out, buf: logBuffer}
		jsonHandler := slog.NewJSONHandler(writer, &slog.HandlerOptions{
			Level: slog.LevelInfo,
		})

		logger := slog.New(jsonHandler)
		slog.SetDefault(logger)
	})
	return err
}

// Close closes the log file.
func Close() {
	if logFile != nil {
		_ = logFile.Close()
	}
}

// GetRecentLogs returns the most recent log lines up to the limit.
func GetRecentLogs(limit int) []string {
	if logBuffer == nil {
		return nil
	}
	return logBuffer.recent(limit)
}

// LogEvent logs a specific domain event.
func LogEvent(eventType string, fields ...any) {
	// Prepend the event type to the fields
	allFields := append([]any{slog.String("event", eventType)}, fields...)
	slog.Info("SystemEvent", allFields...)
}

type teeWriter struct {
	out io.Writer
	buf *lineBuffer
}

func (t *teeWriter) Write(p []byte) (int, error) {
	n, err := t.out.Write(p)
	if n > 0 && t.buf != nil {
		t.buf.write(p[:n])
	}
	return n, err
}

type lineBuffer struct {
	mu      sync.Mutex
	max     int
	lines   []string
	partial bytes.Buffer
}

func newLineBuffer(max int) *lineBuffer {
	return &lineBuffer{max: max}
}

func (b *lineBuffer) write(p []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()

	_, _ = b.partial.Write(p)
	for {
		data := b.partial.Bytes()
		idx := bytes.IndexByte(data, '\n')
		if idx == -1 {
			break
		}
		line := string(data[:idx])
		b.append(line)
		b.partial.Next(idx + 1)
	}
}

func (b *lineBuffer) append(line string) {
	if b.max <= 0 {
		return
	}
	if len(b.lines) >= b.max {
		copy(b.lines, b.lines[1:])
		b.lines[len(b.lines)-1] = line
		return
	}
	b.lines = append(b.lines, line)
}

func (b *lineBuffer) recent(limit int) []string {
	b.mu.Lock()
	defer b.mu.Unlock()

	if limit <= 0 || len(b.lines) == 0 {
		return nil
	}
	if limit > len(b.lines) {
		limit = len(b.lines)
	}
	start := len(b.lines) - limit
	out := make([]string, limit)
	copy(out, b.lines[start:])
	return out
}
