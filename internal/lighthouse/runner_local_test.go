package lighthouse

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/good-native/hover/internal/archive"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeProvider mirrors the archive.ColdStorageProvider used elsewhere in
// the repo (internal/jobs/html_persister_test.go). Kept simple — we
// only need to capture the upload key and payload.
type fakeProvider struct {
	mu        sync.Mutex
	uploads   []fakeUpload
	uploadErr error
}

type fakeUpload struct {
	bucket  string
	key     string
	payload []byte
	opts    archive.UploadOptions
}

func (f *fakeProvider) Upload(_ context.Context, bucket, key string, data io.Reader, opts archive.UploadOptions) error {
	if f.uploadErr != nil {
		return f.uploadErr
	}
	body, err := io.ReadAll(data)
	if err != nil {
		return fmt.Errorf("fake provider: read upload payload: %w", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.uploads = append(f.uploads, fakeUpload{bucket: bucket, key: key, payload: body, opts: opts})
	return nil
}

func (f *fakeProvider) Ping(_ context.Context, _ string) error { return nil }
func (f *fakeProvider) Download(_ context.Context, _, _ string) (io.ReadCloser, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeProvider) Exists(_ context.Context, _, _ string) (bool, error) { return true, nil }
func (f *fakeProvider) Provider() string                                    { return "fake" }

const sampleLighthouseJSON = `{
  "categories": { "performance": { "score": 0.91 } },
  "audits": {
    "largest-contentful-paint":  { "numericValue": 2100 },
    "cumulative-layout-shift":   { "numericValue": 0.05 },
    "interaction-to-next-paint": { "numericValue": 150 },
    "total-blocking-time":       { "numericValue": 200 },
    "first-contentful-paint":    { "numericValue": 1100 },
    "speed-index":               { "numericValue": 2700 },
    "server-response-time":      { "numericValue": 280 },
    "total-byte-weight":         { "numericValue": 1200000 }
  }
}`

// writeFakeLighthouseScript drops a tiny shell script that prints
// canned Lighthouse JSON to stdout and exits 0 on first invocation.
// If failFirst is set, the first call exits 1 with the supplied stderr
// and the second succeeds — used to exercise the transient-retry path.
func writeFakeLighthouseScript(t *testing.T, failFirst bool, transientStderr string) (path string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake unsupported on windows")
	}
	dir := t.TempDir()
	flag := filepath.Join(dir, "called.txt")
	scriptPath := filepath.Join(dir, "fake-lighthouse.sh")

	body := "#!/bin/sh\n"
	if failFirst {
		// First call: touch the flag, write transient stderr, exit 1.
		// Second call: print canned JSON, exit 0.
		body += `if [ -f "` + flag + `" ]; then
  cat <<'JSON'
` + sampleLighthouseJSON + `
JSON
  exit 0
fi
touch "` + flag + `"
echo "` + transientStderr + `" 1>&2
exit 1
`
	} else {
		body += `cat <<'JSON'
` + sampleLighthouseJSON + `
JSON
exit 0
`
	}

	// #nosec G306 -- test fixture: the fake binary must be executable.
	require.NoError(t, os.WriteFile(scriptPath, []byte(body), 0o755))
	return scriptPath
}

// newTestRunner builds a LocalRunner with a fake provider and an
// optional memory-shed shim. memMB < 0 disables the check by setting
// cfg.MemoryShedMB=0. ChromiumBin is a stand-in executable in the
// caller's t.TempDir so NewLocalRunner's validateExecutable check
// passes — the local runner never invokes it directly (lighthouse
// drives Chromium itself), it just plumbs the path into CHROME_PATH.
func newTestRunner(t *testing.T, lhBin string, memMB int, available int) (*LocalRunner, *fakeProvider) {
	t.Helper()
	chromiumStub := filepath.Join(t.TempDir(), "chromium-stub")
	// #nosec G306 -- test fixture: stand-in chromium must be executable.
	require.NoError(t, os.WriteFile(chromiumStub, []byte("#!/bin/sh\nexit 0\n"), 0o755))

	provider := &fakeProvider{}
	r, err := NewLocalRunner(LocalRunnerConfig{
		LighthouseBin: lhBin,
		ChromiumBin:   chromiumStub,
		Provider:      provider,
		Bucket:        "test-bucket",
		MemoryShedMB:  memMB,
	})
	require.NoError(t, err)
	if memMB > 0 {
		r.readMemAvailableMB = func() (int, error) { return available, nil }
	}
	return r, provider
}

func TestNewLocalRunner_ValidatesConfig(t *testing.T) {
	dir := t.TempDir()
	goodLH := filepath.Join(dir, "lighthouse")
	goodCH := filepath.Join(dir, "chromium")
	nonExec := filepath.Join(dir, "non-exec")
	subdir := filepath.Join(dir, "subdir")
	// #nosec G306 -- test fixtures: stand-in binaries must be executable.
	require.NoError(t, os.WriteFile(goodLH, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	// #nosec G306 -- test fixtures: stand-in binaries must be executable.
	require.NoError(t, os.WriteFile(goodCH, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	require.NoError(t, os.WriteFile(nonExec, []byte("not executable"), 0o600))
	require.NoError(t, os.Mkdir(subdir, 0o750))

	cases := []struct {
		name        string
		cfg         LocalRunnerConfig
		errContains string
	}{
		{
			name:        "missing lighthouse bin",
			cfg:         LocalRunnerConfig{ChromiumBin: goodCH, Provider: &fakeProvider{}, Bucket: "b"},
			errContains: "LighthouseBin is required",
		},
		{
			name:        "missing chromium bin",
			cfg:         LocalRunnerConfig{LighthouseBin: goodLH, Provider: &fakeProvider{}, Bucket: "b"},
			errContains: "ChromiumBin is required",
		},
		{
			name:        "missing provider",
			cfg:         LocalRunnerConfig{LighthouseBin: goodLH, ChromiumBin: goodCH, Bucket: "b"},
			errContains: "archive provider is required",
		},
		{
			name:        "missing bucket",
			cfg:         LocalRunnerConfig{LighthouseBin: goodLH, ChromiumBin: goodCH, Provider: &fakeProvider{}},
			errContains: "bucket is required",
		},
		{
			name:        "lighthouse bin does not exist",
			cfg:         LocalRunnerConfig{LighthouseBin: "/nope/lighthouse", ChromiumBin: goodCH, Provider: &fakeProvider{}, Bucket: "b"},
			errContains: "invalid LighthouseBin",
		},
		{
			name:        "chromium bin does not exist",
			cfg:         LocalRunnerConfig{LighthouseBin: goodLH, ChromiumBin: "/nope/chromium", Provider: &fakeProvider{}, Bucket: "b"},
			errContains: "invalid ChromiumBin",
		},
		{
			name:        "lighthouse bin is a directory",
			cfg:         LocalRunnerConfig{LighthouseBin: subdir, ChromiumBin: goodCH, Provider: &fakeProvider{}, Bucket: "b"},
			errContains: "is a directory",
		},
		{
			name:        "lighthouse bin is not executable",
			cfg:         LocalRunnerConfig{LighthouseBin: nonExec, ChromiumBin: goodCH, Provider: &fakeProvider{}, Bucket: "b"},
			errContains: "is not executable",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewLocalRunner(tc.cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.errContains)
		})
	}
}

func TestLocalRunner_MemoryShedReturnsSentinel(t *testing.T) {
	// Use a stub binary so NewLocalRunner's executable validation
	// passes; the memory-shed check trips before the binary is ever
	// invoked, so a no-op script suffices.
	stub := writeFakeLighthouseScript(t, false, "")
	r, provider := newTestRunner(t, stub, 600, 200)
	_, err := r.Run(context.Background(), AuditRequest{
		RunID: 1, JobID: "j", URL: "https://example.com",
		Profile: ProfileMobile, Timeout: time.Second,
	})
	require.ErrorIs(t, err, ErrMemoryShed)
	assert.Empty(t, provider.uploads, "no upload should occur when shed")
}

func TestLocalRunner_HappyPathUploadsAndReturnsKey(t *testing.T) {
	script := writeFakeLighthouseScript(t, false, "")
	r, provider := newTestRunner(t, script, 0, 0)

	t.Setenv("ARCHIVE_PATH_PREFIX", "")
	result, err := r.Run(context.Background(), AuditRequest{
		RunID:        7,
		JobID:        "job-abc",
		PageID:       42,
		SourceTaskID: "task-xyz",
		URL:          "https://example.com/page?token=secret",
		Profile:      ProfileMobile,
		Timeout:      5 * time.Second,
	})
	require.NoError(t, err)

	require.NotNil(t, result.PerformanceScore)
	assert.Equal(t, 91, *result.PerformanceScore)
	assert.Equal(t, "jobs/job-abc/tasks/task-xyz/lighthouse-mobile.json.gz", result.ReportKey)
	assert.Greater(t, result.Duration, time.Duration(0))

	require.Len(t, provider.uploads, 1)
	up := provider.uploads[0]
	assert.Equal(t, "test-bucket", up.bucket)
	assert.Equal(t, result.ReportKey, up.key)
	assert.Equal(t, "application/json", up.opts.ContentType)
	assert.Equal(t, "gzip", up.opts.ContentEncoding)
	assert.Equal(t, "7", up.opts.Metadata["run_id"])
}

// TestLocalRunner_DoesNotPassPresetFlagForMobile pins the contract
// against Lighthouse 12.x's CLI: --preset only accepts 'desktop',
// 'experimental', or 'perf'. Mobile is the implicit default and
// passing --preset=mobile fails argument validation before Chromium
// launches. The fix omits the flag entirely for mobile audits;
// regressing that would silently break every audit in production.
func TestLocalRunner_DoesNotPassPresetFlagForMobile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake unsupported on windows")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "args.txt")
	script := filepath.Join(dir, "fake-lighthouse.sh")
	body := `#!/bin/sh
echo "$@" > "` + argsLog + `"
cat <<'JSON'
` + sampleLighthouseJSON + `
JSON
exit 0
`
	// #nosec G306 -- test fixture: the fake binary must be executable.
	require.NoError(t, os.WriteFile(script, []byte(body), 0o755))

	r, _ := newTestRunner(t, script, 0, 0)
	_, err := r.Run(context.Background(), AuditRequest{
		RunID: 1, JobID: "j", SourceTaskID: "t", URL: "https://example.com",
		Profile: ProfileMobile, Timeout: 5 * time.Second,
	})
	require.NoError(t, err)

	// #nosec G304 -- argsLog is t.TempDir-rooted, written above.
	logged, err := os.ReadFile(argsLog)
	require.NoError(t, err)
	assert.NotContains(t, string(logged), "--preset=mobile",
		"mobile is Lighthouse's implicit default; passing --preset=mobile is rejected")
	assert.NotContains(t, string(logged), "--preset=desktop",
		"desktop preset must not leak into a mobile audit")
}

// TestLocalRunner_PassesPresetForDesktop confirms desktop audits do
// flip --preset=desktop on. Currently dead-code-pathed since v1 only
// schedules mobile, but the runner-level code path exists and we want
// to catch a regression early when desktop ships in Phase 5.
func TestLocalRunner_PassesPresetForDesktop(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake unsupported on windows")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "args.txt")
	script := filepath.Join(dir, "fake-lighthouse.sh")
	body := `#!/bin/sh
echo "$@" > "` + argsLog + `"
cat <<'JSON'
` + sampleLighthouseJSON + `
JSON
exit 0
`
	// #nosec G306 -- test fixture: the fake binary must be executable.
	require.NoError(t, os.WriteFile(script, []byte(body), 0o755))

	r, _ := newTestRunner(t, script, 0, 0)
	_, err := r.Run(context.Background(), AuditRequest{
		RunID: 1, JobID: "j", SourceTaskID: "t", URL: "https://example.com",
		Profile: ProfileDesktop, Timeout: 5 * time.Second,
	})
	require.NoError(t, err)

	// #nosec G304 -- argsLog is t.TempDir-rooted, written above.
	logged, err := os.ReadFile(argsLog)
	require.NoError(t, err)
	assert.Contains(t, string(logged), "--preset=desktop")
}

func TestLocalRunner_FallsBackToRunIDPathWhenNoTaskID(t *testing.T) {
	script := writeFakeLighthouseScript(t, false, "")
	r, provider := newTestRunner(t, script, 0, 0)

	t.Setenv("ARCHIVE_PATH_PREFIX", "")
	result, err := r.Run(context.Background(), AuditRequest{
		RunID: 99, JobID: "job-1", URL: "https://example.com",
		Profile: ProfileMobile, Timeout: 5 * time.Second,
		// SourceTaskID intentionally empty.
	})
	require.NoError(t, err)
	assert.Equal(t, "jobs/job-1/runs/99/lighthouse-mobile.json.gz", result.ReportKey)
	require.Len(t, provider.uploads, 1)
}

func TestLocalRunner_RetriesOnTransientStderr(t *testing.T) {
	script := writeFakeLighthouseScript(t, true, "Inspector.targetCrashed: renderer died")
	r, provider := newTestRunner(t, script, 0, 0)

	result, err := r.Run(context.Background(), AuditRequest{
		RunID: 1, JobID: "j", SourceTaskID: "t", URL: "https://example.com",
		Profile: ProfileMobile, Timeout: 10 * time.Second,
	})
	require.NoError(t, err, "transient failure should retry and succeed")
	require.NotNil(t, result.PerformanceScore)
	require.Len(t, provider.uploads, 1)
}

func TestLocalRunner_NonTransientFailsImmediately(t *testing.T) {
	script := writeFakeLighthouseScript(t, true, "ENOENT: chromium not found")
	r, provider := newTestRunner(t, script, 0, 0)

	_, err := r.Run(context.Background(), AuditRequest{
		RunID: 1, JobID: "j", URL: "https://example.com",
		Profile: ProfileMobile, Timeout: 5 * time.Second,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ENOENT", "stderr tail should be in the error")
	assert.Empty(t, provider.uploads, "no upload on hard failure")
}

// TestLocalRunner_StderrRedactsAuditURL pins the privacy contract on
// the failure path. Lighthouse and Chromium routinely echo the audited
// URL into stderr; without redaction those query strings (which can
// carry session tokens or signed-link tokens) end up in
// lighthouse_runs.error_message and in centralised logs. Mirrors the
// SanitiseAuditURL rule already applied to start/finish log lines.
func TestLocalRunner_StderrRedactsAuditURL(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake unsupported on windows")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "fake.sh")
	// Echo every arg so the URL (always the last positional in our
	// runner) is guaranteed to land in stderr regardless of flag
	// position. Mirrors how real Chromium error paths echo "navigation
	// to <url> failed".
	body := `#!/bin/sh
for a in "$@"; do
  echo "lighthouse stderr ref: $a" 1>&2
done
echo "fatal: navigation failed: missing CDP target" 1>&2
exit 1
`
	// #nosec G306 -- test fixture: the fake binary must be executable.
	require.NoError(t, os.WriteFile(script, []byte(body), 0o755))

	r, _ := newTestRunner(t, script, 0, 0)
	rawURL := "https://example.com/secret?token=leak-me-please&session=abc"
	_, err := r.Run(context.Background(), AuditRequest{
		RunID: 1, JobID: "j", URL: rawURL,
		Profile: ProfileMobile, Timeout: 5 * time.Second,
	})
	require.Error(t, err)
	msg := err.Error()
	assert.NotContains(t, msg, "token=leak-me-please",
		"query token must not survive into the returned error")
	assert.NotContains(t, msg, "session=abc",
		"query token must not survive into the returned error")
	assert.Contains(t, msg, "https://example.com/secret",
		"sanitised URL prefix should remain so diagnostics still locate the page")
}

func TestLocalRunner_TimeoutCancelsAndKillsProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake unsupported on windows")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "sleep.sh")
	// #nosec G306 -- test fixture: the fake binary must be executable.
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nsleep 30\n"), 0o755))

	r, _ := newTestRunner(t, script, 0, 0)

	start := time.Now()
	_, err := r.Run(context.Background(), AuditRequest{
		RunID: 1, JobID: "j", URL: "https://example.com",
		Profile: ProfileMobile, Timeout: 200 * time.Millisecond,
	})
	elapsed := time.Since(start)
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.DeadlineExceeded),
		"expected deadline exceeded, got %v", err)
	assert.Less(t, elapsed, 5*time.Second, "process tree must die promptly on timeout")
}

// TestSanitiseRunnerStderr_RedactsBeforeTruncating pins the order of
// operations: the full URL must be replaced *before* truncation, not
// after. If truncation happened first, a URL straddling the 2 KiB
// cut-off would no longer match `rawURL` and the trailing query token
// would survive into lighthouse_runs.error_message.
func TestSanitiseRunnerStderr_RedactsBeforeTruncating(t *testing.T) {
	rawURL := "https://example.com/path?token=leak-me-please&session=abc"
	// Build a stderr tail where the URL appears just before the
	// truncateForLog 2 KiB cut-off, so half the URL would be lost
	// if truncation ran first.
	prefix := strings.Repeat("noise ", 400) // ~2400 bytes
	tail := []byte(prefix + "fatal: navigation to " + rawURL + " failed")

	out := sanitiseRunnerStderr(rawURL, tail)

	assert.NotContains(t, out, "token=leak-me-please",
		"query token must not survive truncation regardless of where the URL falls")
	assert.NotContains(t, out, "session=abc",
		"query token must not survive truncation regardless of where the URL falls")
	assert.LessOrEqual(t, len(out), 2048+3,
		"truncation must still bound the output (2048 + leading '...' marker)")
}

func TestSanitiseRunnerStderr_NoURLPassthrough(t *testing.T) {
	out := sanitiseRunnerStderr("", []byte("plain stderr without url"))
	assert.Equal(t, "plain stderr without url", out)
}

func TestTransientRetryReason(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{nil, ""},
		{context.Canceled, ""},
		{context.DeadlineExceeded, ""},
		{fmt.Errorf("Inspector.targetCrashed: renderer died"), "target_crashed"},
		{fmt.Errorf("Protocol error: connection lost"), "protocol_error"},
		{fmt.Errorf("Page crashed during eval"), "page_crashed"},
		{fmt.Errorf("Target closed"), "target_closed"},
		{fmt.Errorf("WebSocket is not open"), "websocket_closed"},
		{fmt.Errorf("ENOENT no such file"), ""},
		{fmt.Errorf("invalid URL"), ""},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, transientRetryReason(tc.err), tc.err)
	}
}

