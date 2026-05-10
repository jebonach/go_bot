package logging

import (
	"fmt"
	"log"
	"os"
	"strings"
)

type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

type Logger struct {
	base  *log.Logger
	level Level
}

func New(level string) (*Logger, error) {
	parsed, err := parseLevel(level)
	if err != nil {
		return nil, err
	}

	return &Logger{
		base:  log.New(os.Stdout, "", log.LstdFlags|log.LUTC),
		level: parsed,
	}, nil
}

func (l *Logger) Debugf(format string, args ...any) {
	l.logf(LevelDebug, "DEBUG", format, args...)
}

func (l *Logger) Infof(format string, args ...any) {
	l.logf(LevelInfo, "INFO", format, args...)
}

func (l *Logger) Warnf(format string, args ...any) {
	l.logf(LevelWarn, "WARN", format, args...)
}

func (l *Logger) Errorf(format string, args ...any) {
	l.logf(LevelError, "ERROR", format, args...)
}

func (l *Logger) logf(level Level, prefix string, format string, args ...any) {
	if l == nil || l.base == nil {
		return
	}
	if level < l.level {
		return
	}

	l.base.Printf("level=%s %s", prefix, fmt.Sprintf(format, args...))
}

func parseLevel(level string) (Level, error) {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "", "info":
		return LevelInfo, nil
	case "debug":
		return LevelDebug, nil
	case "warn", "warning":
		return LevelWarn, nil
	case "error":
		return LevelError, nil
	default:
		return 0, fmt.Errorf("unsupported LOG_LEVEL %q", level)
	}
}
