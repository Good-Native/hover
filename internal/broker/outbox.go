package broker

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/good-native/hover/internal/observability"
	"github.com/lib/pq"
)

// DefaultOutboxMaxAttempts caps the worst-case stuck-row age at
// MaxAttempts × MaxBackoff (10 × 5 min = 50 min at defaults).
const DefaultOutboxMaxAttempts = 10

type OutboxSweeperOpts struct {
	Interval    time.Duration
	BatchSize   int
	BaseBackoff time.Duration
	MaxBackoff  time.Duration
	MaxAttempts int
	// StatementTimeout bounds tick DB work; guards against a wedged
	// sweeper tx holding locks indefinitely. 0 keeps DB default.
	StatementTimeout time.Duration
}

// DefaultOutboxSweeperOpts: 500ms interval (5s starved end-to-end
// latency on small jobs); 15s StatementTimeout (HOVER-K3 — pool
// acquire ate several seconds of tick budget under bulk-lane load).
func DefaultOutboxSweeperOpts() OutboxSweeperOpts {
	return OutboxSweeperOpts{
		Interval:         500 * time.Millisecond,
		BatchSize:        200,
		BaseBackoff:      2 * time.Second,
		MaxBackoff:       5 * time.Minute,
		MaxAttempts:      DefaultOutboxMaxAttempts,
		StatementTimeout: 15 * time.Second,
	}
}

// Sweeper drains task_outbox into Redis via Scheduler.ScheduleBatch.
// Multi-replica safe: FOR UPDATE SKIP LOCKED partitions due rows
// across sweepers.
type Sweeper struct {
	db        *sql.DB
	scheduler *Scheduler
	opts      OutboxSweeperOpts
}

func NewOutboxSweeper(db *sql.DB, scheduler *Scheduler, opts OutboxSweeperOpts) *Sweeper {
	if opts.Interval <= 0 {
		opts.Interval = 500 * time.Millisecond
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = 200
	}
	if opts.BaseBackoff <= 0 {
		opts.BaseBackoff = 2 * time.Second
	}
	if opts.MaxBackoff <= 0 {
		opts.MaxBackoff = 5 * time.Minute
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = DefaultOutboxMaxAttempts
	}
	return &Sweeper{db: db, scheduler: scheduler, opts: opts}
}

// Run drives the sweeper loop until ctx is cancelled.
func (s *Sweeper) Run(ctx context.Context) {
	brokerLog.Info("outbox sweeper started",
		"interval", s.opts.Interval, "batch_size", s.opts.BatchSize)

	t := time.NewTicker(s.opts.Interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			brokerLog.Info("outbox sweeper stopped", "reason", ctx.Err())
			return
		case <-t.C:
			if err := s.Tick(ctx); err != nil {
				brokerLog.Error("outbox sweep tick failed", "error", err)
			}
		}
	}
}

type outboxRow struct {
	id              int64
	taskID          string
	jobID           string
	pageID          int
	host            string
	path            string
	priority        float64
	retryCount      int
	sourceType      string
	sourceURL       string
	runAt           time.Time
	attempts        int
	taskType        string
	lighthouseRunID sql.NullInt64
}

