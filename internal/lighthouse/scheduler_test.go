package lighthouse

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/good-native/hover/internal/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSchedulerDB exposes the two SchedulerDB methods the scheduler needs
// without requiring a real *db.DB. Each method returns whatever the test
// pre-loads.
type fakeSchedulerDB struct {
	completed    []db.CompletedTaskForSampling
	completedErr error
	sampled      map[int]db.LighthouseSelectionBand
	sampledErr   error
}

func (f *fakeSchedulerDB) GetCompletedTasksForLighthouseSampling(_ context.Context, _ string) ([]db.CompletedTaskForSampling, error) {
	if f.completedErr != nil {
		return nil, f.completedErr
	}
	return f.completed, nil
}

func (f *fakeSchedulerDB) GetLighthouseRunPageBands(_ context.Context, _ string) (map[int]db.LighthouseSelectionBand, error) {
	if f.sampledErr != nil {
		return nil, f.sampledErr
	}
	if f.sampled == nil {
		return map[int]db.LighthouseSelectionBand{}, nil
	}
	return f.sampled, nil
}

// txRunnerFromMock satisfies TxRunner using a sqlmock-backed *sql.DB.
// Hands the transaction to the supplied function so tests can assert on
// the exact SQL emitted by the scheduler.
type txRunnerFromMock struct {
	db *sql.DB
}

func (r *txRunnerFromMock) ExecuteWithContext(ctx context.Context, fn func(ctx context.Context, tx *sql.Tx) error) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(ctx, tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// TestScheduler_OnMilestone_SchedulesFastestAndSlowest covers the
// happy path: a small completed-task set, no prior samples, scheduler
// picks 1 fastest + 1 slowest and writes both rows in a single tx.
func TestScheduler_OnMilestone_SchedulesFastestAndSlowest(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() { _ = mockDB.Close() })

	fake := &fakeSchedulerDB{
		completed: []db.CompletedTaskForSampling{
			{TaskID: "t-fast", PageID: 1, Host: "example.com", Path: "/", Priority: 0.5, ResponseTime: 200},
			{TaskID: "t-mid", PageID: 2, Host: "example.com", Path: "/about", Priority: 0.5, ResponseTime: 600},
			{TaskID: "t-slow", PageID: 3, Host: "example.com", Path: "/contact", Priority: 0.5, ResponseTime: 1500},
		},
	}

	mock.ExpectBegin()
	// First sample → INSERT lighthouse_runs returning id=10.
	mock.ExpectQuery(`
			INSERT INTO lighthouse_runs (
				job_id, page_id, source_task_id, selection_band, selection_milestone, status
			) VALUES (
				$1, $2, NULLIF($3, ''), $4, $5, 'pending'
			)
			ON CONFLICT (job_id, page_id) DO NOTHING
			RETURNING id
		`).WithArgs("job-x", 1, "t-fast", "fastest", 30).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(10)))
	// Second sample → INSERT lighthouse_runs returning id=11.
	mock.ExpectQuery(`
			INSERT INTO lighthouse_runs (
				job_id, page_id, source_task_id, selection_band, selection_milestone, status
			) VALUES (
				$1, $2, NULLIF($3, ''), $4, $5, 'pending'
			)
			ON CONFLICT (job_id, page_id) DO NOTHING
			RETURNING id
		`).WithArgs("job-x", 3, "t-slow", "slowest", 30).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(11)))
	// Bulk outbox insert.
	mock.ExpectExec(`
			INSERT INTO task_outbox (
				task_id, job_id, page_id, host, path,
				priority, retry_count, source_type, source_url,
				run_at, attempts, created_at,
				task_type, lighthouse_run_id
			)
			SELECT
				t_task, t_job, t_page, t_host, t_path,
				t_priority, 0, 'lighthouse', t_url,
				t_run_at, 0, NOW(),
				'lighthouse', t_run_id
			FROM UNNEST(
				$1::text[],
				$2::text[],
				$3::int[],
				$4::text[],
				$5::text[],
				$6::double precision[],
				$7::text[],
				$8::bigint[],
				$9::timestamptz[]
			) AS u(
				t_task, t_job, t_page, t_host, t_path,
				t_priority, t_url, t_run_id, t_run_at
			)
			ON CONFLICT (task_id) DO NOTHING
		`).WithArgs(
		sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
		sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
		sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
	).WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectCommit()

	s := NewScheduler(fake, &txRunnerFromMock{db: mockDB})
	require.NoError(t, s.OnMilestone(context.Background(), "job-x", 30))
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestScheduler_OnMilestone_NoCompletedTasks short-circuits when the
// crawl has produced nothing yet. No DB writes, no errors.
func TestScheduler_OnMilestone_NoCompletedTasks(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = mockDB.Close() })

	fake := &fakeSchedulerDB{}
	s := NewScheduler(fake, &txRunnerFromMock{db: mockDB})

	require.NoError(t, s.OnMilestone(context.Background(), "job-y", 10))
	// No transaction expected.
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestScheduler_OnMilestone_AllAlreadySampled exits without opening a
// tx when the dedupe set covers every completed task.
func TestScheduler_OnMilestone_AllAlreadySampled(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = mockDB.Close() })

	fake := &fakeSchedulerDB{
		completed: []db.CompletedTaskForSampling{
			{TaskID: "t1", PageID: 1, Host: "example.com", Path: "/", ResponseTime: 200},
			{TaskID: "t2", PageID: 2, Host: "example.com", Path: "/about", ResponseTime: 1500},
		},
		sampled: map[int]db.LighthouseSelectionBand{
			1: db.LighthouseBandFastest,
			2: db.LighthouseBandSlowest,
		},
	}
	s := NewScheduler(fake, &txRunnerFromMock{db: mockDB})

	require.NoError(t, s.OnMilestone(context.Background(), "job-z", 50))
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestScheduler_OnMilestone_LoadCompletedError surfaces the upstream
// error untouched so the caller can log + decide whether to retry.
func TestScheduler_OnMilestone_LoadCompletedError(t *testing.T) {
	mockDB, _, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = mockDB.Close() })

	wantErr := errors.New("boom")
	fake := &fakeSchedulerDB{completedErr: wantErr}
	s := NewScheduler(fake, &txRunnerFromMock{db: mockDB})

	err = s.OnMilestone(context.Background(), "job-err", 20)
	require.Error(t, err)
	assert.ErrorIs(t, err, wantErr)
}

