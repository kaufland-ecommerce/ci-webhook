package setup

import (
	"fmt"
	"io"
	"log/slog"
	"os"
)

type LogInit struct {
	preInitQueue []string
	level        slog.Level
	filePath     string
	json         bool
	handler      slog.Handler
	rootLogger   *slog.Logger
}

func NewLogInit() *LogInit {
	return &LogInit{
		level: slog.LevelError,
	}
}

func (l *LogInit) PreInitLogf(format string, args ...any) {
	l.preInitQueue = append(l.preInitQueue, fmt.Sprintf(format, args...))
}

func (l *LogInit) SetLogFile(path string) {
	l.filePath = path
}

func (l *LogInit) SetVerbose(verbose bool) {
	if verbose {
		l.level = slog.LevelDebug
		return
	}
	l.level = slog.LevelError
}

func (l *LogInit) SetJSON(json bool) {
	l.json = json
}

func (l *LogInit) InitLogger() *slog.Logger {
	var destination io.Writer = os.Stdout
	if l.filePath != "" {
		file, err := os.OpenFile(l.filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o666)
		if err != nil {
			l.PreInitLogf("error opening log file %q: %v", l.filePath, err)
		} else {
			destination = file
		}
	}
	if l.json {
		l.handler = slog.NewJSONHandler(destination, &slog.HandlerOptions{Level: l.level})
	} else {
		l.handler = slog.NewTextHandler(destination, &slog.HandlerOptions{Level: l.level})
	}
	l.rootLogger = slog.New(l.handler)
	slog.SetDefault(l.rootLogger)

	if len(l.preInitQueue) > 0 {
		for _, msg := range l.preInitQueue {
			l.rootLogger.Error(msg)
		}
	}
	return l.rootLogger
}

func (l *LogInit) ShouldExit() bool {
	return len(l.preInitQueue) > 0
}
