package jobs

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/good-native/hover/internal/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockDbQueueWrapper wraps a mock DB to implement DbQueueProvider interface
type mockDbQueueWrapper struct {
	mockDB *sql.DB
}

func (m *mockDbQueueWrapper) Execute(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := m.mockDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := fn(tx); err != nil {
		return err
	}

	return tx.Commit()
}

func (m *mockDbQueueWrapper) EnqueueURLs(ctx context.Context, jobID string, pages []db.Page, sourceType string, sourceURL string) error {
	// Not needed for this test
	return nil
}

func (m *mockDbQueueWrapper) ExecuteMaintenance(ctx context.Context, fn func(*sql.Tx) error) error {
	return m.Execute(ctx, fn)
}

func (m *mockDbQueueWrapper) CleanupStuckJobs(ctx context.Context) error {
	// Not needed for this test
	return nil
}

// TestJobLifecycleCompletion tests the mechanism that determines when a job is finished
func TestJobLifecycleCompletion(t *testing.T) {
	tests := []struct {
		name           string
		jobStatus      JobStatus
		totalTasks     int
		completedTasks int
		failedTasks    int
		skippedTasks   int
		expectedStatus JobStatus
		shouldComplete bool
	}{
		{
			name:           "all_tasks_completed",
			jobStatus:      JobStatusRunning,
			totalTasks:     10,
			completedTasks: 10,
			failedTasks:    0,
			skippedTasks:   0,
			expectedStatus: JobStatusCompleted,
			shouldComplete: true,
		},
		{
			name:           "some_tasks_failed",
			jobStatus:      JobStatusRunning,
			totalTasks:     10,
			completedTasks: 7,
			failedTasks:    3,
			skippedTasks:   0,
			expectedStatus: JobStatusCompleted,
			shouldComplete: true,
		},
		{
			name:           "some_tasks_skipped",
			jobStatus:      JobStatusRunning,
			totalTasks:     10,
			completedTasks: 5,
			failedTasks:    2,
			skippedTasks:   3,
			expectedStatus: JobStatusCompleted,
			shouldComplete: true,
		},
		{
			name:           "tasks_still_pending",
			jobStatus:      JobStatusRunning,
			totalTasks:     10,
			completedTasks: 5,
			failedTasks:    2,
			skippedTasks:   0,
			expectedStatus: JobStatusRunning,
			shouldComplete: false,
		},
		{
			name:           "no_tasks_job",
			jobStatus:      JobStatusRunning,
			totalTasks:     0,
			completedTasks: 0,
			failedTasks:    0,
			skippedTasks:   0,
			expectedStatus: JobStatusCompleted,
			shouldComplete: true,
		},
		{
			name:           "already_completed_job",
			jobStatus:      JobStatusCompleted,
			totalTasks:     10,
			completedTasks: 10,
			failedTasks:    0,
			skippedTasks:   0,
			expectedStatus: JobStatusCompleted,
			shouldComplete: false, // Already complete, no change
		},
		{
			name:           "cancelled_job",
			jobStatus:      JobStatusCancelled,
			totalTasks:     10,
			completedTasks: 5,
			failedTasks:    0,
			skippedTasks:   0,
			expectedStatus: JobStatusCancelled,
			shouldComplete: false, // Cancelled jobs stay cancelled
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test the completion logic
			job := &Job{
				ID:             "test-job",
				Status:         tt.jobStatus,
				TotalTasks:     tt.totalTasks,
				CompletedTasks: tt.completedTasks,
				FailedTasks:    tt.failedTasks,
				SkippedTasks:   tt.skippedTasks,
			}

			// Create a JobManager to test with
			jm := &JobManager{}

			// Check if job should be marked as complete
			isComplete := jm.IsJobComplete(job)
			assert.Equal(t, tt.shouldComplete, isComplete)

			// If complete, update status
			if isComplete && job.Status == JobStatusRunning {
				job.Status = JobStatusCompleted
				job.CompletedAt = time.Now().UTC()
			}

			assert.Equal(t, tt.expectedStatus, job.Status)
		})
	}
}

// TestJobProgressCalculation tests job progress calculation
func TestJobProgressCalculation(t *testing.T) {
	tests := []struct {
		name             string
		totalTasks       int
		completedTasks   int
		failedTasks      int
		skippedTasks     int
		expectedProgress float64
	}{
		{
			name:             "no_tasks",
			totalTasks:       0,
			completedTasks:   0,
			failedTasks:      0,
			skippedTasks:     0,
			expectedProgress: 0.0,
		},
		{
			name:             "all_completed",
			totalTasks:       100,
			completedTasks:   100,
			failedTasks:      0,
			skippedTasks:     0,
			expectedProgress: 100.0,
		},
		{
			name:             "half_completed",
			totalTasks:       100,
			completedTasks:   50,
			failedTasks:      0,
			skippedTasks:     0,
			expectedProgress: 50.0,
		},
		{
			name:             "with_failures",
			totalTasks:       100,
			completedTasks:   60,
			failedTasks:      20,
			skippedTasks:     0,
			expectedProgress: 80.0,
		},
		{
			name:             "with_skipped",
			totalTasks:       100,
			completedTasks:   60,
			failedTasks:      10,
			skippedTasks:     10,
			expectedProgress: 80.0,
		},
		{
			name:             "all_skipped",
			totalTasks:       100,
			completedTasks:   0,
			failedTasks:      0,
			skippedTasks:     100,
			expectedProgress: 100.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			job := &Job{
				TotalTasks:     tt.totalTasks,
				CompletedTasks: tt.completedTasks,
				FailedTasks:    tt.failedTasks,
				SkippedTasks:   tt.skippedTasks,
			}

			// Create a JobManager to test with
			jm := &JobManager{}

			progress := jm.CalculateJobProgress(job)
			assert.InDelta(t, tt.expectedProgress, progress, 0.01)

			// Also test the Progress field calculation
			job.Progress = progress
			assert.InDelta(t, tt.expectedProgress, job.Progress, 0.01)
		})
	}
}

