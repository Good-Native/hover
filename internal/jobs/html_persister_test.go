package jobs

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/good-native/hover/internal/archive"
	"github.com/good-native/hover/internal/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- fakes ---

// fakeProvider implements archive.ColdStorageProvider for testing.
type fakeProvider struct {
	mu          sync.Mutex
	uploads     []fakeUpload
	uploadErr   error
	providerStr string
}

type fakeUpload struct {
	bucket  string
	key     string
	payload []byte
	opts    archive.UploadOptions
}

func (f *fakeProvider) Upload(ctx context.Context, bucket, key string, data io.Reader, opts archive.UploadOptions) error {
	if f.uploadErr != nil {
		return f.uploadErr
	}
	body, _ := io.ReadAll(data)
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
func (f *fakeProvider) Provider() string {
	if f.providerStr != "" {
		return f.providerStr
	}
	return "fake"
}

func (f *fakeProvider) uploadCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.uploads)
}

// fakeDBQueue implements the subset of DbQueueInterface used by HTMLPersister.
type fakeDBQueue struct {
	mu            sync.Mutex
	upsertedRows  []db.TaskHTMLMetadataRow
	upsertErr     error
	upsertErrOnce bool // return error only on first call
	callCount     atomic.Int32
}

func (f *fakeDBQueue) BatchUpsertTaskHTMLMetadata(_ context.Context, rows []db.TaskHTMLMetadataRow) error {
	f.callCount.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.upsertErr != nil {
		if f.upsertErrOnce {
			err := f.upsertErr
			f.upsertErr = nil
			return err
		}
		return f.upsertErr
	}
	f.upsertedRows = append(f.upsertedRows, rows...)
	return nil
}

func (f *fakeDBQueue) rows() []db.TaskHTMLMetadataRow {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]db.TaskHTMLMetadataRow(nil), f.upsertedRows...)
}

// Satisfy the remaining DbQueueInterface methods with no-ops.
func (f *fakeDBQueue) UpdateTaskStatus(_ context.Context, _ *db.Task) error { return nil }
func (f *fakeDBQueue) Execute(_ context.Context, _ func(*sql.Tx) error) error {
	return nil
}
func (f *fakeDBQueue) ExecuteControl(_ context.Context, _ func(*sql.Tx) error) error {
	return nil
}
func (f *fakeDBQueue) ExecuteWithContext(_ context.Context, _ func(context.Context, *sql.Tx) error) error {
	return nil
}
func (f *fakeDBQueue) ExecuteControlWithContext(_ context.Context, _ func(context.Context, *sql.Tx) error) error {
	return nil
}
func (f *fakeDBQueue) ExecuteMaintenance(_ context.Context, _ func(*sql.Tx) error) error {
	return nil
}
func (f *fakeDBQueue) SetConcurrencyOverride(_ db.ConcurrencyOverrideFunc) {}
func (f *fakeDBQueue) UpdateDomainTechnologies(_ context.Context, _ int, _, _ []byte, _ string) error {
	return nil
}
func (f *fakeDBQueue) UpdateTaskHTMLMetadata(_ context.Context, _ string, _ db.TaskHTMLMetadata) error {
	return nil
}
func (f *fakeDBQueue) PromoteWaitingToPending(_ context.Context, _ string, _ int) (int, error) {
	return 0, nil
}

// --- helpers ---

func minPersisterCfg() HTMLPersisterConfig {
	return HTMLPersisterConfig{
		Workers:       1,
		QueueSize:     8,
		BatchSize:     2,
		FlushInterval: 10 * time.Millisecond,
		UploadTimeout: 5 * time.Second,
		Bucket:        "test-bucket",
		Provider:      "fake",
	}
}

func sampleTask(id, jobID string) *db.Task {
	return &db.Task{ID: id, JobID: jobID}
}

func sampleUpload(path string) *TaskHTMLUpload {
	return &TaskHTMLUpload{
		Path:                path,
		ContentType:         "text/html",
		UploadContentType:   "text/html; charset=utf-8",
		ContentEncoding:     "gzip",
		SizeBytes:           100,
		CompressedSizeBytes: 60,
		SHA256:              "abc123",
		CapturedAt:          time.Now().UTC().Truncate(time.Second),
		Payload:             []byte("fake-gzip-payload"),
	}
}

// --- NewHTMLPersister validation ---

