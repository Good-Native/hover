package jobs

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingPersister struct {
	mu    sync.Mutex
	calls []struct {
		domain  string
		seconds int
	}
	failNext atomic.Bool
}

func (p *recordingPersister) UpdateDomainAdaptiveDelay(_ context.Context, domain string, seconds int) error {
	if p.failNext.Swap(false) {
		return errors.New("simulated write error")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, struct {
		domain  string
		seconds int
	}{domain, seconds})
	return nil
}

func (p *recordingPersister) snapshot() []struct {
	domain  string
	seconds int
} {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]struct {
		domain  string
		seconds int
	}, len(p.calls))
	copy(out, p.calls)
	return out
}

func waitForCalls(t *testing.T, p *recordingPersister, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(p.snapshot()) >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("expected at least %d persister calls, got %d", want, len(p.snapshot()))
}

// assertCallCountStable polls the persister for `window` and fails if the
// observed call count ever exceeds `want`. Catches late writes that a
// single Sleep+assert would miss.
func assertCallCountStable(t *testing.T, p *recordingPersister, want int, window time.Duration) {
	t.Helper()
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		if got := len(p.snapshot()); got > want {
			t.Fatalf("expected at most %d persister calls during %s window, got %d", want, window, got)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := len(p.snapshot()); got != want {
		t.Fatalf("expected exactly %d persister calls after %s, got %d", want, window, got)
	}
}

func TestAdaptiveDelayWriteback_NegativeIsNoop(t *testing.T) {
	p := &recordingPersister{}
	w := newAdaptiveDelayWriteback(p)

	w.Observe(context.Background(), "noop.com", -1)

	assertCallCountStable(t, p, 0, 50*time.Millisecond)
}

func TestAdaptiveDelayWriteback_FirstObservationPersists(t *testing.T) {
	p := &recordingPersister{}
	w := newAdaptiveDelayWriteback(p)

	w.Observe(context.Background(), "first.com", 2500)
	waitForCalls(t, p, 1)

	calls := p.snapshot()
	require.Len(t, calls, 1)
	assert.Equal(t, "first.com", calls[0].domain)
	assert.Equal(t, 3, calls[0].seconds, "ms->seconds should round up so sub-second learning is preserved")
}

func TestAdaptiveDelayWriteback_SubSecondRoundsUpToOne(t *testing.T) {
	p := &recordingPersister{}
	w := newAdaptiveDelayWriteback(p)

	// 500ms is the default pacer step; truncating to 0 would reseed an
	// empty adaptive delay after a worker restart.
	w.Observe(context.Background(), "subsec.com", 500)
	waitForCalls(t, p, 1)

	calls := p.snapshot()
	require.Len(t, calls, 1)
	assert.Equal(t, 1, calls[0].seconds)
}

func TestAdaptiveDelayWriteback_DebouncesWithinWindow(t *testing.T) {
	p := &recordingPersister{}
	w := newAdaptiveDelayWriteback(p)
	w.interval = time.Hour // ensure window swallows repeat observations

	ctx := context.Background()
	w.Observe(ctx, "debounce.com", 1000)
	waitForCalls(t, p, 1)

	// Same value, then a moved value — both must be suppressed by the window.
	w.Observe(ctx, "debounce.com", 1000)
	w.Observe(ctx, "debounce.com", 5000)

	assertCallCountStable(t, p, 1, 100*time.Millisecond)
}

func TestAdaptiveDelayWriteback_UnchangedValueSkipsWrite(t *testing.T) {
	p := &recordingPersister{}
	w := newAdaptiveDelayWriteback(p)
	w.interval = time.Nanosecond // window non-blocking; suppression must come from value equality

	ctx := context.Background()
	w.Observe(ctx, "still.com", 2000)
	waitForCalls(t, p, 1)

	w.Observe(ctx, "still.com", 2000)

	assertCallCountStable(t, p, 1, 50*time.Millisecond)
}

func TestAdaptiveDelayWriteback_FailureClearsInFlight(t *testing.T) {
	p := &recordingPersister{}
	p.failNext.Store(true)
	w := newAdaptiveDelayWriteback(p)
	w.interval = time.Nanosecond

	ctx := context.Background()
	w.Observe(ctx, "retry.com", 3000)

	// First write errors; in-flight flag must clear so a follow-up can retry.
	// Hold the lock for the duration of the field read — persist mutates
	// these fields, so checking them lock-free would race the goroutine.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		w.mu.Lock()
		state, ok := w.states["retry.com"]
		cleared := ok && !state.inFlight && state.lastValueSec == -1
		w.mu.Unlock()
		if cleared {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	w.Observe(ctx, "retry.com", 3000)
	waitForCalls(t, p, 1)
}
