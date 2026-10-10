// SPDX-License-Identifier: Apache-2.0

// Package queue is the job queue. It lives in PostgreSQL (REQ-CORE-011).
// Workers lock rows with SELECT ... FOR UPDATE SKIP LOCKED.
package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/polipan/polipan/server/internal/shared/clock"
)

// DefaultMaxAttempts is the number of tries before a job is dead.
const DefaultMaxAttempts = 5

// BaseBackoff is the wait after the first failure. It doubles with each attempt.
const BaseBackoff = 10 * time.Second

// Job is a row of the jobs table.
type Job struct {
	ID          int64
	Kind        string
	Payload     json.RawMessage
	Attempts    int
	MaxAttempts int
}

// Handler runs one job. A returned error makes the job retry later.
type Handler func(ctx context.Context, job Job) error

// Queue reads and writes the jobs table.
type Queue struct {
	pool  *pgxpool.Pool
	clock clock.Clock
	// Backoffs gives a job kind its own waits after failed attempts: the first entry after the first failure, and so
	// on. The last entry is used for later failures. Kinds without an entry use the exponential Backoff.
	Backoffs map[string][]time.Duration
}

// New creates a queue.
func New(pool *pgxpool.Pool, c clock.Clock) *Queue {
	return &Queue{pool: pool, clock: c}
}

// EnqueueOptions are the optional settings of a job.
type EnqueueOptions struct {
	// UniqueKey keeps two queued or running jobs with the same key from existing.
	UniqueKey string
	// RunAt delays the job. Zero means now.
	RunAt time.Time
	// MaxAttempts is the number of tries before the job is dead. Zero means DefaultMaxAttempts.
	MaxAttempts int
}

// Enqueue adds a job. It returns false when a job with the same unique key is already waiting or running.
func (q *Queue) Enqueue(ctx context.Context, kind string, payload any, opt EnqueueOptions) (bool, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return false, err
	}
	runAt := opt.RunAt
	if runAt.IsZero() {
		runAt = q.clock.Now()
	}
	var key *string
	if opt.UniqueKey != "" {
		key = &opt.UniqueKey
	}
	max := opt.MaxAttempts
	if max <= 0 {
		max = DefaultMaxAttempts
	}
	tag, err := q.pool.Exec(ctx, `INSERT INTO jobs (kind, payload, run_at, unique_key, max_attempts)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (unique_key) WHERE state IN ('queued','running') DO NOTHING`,
		kind, body, runAt, key, max)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// Claim locks the next ready job for the worker. It returns nil when there is none.
func (q *Queue) Claim(ctx context.Context, workerID string) (*Job, error) {
	now := q.clock.Now()
	row := q.pool.QueryRow(ctx, `UPDATE jobs SET state = 'running', attempts = attempts + 1,
			locked_by = $1, locked_at = $2
		WHERE id = (
			SELECT id FROM jobs WHERE state = 'queued' AND run_at <= $2
			ORDER BY run_at, id LIMIT 1
			FOR UPDATE SKIP LOCKED)
		RETURNING id, kind, payload, attempts, max_attempts`, workerID, now)
	var j Job
	if err := row.Scan(&j.ID, &j.Kind, &j.Payload, &j.Attempts, &j.MaxAttempts); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &j, nil
}

// Succeed marks the job as done.
func (q *Queue) Succeed(ctx context.Context, id int64) error {
	_, err := q.pool.Exec(ctx, `UPDATE jobs SET state = 'succeeded', last_error = NULL WHERE id = $1`, id)
	return err
}

// Fail records an error. The job runs again after an exponential back-off,
// or becomes dead when it used all attempts.
func (q *Queue) Fail(ctx context.Context, job Job, cause error) error {
	if job.Attempts >= job.MaxAttempts {
		_, err := q.pool.Exec(ctx, `UPDATE jobs SET state = 'dead', last_error = $2 WHERE id = $1`, job.ID, cause.Error())
		return err
	}
	wait := Backoff(job.Attempts)
	if b := q.Backoffs[job.Kind]; len(b) > 0 {
		wait = b[min(job.Attempts-1, len(b)-1)]
	}
	_, err := q.pool.Exec(ctx, `UPDATE jobs SET state = 'queued', run_at = $2, last_error = $3,
		locked_by = NULL, locked_at = NULL WHERE id = $1`,
		job.ID, q.clock.Now().Add(wait), cause.Error())
	return err
}

// Backoff returns the wait after the given number of failed attempts (1, 2, 3, ...).
func Backoff(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	return BaseBackoff << (attempts - 1)
}

// Worker takes jobs and runs them with the handler of their kind.
type Worker struct {
	Queue    *Queue
	ID       string
	Handlers map[string]Handler
	Interval time.Duration
	Log      *slog.Logger
}

// RunOnce claims and runs one job. It returns false when no job was ready.
func (w *Worker) RunOnce(ctx context.Context) (bool, error) {
	job, err := w.Queue.Claim(ctx, w.ID)
	if err != nil || job == nil {
		return false, err
	}
	h, ok := w.Handlers[job.Kind]
	var runErr error
	if !ok {
		runErr = fmt.Errorf("no handler for job kind %q", job.Kind)
	} else {
		runErr = h(ctx, *job)
	}
	if runErr != nil {
		w.Log.Warn("job failed", "job_id", job.ID, "kind", job.Kind, "attempt", job.Attempts, "error", runErr.Error())
		return true, w.Queue.Fail(context.WithoutCancel(ctx), *job, runErr)
	}
	return true, w.Queue.Succeed(context.WithoutCancel(ctx), job.ID)
}

// Run polls until the context ends.
func (w *Worker) Run(ctx context.Context) {
	interval := w.Interval
	if interval <= 0 {
		interval = time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		for {
			ran, err := w.RunOnce(ctx)
			if err != nil {
				w.Log.Error("job queue error", "error", err.Error())
			}
			if !ran || err != nil || ctx.Err() != nil {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
