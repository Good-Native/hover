package jobs

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Harvey-AU/hover/internal/crawler"
	"github.com/Harvey-AU/hover/internal/db"
)

// stubJobManager is a minimal JobManagerInterface impl whose BlockJob
// behaviour can be steered from a test. Other methods are stubbed
// because the breaker only calls BlockJob.
type stubJobManager struct {
	blockJobErr   error
	blockJobBlock chan struct{}
	calls         atomic.Int32
}

func (s *stubJobManager) CreateJob(context.Context, *JobOptions) (*Job, error) { return nil, nil }
func (s *stubJobManager) CancelJob(context.Context, string) error              { return nil }
func (s *stubJobManager) BlockJob(_ context.Context, _ string, _, _ string) error {
	s.calls.Add(1)
	if s.blockJobBlock != nil {
		<-s.blockJobBlock
	}
	return s.blockJobErr
}
func (s *stubJobManager) GetJobStatus(context.Context, string) (*Job, error) { return nil, nil }
func (s *stubJobManager) GetJob(context.Context, string) (*Job, error)       { return nil, nil }
func (s *stubJobManager) EnqueueJobURLs(context.Context, string, []db.Page, string, string) error {
	return nil
}
func (s *stubJobManager) IsJobComplete(*Job) bool           { return false }
func (s *stubJobManager) CalculateJobProgress(*Job) float64 { return 0 }
func (s *stubJobManager) ValidateStatusTransition(_, _ JobStatus) error {
	return nil
}
func (s *stubJobManager) UpdateJobStatus(context.Context, string, JobStatus) error { return nil }
func (s *stubJobManager) MarkJobRunning(context.Context, string) error             { return nil }
func (s *stubJobManager) GetRobotsRules(context.Context, string) (*crawler.RobotsRules, error) {
	return nil, nil
}

// TestMaybeTripFromOutcome_AsyncDispatch verifies the stream worker
// hot path isn't held up by BlockJob's terminal-state DB write. We
// hold BlockJob inside the stub via a channel; MaybeTripFromOutcome
// must return promptly anyway.
func TestMaybeTripFromOutcome_AsyncDispatch(t *testing.T) {
	b := NewWAFCircuitBreaker()
	b.threshold = 1 // trip immediately

	hold := make(chan struct{})
	jm := &stubJobManager{blockJobBlock: hold}

	outcome := &TaskOutcome{
		Task: &db.Task{ID: "t1", JobID: "job-async"},
		CrawlResult: &crawler.CrawlResult{
			WAF: &crawler.WAFDetection{Blocked: true, Vendor: "akamai", Reason: "Server: AkamaiGHost on 403"},
		},
	}

	done := make(chan struct{})
	go func() {
		b.MaybeTripFromOutcome(context.Background(), jm, outcome)
		close(done)
	}()

	select {
	case <-done:
		// Good — returned without waiting on BlockJob.
	case <-time.After(500 * time.Millisecond):
		close(hold) // unblock the dispatched goroutine before failing
		t.Fatalf("MaybeTripFromOutcome did not return promptly; the dispatch is not async")
	}

	close(hold) // let BlockJob complete

	// Wait briefly for BlockJob to be called (still in the goroutine).
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if jm.calls.Load() == 1 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("BlockJob was never called by the dispatch goroutine")
}

// TestMaybeTripFromOutcome_RearmAfterFailure verifies that if BlockJob
// returns an error, a subsequent WAF observation for the same job can
// trip again. Without re-arm, a transient DB error would permanently
// disable the breaker for that job.
func TestMaybeTripFromOutcome_RearmAfterFailure(t *testing.T) {
	b := NewWAFCircuitBreaker()
	b.threshold = 1 // trip immediately

	jm := &stubJobManager{blockJobErr: errors.New("simulated DB blip")}

	outcome := &TaskOutcome{
		Task: &db.Task{ID: "t1", JobID: "job-rearm"},
		CrawlResult: &crawler.CrawlResult{
			WAF: &crawler.WAFDetection{Blocked: true, Vendor: "akamai", Reason: "AkamaiGHost"},
		},
	}

	b.MaybeTripFromOutcome(context.Background(), jm, outcome)

	// Wait for the goroutine to have called BlockJob and re-armed.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if jm.calls.Load() == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if jm.calls.Load() != 1 {
		t.Fatalf("first BlockJob never called")
	}

	// Give the goroutine a moment to re-arm after the err return.
	time.Sleep(50 * time.Millisecond)

	// Second observation: must trip again because the breaker was
	// re-armed. We use Observe directly so the test doesn't depend on
	// goroutine ordering for the second dispatch.
	tripped, _ := b.Observe("job-rearm", outcome.CrawlResult.WAF)
	if !tripped {
		t.Fatalf("breaker did not re-arm: second observation should have tripped")
	}
}

// TestMaybeTripFromOutcome_NoRearmOnSuccess asserts that a successful
// BlockJob does NOT re-arm the breaker. After a successful trip the
// job is terminal and any further observations should be ignored.
func TestMaybeTripFromOutcome_NoRearmOnSuccess(t *testing.T) {
	b := NewWAFCircuitBreaker()
	b.threshold = 1

	jm := &stubJobManager{} // BlockJob returns nil

	outcome := &TaskOutcome{
		Task: &db.Task{ID: "t1", JobID: "job-once"},
		CrawlResult: &crawler.CrawlResult{
			WAF: &crawler.WAFDetection{Blocked: true, Vendor: "akamai"},
		},
	}

	b.MaybeTripFromOutcome(context.Background(), jm, outcome)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if jm.calls.Load() == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // settle

	// Second observation must NOT trip — the job stays single-fired.
	tripped, _ := b.Observe("job-once", outcome.CrawlResult.WAF)
	if tripped {
		t.Fatalf("breaker re-armed after a successful trip; expected single-fire")
	}
}
