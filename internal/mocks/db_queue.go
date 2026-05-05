package mocks

import (
	"context"
	"database/sql"

	"github.com/good-native/hover/internal/db"
	"github.com/stretchr/testify/mock"
)

// MockDbQueue is a mock implementation of DbQueueProvider
type MockDbQueue struct {
	mock.Mock
}

// Execute mocks the Execute method
func (m *MockDbQueue) Execute(ctx context.Context, fn func(*sql.Tx) error) error {
	args := m.Called(ctx, fn)

	// If the test wants to execute the function, it can provide a nil error
	// This allows us to test the transaction logic
	if args.Error(0) == nil && fn != nil {
		// Create a dummy transaction for the function to use
		// In real tests, we might want to pass a mock transaction
		return fn(nil)
	}

	return args.Error(0)
}

// ExecuteControl mocks the ExecuteControl method
func (m *MockDbQueue) ExecuteControl(ctx context.Context, fn func(*sql.Tx) error) error {
	args := m.Called(ctx, fn)

	if args.Error(0) == nil && fn != nil {
		return fn(nil)
	}

	return args.Error(0)
}

// ExecuteControlWithContext mocks the ExecuteControlWithContext method
func (m *MockDbQueue) ExecuteControlWithContext(ctx context.Context, fn func(context.Context, *sql.Tx) error) error {
	args := m.Called(ctx, fn)

	if args.Error(0) == nil && fn != nil {
		return fn(ctx, nil)
	}

	return args.Error(0)
}

// ExecuteMaintenance mocks the ExecuteMaintenance method
func (m *MockDbQueue) ExecuteMaintenance(ctx context.Context, fn func(*sql.Tx) error) error {
	args := m.Called(ctx, fn)

	if args.Error(0) == nil && fn != nil {
		return fn(nil)
	}

	return args.Error(0)
}

// ExecuteWithContext mocks the ExecuteWithContext method
func (m *MockDbQueue) ExecuteWithContext(ctx context.Context, fn func(context.Context, *sql.Tx) error) error {
	args := m.Called(ctx, fn)

	if args.Error(0) == nil && fn != nil {
		return fn(ctx, nil)
	}

	return args.Error(0)
}

// EnqueueURLs mocks the EnqueueURLs method
func (m *MockDbQueue) EnqueueURLs(ctx context.Context, jobID string, pages []db.Page, sourceType string, sourceURL string) error {
	args := m.Called(ctx, jobID, pages, sourceType, sourceURL)
	return args.Error(0)
}

// CleanupStuckJobs mocks the CleanupStuckJobs method
func (m *MockDbQueue) CleanupStuckJobs(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

// CreatePageRecords mocks the CreatePageRecords method
func (m *MockDbQueue) CreatePageRecords(ctx context.Context, dbQueue *db.DbQueue, domainID int, domain string, urls []string) ([]int, []string, []string, error) {
	args := m.Called(ctx, dbQueue, domainID, domain, urls)

	if args.Get(0) == nil {
		return nil, nil, nil, args.Error(3)
	}

	var ids []int
	var hosts []string
	var paths []string

	if v := args.Get(0); v != nil {
		ids = v.([]int)
	}
	if v := args.Get(1); v != nil {
		hosts = v.([]string)
	}
	if v := args.Get(2); v != nil {
		paths = v.([]string)
	}

	return ids, hosts, paths, args.Error(3)
}

// UpdateTaskStatus mocks the UpdateTaskStatus method
func (m *MockDbQueue) UpdateTaskStatus(ctx context.Context, task *db.Task) error {
	args := m.Called(ctx, task)
	return args.Error(0)
}

// PromoteWaitingToPending mocks the PromoteWaitingToPending method.
func (m *MockDbQueue) PromoteWaitingToPending(ctx context.Context, jobID string, limit int) (int, error) {
	args := m.Called(ctx, jobID, limit)
	return args.Int(0), args.Error(1)
}
