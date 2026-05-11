package jobs

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/good-native/hover/internal/crawler"
)

// stubRobotsCrawler implements CrawlerInterface but only the
// DiscoverSitemapsAndRobots method is exercised by the cache. Everything
// else returns zero values; tests must not call them.
type stubRobotsCrawler struct {
	discoverFn func(ctx context.Context, domain string) (*crawler.SitemapDiscoveryResult, error)
	calls      atomic.Int32
}

func (s *stubRobotsCrawler) DiscoverSitemapsAndRobots(ctx context.Context, domain string) (*crawler.SitemapDiscoveryResult, error) {
	s.calls.Add(1)
	return s.discoverFn(ctx, domain)
}

func (s *stubRobotsCrawler) WarmURL(context.Context, string, bool) (*crawler.CrawlResult, error) {
	return nil, nil
}
func (s *stubRobotsCrawler) ParseSitemap(context.Context, string) ([]string, error) {
	return nil, nil
}
func (s *stubRobotsCrawler) FilterURLs(urls []string, _, _ []string) []string { return urls }
func (s *stubRobotsCrawler) GetUserAgent() string                             { return "test" }
func (s *stubRobotsCrawler) Probe(context.Context, string) (crawler.WAFDetection, error) {
	return crawler.WAFDetection{}, nil
}

func newJobManagerWithCrawler(c CrawlerInterface) *JobManager {
	return &JobManager{
		crawler:      c,
		robotsCache:  make(map[string]robotsCacheEntry),
		robotsTTLPos: defaultRobotsTTLPositive,
		robotsTTLNeg: defaultRobotsTTLNegative,
	}
}

func TestGetRobotsRules_CachesSuccessForPositiveTTL(t *testing.T) {
	rules := &crawler.RobotsRules{CrawlDelay: 5}
	stub := &stubRobotsCrawler{
		discoverFn: func(context.Context, string) (*crawler.SitemapDiscoveryResult, error) {
			return &crawler.SitemapDiscoveryResult{RobotsRules: rules}, nil
		},
	}
	jm := newJobManagerWithCrawler(stub)
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		got, err := jm.GetRobotsRules(ctx, "example.com")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != rules {
			t.Fatalf("expected cached rules pointer, got %v", got)
		}
	}
	if c := stub.calls.Load(); c != 1 {
		t.Fatalf("expected one origin fetch under positive cache, got %d", c)
	}
}

func TestGetRobotsRules_CachesErrorForNegativeTTL(t *testing.T) {
	fetchErr := errors.New("429 Too Many Requests")
	stub := &stubRobotsCrawler{
		discoverFn: func(context.Context, string) (*crawler.SitemapDiscoveryResult, error) {
			return nil, fetchErr
		},
	}
	jm := newJobManagerWithCrawler(stub)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		_, err := jm.GetRobotsRules(ctx, "throttled.com")
		if err == nil {
			t.Fatalf("expected wrapped fetch error, got nil")
		}
		if !errors.Is(err, fetchErr) {
			t.Fatalf("expected wrapped 429 error, got %v", err)
		}
	}
	if c := stub.calls.Load(); c != 1 {
		t.Fatalf("expected one origin fetch under negative cache, got %d", c)
	}
}

