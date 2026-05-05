package lighthouse

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/good-native/hover/internal/archive"
	"github.com/good-native/hover/internal/observability"
)

// ErrMemoryShed signals the consumer to leave the row in 'running' and skip XAck so XAUTOCLAIM redelivers once memory recovers.
var ErrMemoryShed = errors.New("lighthouse runner shed audit due to low memory")

// 16 KiB keeps the trailing stack/protocol-error context without bloating the TEXT error_message column.
const stderrTailBytes = 16 * 1024

// Stderr substrings recognised as transient Chromium failures worth one retry; the reason tag feeds the run_retries_total metric.
var transientStderrSubstrings = []struct {
	pattern string
	reason  string
}{
	{"Inspector.targetCrashed", "target_crashed"},
	{"Protocol error", "protocol_error"},
	{"Page crashed", "page_crashed"},
	{"WebSocket is not open", "websocket_closed"},
	{"Target closed", "target_closed"},
}

type LocalRunnerConfig struct {
	LighthouseBin string
	ChromiumBin   string
	Provider      archive.ColdStorageProvider
	Bucket        string
	MemoryShedMB  int
	ProfilePreset Profile // defaults to ProfileMobile if empty
}

type LocalRunner struct {
	cfg                LocalRunnerConfig
	readMemAvailableMB func() (int, error)
	now                func() time.Time
}

func NewLocalRunner(cfg LocalRunnerConfig) (*LocalRunner, error) {
	if strings.TrimSpace(cfg.LighthouseBin) == "" {
		return nil, errors.New("local runner: LighthouseBin is required")
	}
	if err := validateExecutable(cfg.LighthouseBin); err != nil {
		return nil, fmt.Errorf("local runner: invalid LighthouseBin: %w", err)
	}
	if strings.TrimSpace(cfg.ChromiumBin) == "" {
		return nil, errors.New("local runner: ChromiumBin is required")
	}
	if err := validateExecutable(cfg.ChromiumBin); err != nil {
		return nil, fmt.Errorf("local runner: invalid ChromiumBin: %w", err)
	}
	if cfg.Provider == nil {
		return nil, errors.New("local runner: archive provider is required")
	}
	if strings.TrimSpace(cfg.Bucket) == "" {
		return nil, errors.New("local runner: bucket is required")
	}
	if cfg.ProfilePreset == "" {
		cfg.ProfilePreset = ProfileMobile
	}
	return &LocalRunner{
		cfg:                cfg,
		readMemAvailableMB: defaultReadMemAvailableMB,
		now:                time.Now,
	}, nil
}

// Fail at boot rather than ENOENT on the first audit, which would already have moved the row to 'running' and triggered a redelivery storm.
func validateExecutable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory", path)
	}
	if info.Mode()&0o111 == 0 {
		return fmt.Errorf("%s is not executable", path)
	}
	return nil
}

// At most one retry on a transient Chromium failure detected via stderr substring match.
func (l *LocalRunner) Run(ctx context.Context, req AuditRequest) (AuditResult, error) {
	if l.cfg.MemoryShedMB > 0 {
		avail, err := l.readMemAvailableMB()
		if err != nil {
			lighthouseLog.Warn("memory shed check failed; proceeding",
				"error", err, "run_id", req.RunID, "job_id", req.JobID,
			)
		} else if avail < l.cfg.MemoryShedMB {
			lighthouseLog.Info("memory shed: deferring audit",
				"run_id", req.RunID, "job_id", req.JobID,
				"available_mb", avail, "threshold_mb", l.cfg.MemoryShedMB,
			)
			return AuditResult{}, ErrMemoryShed
		}
	}

	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}

	start := l.now()

	stdout, runErr := l.runOnce(ctx, req)
	if reason := transientRetryReason(runErr); reason != "" && ctx.Err() == nil {
		lighthouseLog.Warn("lighthouse transient failure; retrying once",
			"run_id", req.RunID, "job_id", req.JobID,
			"reason", reason, "error", runErr,
		)
		observability.RecordLighthouseRunRetry(ctx, req.JobID, reason)
		stdout, runErr = l.runOnce(ctx, req)
	}
	if runErr != nil {
		return AuditResult{}, runErr
	}

	result, parseErr := ParseReport(stdout)
	if parseErr != nil {
		return AuditResult{}, parseErr
	}

	key, uploadErr := l.uploadReport(ctx, req, stdout)
	if uploadErr != nil {
		return AuditResult{}, uploadErr
	}
	result.ReportKey = key
	result.Duration = l.now().Sub(start)

	return result, nil
}

