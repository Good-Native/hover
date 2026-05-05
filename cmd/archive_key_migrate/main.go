package main

import (
	"bytes"
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/good-native/hover/internal/archive"
	"github.com/good-native/hover/internal/db"
	"github.com/good-native/hover/internal/logging"
	"github.com/joho/godotenv"
)

var startupLog = logging.Component("startup")

type archivedTask struct {
	TaskID string
	JobID  string
	Bucket string
	OldKey string
	NewKey string
}

func main() {
	logging.Setup(logging.ParseLevel("info"), "production")
	defer func() {
		if a := logging.StdoutAsync(); a != nil {
			a.Close()
		}
	}()

	var (
		apply = flag.Bool("apply", false, "apply the migration; default is dry run")
		limit = flag.Int("limit", 0, "maximum number of archived rows to process; 0 means no limit")
	)
	flag.Parse()

	_ = godotenv.Load()

	ctx := context.Background()
	pgDB, err := db.InitFromEnv()
	if err != nil {
		startupLog.Fatal("Failed to connect to database", "error", err)
	}
	defer pgDB.Close()

	provider, err := archive.NewS3Provider(
		os.Getenv("ARCHIVE_ENDPOINT"),
		os.Getenv("ARCHIVE_ACCESS_KEY_ID"),
		os.Getenv("ARCHIVE_SECRET_ACCESS_KEY"),
		os.Getenv("ARCHIVE_REGION"),
		os.Getenv("ARCHIVE_PROVIDER"),
	)
	if err != nil {
		startupLog.Fatal("Failed to initialise archive provider", "error", err)
	}

	rows, err := loadArchivedTasks(ctx, pgDB.GetDB(), *limit)
	if err != nil {
		startupLog.Fatal("Failed to query archived task keys", "error", err)
	}

	if len(rows) == 0 {
		startupLog.Info("No archived task keys require migration")
		return
	}

	startupLog.Info("Prepared archive key migration",
		"rows", len(rows),
		"apply", *apply,
	)

	for _, row := range rows {
		if !*apply {
			startupLog.Info("Would migrate archived task key",
				"task_id", row.TaskID,
				"job_id", row.JobID,
				"old_key", row.OldKey,
				"new_key", row.NewKey,
			)
			continue
		}

		if err := migrateArchivedTask(ctx, pgDB.GetDB(), provider, row); err != nil {
			startupLog.Error("Failed to migrate archived task key",
				"error", err,
				"task_id", row.TaskID,
				"job_id", row.JobID,
				"old_key", row.OldKey,
				"new_key", row.NewKey,
			)
			continue
		}

		startupLog.Info("Migrated archived task key",
			"task_id", row.TaskID,
			"job_id", row.JobID,
			"new_key", row.NewKey,
		)
	}
}

func loadArchivedTasks(ctx context.Context, sqlDB *sql.DB, limit int) ([]archivedTask, error) {
	query := `
		SELECT id, job_id, html_archive_bucket, html_archive_key
		FROM tasks
		WHERE html_archived_at IS NOT NULL
		  AND html_archive_bucket IS NOT NULL
		  AND html_archive_key LIKE 'jobs/%/tasks/page-path/%.html.gz'
		ORDER BY html_archived_at ASC
	`
	var args []any
	if limit > 0 {
		query += " LIMIT $1"
		args = append(args, limit)
	}

	rows, err := sqlDB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query archived tasks: %w", err)
	}
	defer rows.Close()

	var result []archivedTask
	for rows.Next() {
		var row archivedTask
		if err := rows.Scan(&row.TaskID, &row.JobID, &row.Bucket, &row.OldKey); err != nil {
			return nil, fmt.Errorf("scan archived task row: %w", err)
		}
		row.NewKey = archive.ColdKey(row.JobID, row.TaskID)
		if row.NewKey == row.OldKey {
			continue
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate archived task rows: %w", err)
	}

	return result, nil
}

func migrateArchivedTask(ctx context.Context, sqlDB *sql.DB, provider *archive.S3Provider, row archivedTask) error {
	oldBody, err := provider.Download(ctx, row.Bucket, row.OldKey)
	if err != nil {
		return fmt.Errorf("download old object: %w", err)
	}
	data, readErr := io.ReadAll(oldBody)
	closeErr := oldBody.Close()
	if readErr != nil {
		return fmt.Errorf("read old object: %w", readErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close old object body: %w", closeErr)
	}

	newExists, err := provider.Exists(ctx, row.Bucket, row.NewKey)
	if err != nil {
		return fmt.Errorf("check new object existence: %w", err)
	}
	if newExists {
		newBody, err := provider.Download(ctx, row.Bucket, row.NewKey)
		if err != nil {
			return fmt.Errorf("download existing new object: %w", err)
		}
		newData, readErr := io.ReadAll(newBody)
		closeErr := newBody.Close()
		if readErr != nil {
			return fmt.Errorf("read existing new object: %w", readErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close existing new object body: %w", closeErr)
		}
		if !bytes.Equal(data, newData) {
			return fmt.Errorf("new object already exists with different content")
		}
	} else {
		if err := provider.Upload(ctx, row.Bucket, row.NewKey, bytes.NewReader(data), archive.UploadOptions{
			ContentType:     "text/html",
			ContentEncoding: "gzip",
			Metadata: map[string]string{
				"task_id": row.TaskID,
				"job_id":  row.JobID,
			},
		}); err != nil {
			return fmt.Errorf("upload new object: %w", err)
		}
	}

	ok, err := provider.Exists(ctx, row.Bucket, row.NewKey)
	if err != nil {
		return fmt.Errorf("verify new object existence: %w", err)
	}
	if !ok {
		return fmt.Errorf("new object missing after upload")
	}

	if _, err := sqlDB.ExecContext(ctx, `
		UPDATE tasks
		SET html_archive_key = $2
		WHERE id = $1
		  AND html_archive_key = $3
	`, row.TaskID, row.NewKey, row.OldKey); err != nil {
		return fmt.Errorf("update task archive key: %w", err)
	}

	if err := provider.Delete(ctx, row.Bucket, row.OldKey); err != nil {
		return fmt.Errorf("delete old object: %w", err)
	}

	return nil
}
