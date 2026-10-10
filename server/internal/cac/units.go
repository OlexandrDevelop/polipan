// SPDX-License-Identifier: Apache-2.0

package cac

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// CPUMillis converts "500m", "0.5" or "2" to millicores (07 section 4).
func CPUMillis(s string) (int, error) {
	s = strings.TrimSpace(s)
	if rest, ok := strings.CutSuffix(s, "m"); ok {
		n, err := strconv.Atoi(rest)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("cpu %q is not valid", s)
		}
		return n, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 {
		return 0, fmt.Errorf("cpu %q is not valid", s)
	}
	return int(math.Round(f * 1000)), nil
}

var sizeUnits = map[string]int64{"Ki": 1 << 10, "Mi": 1 << 20, "Gi": 1 << 30, "Ti": 1 << 40}

// Bytes converts "512Mi" to bytes (07 section 4).
func Bytes(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if len(s) < 3 {
		return 0, fmt.Errorf("size %q is not valid", s)
	}
	mult, ok := sizeUnits[s[len(s)-2:]]
	if !ok {
		return 0, fmt.Errorf("size %q needs the unit Ki, Mi, Gi or Ti", s)
	}
	n, err := strconv.ParseInt(s[:len(s)-2], 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("size %q is not valid", s)
	}
	return n * mult, nil
}

// Seconds converts a duration like 30s, 5m, 24h or 7d to seconds (07 section 4). ms rounds up.
func Seconds(s string) (int, error) {
	s = strings.TrimSpace(s)
	units := []struct {
		suffix string
		mult   float64
	}{{"ms", 0.001}, {"s", 1}, {"m", 60}, {"h", 3600}, {"d", 86400}}
	for _, u := range units {
		if rest, ok := strings.CutSuffix(s, u.suffix); ok {
			n, err := strconv.Atoi(rest)
			if err != nil || n < 0 {
				break
			}
			return int(math.Ceil(float64(n) * u.mult)), nil
		}
	}
	return 0, fmt.Errorf("duration %q is not valid (for example 30s, 5m, 7d)", s)
}

// StatusRange converts "200-399" or "204" to [min, max].
func StatusRange(s string) ([]int, error) {
	lo, hi, found := strings.Cut(s, "-")
	a, err := strconv.Atoi(lo)
	if err != nil {
		return nil, fmt.Errorf("expected_status %q is not valid", s)
	}
	if !found {
		return []int{a}, nil
	}
	b, err := strconv.Atoi(hi)
	if err != nil || b < a {
		return nil, fmt.Errorf("expected_status %q is not valid", s)
	}
	return []int{a, b}, nil
}
