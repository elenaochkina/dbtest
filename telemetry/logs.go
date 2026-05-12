package telemetry

import (
	"log/slog"
	"os"
)

// LogConfig holds configuration for structured logging.
type LogConfig struct {
	LogLevel string // "info", "debug", "warn"
}

// InitLogging sets up a JSON structured logger and installs it as the global default.
// After this call, slog.Info(...) anywhere in the program uses this logger.
func InitLogging(cfg LogConfig) {
	var level slog.Level
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	default:
		level = slog.LevelInfo
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
	}))
	slog.SetDefault(logger)
}
