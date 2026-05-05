package broker

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/good-native/hover/internal/observability"
	"github.com/lib/pq"
	"github.com/redis/go-redis/v9"
)

type RunningCounters struct {
	client *Client
}

func NewRunningCounters(client *Client) *RunningCounters {
	return &RunningCounters{client: client}
}

func (rc *RunningCounters) Increment(ctx context.Context, jobID string) (int64, error) {
	return rc.client.rdb.HIncrBy(ctx, RunningCountersKey, jobID, 1).Result()
}

func (rc *RunningCounters) Decrement(ctx context.Context, jobID string) (int64, error) {
	val, err := rc.client.rdb.HIncrBy(ctx, RunningCountersKey, jobID, -1).Result()
	if err != nil {
		return 0, err
	}
	if val <= 0 {
		if err := rc.client.rdb.HDel(ctx, RunningCountersKey, jobID).Err(); err != nil {
			brokerLog.Warn("failed to clean zero counter entry", "error", err, "job_id", jobID)
		}
		return 0, nil
	}
	return val, nil
}

func (rc *RunningCounters) Get(ctx context.Context, jobID string) (int64, error) {
	val, err := rc.client.rdb.HGet(ctx, RunningCountersKey, jobID).Int64()
	if err == redis.Nil {
		return 0, nil
	}
	return val, err
}

func (rc *RunningCounters) GetAll(ctx context.Context) (map[string]int64, error) {
	result, err := rc.client.rdb.HGetAll(ctx, RunningCountersKey).Result()
	if err != nil {
		return nil, err
	}

	counts := make(map[string]int64, len(result))
	for jobID, v := range result {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			brokerLog.Warn("non-numeric running counter", "job_id", jobID, "value", v, "error", err)
			continue
		}
		counts[jobID] = n
	}
	return counts, nil
}

// Atomic Del+HSet — concurrent HIncrBy between the two would silently
// drop updates and persist drift until the next reconcile (120s).
var reconcileScript = redis.NewScript(`
redis.call('DEL', KEYS[1])
if #ARGV >= 2 then
    redis.call('HSET', KEYS[1], unpack(ARGV))
end
return 1
`)

func (rc *RunningCounters) Reconcile(ctx context.Context, counts map[string]int64) error {
	if len(counts) == 0 {
		return rc.client.rdb.Del(ctx, RunningCountersKey).Err()
	}

	args := make([]interface{}, 0, len(counts)*2)
	for jobID, count := range counts {
		if count > 0 {
			args = append(args, jobID, strconv.FormatInt(count, 10))
		}
	}
	if len(args) == 0 {
		return rc.client.rdb.Del(ctx, RunningCountersKey).Err()
	}

	return reconcileScript.Run(ctx, rc.client.rdb, []string{RunningCountersKey}, args...).Err()
}

func (rc *RunningCounters) RemoveJob(ctx context.Context, jobID string) error {
	return rc.client.rdb.HDel(ctx, RunningCountersKey, jobID).Err()
}

type DBSyncFunc func(ctx context.Context, counts map[string]int64) error

func (rc *RunningCounters) StartDBSync(ctx context.Context, interval time.Duration, syncFn DBSyncFunc) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			counts, err := rc.GetAll(ctx)
			if err != nil {
				brokerLog.Error("failed to read running counters for DB sync", "error", err)
				continue
			}
			if err := syncFn(ctx, counts); err != nil {
				brokerLog.Error("failed to sync running counters to DB", "error", err)
			}
		}
	}
}

// No outer tx: wrapping per-job UPDATEs together held row locks that
// deadlocked with the update_job_queue_counters AFTER trigger and
// saturated the bulk DB pool. Skew metric loses tx-snapshot
// consistency but Redis/PG already drift between ticks.
func DefaultDBSyncFunc(sqlDB *sql.DB) DBSyncFunc {
	return func(ctx context.Context, counts map[string]int64) error {
		if len(counts) == 0 {
			_, err := sqlDB.ExecContext(ctx,
				`UPDATE jobs SET running_tasks = 0
				   WHERE running_tasks > 0
				     AND status IN ('running', 'pending')`)
			return err
		}

		jobIDs := make([]string, 0, len(counts))
		for jobID := range counts {
			jobIDs = append(jobIDs, jobID)
		}

		priorCounts := make(map[string]int64, len(jobIDs))
		rows, qerr := sqlDB.QueryContext(ctx,
			`SELECT id, running_tasks FROM jobs WHERE id = ANY($1)`,
			pq.Array(jobIDs))
		if qerr == nil {
			for rows.Next() {
				var id string
				var rt int64
				if scanErr := rows.Scan(&id, &rt); scanErr == nil {
					priorCounts[id] = rt
				}
			}
			_ = rows.Close()
		}

		// ExecContext (not Prepare) avoids SQLSTATE 42P05 under pgbouncer
		// transaction pooling — pgx v5 hashes to deterministic stmt_<md5>
		// names that clash across logical clients.
		// GREATEST(0,…) clamps a transient negative Redis counter to keep
		// the jobs_running_tasks_non_negative CHECK constraint (HOVER-K4).
		for _, jobID := range jobIDs {
			count := counts[jobID]
			if _, err := sqlDB.ExecContext(ctx,
				`UPDATE jobs SET running_tasks = GREATEST(0, $1)
				   WHERE id = $2 AND status IN ('running', 'pending')`,
				count, jobID); err != nil {
				return fmt.Errorf("update job %s: %w", jobID, err)
			}
			observability.RecordBrokerCounterSyncSkew(ctx, jobID,
				math.Abs(float64(count-priorCounts[jobID])))
		}

		// Don't sweep finished-job zeros here: the wide UPDATE deadlocks
		// with update_job_queue_counters. Reconcile loop (120s) catches up.

		return nil
	}
}
