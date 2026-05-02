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

	// Capture deploy-identifying values once so we don't re-read env on every
	// event. Logged to stderr at startup so Fly logs show which Fly runtime
	// env vars actually got populated on this machine.
	appName := strings.TrimSpace(os.Getenv("FLY_APP_NAME"))
	region := strings.TrimSpace(os.Getenv("FLY_REGION"))
	release := deployRelease()
	server := serverName()
	fmt.Fprintf(os.Stderr,
		"sentry: init env=%q app=%q region=%q process=%q release=%q server=%q\n",
		opts.Environment, appName, region, opts.Process, release, server,
	)

	clientOpts := sentry.ClientOptions{
		Dsn:              opts.DSN,
		Environment:      opts.Environment,
		Release:          release,
		ServerName:       server,
		AttachStacktrace: true,
		Debug:            opts.Debug,
		BeforeSend:       wrapBeforeSend(appName, region, opts.Process),
	}
	if opts.TracesSampleRate > 0 {
		clientOpts.TracesSampleRate = opts.TracesSampleRate
	}

	if err := sentry.Init(clientOpts); err != nil {
		return flush, fmt.Errorf("sentry.Init: %w", err)
	}

	return func() { sentry.Flush(2 * time.Second) }, nil
}

// wrapBeforeSend delegates to the existing BeforeSend normalisation first,
// then stamps deploy-identifying tags onto every non-nil event without
// overwriting any caller-provided values. The earlier approach used
// sentry.ConfigureScope, but staging diagnostics showed scope tags were not
// reaching events captured via the sentryslog handler — likely a
// goroutine-local hub interaction. Stamping in BeforeSend is unconditional.
func wrapBeforeSend(app, region, process string) func(*sentry.Event, *sentry.EventHint) *sentry.Event {
	return func(event *sentry.Event, hint *sentry.EventHint) *sentry.Event {
		event = BeforeSend(event, hint)
		if event == nil {
			return nil
		}
		if event.Tags == nil {
			event.Tags = make(map[string]string)
		}
		if app != "" && event.Tags["app"] == "" {
			event.Tags["app"] = app
		}
		if region != "" && event.Tags["region"] == "" {
			event.Tags["region"] = region
		}
		if process != "" && event.Tags["process"] == "" {
			event.Tags["process"] = process
		}
		return event
	}
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
