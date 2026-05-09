// Package logging provides a unified slog + Sentry logging interface.
//
// Every log call automatically includes a component tag for structured
// monitoring. Error-level calls auto-capture to Sentry via the
// sentry-go/slog handler. Use NoCapture in the context to suppress
// capture for expected errors.
package logging

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/getsentry/sentry-go"
	sentryslog "github.com/getsentry/sentry-go/slog"
)

var uuidPattern = regexp.MustCompile(
	`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`,
)

var numberPattern = regexp.MustCompile(`\b\d{3,}\b`)

type contextKey int

const (
	noCaptureKey contextKey = iota
)

// NoCapture returns a derived context that suppresses Sentry capture for any
// ErrorContext call made with it. The suppression persists for the lifetime of
// the derived context — construct it inline so the scope is limited to one call:
//
//	log.ErrorContext(logging.NoCapture(ctx), "expected 404", "url", url, "error", err)
//
// Do not store the derived context and reuse it, or all subsequent error log
// calls on that context will also be silently dropped from Sentry.
func NoCapture(ctx context.Context) context.Context {
	return context.WithValue(ctx, noCaptureKey, true)
}

func isNoCapture(ctx context.Context) bool {
	v, _ := ctx.Value(noCaptureKey).(bool)
	return v
}

// Normalises dynamic values to prevent fragmenting Sentry issue grouping.
func BeforeSend(event *sentry.Event, hint *sentry.EventHint) *sentry.Event {
	if v, ok := event.Extra["no_capture"]; ok {
		if suppress, _ := v.(bool); suppress {
			return nil
		}
	}

	event.Message = normaliseMessage(event.Message)
	for i := range event.Exception {
		event.Exception[i].Value = normaliseMessage(event.Exception[i].Value)
	}
	return event
}

func normaliseMessage(msg string) string {
	msg = uuidPattern.ReplaceAllString(msg, "<uuid>")
	msg = numberPattern.ReplaceAllString(msg, "<n>")
	return msg
}

// Rebuilds the underlying slog.Logger at emit time so it picks up the
// fanout handler installed by Setup after sentry.Init.
type Logger struct {
	component string
	attrs     []any
}

func Component(name string) *Logger {
	return &Logger{component: name}
}

func (l *Logger) With(args ...any) *Logger {
	newAttrs := make([]any, len(l.attrs)+len(args))
	copy(newAttrs, l.attrs)
	copy(newAttrs[len(l.attrs):], args)
	return &Logger{component: l.component, attrs: newAttrs}
}

func (l *Logger) base() *slog.Logger {
	b := slog.Default().With("component", l.component)
	if len(l.attrs) > 0 {
		b = b.With(l.attrs...)
	}
	return b
}

func (l *Logger) Debug(msg string, args ...any) {
	l.base().Debug(l.prefix(msg), args...)
}

func (l *Logger) DebugContext(ctx context.Context, msg string, args ...any) {
	l.base().DebugContext(ctx, l.prefix(msg), args...)
}

func (l *Logger) Info(msg string, args ...any) {
	l.base().Info(l.prefix(msg), args...)
}

func (l *Logger) InfoContext(ctx context.Context, msg string, args ...any) {
	l.base().InfoContext(ctx, l.prefix(msg), args...)
}

func (l *Logger) Warn(msg string, args ...any) {
	l.base().Warn(l.prefix(msg), args...)
}

func (l *Logger) WarnContext(ctx context.Context, msg string, args ...any) {
	l.base().WarnContext(ctx, l.prefix(msg), args...)
}

func (l *Logger) Error(msg string, args ...any) {
	l.base().Error(l.prefix(msg), l.withSentryAttrs(msg, args)...)
}

// Use NoCapture(ctx) to suppress Sentry capture for expected errors.
func (l *Logger) ErrorContext(ctx context.Context, msg string, args ...any) {
	l.base().ErrorContext(ctx, l.prefix(msg), l.withSentryAttrs(msg, args)...)
}

func (l *Logger) Fatal(msg string, args ...any) {
	l.base().Error(l.prefix(msg), l.withSentryAttrs(msg, args)...)
	sentry.Flush(5 * time.Second)
	os.Exit(1)
}

func (l *Logger) prefix(msg string) string {
	return "[" + l.component + "] " + msg
}

func (l *Logger) withSentryAttrs(msg string, args []any) []any {
	return append(args,
		slog.Group("tags",
			slog.String("component", l.component),
		),
		slog.Any("fingerprint", []string{l.component, normaliseMessage(msg)}),
	)
}

type fanoutHandler struct {
	handlers []slog.Handler
}

