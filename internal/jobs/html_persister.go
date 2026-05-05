package jobs

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/good-native/hover/internal/archive"
	"github.com/good-native/hover/internal/db"
	"github.com/good-native/hover/internal/logging"
	"github.com/good-native/hover/internal/observability"
)

type HTMLPersisterConfig struct {
	Workers int
	// When full, new enqueues drop the payload — HTML capture is best-effort
	// and must not block the stream worker loop.
	QueueSize     int
	BatchSize     int
	FlushInterval time.Duration
	// Without a per-call cap a hung R2 connection would occupy a worker
	// indefinitely and starve the queue.
	UploadTimeout time.Duration
	Bucket        string
	Provider      string
}

// Defaults tuned for ~4k tasks/min: 256-deep queue absorbs transient R2
// hiccups; 8 workers matches the historical pool that ran cleanly under load.
func DefaultHTMLPersisterConfig() HTMLPersisterConfig {
	return HTMLPersisterConfig{
		Workers:       8,
		QueueSize:     256,
		BatchSize:     16,
		FlushInterval: 250 * time.Millisecond,
		UploadTimeout: 30 * time.Second,
	}
}

type HTMLPersisterDeps struct {
	Provider archive.ColdStorageProvider
	DBQueue  DbQueueInterface
}

type persistJob struct {
	taskID string
	jobID  string
	upload *TaskHTMLUpload
}

// HTMLPersister streams completed-task HTML payloads to R2 and stamps the
// resulting metadata onto the task row. See issue #332 for context.
type HTMLPersister struct {
	cfg  HTMLPersisterConfig
	deps HTMLPersisterDeps

	queue chan persistJob

	// Separate WGs so Stop waits for the queue to drain before tearing
	// down the probe loop and cancelling the shared context.
	uploadWG sync.WaitGroup
	probeWG  sync.WaitGroup
	cancel   context.CancelFunc

	// Gates Enqueue once Stop begins, so concurrent senders can't race
	// the channel close.
	stopped atomic.Bool

	stopCh chan struct{}

	startOnce sync.Once
	stopOnce  sync.Once
}

func NewHTMLPersister(cfg HTMLPersisterConfig, deps HTMLPersisterDeps) (*HTMLPersister, error) {
	if deps.Provider == nil {
		return nil, errors.New("html persister: cold storage provider is required")
	}
	if deps.DBQueue == nil {
		return nil, errors.New("html persister: db queue is required")
	}
	if cfg.Workers < 1 {
		cfg.Workers = 1
	}
	if cfg.QueueSize < 1 {
		cfg.QueueSize = 1
	}
	if cfg.BatchSize < 1 {
		cfg.BatchSize = 1
	}
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = 250 * time.Millisecond
	}
	if cfg.UploadTimeout <= 0 {
		cfg.UploadTimeout = 30 * time.Second
	}
	if cfg.Bucket == "" {
		return nil, errors.New("html persister: bucket is required")
	}
	if cfg.Provider == "" {
		cfg.Provider = deps.Provider.Provider()
	}

	return &HTMLPersister{
		cfg:    cfg,
		deps:   deps,
		queue:  make(chan persistJob, cfg.QueueSize),
		stopCh: make(chan struct{}),
	}, nil
}

func (p *HTMLPersister) Start(ctx context.Context) {
	p.startOnce.Do(func() {
		ctx, p.cancel = context.WithCancel(ctx)

		for i := 0; i < p.cfg.Workers; i++ {
			p.uploadWG.Add(1)
			go func(id int) {
				defer p.uploadWG.Done()
				p.workerLoop(ctx, id)
			}(i)
		}

		p.probeWG.Add(1)
		go func() {
			defer p.probeWG.Done()
			p.probeLoop(ctx)
		}()

		jobsLog.Info("html persister started",
			"workers", p.cfg.Workers,
			"queue_size", p.cfg.QueueSize,
			"batch_size", p.cfg.BatchSize,
			"bucket", p.cfg.Bucket,
		)
	})
}

// Stop drains already-accepted uploads before exiting. Hand-off ordering:
// flip stopped → close queue → wait for uploads → close stopCh → cancel ctx.
// If the parent ctx is cancelled externally first, in-flight uploads abort
// via uploadCtx and the remaining queue is dropped.
func (p *HTMLPersister) Stop() {
	p.stopOnce.Do(func() {
		p.stopped.Store(true)
		close(p.queue)

		p.uploadWG.Wait()

		close(p.stopCh)
		if p.cancel != nil {
			p.cancel()
		}
		p.probeWG.Wait()
		jobsLog.Info("html persister stopped")
	})
}

// Enqueue is non-blocking: a full queue drops the payload and emits a
// "skipped" metric. Returns true when the payload was accepted.
func (p *HTMLPersister) Enqueue(ctx context.Context, task *db.Task, upload *TaskHTMLUpload) bool {
	if p == nil || task == nil || upload == nil {
		return false
	}
	if p.stopped.Load() {
		observability.RecordHTMLPersistUpload(ctx, "skipped")
		return false
	}
	job := persistJob{
		taskID: task.ID,
		jobID:  task.JobID,
		upload: upload,
	}
	select {
	case p.queue <- job:
		return true
	default:
		observability.RecordHTMLPersistUpload(ctx, "skipped")
		return false
	}
}

