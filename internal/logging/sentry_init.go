package logging

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/getsentry/sentry-go"
)

// SentryOptions configures the Sentry SDK for one of the Fly binaries.
type SentryOptions struct {
	DSN              string
	Environment      string
	Process          string  // "app" | "worker" | "analysis" — emitted as the `process` scope tag.
	TracesSampleRate float64 // 0 = leave at SDK default (off).
	Debug            bool
}

// InitSentry calls sentry.Init with project-wide defaults and attaches the
// deploy-identifying tags read from Fly env vars (app, region, process,
// release, server_name). Returns a flush closure that callers should defer
// regardless of outcome — it is a no-op when the DSN is empty or init failed,
// so call sites do not need a separate guard around the defer.
func InitSentry(opts SentryOptions) (func(), error) {
	flush := func() {}
	if opts.DSN == "" {
		return flush, nil
	}

	clientOpts := sentry.ClientOptions{
		Dsn:              opts.DSN,
		Environment:      opts.Environment,
		Release:          deployRelease(),
		ServerName:       serverName(),
		AttachStacktrace: true,
		Debug:            opts.Debug,
		BeforeSend:       BeforeSend,
	}
	if opts.TracesSampleRate > 0 {
		clientOpts.TracesSampleRate = opts.TracesSampleRate
	}

	if err := sentry.Init(clientOpts); err != nil {
		return flush, fmt.Errorf("sentry.Init: %w", err)
	}

	sentry.ConfigureScope(func(scope *sentry.Scope) {
		if v := strings.TrimSpace(os.Getenv("FLY_APP_NAME")); v != "" {
			scope.SetTag("app", v)
		}
		if v := strings.TrimSpace(os.Getenv("FLY_REGION")); v != "" {
			scope.SetTag("region", v)
		}
		if opts.Process != "" {
			scope.SetTag("process", opts.Process)
		}
	})

	return func() { sentry.Flush(2 * time.Second) }, nil
}

// deployRelease returns the most stable deploy identifier we can read from
// runtime env. Fly populates FLY_RELEASE_VERSION (monotonic deploy counter,
// e.g. "v123") and FLY_IMAGE_REF (registry SHA) on every machine; either is
// good enough to pin a Sentry event to the deploy that emitted it.
func deployRelease() string {
	if v := strings.TrimSpace(os.Getenv("FLY_RELEASE_VERSION")); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("FLY_IMAGE_REF")); v != "" {
		return v
	}
	return ""
}

func serverName() string {
	if v := strings.TrimSpace(os.Getenv("FLY_MACHINE_ID")); v != "" {
		return v
	}
	h, _ := os.Hostname()
	return h
}
