package logbus

import (
	"errors"
	"fmt"
	"log/slog"
	"testing"
)

func TestHandlerFormatsAttributes(t *testing.T) {
	bus := New()
	logger := slog.New(bus.Handler()).With("package", "benchmark")
	logger.Info("seeded row", "table", "warehouse", "id", 3)
	logger.WithGroup("probe").Warn("probe failed", "error", errors.New("timeout"), slog.Group("target", "port", 5433))

	lines := bus.Collect()
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	want := []Line{
		{Level: slog.LevelInfo, Message: "seeded row", Attrs: "package=benchmark table=warehouse id=3"},
		{Level: slog.LevelWarn, Message: "probe failed", Attrs: "package=benchmark probe.error=timeout probe.target.port=5433"},
	}
	for index, line := range lines {
		line.Time = want[index].Time
		if line != want[index] {
			t.Errorf("line %d = %+v, want %+v", index, line, want[index])
		}
	}
}

func TestHandlerSkipsDebug(t *testing.T) {
	bus := New()
	slog.New(bus.Handler()).Debug("noisy")
	if lines := bus.Collect(); len(lines) != 0 {
		t.Fatalf("got %d lines, want 0", len(lines))
	}
}

func TestCollectEmptiesBus(t *testing.T) {
	bus := New()
	slog.New(bus.Handler()).Info("first")
	bus.Collect()
	if lines := bus.Collect(); len(lines) != 0 {
		t.Fatalf("second collect got %d lines, want 0", len(lines))
	}
}

func TestBusKeepsNewestLines(t *testing.T) {
	bus := New()
	logger := slog.New(bus.Handler())
	for index := range 2005 {
		logger.Info(fmt.Sprint(index))
	}
	lines := bus.Collect()
	if len(lines) != 2000 || lines[0].Message != "5" {
		t.Fatalf("got %d lines starting at %q, want 2000 starting at 5", len(lines), lines[0].Message)
	}
}