// TestScheduler_OnMilestone_ReconcileRetagsAt100 covers the 100%
// reconciliation pass: samples come back from SelectSamples tagged as
// fastest/slowest, but the scheduler must retag them to 'reconcile'
// before persisting so the analytics layer can distinguish the
// catch-up pass from per-decade picks.
func TestScheduler_OnMilestone_ReconcileRetagsAt100(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() { _ = mockDB.Close() })

	fake := &fakeSchedulerDB{
		completed: []db.CompletedTaskForSampling{
			{TaskID: "t1", PageID: 1, Host: "example.com", Path: "/", ResponseTime: 200},
			{TaskID: "t2", PageID: 2, Host: "example.com", Path: "/slow", ResponseTime: 1500},
		},
	}

	mock.ExpectBegin()
	mock.ExpectQuery(`
			INSERT INTO lighthouse_runs (
				job_id, page_id, source_task_id, selection_band, selection_milestone, status
			) VALUES (
				$1, $2, NULLIF($3, ''), $4, $5, 'pending'
			)
			ON CONFLICT (job_id, page_id) DO NOTHING
			RETURNING id
		`).WithArgs("job-r", 1, "t1", "reconcile", 100).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(20)))
	mock.ExpectQuery(`
			INSERT INTO lighthouse_runs (
				job_id, page_id, source_task_id, selection_band, selection_milestone, status
			) VALUES (
				$1, $2, NULLIF($3, ''), $4, $5, 'pending'
			)
			ON CONFLICT (job_id, page_id) DO NOTHING
			RETURNING id
		`).WithArgs("job-r", 2, "t2", "reconcile", 100).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(21)))
	mock.ExpectExec(`
			INSERT INTO task_outbox (
				task_id, job_id, page_id, host, path,
				priority, retry_count, source_type, source_url,
				run_at, attempts, created_at,
				task_type, lighthouse_run_id
			)
			SELECT
				t_task, t_job, t_page, t_host, t_path,
				t_priority, 0, 'lighthouse', t_url,
				t_run_at, 0, NOW(),
				'lighthouse', t_run_id
			FROM UNNEST(
				$1::text[],
				$2::text[],
				$3::int[],
				$4::text[],
				$5::text[],
				$6::double precision[],
				$7::text[],
				$8::bigint[],
				$9::timestamptz[]
			) AS u(
				t_task, t_job, t_page, t_host, t_path,
				t_priority, t_url, t_run_id, t_run_at
			)
			ON CONFLICT (task_id) DO NOTHING
		`).WithArgs(
		sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
		sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
		sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
	).WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectCommit()

	s := NewScheduler(fake, &txRunnerFromMock{db: mockDB})
	require.NoError(t, s.OnMilestone(context.Background(), "job-r", 100))
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestLighthouseAuditURL pins the URL composition rule the scheduler
// relies on. The runner audits this string verbatim, so tests must
// catch any drift between scheduler and runner expectations.
func TestLighthouseAuditURL(t *testing.T) {
	tests := []struct {
		name     string
		host     string
		path     string
		expected string
	}{
		{"with path", "example.com", "/about", "https://example.com/about"},
		{"empty path defaults to slash", "example.com", "", "https://example.com/"},
		{"empty host returns empty", "", "/", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, lighthouseAuditURL(tc.host, tc.path))
		})
	}
}
