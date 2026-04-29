package jobs

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIsJobInTerminalStatus_Terminal asserts the cheap mid-loop status
// check returns true for every terminal status. Used by processSitemap
// between batches to short-circuit work for jobs already transitioned
// to blocked/cancelled/etc by a concurrent BlockJob/CancelJob.
func TestIsJobInTerminalStatus_Terminal(t *testing.T) {
	for _, status := range []string{"blocked", "cancelled", "failed", "completed", "archived"} {
		t.Run(status, func(t *testing.T) {
			mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
			require.NoError(t, err)
			defer mockDB.Close()

			jm := &JobManager{
				db:             mockDB,
				dbQueue:        &mockDbQueueWrapper{mockDB: mockDB},
				processedPages: make(map[string]struct{}),
			}

			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT status FROM jobs WHERE id = \$1`).
				WithArgs("job-x").
				WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow(status))
			mock.ExpectCommit()

			if !jm.isJobInTerminalStatus(context.Background(), "job-x") {
				t.Errorf("status %q must be reported as terminal", status)
			}
			assert.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

// TestIsJobInTerminalStatus_NotTerminal asserts the active statuses
// the sitemap loop legitimately encounters mid-discovery do not abort
// the loop.
func TestIsJobInTerminalStatus_NotTerminal(t *testing.T) {
	for _, status := range []string{"pending", "running", "initializing", "paused"} {
		t.Run(status, func(t *testing.T) {
			mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
			require.NoError(t, err)
			defer mockDB.Close()

			jm := &JobManager{
				db:             mockDB,
				dbQueue:        &mockDbQueueWrapper{mockDB: mockDB},
				processedPages: make(map[string]struct{}),
			}

			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT status FROM jobs WHERE id = \$1`).
				WithArgs("job-x").
				WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow(status))
			mock.ExpectCommit()

			if jm.isJobInTerminalStatus(context.Background(), "job-x") {
				t.Errorf("status %q must NOT be reported as terminal", status)
			}
			assert.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

// TestIsJobInTerminalStatus_QueryErrorContinues asserts a transient
// query failure does not silently abort a healthy crawl: the function
// returns false (not terminal) so the sitemap loop keeps going.
func TestIsJobInTerminalStatus_QueryErrorContinues(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	defer mockDB.Close()

	jm := &JobManager{
		db:             mockDB,
		dbQueue:        &mockDbQueueWrapper{mockDB: mockDB},
		processedPages: make(map[string]struct{}),
	}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status FROM jobs WHERE id = \$1`).
		WithArgs("job-x").
		WillReturnError(errors.New("transient DB blip"))
	mock.ExpectRollback()

	if jm.isJobInTerminalStatus(context.Background(), "job-x") {
		t.Errorf("DB error must not surface as terminal — false-positive would stall a healthy crawl")
	}
	assert.NoError(t, mock.ExpectationsWereMet())
}