func (l *LocalRunner) runOnce(ctx context.Context, req AuditRequest) ([]byte, error) {
	profile := req.Profile
	if profile == "" {
		profile = l.cfg.ProfilePreset
	}

	// Lighthouse 12.x --preset accepts only 'desktop', 'experimental', or 'perf'; mobile is implicit and --preset=mobile fails CLI validation.
	args := []string{
		"--output=json",
		"--quiet",
		"--max-wait-for-load=45000",
		"--chrome-flags=--headless=new --no-sandbox --disable-gpu",
	}
	if profile == ProfileDesktop {
		args = append(args, "--preset=desktop")
	}
	args = append(args, req.URL)

	// #nosec G204 -- LighthouseBin is trusted env config; req.URL is validated by Lighthouse and all flags are hard-coded.
	cmd := exec.CommandContext(ctx, l.cfg.LighthouseBin, args...)
	// Own process group so a cancelled Run can SIGKILL renderers via negative-pid; without this they orphan and eat memory.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// CHROME_PATH avoids Lighthouse's $PATH discovery, which fails in slim containers.
	cmd.Env = append(os.Environ(),
		"CHROME_PATH="+l.cfg.ChromiumBin,
	)
	var stdout bytes.Buffer
	stderr := newTailBuffer(stderrTailBytes)
	cmd.Stdout = &stdout
	cmd.Stderr = stderr

	startErr := cmd.Start()
	if startErr != nil {
		return nil, fmt.Errorf("start lighthouse: %w", startErr)
	}

	pgid := cmd.Process.Pid
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	select {
	case <-ctx.Done():
		// Process may already have exited between select fire and syscall.
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		<-waitDone
		return nil, ctx.Err()
	case waitErr := <-waitDone:
		if waitErr != nil {
			tail := stderr.tail()
			return nil, fmt.Errorf("lighthouse exit: %w (stderr: %s)",
				waitErr, sanitiseRunnerStderr(req.URL, tail))
		}
	}

	return stdout.Bytes(), nil
}

func (l *LocalRunner) uploadReport(ctx context.Context, req AuditRequest, raw []byte) (string, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(raw); err != nil {
		return "", fmt.Errorf("gzip lighthouse report: %w", err)
	}
	if err := gz.Close(); err != nil {
		return "", fmt.Errorf("gzip close: %w", err)
	}

	profile := string(req.Profile)
	if profile == "" {
		profile = string(l.cfg.ProfilePreset)
	}

	key := archive.LighthouseObjectPath(req.JobID, req.SourceTaskID, profile, req.RunID)

	uploadErr := l.cfg.Provider.Upload(ctx, l.cfg.Bucket, key, &buf, archive.UploadOptions{
		ContentType:     "application/json",
		ContentEncoding: "gzip",
		Metadata: map[string]string{
			"run_id":  strconv.FormatInt(req.RunID, 10),
			"job_id":  req.JobID,
			"profile": profile,
		},
	})
	if uploadErr != nil {
		return "", fmt.Errorf("upload lighthouse report: %w", uploadErr)
	}
	return key, nil
}

// Empty return means fail fast; context cancel/deadline are never retried since they are caller signals.
func transientRetryReason(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ""
	}
	msg := err.Error()
	for _, e := range transientStderrSubstrings {
		if strings.Contains(msg, e.pattern) {
			return e.reason
		}
	}
	return ""
}

// Ring buffer keeping only the last `cap` bytes; a wedged Lighthouse run can emit megabytes of debug stderr.
type tailBuffer struct {
	cap  int
	buf  []byte
	full bool
	pos  int
}

func newTailBuffer(cap int) *tailBuffer {
	return &tailBuffer{cap: cap, buf: make([]byte, 0, cap)}
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if n >= t.cap {
		t.buf = append(t.buf[:0], p[n-t.cap:]...)
		t.full = true
		t.pos = 0
		return n, nil
	}
	if !t.full && len(t.buf)+n <= t.cap {
		t.buf = append(t.buf, p...)
		if len(t.buf) == t.cap {
			t.full = true
			t.pos = 0
		}
		return n, nil
	}
	if !t.full {
		t.buf = append(t.buf, make([]byte, t.cap-len(t.buf))...)
		t.full = true
		t.pos = 0
	}
	for i := 0; i < n; i++ {
		t.buf[t.pos] = p[i]
		t.pos = (t.pos + 1) % t.cap
	}
	return n, nil
}

func (t *tailBuffer) tail() []byte {
	if !t.full {
		out := make([]byte, len(t.buf))
		copy(out, t.buf)
		return out
	}
	out := make([]byte, t.cap)
	copy(out, t.buf[t.pos:])
	copy(out[t.cap-t.pos:], t.buf[:t.pos])
	return out
}

// Caps stderr text to fit Grafana's structured-log line size limit.
func truncateForLog(b []byte) string {
	const max = 2048
	if len(b) <= max {
		return string(b)
	}
	return "..." + string(b[len(b)-max:])
}

// Lighthouse and Chromium echo the raw URL into stderr, which can leak session/signed-link tokens via error_message.
// Order matters: redact before truncating, otherwise a URL straddling the 2 KiB cut-off would no longer match and leak partial query/fragment.
func sanitiseRunnerStderr(rawURL string, tail []byte) string {
	msg := string(tail)
	if rawURL != "" {
		msg = strings.ReplaceAll(msg, rawURL, SanitiseAuditURL(rawURL))
	}
	return truncateForLog([]byte(msg))
}

// Returns an error rather than zero on non-Linux or unreadable /proc/meminfo so the caller can log-and-continue.
func defaultReadMemAvailableMB() (int, error) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "MemAvailable:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, fmt.Errorf("malformed MemAvailable line: %q", line)
		}
		kb, parseErr := strconv.Atoi(fields[1])
		if parseErr != nil {
			return 0, fmt.Errorf("parse MemAvailable: %w", parseErr)
		}
		return kb / 1024, nil
	}
	return 0, errors.New("MemAvailable not found in /proc/meminfo")
}

var _ Runner = (*LocalRunner)(nil)
