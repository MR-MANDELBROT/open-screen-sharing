package libsharing

import (
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
)

type LogHandler interface {
	Log(level int32, message string)
}

type logTarget struct {
	handler LogHandler
	minimum slog.Level
}

var currentLogTarget atomic.Pointer[logTarget]

func SetLogHandler(handler LogHandler, minimumLevel int32) {
	if handler == nil {
		currentLogTarget.Store(nil)
		return
	}
	currentLogTarget.Store(&logTarget{handler: handler, minimum: slog.Level(minimumLevel)})
}

func newLogger() *slog.Logger {
	target := currentLogTarget.Load()
	if target == nil {
		return nil
	}
	return slog.New(&logBridge{target: target})
}

type logBridge struct {
	target *logTarget
	prefix string
	attrs  string
}

func (b *logBridge) Enabled(_ context.Context, level slog.Level) bool {
	return level >= b.target.minimum
}

func (b *logBridge) Handle(_ context.Context, record slog.Record) error {
	var line strings.Builder
	line.WriteString(record.Message)
	line.WriteString(b.attrs)
	record.Attrs(func(attr slog.Attr) bool {
		appendLogAttr(&line, b.prefix, attr)
		return true
	})
	b.target.handler.Log(int32(record.Level), line.String())
	return nil
}

func (b *logBridge) WithAttrs(attrs []slog.Attr) slog.Handler {
	var line strings.Builder
	line.WriteString(b.attrs)
	for _, attr := range attrs {
		appendLogAttr(&line, b.prefix, attr)
	}
	return &logBridge{target: b.target, prefix: b.prefix, attrs: line.String()}
}

func (b *logBridge) WithGroup(name string) slog.Handler {
	if name == "" {
		return b
	}
	return &logBridge{target: b.target, prefix: b.prefix + name + ".", attrs: b.attrs}
}

func appendLogAttr(line *strings.Builder, prefix string, attr slog.Attr) {
	value := attr.Value.Resolve()
	if value.Kind() == slog.KindGroup {
		groupPrefix := prefix
		if attr.Key != "" {
			groupPrefix += attr.Key + "."
		}
		for _, member := range value.Group() {
			appendLogAttr(line, groupPrefix, member)
		}
		return
	}
	if attr.Key == "" {
		return
	}
	line.WriteByte(' ')
	line.WriteString(prefix)
	line.WriteString(attr.Key)
	line.WriteByte('=')
	line.WriteString(value.String())
}