// TestJobLifecycleStatusTransitions tests valid job status transitions
func TestJobLifecycleStatusTransitions(t *testing.T) {
	tests := []struct {
		name          string
		fromStatus    JobStatus
		toStatus      JobStatus
		isValid       bool
		expectedError string
	}{
		{
			name:       "pending_to_running",
			fromStatus: JobStatusPending,
			toStatus:   JobStatusRunning,
			isValid:    true,
		},
		{
			name:       "running_to_completed",
			fromStatus: JobStatusRunning,
			toStatus:   JobStatusCompleted,
			isValid:    true,
		},
		{
			name:       "running_to_cancelled",
			fromStatus: JobStatusRunning,
			toStatus:   JobStatusCancelled,
			isValid:    true,
		},
		{
			name:       "pending_to_cancelled",
			fromStatus: JobStatusPending,
			toStatus:   JobStatusCancelled,
			isValid:    true,
		},
		{
			name:       "completed_to_running_restart",
			fromStatus: JobStatusCompleted,
			toStatus:   JobStatusRunning,
			isValid:    true, // Restart is allowed
		},
		{
			name:       "cancelled_to_running_restart",
			fromStatus: JobStatusCancelled,
			toStatus:   JobStatusRunning,
			isValid:    true, // Restart is allowed
		},
		{
			name:       "failed_to_running_restart",
			fromStatus: JobStatusFailed,
			toStatus:   JobStatusRunning,
			isValid:    true, // Restart is allowed
		},
		{
			name:          "completed_to_pending_invalid",
			fromStatus:    JobStatusCompleted,
			toStatus:      JobStatusPending,
			isValid:       false,
			expectedError: "invalid status transition",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			job := &Job{
				ID:     "test-job",
				Status: tt.fromStatus,
			}

			// Create a JobManager to test with
			jm := &JobManager{}

			err := jm.ValidateStatusTransition(job.Status, tt.toStatus)

			if tt.isValid {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
				if tt.expectedError != "" {
					assert.Contains(t, err.Error(), tt.expectedError)
				}
			}
		})
	}
}

// TestJobManagerUpdateJobStatus tests the job status update mechanism
func TestJobManagerUpdateJobStatus(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer mockDB.Close()

	ctx := context.Background()

	// Create a mock DbQueue that wraps our mock DB
	mockDbQueue := &mockDbQueueWrapper{
		mockDB: mockDB,
	}

	jm := &JobManager{
		db:      mockDB,
		dbQueue: mockDbQueue,
	}

	tests := []struct {
		name        string
		jobID       string
		newStatus   JobStatus
		setupMock   func()
		expectError bool
	}{
		{
			name:      "update_to_completed",
			jobID:     "job-1",
			newStatus: JobStatusCompleted,
			setupMock: func() {
				mock.ExpectBegin()
				mock.ExpectExec("UPDATE jobs SET status = \\$1, completed_at = \\$2").
					WithArgs(string(JobStatusCompleted), sqlmock.AnyArg(), "job-1").
					WillReturnResult(sqlmock.NewResult(1, 1))
				mock.ExpectCommit()
			},
			expectError: false,
		},
		{
			name:      "update_to_running",
			jobID:     "job-2",
			newStatus: JobStatusRunning,
			setupMock: func() {
				mock.ExpectBegin()
				mock.ExpectExec("UPDATE jobs SET status = \\$1, started_at = \\$2").
					WithArgs(string(JobStatusRunning), sqlmock.AnyArg(), "job-2").
					WillReturnResult(sqlmock.NewResult(1, 1))
				mock.ExpectCommit()
			},
			expectError: false,
		},
		{
			name:      "update_to_cancelled",
			jobID:     "job-3",
			newStatus: JobStatusCancelled,
			setupMock: func() {
				mock.ExpectBegin()
				mock.ExpectExec("UPDATE jobs SET status = \\$1").
					WithArgs(string(JobStatusCancelled), "job-3").
					WillReturnResult(sqlmock.NewResult(1, 1))
				mock.ExpectCommit()
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setupMock()

			err := jm.UpdateJobStatus(ctx, tt.jobID, tt.newStatus)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			// Verify all expectations were met
			assert.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

// TestJobManager_MarkJobRunning verifies the guarded transition flips
// the pre-running statuses → running and stamps started_at when not
// already set. The WHERE clause matches both 'pending' and
// 'initializing' so sitemap jobs that spend a real window in the
// initialising state still flip on first dispatch — without that the
// first-dispatch hook would silently miss them and the "Starting up"
// pill would never go away.
func TestJobManager_MarkJobRunning(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer mockDB.Close()

	ctx := context.Background()

	mockDbQueue := &mockDbQueueWrapper{mockDB: mockDB}
	jm := &JobManager{db: mockDB, dbQueue: mockDbQueue}

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE jobs\s+SET status = 'running',\s+started_at = COALESCE\(started_at, NOW\(\)\)\s+WHERE id = \$1\s+AND status IN \('pending', 'initializing'\)`).
		WithArgs("job-mark").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	require.NoError(t, jm.MarkJobRunning(ctx, "job-mark"))
	require.NoError(t, mock.ExpectationsWereMet())
}
