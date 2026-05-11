package jobs

import (
	"context"
	"sync"
	"time"
)

// Default cadence: write-back at most every 5 minutes per domain. Each
// Release returns a fresh adaptive_delay_ms reading, but the value rarely
// changes between calls — debouncing keeps the per-task hot path off of
// Postgres while still persisting the learned rate within an order of
// magnitude of when it stabilises.
const defaultAdaptiveWritebackInterval = 5 * time.Minute

type adaptiveDelayPersister interface {
	UpdateDomainAdaptiveDelay(ctx context.Context, domain string, adaptiveDelaySeconds int) error
}

type domainWriteState struct {
	// lastAttemptAt drives the debounce window so a failing Postgres
	// can't trigger one retry per Release on the hot path.
	lastAttemptAt time.Time
	lastValueSec  int
	inFlight      bool
}

// adaptiveDelayWriteback debounces writes of the pacer's learned per-domain
// delay to the durable domains.adaptive_delay_seconds column. The pacer
// surfaces the post-update delay on every Release; this type swallows the
// firehose and lets through one UPDATE per domain per debounce window when
// the value has actually moved.
type adaptiveDelayWriteback struct {
	persister adaptiveDelayPersister
	interval  time.Duration

	mu     sync.Mutex
	states map[string]*domainWriteState
}

func newAdaptiveDelayWriteback(persister adaptiveDelayPersister) *adaptiveDelayWriteback {
	return &adaptiveDelayWriteback{
		persister: persister,
		interval:  defaultAdaptiveWritebackInterval,
		states:    make(map[string]*domainWriteState),
	}
}

// Observe is called from the Release hot path. newDelayMS < 0 means the
// pacer did not touch adaptive_delay this release (no success and no
// rate-limit) — nothing to persist. The write fires in a goroutine so the
// caller never blocks on Postgres.
func (w *adaptiveDelayWriteback) Observe(ctx context.Context, domain string, newDelayMS int) {
	if w == nil || w.persister == nil || domain == "" || newDelayMS < 0 {
		return
	}

	// Ceil ms→sec so a sub-second learned back-off (e.g. the default 500ms
	// step) doesn't serialise to zero and reseed an empty adaptive delay
	// after restart. The schema column is integer seconds; rounding up is
	// the conservative choice — preferring slightly slower next-start
	// crawl rate over reopening the burst we are trying to prevent.
	seconds := 0
	if newDelayMS > 0 {
		seconds = (newDelayMS + 999) / 1000
	}

	w.mu.Lock()
	state, ok := w.states[domain]
	if !ok {
		state = &domainWriteState{lastValueSec: -1}
		w.states[domain] = state
	}

	now := time.Now()
	tooSoon := !state.lastAttemptAt.IsZero() && now.Sub(state.lastAttemptAt) < w.interval
	unchanged := state.lastValueSec == seconds
	if state.inFlight || tooSoon || unchanged {
		w.mu.Unlock()
		return
	}
	state.inFlight = true
	state.lastAttemptAt = now
	w.mu.Unlock()

	// Detach from the caller's context: when the worker pool's Stop()
	// cancels its ctx, in-flight write-backs would abort and the learned
	// adaptive delay would be lost. A bounded timeout keeps a hung DB
	// from leaking goroutines.
	_ = ctx
	go func() { //nolint:gosec // G118: detached on purpose so Stop() cannot abort the write-back
		persistCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		w.persist(persistCtx, domain, seconds)
	}()
}

func (w *adaptiveDelayWriteback) persist(ctx context.Context, domain string, seconds int) {
	err := w.persister.UpdateDomainAdaptiveDelay(ctx, domain, seconds)

	w.mu.Lock()
	defer w.mu.Unlock()
	state, ok := w.states[domain]
	if !ok {
		return
	}
	state.inFlight = false
	if err != nil {
		// Debounce keys off lastAttemptAt (set in Observe) so a failing
		// Postgres doesn't get retried on every subsequent Release.
		jobsLog.Warn("adaptive-delay write-back failed, will retry after debounce window",
			"error", err, "domain", domain, "adaptive_delay_seconds", seconds)
		return
	}
	state.lastValueSec = seconds
	jobsLog.Debug("persisted adaptive delay",
		"domain", domain, "adaptive_delay_seconds", seconds)
}