func TestNewHTMLPersister_NilProvider(t *testing.T) {
	t.Parallel()
	cfg := minPersisterCfg()
	_, err := NewHTMLPersister(cfg, HTMLPersisterDeps{Provider: nil, DBQueue: &fakeDBQueue{}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cold storage provider is required")
}

func TestNewHTMLPersister_NilDBQueue(t *testing.T) {
	t.Parallel()
	cfg := minPersisterCfg()
	_, err := NewHTMLPersister(cfg, HTMLPersisterDeps{Provider: &fakeProvider{}, DBQueue: nil})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "db queue is required")
}

func TestNewHTMLPersister_EmptyBucket(t *testing.T) {
	t.Parallel()
	cfg := minPersisterCfg()
	cfg.Bucket = ""
	_, err := NewHTMLPersister(cfg, HTMLPersisterDeps{Provider: &fakeProvider{}, DBQueue: &fakeDBQueue{}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bucket is required")
}

func TestNewHTMLPersister_ClampsZeroWorkers(t *testing.T) {
	t.Parallel()
	cfg := minPersisterCfg()
	cfg.Workers = 0
	p, err := NewHTMLPersister(cfg, HTMLPersisterDeps{Provider: &fakeProvider{}, DBQueue: &fakeDBQueue{}})
	require.NoError(t, err)
	assert.Equal(t, 1, p.cfg.Workers)
}

func TestNewHTMLPersister_ClampsZeroQueueSize(t *testing.T) {
	t.Parallel()
	cfg := minPersisterCfg()
	cfg.QueueSize = 0
	p, err := NewHTMLPersister(cfg, HTMLPersisterDeps{Provider: &fakeProvider{}, DBQueue: &fakeDBQueue{}})
	require.NoError(t, err)
	assert.Equal(t, 1, p.cfg.QueueSize)
}

func TestNewHTMLPersister_ClampsZeroBatchSize(t *testing.T) {
	t.Parallel()
	cfg := minPersisterCfg()
	cfg.BatchSize = 0
	p, err := NewHTMLPersister(cfg, HTMLPersisterDeps{Provider: &fakeProvider{}, DBQueue: &fakeDBQueue{}})
	require.NoError(t, err)
	assert.Equal(t, 1, p.cfg.BatchSize)
}

func TestNewHTMLPersister_DefaultsFlushInterval(t *testing.T) {
	t.Parallel()
	cfg := minPersisterCfg()
	cfg.FlushInterval = 0
	p, err := NewHTMLPersister(cfg, HTMLPersisterDeps{Provider: &fakeProvider{}, DBQueue: &fakeDBQueue{}})
	require.NoError(t, err)
	assert.Equal(t, 250*time.Millisecond, p.cfg.FlushInterval)
}

func TestNewHTMLPersister_DefaultsUploadTimeout(t *testing.T) {
	t.Parallel()
	cfg := minPersisterCfg()
	cfg.UploadTimeout = 0
	p, err := NewHTMLPersister(cfg, HTMLPersisterDeps{Provider: &fakeProvider{}, DBQueue: &fakeDBQueue{}})
	require.NoError(t, err)
	assert.Equal(t, 30*time.Second, p.cfg.UploadTimeout)
}

func TestNewHTMLPersister_FallsBackToProviderName(t *testing.T) {
	t.Parallel()
	cfg := minPersisterCfg()
	cfg.Provider = ""
	p, err := NewHTMLPersister(cfg, HTMLPersisterDeps{
		Provider: &fakeProvider{providerStr: "r2"},
		DBQueue:  &fakeDBQueue{},
	})
	require.NoError(t, err)
	assert.Equal(t, "r2", p.cfg.Provider)
}

// --- DefaultHTMLPersisterConfig ---

func TestDefaultHTMLPersisterConfig(t *testing.T) {
	t.Parallel()
	cfg := DefaultHTMLPersisterConfig()
	assert.Equal(t, 8, cfg.Workers)
	assert.Equal(t, 256, cfg.QueueSize)
	assert.Equal(t, 16, cfg.BatchSize)
	assert.Equal(t, 250*time.Millisecond, cfg.FlushInterval)
	assert.Equal(t, 30*time.Second, cfg.UploadTimeout)
}

// --- Enqueue ---

func TestEnqueue_NilPersister(t *testing.T) {
	t.Parallel()
	var p *HTMLPersister
	// Must not panic; returns false.
	assert.False(t, p.Enqueue(context.Background(), sampleTask("t1", "j1"), sampleUpload("path")))
}

func TestEnqueue_NilTask(t *testing.T) {
	t.Parallel()
	p, err := NewHTMLPersister(minPersisterCfg(), HTMLPersisterDeps{
		Provider: &fakeProvider{},
		DBQueue:  &fakeDBQueue{},
	})
	require.NoError(t, err)
	assert.False(t, p.Enqueue(context.Background(), nil, sampleUpload("path")))
}

func TestEnqueue_NilUpload(t *testing.T) {
	t.Parallel()
	p, err := NewHTMLPersister(minPersisterCfg(), HTMLPersisterDeps{
		Provider: &fakeProvider{},
		DBQueue:  &fakeDBQueue{},
	})
	require.NoError(t, err)
	assert.False(t, p.Enqueue(context.Background(), sampleTask("t1", "j1"), nil))
}

func TestEnqueue_AfterStop(t *testing.T) {
	t.Parallel()
	provider := &fakeProvider{}
	dbq := &fakeDBQueue{}
	p, err := NewHTMLPersister(minPersisterCfg(), HTMLPersisterDeps{Provider: provider, DBQueue: dbq})
	require.NoError(t, err)
	p.Start(context.Background())
	p.Stop()

	accepted := p.Enqueue(context.Background(), sampleTask("t1", "j1"), sampleUpload("p"))
	assert.False(t, accepted, "enqueue after Stop must be rejected")
}

func TestEnqueue_QueueFull(t *testing.T) {
	t.Parallel()
	cfg := minPersisterCfg()
	cfg.QueueSize = 1
	cfg.Workers = 0 // no workers — queue will fill immediately
	cfg.Workers = 1

	// Use a provider that blocks so the worker can't drain the queue
	// while we stuff payloads in.
	blockCh := make(chan struct{})
	blocker := &blockingProvider{blockCh: blockCh}

	p, err := NewHTMLPersister(cfg, HTMLPersisterDeps{Provider: blocker, DBQueue: &fakeDBQueue{}})
	require.NoError(t, err)
	p.Start(context.Background())
	defer func() {
		close(blockCh)
		p.Stop()
	}()

	// Flood the queue until one is rejected.
	rejected := false
	for i := 0; i < 100; i++ {
		if !p.Enqueue(context.Background(), sampleTask("t", "j"), sampleUpload("k")) {
			rejected = true
			break
		}
	}
	assert.True(t, rejected, "should reject payload when queue is full")
}

// --- QueueDepth ---

func TestQueueDepth_NilPersister(t *testing.T) {
	t.Parallel()
	var p *HTMLPersister
	assert.Equal(t, 0, p.QueueDepth())
}

func TestQueueDepth_Empty(t *testing.T) {
	t.Parallel()
	p, err := NewHTMLPersister(minPersisterCfg(), HTMLPersisterDeps{
		Provider: &fakeProvider{},
		DBQueue:  &fakeDBQueue{},
	})
	require.NoError(t, err)
	assert.Equal(t, 0, p.QueueDepth())
}

// --- Start/Stop idempotency ---

func TestStart_Idempotent(t *testing.T) {
	t.Parallel()
	p, err := NewHTMLPersister(minPersisterCfg(), HTMLPersisterDeps{
		Provider: &fakeProvider{},
		DBQueue:  &fakeDBQueue{},
	})
	require.NoError(t, err)
	ctx := context.Background()
	p.Start(ctx)
	p.Start(ctx) // second call must not panic or double the workers
	p.Stop()
}

func TestStop_Idempotent(t *testing.T) {
	t.Parallel()
	p, err := NewHTMLPersister(minPersisterCfg(), HTMLPersisterDeps{
		Provider: &fakeProvider{},
		DBQueue:  &fakeDBQueue{},
	})
	require.NoError(t, err)
	p.Start(context.Background())
	p.Stop()
	p.Stop() // second Stop must not panic
}

// --- End-to-end: upload flows through to metadata flush ---

func TestPersister_UploadAndFlush(t *testing.T) {
	t.Parallel()
	provider := &fakeProvider{}
	dbq := &fakeDBQueue{}

	cfg := minPersisterCfg()
	cfg.BatchSize = 1 // flush after every upload
	cfg.FlushInterval = 100 * time.Millisecond

	p, err := NewHTMLPersister(cfg, HTMLPersisterDeps{Provider: provider, DBQueue: dbq})
	require.NoError(t, err)
	p.Start(context.Background())
	defer p.Stop()

	task := sampleTask("task-abc", "job-xyz")
	upload := sampleUpload("jobs/job-xyz/tasks/task-abc/page-content.html.gz")

	accepted := p.Enqueue(context.Background(), task, upload)
	require.True(t, accepted)

	// Wait for the upload and metadata flush to complete.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if provider.uploadCount() >= 1 && len(dbq.rows()) >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	require.Equal(t, 1, provider.uploadCount(), "one R2 upload expected")
	rows := dbq.rows()
	require.Len(t, rows, 1)
	row := rows[0]
	assert.Equal(t, "task-abc", row.TaskID)
	assert.Equal(t, "test-bucket", row.Metadata.StorageBucket)
	assert.Equal(t, upload.Path, row.Metadata.StoragePath)
	assert.Equal(t, upload.ContentType, row.Metadata.ContentType)
	assert.Equal(t, upload.ContentEncoding, row.Metadata.ContentEncoding)
	assert.Equal(t, upload.SizeBytes, row.Metadata.SizeBytes)
	assert.Equal(t, upload.CompressedSizeBytes, row.Metadata.CompressedSizeBytes)
	assert.Equal(t, upload.SHA256, row.Metadata.SHA256)
	// ArchivedAt must equal CapturedAt (direct-to-R2 path skips the sweep)
	assert.Equal(t, upload.CapturedAt, row.Metadata.CapturedAt)
	assert.Equal(t, upload.CapturedAt, row.Metadata.ArchivedAt)
}

func TestPersister_BatchFlushOnSizeThreshold(t *testing.T) {
	t.Parallel()
	provider := &fakeProvider{}
	dbq := &fakeDBQueue{}

	cfg := minPersisterCfg()
	cfg.Workers = 1
	cfg.BatchSize = 3
	cfg.FlushInterval = 10 * time.Second // long — only size flush matters here

	p, err := NewHTMLPersister(cfg, HTMLPersisterDeps{Provider: provider, DBQueue: dbq})
	require.NoError(t, err)
	p.Start(context.Background())
	defer p.Stop()

	for i := 0; i < 3; i++ {
		id := "t" + string(rune('0'+i))
		accepted := p.Enqueue(context.Background(), sampleTask(id, "j"), sampleUpload("key"))
		require.True(t, accepted)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(dbq.rows()) >= 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	assert.GreaterOrEqual(t, len(dbq.rows()), 3, "all three rows should be flushed via size trigger")
}

func TestPersister_DrainOnStop(t *testing.T) {
	t.Parallel()
	provider := &fakeProvider{}
	dbq := &fakeDBQueue{}

	cfg := minPersisterCfg()
	cfg.Workers = 1
	cfg.BatchSize = 10 // won't size-flush during test
	cfg.FlushInterval = 10 * time.Second

	p, err := NewHTMLPersister(cfg, HTMLPersisterDeps{Provider: provider, DBQueue: dbq})
	require.NoError(t, err)
	p.Start(context.Background())

	// Enqueue a couple of payloads before stopping.
	for i := 0; i < 2; i++ {
		id := "t" + string(rune('0'+i))
		p.Enqueue(context.Background(), sampleTask(id, "j"), sampleUpload("key"))
	}

	// Stop must drain the queue — both uploads and final metadata flush.
	p.Stop()

	assert.Equal(t, 2, provider.uploadCount(), "uploads must complete before Stop returns")
	assert.Len(t, dbq.rows(), 2, "metadata flush must happen on queue close")
}

func TestPersister_UploadError_DoesNotFlushMetadata(t *testing.T) {
	t.Parallel()
	provider := &fakeProvider{uploadErr: errors.New("R2 unavailable")}
	dbq := &fakeDBQueue{}

	cfg := minPersisterCfg()
	cfg.Workers = 1
	cfg.BatchSize = 1
	cfg.FlushInterval = 10 * time.Millisecond

	p, err := NewHTMLPersister(cfg, HTMLPersisterDeps{Provider: provider, DBQueue: dbq})
	require.NoError(t, err)
	p.Start(context.Background())

	p.Enqueue(context.Background(), sampleTask("t1", "j1"), sampleUpload("key"))
	p.Stop()

	// The upload failed — no metadata should have been written.
	assert.Len(t, dbq.rows(), 0, "no metadata row on upload error")
}

func TestPersister_UsesTaskHTMLObjectPath_WhenPathEmpty(t *testing.T) {
	t.Parallel()
	provider := &fakeProvider{}
	dbq := &fakeDBQueue{}

	cfg := minPersisterCfg()
	cfg.Workers = 1
	cfg.BatchSize = 1
	cfg.FlushInterval = 10 * time.Millisecond

	p, err := NewHTMLPersister(cfg, HTMLPersisterDeps{Provider: provider, DBQueue: dbq})
	require.NoError(t, err)
	p.Start(context.Background())
	defer p.Stop()

	// Upload with empty Path — persister must derive it from job/task IDs.
	upload := sampleUpload("")
	upload.Path = ""
	p.Enqueue(context.Background(), sampleTask("task-1", "job-1"), upload)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(dbq.rows()) >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	rows := dbq.rows()
	require.Len(t, rows, 1)
	expected := "jobs/job-1/tasks/task-1/page-content.html.gz"
	assert.Equal(t, expected, rows[0].Metadata.StoragePath)

	provider.mu.Lock()
	defer provider.mu.Unlock()
	require.Len(t, provider.uploads, 1)
	assert.Equal(t, expected, provider.uploads[0].key)
}

func TestPersister_MultipleWorkers(t *testing.T) {
	t.Parallel()
	provider := &fakeProvider{}
	dbq := &fakeDBQueue{}

	cfg := minPersisterCfg()
	cfg.Workers = 4
	cfg.BatchSize = 1
	cfg.FlushInterval = 10 * time.Millisecond
	cfg.QueueSize = 64

	p, err := NewHTMLPersister(cfg, HTMLPersisterDeps{Provider: provider, DBQueue: dbq})
	require.NoError(t, err)
	p.Start(context.Background())

	const n = 20
	for i := 0; i < n; i++ {
		id := "task-" + string(rune('a'+i%26))
		p.Enqueue(context.Background(), sampleTask(id, "job-x"), sampleUpload("key"))
	}

	p.Stop()

	assert.Equal(t, n, provider.uploadCount())
	assert.Len(t, dbq.rows(), n)
}

// TestPersister_MetadataFlushRetainedOnError verifies that a DB flush failure
// keeps the batch so the next flush can retry — uploaded R2 objects must not
// lose their metadata pointer.
func TestPersister_MetadataFlushRetainedOnError(t *testing.T) {
	t.Parallel()
	provider := &fakeProvider{}
	dbq := &fakeDBQueue{
		upsertErr:     errors.New("db transient error"),
		upsertErrOnce: true, // only the first flush fails
	}

	cfg := minPersisterCfg()
	cfg.Workers = 1
	cfg.BatchSize = 1
	cfg.FlushInterval = 20 * time.Millisecond

	p, err := NewHTMLPersister(cfg, HTMLPersisterDeps{Provider: provider, DBQueue: dbq})
	require.NoError(t, err)
	p.Start(context.Background())

	p.Enqueue(context.Background(), sampleTask("t1", "j1"), sampleUpload("key"))

	// Wait for retry (second flush should succeed).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(dbq.rows()) >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	p.Stop()

	// The upload happened once; the metadata eventually flushed after retry.
	assert.Equal(t, 1, provider.uploadCount())
	assert.Len(t, dbq.rows(), 1, "metadata must be retried and eventually land")
}

// blockingProvider is a ColdStorageProvider whose Upload blocks until blockCh is closed.
type blockingProvider struct {
	blockCh chan struct{}
}

func (b *blockingProvider) Upload(ctx context.Context, _, _ string, _ io.Reader, _ archive.UploadOptions) error {
	select {
	case <-b.blockCh:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}
func (b *blockingProvider) Ping(_ context.Context, _ string) error { return nil }
func (b *blockingProvider) Download(_ context.Context, _, _ string) (io.ReadCloser, error) {
	return nil, errors.New("not implemented")
}
func (b *blockingProvider) Exists(_ context.Context, _, _ string) (bool, error) { return true, nil }
func (b *blockingProvider) Provider() string                                    { return "blocking" }
