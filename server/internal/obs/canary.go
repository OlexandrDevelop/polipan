// SPDX-License-Identifier: Apache-2.0

package obs

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"time"
)

// CanaryStats are the numbers of one canary step window (08-deploy-engine section 6).
type CanaryStats struct {
	GreenRequests float64 // requests of the new release
	Green5xx      float64 // of them with a 5xx answer
	GreenP95      float64 // seconds
	BlueP95       float64 // seconds
	BlueP95Known  bool
	GreenP95Known bool
}

// CanaryStats reads the ingress metrics of the green and the blue release of a service over a window.
// The ingress series carry the service and the release (REQ-OBS-103).
func (b *Backends) CanaryStats(ctx context.Context, serviceID, green, blue string, window time.Duration) (CanaryStats, error) {
	var s CanaryStats
	if !idRe.MatchString(serviceID) || !idRe.MatchString(green) || (blue != "" && !idRe.MatchString(blue)) {
		return s, fmt.Errorf("%w: bad id", ErrBadQuery)
	}
	if window < 30*time.Second {
		window = 30 * time.Second
	}
	w := strconv.Itoa(int(window.Seconds())) + "s"
	sel := func(rel string, extra string) string {
		return "{service=" + strconv.Quote(serviceID) + ",release=" + strconv.Quote(rel) + extra + "}"
	}
	one := func(q string) (float64, bool, error) {
		vs, err := b.Query(ctx, q, time.Time{})
		if err != nil {
			return 0, false, err
		}
		if len(vs) == 0 || math.IsNaN(vs[0].Value) {
			return 0, false, nil
		}
		return vs[0].Value, true, nil
	}
	var err error
	if s.GreenRequests, _, err = one("sum(increase(polipan_http_requests_total" + sel(green, "") + "[" + w + "]))"); err != nil {
		return s, err
	}
	if s.Green5xx, _, err = one("sum(increase(polipan_http_requests_total" + sel(green, `,status_class="5xx"`) + "[" + w + "]))"); err != nil {
		return s, err
	}
	hq := func(rel string) string {
		return "histogram_quantile(0.95, sum by (le) (increase(polipan_http_request_duration_seconds_bucket" + sel(rel, "") + "[" + w + "])))"
	}
	if s.GreenP95, s.GreenP95Known, err = one(hq(green)); err != nil {
		return s, err
	}
	if blue != "" {
		if s.BlueP95, s.BlueP95Known, err = one(hq(blue)); err != nil {
			return s, err
		}
	}
	return s, nil
}
