// SPDX-License-Identifier: Apache-2.0

package backup

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Cron is a cron expression with five fields (minute, hour, day of month, month, day of week) in UTC.
// It supports *, lists (1,3), ranges (1-5), steps (*/15, 10-30/5) and the names of the months and the days.
type Cron struct {
	min, hour, dom, month, dow uint64 // bit sets
	domStar, dowStar           bool
}

var (
	monthNames = map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}
	dayNames   = map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}
)

// ParseCron reads a cron expression.
func ParseCron(expr string) (*Cron, error) {
	f := strings.Fields(expr)
	if len(f) != 5 {
		return nil, fmt.Errorf("a cron expression has 5 fields (minute hour day month weekday), got %d", len(f))
	}
	c := &Cron{}
	var err error
	if c.min, err = parseField(f[0], 0, 59, nil); err != nil {
		return nil, fmt.Errorf("minute: %w", err)
	}
	if c.hour, err = parseField(f[1], 0, 23, nil); err != nil {
		return nil, fmt.Errorf("hour: %w", err)
	}
	if c.dom, err = parseField(f[2], 1, 31, nil); err != nil {
		return nil, fmt.Errorf("day of month: %w", err)
	}
	if c.month, err = parseField(f[3], 1, 12, monthNames); err != nil {
		return nil, fmt.Errorf("month: %w", err)
	}
	if c.dow, err = parseField(f[4], 0, 7, dayNames); err != nil {
		return nil, fmt.Errorf("day of week: %w", err)
	}
	if c.dow&(1<<7) != 0 { // 7 is Sunday too
		c.dow |= 1
	}
	c.domStar, c.dowStar = strings.HasPrefix(f[2], "*"), strings.HasPrefix(f[4], "*")
	return c, nil
}

func parseField(s string, lo, hi int, names map[string]int) (uint64, error) {
	var set uint64
	for _, part := range strings.Split(s, ",") {
		step := 1
		if base, st, ok := strings.Cut(part, "/"); ok {
			n, err := strconv.Atoi(st)
			if err != nil || n < 1 {
				return 0, fmt.Errorf("%q is not a valid step", st)
			}
			step, part = n, base
		}
		a, b := lo, hi
		if part != "*" {
			from, to, isRange := strings.Cut(part, "-")
			var err error
			if a, err = atoi(from, names); err != nil {
				return 0, err
			}
			b = a
			if isRange {
				if b, err = atoi(to, names); err != nil {
					return 0, err
				}
			} else if strings.Contains(s, "/") {
				b = hi
			}
		}
		if a < lo || b > hi || a > b {
			return 0, fmt.Errorf("%q is outside %d-%d", part, lo, hi)
		}
		for v := a; v <= b; v += step {
			set |= 1 << uint(v)
		}
	}
	if set == 0 {
		return 0, fmt.Errorf("%q matches nothing", s)
	}
	return set, nil
}

func atoi(s string, names map[string]int) (int, error) {
	if v, ok := names[strings.ToLower(s)]; ok {
		return v, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%q is not a number", s)
	}
	return n, nil
}

func (c *Cron) dayMatches(t time.Time) bool {
	dom := c.dom&(1<<uint(t.Day())) != 0
	dow := c.dow&(1<<uint(t.Weekday())) != 0
	switch {
	case c.domStar && c.dowStar:
		return true
	case c.domStar:
		return dow
	case c.dowStar:
		return dom
	}
	return dom || dow // both are restricted: either one matches (like Vixie cron)
}

// Next returns the first time after t that matches, with the seconds at zero.
func (c *Cron) Next(t time.Time) time.Time {
	t = t.UTC().Truncate(time.Minute).Add(time.Minute)
	end := t.AddDate(5, 0, 0)
	for t.Before(end) {
		switch {
		case c.month&(1<<uint(t.Month())) == 0:
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, time.UTC)
		case !c.dayMatches(t):
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, time.UTC)
		case c.hour&(1<<uint(t.Hour())) == 0:
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, time.UTC)
		case c.min&(1<<uint(t.Minute())) == 0:
			t = t.Add(time.Minute)
		default:
			return t
		}
	}
	return time.Time{}
}

// Prev returns the last time at or before t that matches. It is zero when there is none in the last 5 years.
func (c *Cron) Prev(t time.Time) time.Time {
	t = t.UTC().Truncate(time.Minute)
	for _, back := range []time.Duration{2 * time.Hour, 26 * time.Hour, 8 * 24 * time.Hour, 32 * 24 * time.Hour, 400 * 24 * time.Hour, 5 * 366 * 24 * time.Hour} {
		var last time.Time
		for n := c.Next(t.Add(-back).Add(-time.Minute)); !n.IsZero() && !n.After(t); n = c.Next(n) {
			last = n
		}
		if !last.IsZero() {
			return last
		}
	}
	return time.Time{}
}

// Interval is the time between two matches that follow each other after t. It is used to judge "2 x the check interval".
func (c *Cron) Interval(t time.Time) time.Duration {
	n1 := c.Next(t)
	if n1.IsZero() {
		return 0
	}
	n2 := c.Next(n1)
	if n2.IsZero() {
		return 0
	}
	return n2.Sub(n1)
}
