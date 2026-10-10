// SPDX-License-Identifier: Apache-2.0

package obs

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Point is one sample: unix seconds and a value.
type Point [2]float64

// Series is one line of a chart.
type Series struct {
	Metric string            `json:"metric"`
	Labels map[string]string `json:"labels,omitempty"`
	Points []Point           `json:"points"`
}

// MaxPoints is the most points a chart gets (AC-OBS-05).
const MaxPoints = 1500

// ScrapeInterval is the scrape interval of the agents (REQ-OBS-003).
const ScrapeInterval = 15 * time.Second

var niceSteps = []time.Duration{15 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute,
	10 * time.Minute, 15 * time.Minute, 30 * time.Minute, time.Hour, 2 * time.Hour, 3 * time.Hour, 6 * time.Hour,
	12 * time.Hour, 24 * time.Hour}

// ChooseStep picks the query step. It is never below the scrape interval, never below what the client asked,
// and big enough that the range has at most MaxPoints points.
func ChooseStep(from, to time.Time, requested time.Duration) time.Duration {
	span := to.Sub(from)
	if span < 0 {
		span = 0
	}
	need := time.Duration(math.Ceil(float64(span) / float64(MaxPoints)))
	if requested > need {
		need = requested
	}
	if need < ScrapeInterval {
		need = ScrapeInterval
	}
	for _, s := range niceSteps {
		if s >= need {
			return s
		}
	}
	return need.Round(time.Hour)
}

// Scope selects the series of a metrics query. All values are ids from the database.
type Scope struct {
	OrgID     string
	ServiceID string
	NodeID    string
}

var idRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,80}$`)

func lbl(name, value string) string {
	return name + "=" + strconv.Quote(value)
}

// matcher builds the label selector of a scope: the org is always in it for container series.
func (s Scope) matcher(extra ...string) (string, error) {
	var parts []string
	if s.OrgID != "" {
		if !idRe.MatchString(s.OrgID) {
			return "", fmt.Errorf("%w: bad org id", ErrBadQuery)
		}
		parts = append(parts, lbl("org", s.OrgID))
	}
	if s.ServiceID != "" {
		if !idRe.MatchString(s.ServiceID) {
			return "", fmt.Errorf("%w: bad service id", ErrBadQuery)
		}
		parts = append(parts, lbl("service", s.ServiceID))
	}
	if s.NodeID != "" {
		if !idRe.MatchString(s.NodeID) {
			return "", fmt.Errorf("%w: bad node id", ErrBadQuery)
		}
		parts = append(parts, lbl("node", s.NodeID))
	}
	parts = append(parts, extra...)
	return "{" + strings.Join(parts, ",") + "}", nil
}

// MetricNames are the names of the API (openapi MetricNames).
var MetricNames = []string{"cpu", "memory", "network_in", "network_out", "disk", "requests", "status_5xx_rate",
	"latency_p50", "latency_p95", "latency_p99", "restarts", "load"}

// Expr returns the query of one API metric for a scope. window is the rate window.
func Expr(name string, s Scope, window time.Duration) (string, error) {
	w := fmt.Sprintf("%ds", int(window.Seconds()))
	node := s.NodeID != "" && s.ServiceID == ""
	cont, err := s.matcher()
	if err != nil {
		return "", err
	}
	// Node series have only the node label.
	nodeOnly, err := Scope{NodeID: s.NodeID}.matcher()
	if err != nil {
		return "", err
	}
	switch name {
	case "cpu":
		if node {
			return "polipan_node_cpu_usage_millis" + nodeOnly, nil
		}
		return "sum(polipan_container_cpu_usage_millis" + cont + ")", nil
	case "memory":
		if node {
			return "polipan_node_memory_bytes" + nodeOnly, nil
		}
		return "sum(polipan_container_memory_bytes" + cont + ")", nil
	case "network_in":
		return "sum(rate(polipan_container_net_rx_bytes" + cont + "[" + w + "]))", nil
	case "network_out":
		return "sum(rate(polipan_container_net_tx_bytes" + cont + "[" + w + "]))", nil
	case "disk":
		if node {
			return "polipan_node_disk_used_bytes" + nodeOnly, nil
		}
		m, err := s.matcher()
		if err != nil {
			return "", err
		}
		return "sum(polipan_volume_used_bytes" + m + ")", nil
	case "requests":
		m, err := s.matcher()
		if err != nil {
			return "", err
		}
		return "sum(rate(polipan_http_requests_total" + m + "[" + w + "]))", nil
	case "status_5xx_rate":
		all, err := s.matcher()
		if err != nil {
			return "", err
		}
		bad, err := s.matcher(lbl("status_class", "5xx"))
		if err != nil {
			return "", err
		}
		return "sum(rate(polipan_http_requests_total" + bad + "[" + w + "])) / sum(rate(polipan_http_requests_total" + all + "[" + w + "]))", nil
	case "latency_p50", "latency_p95", "latency_p99":
		q := map[string]string{"latency_p50": "0.5", "latency_p95": "0.95", "latency_p99": "0.99"}[name]
		m, err := s.matcher()
		if err != nil {
			return "", err
		}
		return "histogram_quantile(" + q + ", sum by (le) (rate(polipan_http_request_duration_seconds_bucket" + m + "[" + w + "])))", nil
	case "restarts":
		return "sum(increase(polipan_container_restarts_total" + cont + "[" + w + "]))", nil
	case "load":
		if !node {
			return "", fmt.Errorf("%w: load is a node metric", ErrBadQuery)
		}
		return "polipan_node_load1" + nodeOnly, nil
	}
	return "", fmt.Errorf("%w: unknown metric %q", ErrBadQuery, name)
}

// rangeResult is the answer of query_range and query.
type promResult struct {
	Status    string `json:"status"`
	Error     string `json:"error"`
	ErrorType string `json:"errorType"`
	Data      struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Values [][2]any          `json:"values"`
			Value  [2]any            `json:"value"`
		} `json:"result"`
	} `json:"data"`
}

func parseSample(v [2]any) (Point, bool) {
	ts, ok := v[0].(float64)
	if !ok {
		return Point{}, false
	}
	s, ok := v[1].(string)
	if !ok {
		return Point{}, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return Point{}, false
	}
	return Point{ts, f}, true
}

// QueryRange runs a range query. Series have the name given in metric.
func (b *Backends) QueryRange(ctx context.Context, metric, query string, from, to time.Time, step time.Duration) ([]Series, error) {
	if !b.MetricsEnabled() {
		return nil, ErrUnavailable
	}
	q := url.Values{}
	q.Set("query", query)
	q.Set("start", strconv.FormatInt(from.Unix(), 10))
	q.Set("end", strconv.FormatInt(to.Unix(), 10))
	q.Set("step", fmt.Sprintf("%ds", int(step.Seconds())))
	resp, err := b.do(ctx, "POST", b.MetricsURL, "/api/v1/query_range", nil, "application/x-www-form-urlencoded", strings.NewReader(q.Encode()))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := check(resp); err != nil {
		return nil, err
	}
	var r promResult
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if r.Status != "success" {
		return nil, fmt.Errorf("%w: %s", ErrBadQuery, r.Error)
	}
	out := []Series{}
	for _, res := range r.Data.Result {
		s := Series{Metric: metric, Labels: res.Metric, Points: []Point{}}
		for _, v := range res.Values {
			if p, ok := parseSample(v); ok {
				s.Points = append(s.Points, p)
			}
		}
		if len(s.Points) > 0 {
			out = append(out, s)
		}
	}
	return out, nil
}

// Sample is one value of an instant query.
type Sample struct {
	Labels map[string]string
	Value  float64
}

// Query runs an instant query at a time (zero: now).
func (b *Backends) Query(ctx context.Context, query string, at time.Time) ([]Sample, error) {
	if !b.MetricsEnabled() {
		return nil, ErrUnavailable
	}
	q := url.Values{}
	q.Set("query", query)
	if !at.IsZero() {
		q.Set("time", strconv.FormatInt(at.Unix(), 10))
	}
	resp, err := b.do(ctx, "POST", b.MetricsURL, "/api/v1/query", nil, "application/x-www-form-urlencoded", strings.NewReader(q.Encode()))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := check(resp); err != nil {
		return nil, err
	}
	var r promResult
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if r.Status != "success" {
		return nil, fmt.Errorf("%w: %s", ErrBadQuery, r.Error)
	}
	var out []Sample
	for _, res := range r.Data.Result {
		if p, ok := parseSample(res.Value); ok {
			out = append(out, Sample{Labels: res.Metric, Value: p[1]})
		}
	}
	return out, nil
}

// MetricsQuery is a request of the metrics API.
type MetricsQuery struct {
	Scope     Scope
	Metrics   []string
	From, To  time.Time
	Step      time.Duration // requested, may be zero
	Retention time.Duration // the org retention, zero means no limit
}

// MetricsResult is the answer of the metrics API (openapi MetricResult).
type MetricsResult struct {
	StepSeconds int      `json:"step_seconds"`
	Series      []Series `json:"series"`
}

// Metrics runs the queries of the API metrics for a scope. The range is cut to the retention of the org.
func (b *Backends) Metrics(ctx context.Context, now time.Time, q MetricsQuery) (*MetricsResult, error) {
	to, from := q.To, q.From
	if to.IsZero() {
		to = now
	}
	if from.IsZero() {
		from = to.Add(-time.Hour)
	}
	if q.Retention > 0 && from.Before(now.Add(-q.Retention)) {
		from = now.Add(-q.Retention)
	}
	if !to.After(from) {
		return nil, fmt.Errorf("%w: the end is not after the start", ErrBadQuery)
	}
	step := ChooseStep(from, to, q.Step)
	window := step
	if window < 4*ScrapeInterval {
		window = 4 * ScrapeInterval // rate() needs at least two samples in the window
	}
	res := &MetricsResult{StepSeconds: int(step.Seconds()), Series: []Series{}}
	names := append([]string{}, q.Metrics...)
	sort.Strings(names)
	for _, name := range names {
		expr, err := Expr(name, q.Scope, window)
		if err != nil {
			return nil, err
		}
		series, err := b.QueryRange(ctx, name, expr, from, to, step)
		if err != nil {
			return nil, err
		}
		res.Series = append(res.Series, series...)
	}
	return res, nil
}
