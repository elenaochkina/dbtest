package telemetry

import (
	"io"
	"log/slog"
	"os"
)

// LogConfig holds configuration for structured logging.
type LogConfig struct {
	LogLevel string    // "info", "debug", "warn"
	Output   io.Writer // where to write logs; defaults to os.Stdout if nil
}

// InitLogging sets up a JSON structured logger and installs it as the global default.
func InitLogging(cfg LogConfig) *slog.Logger {
	var level slog.Level
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	default:
		level = slog.LevelInfo
	}

	out := cfg.Output
	if out == nil {
		out = os.Stdout
	}

	return slog.New(slog.NewJSONHandler(out, &slog.HandlerOptions{
		Level: level,
	}))

}
