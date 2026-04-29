package jobs

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFailJobWithMessage_StatusGuard asserts the fallback failJob
// transition only writes when the job is still in a pre-terminal
// status. Without the guard, a fallback after BlockJob's DB error
// could overwrite a freshly-completed job from a concurrent worker.
func TestFailJobWithMessage_StatusGuard(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	defer mockDB.Close()

	jm := &JobManager{
		db:             mockDB,
		dbQueue:        &mockDbQueueWrapper{mockDB: mockDB},
		processedPages: make(map[string]struct{}),
	}

	const jobID = "job-fallback-1"

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE jobs\s+SET status = \$1, error_message = \$2, completed_at = \$3\s+WHERE id = \$4\s+AND status IN \(\$5, \$6, \$7, \$8\)`).
		WithArgs(string(JobStatusFailed), "boom", sqlmock.AnyArg(), jobID,
			string(JobStatusRunning), string(JobStatusPending), string(JobStatusPaused), string(JobStatusInitialising)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err = jm.failJobWithMessage(context.Background(), jobID, "boom")
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}
