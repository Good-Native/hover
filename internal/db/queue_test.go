package db

import "testing"

func TestIsTerminalJobStatus(t *testing.T) {
	cases := []struct {
		status string
		want   bool
	}{
		// Terminal statuses — EnqueueURLs short-circuits for these.
		{"completed", true},
		{"failed", true},
		{"cancelled", true},
		{"archived", true},
		{"blocked", true},

		// Non-terminal — task inserts proceed.
		{"pending", false},
		{"running", false},
		{"initializing", false},
		{"paused", false},

		// Unknown / empty — must not be treated as terminal so a typo in
		// the DB doesn't silently disable enqueueing.
		{"", false},
		{"unknown", false},
		{"BLOCKED", false}, // case-sensitive: DB uses lowercase
	}
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			if got := IsTerminalJobStatus(tc.status); got != tc.want {
				t.Errorf("IsTerminalJobStatus(%q) = %v, want %v", tc.status, got, tc.want)
			}
		})
	}
}

func TestClassifyEnqueuedTaskDropsOverflowAtMaxPages(t *testing.T) {
	disposition := classifyEnqueuedTask(10, 10, 0, 0, 3)
	if disposition != enqueueTaskDrop {
		t.Fatalf("expected overflow task to be dropped, got %q", disposition)
	}
}

func TestClassifyEnqueuedTaskUsesPendingThenWaitingBeforeDropping(t *testing.T) {
	availableSlots := 2
	maxPages := 4
	currentTaskCount := 0
	pendingCount := 0
	waitingCount := 0

	var got []enqueueTaskDisposition
	for range 5 {
		disposition := classifyEnqueuedTask(maxPages, currentTaskCount, pendingCount, waitingCount, availableSlots)
		got = append(got, disposition)
		switch disposition {
		case enqueueTaskPending:
			pendingCount++
		case enqueueTaskWaiting:
			waitingCount++
		case enqueueTaskDrop:
		}
	}

	want := []enqueueTaskDisposition{
		enqueueTaskPending,
		enqueueTaskPending,
		enqueueTaskWaiting,
		enqueueTaskWaiting,
		enqueueTaskDrop,
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d dispositions, got %d", len(want), len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected disposition %d to be %q, got %q", i, want[i], got[i])
		}
	}
}
