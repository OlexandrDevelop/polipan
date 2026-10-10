// SPDX-License-Identifier: Apache-2.0

package queue_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/polipan/polipan/server/internal/db"
	"github.com/polipan/polipan/server/internal/db/dbtest"
	"github.com/polipan/polipan/server/internal/queue"
	"github.com/polipan/polipan/server/internal/shared/clock"
)

// REQ-CORE-011: the queue works on PostgreSQL: unique keys, claim, back-off and dead jobs.
func TestREQ_CORE_011_QueueRetryAndUniqueKey(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.NewDatabase(t, dbtest.Admin(t))
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	q := queue.New(pool, clk)

	ok, err := q.Enqueue(ctx, "demo", map[string]int{"n": 1}, queue.EnqueueOptions{UniqueKey: "same"})
	if err != nil || !ok {
		t.Fatalf("first enqueue: %v %v", ok, err)
	}
	if ok, _ := q.Enqueue(ctx, "demo", nil, queue.EnqueueOptions{UniqueKey: "same"}); ok {
		t.Fatal("a second job with the same unique key was added")
	}

	for attempt := 1; attempt <= queue.DefaultMaxAttempts; attempt++ {
		job, err := q.Claim(ctx, "w1")
		if err != nil || job == nil {
			t.Fatalf("attempt %d: claim %v %v", attempt, job, err)
		}
		if job.Attempts != attempt {
			t.Fatalf("attempts %d, want %d", job.Attempts, attempt)
		}
		if again, _ := q.Claim(ctx, "w2"); again != nil {
			t.Fatal("a running job was claimed twice")
		}
		if err := q.Fail(ctx, *job, errors.New("boom")); err != nil {
			t.Fatal(err)
		}
		if attempt == queue.DefaultMaxAttempts {
			break
		}
		if early, _ := q.Claim(ctx, "w1"); early != nil {
			t.Fatal("the job ran again before its back-off ended")
		}
		clk.Advance(queue.Backoff(attempt))
	}
	var state string
	if err := pool.QueryRow(ctx, "SELECT state FROM jobs").Scan(&state); err != nil || state != "dead" {
		t.Fatalf("state %q, err %v", state, err)
	}
}
