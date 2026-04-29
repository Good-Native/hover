package jobs

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBlockJob_LockOrder asserts the block transaction issues
// statements in the same lock order as CancelJob — tasks → jobs →
// task_outbox → domains. The first three mirror the worker batch path
// and AFTER STATEMENT counter trigger; reversing them is the same
// 40P01 deadlock the cancel test guards against. The fourth (domains)
// must come last so nothing else holds a domain row lock for the
// duration of the long tasks UPDATE.
func TestBlockJob_LockOrder(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	defer mockDB.Close()

	jm := &JobManager{
		db:             mockDB,
		dbQueue:        &mockDbQueueWrapper{mockDB: mockDB},
		processedPages: make(map[string]struct{}),
	}

	const (
		jobID  = "job-block-1"
		vendor = "akamai"
		reason = "Server: AkamaiGHost on 403"
	)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT[\s\S]+FROM jobs j[\s\S]+JOIN domains d`).
		WithArgs(jobID).
		WillReturnRows(jobRow(jobID, JobStatusRunning))
	mock.ExpectCommit()

	mock.ExpectBegin()

	// 1. Tasks first, ORDER BY id.
	mock.ExpectExec(`(?s)WITH picked AS \(\s*SELECT id FROM tasks\s+WHERE job_id = \$1\s+AND status IN \(\$3, \$4\)\s+ORDER BY id\s+FOR UPDATE\s*\)\s*UPDATE tasks t\s+SET status = \$2`).
		WithArgs(jobID, string(TaskStatusSkipped), string(TaskStatusPending), string(TaskStatusWaiting)).
		WillReturnResult(sqlmock.NewResult(0, 5))

	// 2. Jobs second — error_message carries the WAF reason. The
	//    status IN (...) clause is the CAS guard: we only write when
	//    the job is still in a pre-terminal state, so a concurrent
	//    completion can't be silently overwritten.
	mock.ExpectExec(`UPDATE jobs\s+SET status = \$1, completed_at = \$2, error_message = \$3\s+WHERE id = \$4\s+AND status IN \(\$5, \$6, \$7, \$8\)`).
		WithArgs(string(JobStatusBlocked), sqlmock.AnyArg(), sqlmock.AnyArg(), jobID,
			string(JobStatusRunning), string(JobStatusPending), string(JobStatusPaused), string(JobStatusInitialising)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	// 3. Outbox cleanup.
	mock.ExpectExec(`DELETE FROM task_outbox WHERE job_id = \$1`).
		WithArgs(jobID).
		WillReturnResult(sqlmock.NewResult(0, 5))

	// 4. Domain row last.
	mock.ExpectExec(`UPDATE domains\s+SET waf_blocked\s+= TRUE,\s+waf_vendor\s+= \$1,\s+waf_blocked_at = NOW\(\)\s+WHERE id = \(SELECT domain_id FROM jobs WHERE id = \$2\)`).
		WithArgs(vendor, jobID).
		WillReturnResult(sqlmock.NewResult(0, 1))

	mock.ExpectCommit()

	err = jm.BlockJob(context.Background(), jobID, vendor, reason)
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestBlockJob_AlreadyBlockedIsNoOp covers the circuit-breaker-races-
// pre-flight scenario: a second BlockJob against an already-blocked
// job must not run the transaction again.
func TestBlockJob_AlreadyBlockedIsNoOp(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	defer mockDB.Close()

	jm := &JobManager{
		db:             mockDB,
		dbQueue:        &mockDbQueueWrapper{mockDB: mockDB},
		processedPages: make(map[string]struct{}),
	}

	const jobID = "job-already-blocked"

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT[\s\S]+FROM jobs j[\s\S]+JOIN domains d`).
		WithArgs(jobID).
		WillReturnRows(jobRow(jobID, JobStatusBlocked))
	mock.ExpectCommit()

	err = jm.BlockJob(context.Background(), jobID, "akamai", "")
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestBlockJob_RaceLostReturnsNil simulates the GetJob-pre-read-then-
// concurrent-completion race: the pre-read sees `running`, the worker
// completes the job, then BlockJob's tx fires and the CAS WHERE clause
// matches zero rows. The whole tx must roll back (no outbox/domains
// writes) and BlockJob must return nil — surfacing an error here
// would be a red toast for a benign race.
func TestBlockJob_RaceLostReturnsNil(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	defer mockDB.Close()

	jm := &JobManager{
		db:             mockDB,
		dbQueue:        &mockDbQueueWrapper{mockDB: mockDB},
		processedPages: make(map[string]struct{}),
	}

	const jobID = "job-race-lost"

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT[\s\S]+FROM jobs j[\s\S]+JOIN domains d`).
		WithArgs(jobID).
		WillReturnRows(jobRow(jobID, JobStatusRunning))
	mock.ExpectCommit()

	mock.ExpectBegin()

	// Tasks update can affect any number of rows — irrelevant once
	// the CAS misses; the tx will roll back.
	mock.ExpectExec(`(?s)WITH picked AS \(\s*SELECT id FROM tasks`).
		WithArgs(jobID, string(TaskStatusSkipped), string(TaskStatusPending), string(TaskStatusWaiting)).
		WillReturnResult(sqlmock.NewResult(0, 0))

	// CAS UPDATE matches zero rows — the concurrent terminal write
	// removed the job from the eligible status set.
	mock.ExpectExec(`UPDATE jobs\s+SET status = \$1, completed_at = \$2, error_message = \$3\s+WHERE id = \$4\s+AND status IN`).
		WithArgs(string(JobStatusBlocked), sqlmock.AnyArg(), sqlmock.AnyArg(), jobID,
			string(JobStatusRunning), string(JobStatusPending), string(JobStatusPaused), string(JobStatusInitialising)).
		WillReturnResult(sqlmock.NewResult(0, 0))

	// No DELETE FROM task_outbox, no UPDATE domains — the tx must
	// roll back as soon as the CAS RowsAffected check trips.
	mock.ExpectRollback()

	err = jm.BlockJob(context.Background(), jobID, "akamai", "Server: AkamaiGHost on 403")
	require.NoError(t, err, "race-lost must surface as nil success, not error")
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestBlockJob_TerminalStatusErrors asserts a block against a
// completed/failed/cancelled job returns an error rather than
// silently overwriting a finished result.
func TestBlockJob_TerminalStatusErrors(t *testing.T) {
	for _, status := range []JobStatus{JobStatusCompleted, JobStatusFailed, JobStatusCancelled} {
		t.Run(string(status), func(t *testing.T) {
			mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
			require.NoError(t, err)
			defer mockDB.Close()

			jm := &JobManager{
				db:             mockDB,
				dbQueue:        &mockDbQueueWrapper{mockDB: mockDB},
				processedPages: make(map[string]struct{}),
			}

			const jobID = "job-terminal"

			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT[\s\S]+FROM jobs j[\s\S]+JOIN domains d`).
				WithArgs(jobID).
				WillReturnRows(jobRow(jobID, status))
			mock.ExpectCommit()

			err = jm.BlockJob(context.Background(), jobID, "akamai", "")
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "cannot be blocked")
			assert.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