func (h *fanoutHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, hh := range h.handlers {
		if hh.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (h *fanoutHandler) Handle(ctx context.Context, record slog.Record) error {
	var firstErr error
	for _, hh := range h.handlers {
		if hh.Enabled(ctx, record.Level) {
			if err := hh.Handle(ctx, record.Clone()); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

func (h *fanoutHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	handlers := make([]slog.Handler, len(h.handlers))
	for i, hh := range h.handlers {
		handlers[i] = hh.WithAttrs(attrs)
	}
	return &fanoutHandler{handlers: handlers}
}

func (h *fanoutHandler) WithGroup(name string) slog.Handler {
	handlers := make([]slog.Handler, len(h.handlers))
	for i, hh := range h.handlers {
		handlers[i] = hh.WithGroup(name)
	}
	return &fanoutHandler{handlers: handlers}
}

var stdoutAsync *AsyncWriter

// nil if Setup has not been called or async logging is disabled (development).
func StdoutAsync() *AsyncWriter { return stdoutAsync }

// Call after sentry.Init(). Development uses synchronous text for
// deterministic test output; production uses async JSON so no goroutine
// can wedge in slog.Handler.Handle on backpressured stdout.
func Setup(level slog.Level, env string) {
	var outputHandler slog.Handler

	opts := &slog.HandlerOptions{Level: level}

	if env == "development" {
		outputHandler = slog.NewTextHandler(os.Stdout, opts)
	} else {
		stdoutAsync = NewAsyncWriter(os.Stdout, 8192)
		outputHandler = slog.NewJSONHandler(stdoutAsync, opts)
	}

	sentryHandler := sentryslog.Option{
		EventLevel: []slog.Level{slog.LevelError},
		// LogLevel forwards entries to the Sentry Logs surface (separate
		// from error events). Warn+ keeps volume well under the included
		// quota and avoids duplicating Info-level chatter that already
		// lives in stdout/Fly logs.
		LogLevel:  []slog.Level{slog.LevelWarn, slog.LevelError},
		Converter: componentConverter,
		AttrFromContext: []func(ctx context.Context) []slog.Attr{
			noCaptureAttr,
		},
	}.NewSentryHandler(context.Background())

	slog.SetDefault(slog.New(&fanoutHandler{
		handlers: []slog.Handler{outputHandler, sentryHandler},
	}))
}

func componentConverter(addSource bool, replaceAttr func([]string, slog.Attr) slog.Attr, loggerAttr []slog.Attr, groups []string, record *slog.Record, hub *sentry.Hub) *sentry.Event {
	event := &sentry.Event{
		Level:   sentryLevel(record.Level),
		Message: record.Message,
		Tags:    make(map[string]string),
		Extra:   make(map[string]interface{}),
	}

	for _, a := range loggerAttr {
		processAttr(event, a)
	}

	record.Attrs(func(a slog.Attr) bool {
		processAttr(event, a)
		return true
	})

	if _, ok := event.Tags["component"]; !ok {
		event.Tags["component"] = "unknown"
	}

	if len(event.Fingerprint) == 0 {
		event.Fingerprint = []string{
			event.Tags["component"],
			normaliseMessage(record.Message),
		}
	}

	return event
}

func processAttr(event *sentry.Event, a slog.Attr) {
	switch a.Key {
	case "fingerprint":
		if fp, ok := a.Value.Any().([]string); ok {
			event.Fingerprint = fp
		}

	case "tags":
		if a.Value.Kind() == slog.KindGroup {
			for _, ga := range a.Value.Group() {
				event.Tags[ga.Key] = ga.Value.String()
			}
		}

	case "error", "err":
		if err, ok := a.Value.Any().(error); ok {
			event.SetException(err, -1)
		} else {
			event.Extra[a.Key] = a.Value.String()
		}

	case "no_capture":
		// Passed through so BeforeSend can drop the event.
		if b, ok := a.Value.Any().(bool); ok && b {
			event.Extra["no_capture"] = true
		}

	default:
		// Only the explicit "tags" group is low-cardinality enough for
		// Sentry tags; everything else (job IDs, domains, org IDs)
		// would fragment the tag index.
		switch a.Value.Kind() {
		case slog.KindGroup:
			for _, ga := range a.Value.Group() {
				event.Extra[a.Key+"."+ga.Key] = ga.Value.Any()
			}
		default:
			event.Extra[a.Key] = a.Value.Any()
		}
	}
}

func noCaptureAttr(ctx context.Context) []slog.Attr {
	if isNoCapture(ctx) {
		return []slog.Attr{slog.Bool("no_capture", true)}
	}
	return nil
}

func sentryLevel(l slog.Level) sentry.Level {
	switch {
	case l >= slog.LevelError:
		return sentry.LevelError
	case l >= slog.LevelWarn:
		return sentry.LevelWarning
	case l >= slog.LevelInfo:
		return sentry.LevelInfo
	default:
		return sentry.LevelDebug
	}
}

// Accepts zerolog-compatible names for backwards compatibility.
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug", "trace":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error", "fatal", "panic":
		return slog.LevelError
	default:
		fmt.Fprintf(os.Stderr, "logging: unrecognised log level %q, defaulting to warn\n", s)
		return slog.LevelWarn
	}
}