func TestTailBuffer_KeepsLastBytes(t *testing.T) {
	t.Run("under cap stays as-is", func(t *testing.T) {
		buf := newTailBuffer(10)
		_, _ = buf.Write([]byte("hello"))
		assert.Equal(t, "hello", string(buf.tail()))
	})
	t.Run("at cap fills exactly", func(t *testing.T) {
		buf := newTailBuffer(5)
		_, _ = buf.Write([]byte("hello"))
		assert.Equal(t, "hello", string(buf.tail()))
	})
	t.Run("over cap keeps last cap bytes in chronological order", func(t *testing.T) {
		buf := newTailBuffer(5)
		_, _ = buf.Write([]byte("abc"))
		_, _ = buf.Write([]byte("defgh"))
		assert.Equal(t, "defgh", string(buf.tail()))
	})
	t.Run("single oversized write truncates to last cap", func(t *testing.T) {
		buf := newTailBuffer(4)
		_, _ = buf.Write([]byte("abcdefgh"))
		assert.Equal(t, "efgh", string(buf.tail()))
	})
	t.Run("wrap-around preserves order", func(t *testing.T) {
		buf := newTailBuffer(5)
		_, _ = buf.Write([]byte("abcde"))
		_, _ = buf.Write([]byte("fg"))
		assert.Equal(t, "cdefg", string(buf.tail()))
	})
}
