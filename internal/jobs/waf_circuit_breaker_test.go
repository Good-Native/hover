package jobs

import (
	"testing"

	"github.com/Harvey-AU/hover/internal/crawler"
)

func TestWAFCircuitBreaker_TripsAtThreshold(t *testing.T) {
	b := &WAFCircuitBreaker{
		threshold: 3,
		counts:    make(map[string]int),
		tripped:   make(map[string]struct{}),
		vendors:   make(map[string]crawler.WAFDetection),
	}

	det := &crawler.WAFDetection{Blocked: true, Vendor: crawler.WAFVendorAkamai, Reason: "Server: AkamaiGHost on 403"}

	if tripped, _ := b.Observe("job-1", det); tripped {
		t.Fatalf("expected no trip after first WAF observation")
	}
	if tripped, _ := b.Observe("job-1", det); tripped {
		t.Fatalf("expected no trip after second WAF observation")
	}
	tripped, vendor := b.Observe("job-1", det)
	if !tripped {
		t.Fatalf("expected trip at third consecutive WAF observation")
	}
	if vendor.Vendor != crawler.WAFVendorAkamai {
		t.Errorf("vendor = %q, want %q", vendor.Vendor, crawler.WAFVendorAkamai)
	}
}

func TestWAFCircuitBreaker_NonWAFResetsCounter(t *testing.T) {
	b := &WAFCircuitBreaker{
		threshold: 3,
		counts:    make(map[string]int),
		tripped:   make(map[string]struct{}),
		vendors:   make(map[string]crawler.WAFDetection),
	}

	det := &crawler.WAFDetection{Blocked: true, Vendor: "akamai"}

	b.Observe("job-1", det)
	b.Observe("job-1", det)
	// One healthy response wipes the streak.
	b.Observe("job-1", nil)
	b.Observe("job-1", det)
	b.Observe("job-1", det)
	tripped, _ := b.Observe("job-1", det)
	if !tripped {
		t.Fatalf("expected trip after three consecutive blocks following a reset")
	}
}

func TestWAFCircuitBreaker_FiresOncePerJob(t *testing.T) {
	b := &WAFCircuitBreaker{
		threshold: 1,
		counts:    make(map[string]int),
		tripped:   make(map[string]struct{}),
		vendors:   make(map[string]crawler.WAFDetection),
	}

	det := &crawler.WAFDetection{Blocked: true, Vendor: "akamai"}

	if tripped, _ := b.Observe("job-1", det); !tripped {
		t.Fatalf("expected trip on first observation (threshold=1)")
	}
	for i := 0; i < 5; i++ {
		if tripped, _ := b.Observe("job-1", det); tripped {
			t.Fatalf("breaker fired more than once for the same job")
		}
	}
}

func TestWAFCircuitBreaker_PerJobIsolation(t *testing.T) {
	b := &WAFCircuitBreaker{
		threshold: 2,
		counts:    make(map[string]int),
		tripped:   make(map[string]struct{}),
		vendors:   make(map[string]crawler.WAFDetection),
	}

	det := &crawler.WAFDetection{Blocked: true, Vendor: "akamai"}

	// A has one WAF response and then resets to zero. B has only one
	// WAF response so far. Neither has reached threshold 2.
	b.Observe("job-A", det)
	b.Observe("job-B", det)
	b.Observe("job-A", nil) // resets A only

	// Next A block: A's counter is 1, must not trip.
	if tripped, _ := b.Observe("job-A", det); tripped {
		t.Errorf("job-A tripped after only one WAF response since reset")
	}
	// Next A block: A reaches threshold of 2 — must trip.
	if tripped, _ := b.Observe("job-A", det); !tripped {
		t.Errorf("job-A should trip at second consecutive block since reset")
	}
	// B is independent: still at 1 hit, must not trip on a single more
	// block taking it to 2 — wait, that's a trip. Instead verify B
	// hasn't tripped yet by sending one healthy response then re-checking.
	b.Observe("job-B", nil)
	if tripped, _ := b.Observe("job-B", det); tripped {
		t.Errorf("job-B tripped on a single block following a reset")
	}
}