func TestGetRobotsRules_RefetchesAfterPositiveTTL(t *testing.T) {
	rules := &crawler.RobotsRules{}
	stub := &stubRobotsCrawler{
		discoverFn: func(context.Context, string) (*crawler.SitemapDiscoveryResult, error) {
			return &crawler.SitemapDiscoveryResult{RobotsRules: rules}, nil
		},
	}
	jm := newJobManagerWithCrawler(stub)
	jm.robotsTTLPos = 0 // every read is a miss
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := jm.GetRobotsRules(ctx, "expiring.com"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if c := stub.calls.Load(); c != 3 {
		t.Fatalf("expected 3 origin fetches with zero TTL, got %d", c)
	}
}

func TestGetRobotsRules_RefetchesAfterNegativeTTLExpires(t *testing.T) {
	var attempts atomic.Int32
	stub := &stubRobotsCrawler{
		discoverFn: func(context.Context, string) (*crawler.SitemapDiscoveryResult, error) {
			if attempts.Add(1) == 1 {
				return nil, errors.New("transient 429")
			}
			return &crawler.SitemapDiscoveryResult{RobotsRules: &crawler.RobotsRules{}}, nil
		},
	}
	jm := newJobManagerWithCrawler(stub)
	jm.robotsTTLNeg = 0 // failure entry expires immediately
	ctx := context.Background()

	if _, err := jm.GetRobotsRules(ctx, "recover.com"); err == nil {
		t.Fatalf("expected initial failure")
	}
	// Negative entry is already expired — second call must refetch and succeed.
	if _, err := jm.GetRobotsRules(ctx, "recover.com"); err != nil {
		t.Fatalf("expected recovery on refetch, got %v", err)
	}
	if c := stub.calls.Load(); c != 2 {
		t.Fatalf("expected 2 origin fetches after negative TTL expiry, got %d", c)
	}
}

func TestGetRobotsRules_NormalisesDomain(t *testing.T) {
	rules := &crawler.RobotsRules{}
	stub := &stubRobotsCrawler{
		discoverFn: func(context.Context, string) (*crawler.SitemapDiscoveryResult, error) {
			return &crawler.SitemapDiscoveryResult{RobotsRules: rules}, nil
		},
	}
	jm := newJobManagerWithCrawler(stub)
	ctx := context.Background()

	if _, err := jm.GetRobotsRules(ctx, "https://Example.com/"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := jm.GetRobotsRules(ctx, "example.com"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c := stub.calls.Load(); c != 1 {
		t.Fatalf("normalised keys should share cache; got %d origin fetches", c)
	}
}

func TestGetRobotsRules_CollapsesConcurrentMisses(t *testing.T) {
	gate := make(chan struct{})
	stub := &stubRobotsCrawler{
		discoverFn: func(context.Context, string) (*crawler.SitemapDiscoveryResult, error) {
			<-gate
			return &crawler.SitemapDiscoveryResult{RobotsRules: &crawler.RobotsRules{}}, nil
		},
	}
	jm := newJobManagerWithCrawler(stub)
	ctx := context.Background()

	const fanout = 20
	var wg sync.WaitGroup
	errCh := make(chan error, fanout)
	wg.Add(fanout)
	for i := 0; i < fanout; i++ {
		go func() {
			defer wg.Done()
			if _, err := jm.GetRobotsRules(ctx, "swarm.com"); err != nil {
				errCh <- err
			}
		}()
	}

	// Wait until the discoverFn is parked on the gate, then release.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && stub.calls.Load() == 0 {
		time.Sleep(2 * time.Millisecond)
	}
	close(gate)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("unexpected error from concurrent caller: %v", err)
	}

	if c := stub.calls.Load(); c != 1 {
		t.Fatalf("singleflight should collapse %d concurrent misses to one fetch, got %d", fanout, c)
	}
}

func TestGetRobotsRules_DoesNotCacheContextCancellation(t *testing.T) {
	var attempts atomic.Int32
	stub := &stubRobotsCrawler{
		discoverFn: func(_ context.Context, _ string) (*crawler.SitemapDiscoveryResult, error) {
			if attempts.Add(1) == 1 {
				return nil, context.Canceled
			}
			return &crawler.SitemapDiscoveryResult{RobotsRules: &crawler.RobotsRules{}}, nil
		},
	}
	jm := newJobManagerWithCrawler(stub)
	jm.robotsTTLNeg = time.Hour // would normally suppress a refetch

	// First call surfaces the cancellation error but must NOT cache it.
	if _, err := jm.GetRobotsRules(context.Background(), "cancel.com"); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected wrapped context.Canceled, got %v", err)
	}

	// Second call must refetch and succeed — a transient cancel cannot
	// poison the shared cache.
	if _, err := jm.GetRobotsRules(context.Background(), "cancel.com"); err != nil {
		t.Fatalf("expected recovery on refetch, got %v", err)
	}
	if c := stub.calls.Load(); c != 2 {
		t.Fatalf("expected 2 origin fetches when cancellation is not cached, got %d", c)
	}
}

func TestGetRobotsRules_FetchesUnderCanonicalDomain(t *testing.T) {
	var seen string
	stub := &stubRobotsCrawler{
		discoverFn: func(_ context.Context, domain string) (*crawler.SitemapDiscoveryResult, error) {
			seen = domain
			return &crawler.SitemapDiscoveryResult{RobotsRules: &crawler.RobotsRules{}}, nil
		},
	}
	jm := newJobManagerWithCrawler(stub)

	if _, err := jm.GetRobotsRules(context.Background(), "HTTPS://www.Example.COM/"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if seen != "example.com" {
		t.Fatalf("origin fetch must use the canonical key; got %q", seen)
	}
}

// Compile-time guard that the stub satisfies CrawlerInterface; if a method
// is added to the interface, this assertion breaks at build rather than
// at test time.
var _ CrawlerInterface = (*stubRobotsCrawler)(nil)
