// SPDX-License-Identifier: Apache-2.0

package backup

import (
	"testing"
	"time"
)

func TestCron(t *testing.T) {
	at := func(s string) time.Time {
		v, err := time.Parse("2006-01-02 15:04", s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	tests := []struct {
		expr, from, next, prev string
	}{
		{"0 2 * * *", "2026-10-10 10:00", "2026-10-11 02:00", "2026-10-10 02:00"},
		{"0 3 * * 0", "2026-10-10 10:00", "2026-10-11 03:00", "2026-10-04 03:00"}, // 2026-10-10 is a Saturday
		{"*/15 * * * *", "2026-10-10 10:07", "2026-10-10 10:15", "2026-10-10 10:00"},
		{"30 4 1,15 * *", "2026-10-10 10:00", "2026-10-15 04:30", "2026-10-01 04:30"},
		{"0 0 1 jan *", "2026-10-10 10:00", "2027-01-01 00:00", "2026-01-01 00:00"},
		{"10-20/5 8 * * mon-fri", "2026-10-09 08:12", "2026-10-09 08:15", "2026-10-09 08:10"},
		{"0 2 * * *", "2026-10-10 02:00", "2026-10-11 02:00", "2026-10-10 02:00"},
	}
	for _, tc := range tests {
		c, err := ParseCron(tc.expr)
		if err != nil {
			t.Fatalf("%s: %v", tc.expr, err)
		}
		if got := c.Next(at(tc.from)); !got.Equal(at(tc.next)) {
			t.Errorf("%s next from %s = %s, want %s", tc.expr, tc.from, got, tc.next)
		}
		if got := c.Prev(at(tc.from)); !got.Equal(at(tc.prev)) {
			t.Errorf("%s prev from %s = %s, want %s", tc.expr, tc.from, got, tc.prev)
		}
	}
	if d := mustCron(t, "0 3 * * 0").Interval(time.Now()); d != 7*24*time.Hour {
		t.Errorf("weekly interval = %s", d)
	}
	for _, bad := range []string{"", "* * * *", "61 * * * *", "* 24 * * *", "a b c d e", "*/0 * * * *", "5-1 * * * *"} {
		if _, err := ParseCron(bad); err == nil {
			t.Errorf("%q should not parse", bad)
		}
	}
}

func mustCron(t *testing.T, s string) *Cron {
	c, err := ParseCron(s)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