func TestWAFCircuitBreaker_ForgetClearsState(t *testing.T) {
	b := &WAFCircuitBreaker{
		threshold: 3,
		counts:    make(map[string]int),
		tripped:   make(map[string]struct{}),
		vendors:   make(map[string]crawler.WAFDetection),
	}

	det := &crawler.WAFDetection{Blocked: true, Vendor: "akamai"}

	b.Observe("job-1", det)
	b.Observe("job-1", det)
	b.Forget("job-1")
	// Forget is intended for terminal-state cleanup — after it, fresh
	// observations start from zero again.
	if tripped, _ := b.Observe("job-1", det); tripped {
		t.Fatalf("Forget did not reset counter (tripped on first post-forget observation)")
	}
}

func TestWAFCircuitBreaker_NilSafe(t *testing.T) {
	var b *WAFCircuitBreaker
	tripped, _ := b.Observe("job-1", &crawler.WAFDetection{Blocked: true})
	if tripped {
		t.Fatalf("nil receiver tripped")
	}
	b.Forget("job-1")                        // must not panic
	b.Rearm("job-1", crawler.WAFDetection{}) // must not panic
}

// TestWAFCircuitBreaker_RearmSeedsToThresholdMinusOne verifies the
// post-Rearm fast-retrip behaviour: after a failed BlockJob dispatch,
// the breaker is primed so a single subsequent blocked outcome
// retrips immediately, rather than re-establishing the full
// consecutive-WAF streak from zero. The first trip already proved the
// site is consistently walling us; the retry shouldn't waste N-1
// blocked observations re-proving it.
func TestWAFCircuitBreaker_RearmSeedsToThresholdMinusOne(t *testing.T) {
	b := &WAFCircuitBreaker{
		threshold: 3,
		counts:    make(map[string]int),
		tripped:   make(map[string]struct{}),
		vendors:   make(map[string]crawler.WAFDetection),
	}

	det := &crawler.WAFDetection{Blocked: true, Vendor: "akamai", Reason: "AkamaiGHost"}

	// Establish the first trip the long way.
	b.Observe("job-1", det) // 1
	b.Observe("job-1", det) // 2
	tripped, vendor := b.Observe("job-1", det)
	if !tripped {
		t.Fatalf("expected first trip at threshold=3")
	}

	// Simulate BlockJob failure: caller re-arms with the captured vendor.
	b.Rearm("job-1", vendor)

	// One blocked observation must be enough to retrip — we already
	// established consecutiveness on the first trip.
	tripped, retripVendor := b.Observe("job-1", det)
	if !tripped {
		t.Fatalf("expected immediate retrip after Rearm; took more than 1 blocked observation")
	}
	if retripVendor.Vendor != "akamai" {
		t.Errorf("retrip vendor lost: got %q, want %q", retripVendor.Vendor, "akamai")
	}
}

// TestWAFCircuitBreaker_RearmThenHealthyResetsStreak asserts the
// recovery path: if the site stops walling us between trip attempts
// (a non-blocked response arrives), the seeded counter clears so the
// breaker doesn't pre-load its retrip from stale evidence.
func TestWAFCircuitBreaker_RearmThenHealthyResetsStreak(t *testing.T) {
	b := &WAFCircuitBreaker{
		threshold: 3,
		counts:    make(map[string]int),
		tripped:   make(map[string]struct{}),
		vendors:   make(map[string]crawler.WAFDetection),
	}

	det := &crawler.WAFDetection{Blocked: true, Vendor: "akamai"}

	// Trip once.
	b.Observe("job-1", det)
	b.Observe("job-1", det)
	_, vendor := b.Observe("job-1", det)

	// Re-arm (simulates BlockJob failure).
	b.Rearm("job-1", vendor)

	// A healthy (non-blocked) response between trip attempts must
	// clear the seeded counter — the site might have recovered.
	b.Observe("job-1", nil)

	// One blocked observation now must NOT retrip — counter starts
	// fresh, needs threshold=3 again.
	if tripped, _ := b.Observe("job-1", det); tripped {
		t.Fatalf("breaker retripped after a healthy response cleared the seed; recovery path broken")
	}
}