func (p *HTMLPersister) QueueDepth() int {
	if p == nil {
		return 0
	}
	return len(p.queue)
}

func (p *HTMLPersister) workerLoop(ctx context.Context, workerID int) {
	log := jobsLog.With("html_persist_worker", workerID)
	batch := make([]db.TaskHTMLMetadataRow, 0, p.cfg.BatchSize)
	ticker := time.NewTicker(p.cfg.FlushInterval)
	defer ticker.Stop()

	// Cap retained-on-error batch so a sustained DB outage can't grow
	// per-worker memory without bound.
	maxRetained := p.cfg.BatchSize * 8

	flush := func(reason string) {
		if len(batch) == 0 {
			return
		}
		// Detached context so a cancellation between upload-success and
		// metadata-write doesn't strand just-uploaded rows without their
		// metadata. UPDATE is idempotent via COALESCE/NULLIF.
		flushCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := p.deps.DBQueue.BatchUpsertTaskHTMLMetadata(flushCtx, batch); err != nil {
			// Retain batch so the next flush retries — dropping it would
			// orphan the just-uploaded R2 objects.
			observability.RecordHTMLPersistUpload(flushCtx, "flush_err")
			log.Error("html metadata flush failed — retaining batch for retry",
				"error", err, "rows", len(batch), "reason", reason)
			if len(batch) > maxRetained {
				dropped := len(batch) - maxRetained
				batch = append(batch[:0], batch[dropped:]...)
				log.Warn("html metadata retained batch capped — oldest rows dropped",
					"dropped", dropped, "kept", len(batch))
			}
			return
		}
		log.Debug("html metadata flushed",
			"rows", len(batch), "reason", reason)
		batch = batch[:0]
	}

	for {
		select {
		case <-ctx.Done():
			flush("shutdown")
			return
		case <-ticker.C:
			flush("interval")
		case job, ok := <-p.queue:
			if !ok {
				flush("closed")
				return
			}
			row, success := p.uploadOne(ctx, log, job)
			if success {
				batch = append(batch, row)
				if len(batch) >= p.cfg.BatchSize {
					flush("size")
				}
			}
		}
	}
}

func (p *HTMLPersister) uploadOne(ctx context.Context, log *logging.Logger, job persistJob) (db.TaskHTMLMetadataRow, bool) {
	uploadCtx, cancel := context.WithTimeout(ctx, p.cfg.UploadTimeout)
	defer cancel()

	upload := job.upload
	key := upload.Path
	if key == "" {
		key = archive.TaskHTMLObjectPath(job.jobID, job.taskID)
	}

	start := time.Now()
	err := p.deps.Provider.Upload(uploadCtx, p.cfg.Bucket, key, bytes.NewReader(upload.Payload), archive.UploadOptions{
		ContentType:     upload.UploadContentType,
		ContentEncoding: upload.ContentEncoding,
		Metadata: map[string]string{
			"task-id":     job.taskID,
			"job-id":      job.jobID,
			"sha256":      upload.SHA256,
			"captured-at": upload.CapturedAt.UTC().Format(time.RFC3339Nano),
		},
	})
	elapsed := time.Since(start)
	observability.RecordHTMLPersistUploadDuration(ctx, elapsed)
	observability.RecordHTMLPersistBodyBytes(ctx, upload.CompressedSizeBytes)

	if err != nil {
		observability.RecordHTMLPersistUpload(ctx, "err")
		log.Warn("html upload failed",
			"error", err,
			"task_id", job.taskID,
			"bucket", p.cfg.Bucket,
			"key", key,
			"elapsed_ms", elapsed.Milliseconds(),
		)
		return db.TaskHTMLMetadataRow{}, false
	}

	observability.RecordHTMLPersistUpload(ctx, "ok")

	return db.TaskHTMLMetadataRow{
		TaskID: job.taskID,
		Metadata: db.TaskHTMLMetadata{
			StorageBucket:       p.cfg.Bucket,
			StoragePath:         key,
			ContentType:         upload.ContentType,
			ContentEncoding:     upload.ContentEncoding,
			SizeBytes:           upload.SizeBytes,
			CompressedSizeBytes: upload.CompressedSizeBytes,
			SHA256:              upload.SHA256,
			CapturedAt:          upload.CapturedAt,
			// Stamp ArchivedAt at write time so the archive sweep
			// (html_archived_at IS NULL) doesn't re-pick these rows for a
			// wasteful R2-to-R2 copy.
			ArchivedAt: upload.CapturedAt,
		},
	}, true
}

func (p *HTMLPersister) probeLoop(ctx context.Context) {
	ticker := time.NewTicker(p.cfg.FlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-p.stopCh:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			observability.RecordHTMLPersistQueueDepth(ctx, len(p.queue))
		}
	}
}
