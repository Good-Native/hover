package logging

import (
	"testing"
)

func TestInitSentryNoOpWhenDSNEmpty(t *testing.T) {
	flush, err := InitSentry(SentryOptions{DSN: ""})
	if err != nil {
		t.Fatalf("expected no error for empty DSN, got %v", err)
	}
	if flush == nil {
		t.Fatal("flush func should never be nil — callers defer it unconditionally")
	}
	flush() // must not panic
}

func TestInitSentryWithDSN(t *testing.T) {
	t.Setenv("FLY_APP_NAME", "hover-pr-372")
	t.Setenv("FLY_REGION", "syd")
	t.Setenv("FLY_RELEASE_VERSION", "v999")
	t.Setenv("FLY_MACHINE_ID", "machine-abc")

	flush, err := InitSentry(SentryOptions{
		DSN:              "https://public@example.invalid/1",
		Environment:      "test",
		Process:          "worker",
		TracesSampleRate: 0.5,
	})
	if err != nil {
		t.Fatalf("InitSentry: %v", err)
	}
	if flush == nil {
		t.Fatal("flush should never be nil")
	}
	flush() // must not panic
}

func TestDeployReleasePrecedence(t *testing.T) {
	t.Setenv("FLY_RELEASE_VERSION", "")
	t.Setenv("FLY_IMAGE_REF", "")
	if got := deployRelease(); got != "" {
		t.Errorf("expected empty release with no env vars, got %q", got)
	}

	t.Setenv("FLY_IMAGE_REF", "registry.fly.io/hover@sha256:abc")
	if got := deployRelease(); got != "registry.fly.io/hover@sha256:abc" {
		t.Errorf("expected fallback to FLY_IMAGE_REF, got %q", got)
	}

	t.Setenv("FLY_RELEASE_VERSION", "v123")
	if got := deployRelease(); got != "v123" {
		t.Errorf("FLY_RELEASE_VERSION should win over FLY_IMAGE_REF, got %q", got)
	}

	t.Setenv("FLY_RELEASE_VERSION", "  v124  ")
	if got := deployRelease(); got != "v124" {
		t.Errorf("expected FLY_RELEASE_VERSION to be trimmed, got %q", got)
	}
}

func TestServerNameFallback(t *testing.T) {
	t.Setenv("FLY_MACHINE_ID", "9080e9eb1a5e08")
	if got := serverName(); got != "9080e9eb1a5e08" {
		t.Errorf("expected FLY_MACHINE_ID, got %q", got)
	}

	t.Setenv("FLY_MACHINE_ID", "")
	got := serverName()
	if got == "" {
		t.Error("expected hostname fallback when FLY_MACHINE_ID is empty")
	}
}