// Tick runs a single sweep iteration. Exported for tests.
func (s *Sweeper) Tick(ctx context.Context) error {
	// Tx-level context deadline so ScheduleBatch wedging on Redis
	// can't hold row locks indefinitely (SET LOCAL only fires
	// during SQL execution).
	tickCtx := ctx
	cancel := func() {}
	if s.opts.StatementTimeout > 0 {
		tickCtx, cancel = context.WithTimeout(ctx, s.opts.StatementTimeout)
	}
	defer cancel()

	tx, err := s.db.BeginTx(tickCtx, nil)
	if err != nil {
		return fmt.Errorf("outbox: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Belt-and-braces in case pgbouncer masks the client cancellation.
	if s.opts.StatementTimeout > 0 {
		if _, err := tx.ExecContext(tickCtx,
			fmt.Sprintf(`SET LOCAL statement_timeout = %d`,
				s.opts.StatementTimeout.Milliseconds()),
		); err != nil {
			return fmt.Errorf("outbox: set statement_timeout: %w", err)
		}
	}

	rows, err := tx.QueryContext(tickCtx, `
		SELECT id, task_id, job_id, page_id, host, path,
		       priority, retry_count, source_type, source_url,
		       run_at, attempts, task_type, lighthouse_run_id
		FROM task_outbox
		WHERE run_at <= NOW()
		ORDER BY run_at
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	`, s.opts.BatchSize)
	if err != nil {
		return fmt.Errorf("outbox: select due rows: %w", err)
	}

	var claimed []outboxRow
	for rows.Next() {
		var r outboxRow
		if err := rows.Scan(
			&r.id, &r.taskID, &r.jobID, &r.pageID, &r.host, &r.path,
			&r.priority, &r.retryCount, &r.sourceType, &r.sourceURL,
			&r.runAt, &r.attempts, &r.taskType, &r.lighthouseRunID,
		); err != nil {
			_ = rows.Close()
			return fmt.Errorf("outbox: scan row: %w", err)
		}
		claimed = append(claimed, r)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("outbox: iterate rows: %w", err)
	}

	if len(claimed) == 0 {
		return tx.Commit()
	}

	entries := make([]ScheduleEntry, 0, len(claimed))
	for _, r := range claimed {
		taskType := r.taskType
		if taskType == "" {
			// Defensive — column has NOT NULL DEFAULT 'crawl' but the
			// dispatcher routes on this and an empty would misroute.
			taskType = "crawl"
		}
		var lhRunID int64
		if r.lighthouseRunID.Valid {
			lhRunID = r.lighthouseRunID.Int64
		}
		entries = append(entries, ScheduleEntry{
			TaskID:          r.taskID,
			JobID:           r.jobID,
			PageID:          r.pageID,
			Host:            r.host,
			Path:            r.path,
			Priority:        r.priority,
			RetryCount:      r.retryCount,
			SourceType:      r.sourceType,
			SourceURL:       r.sourceURL,
			RunAt:           r.runAt,
			TaskType:        taskType,
			LighthouseRunID: lhRunID,
		})
	}

	schedErr := s.scheduler.ScheduleBatch(tickCtx, entries)

	// Partition by ScheduleBatch outcome: nil → delete all, *BatchError
	// → split, other → all retry.
	var (
		succeeded  []int64     // task_outbox.id values to DELETE
		retry      []outboxRow // rows to bump attempts / run_at
		deadLetter []outboxRow // rows at or over MaxAttempts
		lastErrMsg string
	)

	var be *BatchError
	switch {
	case schedErr == nil:
		succeeded = make([]int64, 0, len(claimed))
		for _, r := range claimed {
			succeeded = append(succeeded, r.id)
		}
	case errors.As(schedErr, &be):
		failedSet := make(map[int]struct{}, len(be.FailedIndices))
		for _, idx := range be.FailedIndices {
			failedSet[idx] = struct{}{}
		}
		succeeded = make([]int64, 0, len(claimed)-len(failedSet))
		retry = make([]outboxRow, 0, len(failedSet))
		for i, r := range claimed {
			if _, bad := failedSet[i]; bad {
				retry = append(retry, r)
				continue
			}
			succeeded = append(succeeded, r.id)
		}
		lastErrMsg = be.Err.Error()
	default:
		retry = append([]outboxRow(nil), claimed...)
		lastErrMsg = schedErr.Error()
	}

	// attempts+1 because bumpAttempts will +1 this tick — a row at
	// MaxAttempts-1 reaches MaxAttempts now and is terminal.
	if len(retry) > 0 && s.opts.MaxAttempts > 0 {
		kept := retry[:0]
		for _, r := range retry {
			if r.attempts+1 >= s.opts.MaxAttempts {
				deadLetter = append(deadLetter, r)
				continue
			}
			kept = append(kept, r)
		}
		retry = kept
	}

	if len(succeeded) > 0 {
		if _, err := tx.ExecContext(tickCtx,
			`DELETE FROM task_outbox WHERE id = ANY($1)`,
			pq.Array(succeeded),
		); err != nil {
			return fmt.Errorf("outbox: delete dispatched rows: %w", err)
		}
	}

	retryIDs := make([]int64, 0, len(retry))
	for _, r := range retry {
		retryIDs = append(retryIDs, r.id)
	}
	if len(retryIDs) > 0 {
		if err := s.bumpAttempts(tickCtx, tx, retryIDs); err != nil {
			return fmt.Errorf("outbox: bump attempts: %w", err)
		}
	}

	if len(deadLetter) > 0 {
		if err := s.moveToDeadLetter(tickCtx, tx, deadLetter, lastErrMsg); err != nil {
			return fmt.Errorf("outbox: dead-letter: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("outbox: commit: %w", err)
	}

	observability.RecordBrokerOutboxSweep(ctx, "dispatched", len(succeeded))
	observability.RecordBrokerOutboxSweep(ctx, "retried", len(retry))
	observability.RecordBrokerOutboxSweep(ctx, "dead_lettered", len(deadLetter))

	if schedErr != nil {
		brokerLog.Debug("outbox sweep tick partial",
			"dispatched", len(succeeded),
			"retried", len(retry),
			"dead_lettered", len(deadLetter),
			"schedule_err", schedErr)
		return fmt.Errorf("outbox: schedule batch: %w", schedErr)
	}

	brokerLog.Debug("outbox sweep tick dispatched",
		"dispatched", len(succeeded))
	return nil
}

// moveToDeadLetter copies rows to task_outbox_dead and deletes from
// task_outbox in the caller's tx so the move is atomic.
func (s *Sweeper) moveToDeadLetter(ctx context.Context, tx *sql.Tx, rows []outboxRow, lastErr string) error {
	if len(rows) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.id)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO task_outbox_dead (
			original_id, task_id, job_id, page_id, host, path,
			priority, retry_count, source_type, source_url,
			run_at, attempts, created_at, last_error,
			task_type, lighthouse_run_id
		)
		SELECT id, task_id, job_id, page_id, host, path,
		       priority, retry_count, source_type, source_url,
		       run_at, attempts + 1, created_at, $2,
		       task_type, lighthouse_run_id
		FROM task_outbox
		WHERE id = ANY($1)
	`, pq.Array(ids), lastErr); err != nil {
		return fmt.Errorf("insert dead rows: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM task_outbox WHERE id = ANY($1)`,
		pq.Array(ids),
	); err != nil {
		return fmt.Errorf("delete dead rows: %w", err)
	}
	brokerLog.Warn("outbox rows dead-lettered",
		"count", len(ids), "last_error", lastErr)
	return nil
}

// bumpAttempts grows backoff exponentially in BaseBackoff doublings,
// capped at MaxBackoff.
func (s *Sweeper) bumpAttempts(ctx context.Context, tx *sql.Tx, ids []int64) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE task_outbox
		SET attempts = attempts + 1,
		    run_at = NOW() + LEAST(
		        $1::interval * POWER(2, attempts),
		        $2::interval
		    )
		WHERE id = ANY($3)
	`,
		intervalString(s.opts.BaseBackoff),
		intervalString(s.opts.MaxBackoff),
		pq.Array(ids),
	)
	return err
}

func intervalString(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	return fmt.Sprintf("%d milliseconds", d.Milliseconds())
}
