// Package logbus collects log lines in memory for the console to show.
package logbus

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
)

// Line is one log record.
type Line struct {
	Time    time.Time
	Level   slog.Level
	Message string
	Attrs   string
}

// Bus holds log lines until the console collects them. It keeps at most 2000.
type Bus struct {
	mutex sync.Mutex
	lines []Line
}

func New() *Bus {
	return &Bus{}
}

// Handler returns an slog.Handler that adds each record at info level or
// above to the bus.
func (b *Bus) Handler() slog.Handler {
	return &handler{bus: b}
}

// Collect returns the lines added since the previous call and empties the bus.
func (b *Bus) Collect() []Line {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	lines := b.lines
	b.lines = nil
	return lines
}

func (b *Bus) add(line Line) {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	b.lines = append(b.lines, line)
	if len(b.lines) > 2000 {
		b.lines = b.lines[len(b.lines)-2000:]
	}
}

// handler is the slog.Handler behind Bus.Handler. attrs holds attributes
// from WithAttrs; group is the prefix from WithGroup.
type handler struct {
	bus   *Bus
	attrs []slog.Attr
	group string
}

// Skips Debug level
// Shows only Info, Warn, and Error
func (h *handler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelInfo
}

// Handle formats the record's attributes as key=value pairs and adds the
// line to the bus.
func (h *handler) Handle(_ context.Context, record slog.Record) error {
	var text strings.Builder
	for _, attr := range h.attrs {
		writeAttr(&text, "", attr)
	}
	record.Attrs(func(attr slog.Attr) bool {
		writeAttr(&text, h.group, attr)
		return true
	})
	h.bus.add(Line{Time: record.Time, Level: record.Level, Message: record.Message, Attrs: strings.TrimSpace(text.String())})
	return nil
}

func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clone := *h
	clone.attrs = slices.Clone(h.attrs)
	for _, attr := range attrs {
		clone.attrs = append(clone.attrs, slog.Attr{Key: join(h.group, attr.Key), Value: attr.Value})
	}
	return &clone
}

func (h *handler) WithGroup(name string) slog.Handler {
	clone := *h
	clone.group = join(h.group, name)
	return &clone
}

// writeAttr writes " key=value", or one pair per member of a group, with
// keys prefixed by group.
func writeAttr(text *strings.Builder, group string, attr slog.Attr) {
	attr.Value = attr.Value.Resolve()
	if attr.Equal(slog.Attr{}) {
		return
	}
	if attr.Value.Kind() == slog.KindGroup {
		for _, member := range attr.Value.Group() {
			writeAttr(text, join(group, attr.Key), member)
		}
		return
	}
	text.WriteString(" " + join(group, attr.Key) + "=" + attr.Value.String())
}

// join returns key prefixed by group and a dot, or key alone when group is
// empty.
func join(group, key string) string {
	if group == "" {
		return key
	}
	if key == "" {
		return group
	}
	return group + "." + key
}
