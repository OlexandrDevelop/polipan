// SPDX-License-Identifier: Apache-2.0

package obs

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/polipan/polipan/server/internal/pki"
	"github.com/polipan/polipan/server/internal/shared/clock"
)

// Ingest takes metrics and log batches from the agents (12-observability section 7). The node is the common name of
// its client certificate. The server sets the tenant labels from its own data: an agent can write only for
// services that have a placement on its node.
type Ingest struct {
	DB       *pgxpool.Pool
	Backends *Backends
	Log      *slog.Logger
	Clock    clock.Clock

	mu    sync.Mutex
	cache map[string]*nodeScope
	locks map[string]*sync.Mutex
}

type svcScope struct{ org, project, env string }

type nodeScope struct {
	at  time.Time
	svc map[string]svcScope
}

const (
	scopeTTL      = 10 * time.Second
	maxBodyBytes  = 16 << 20
	maxLineBytes  = 128 << 10
	maxMsgBytes   = 70 << 10
	maxFutureSkew = 5 * time.Minute
)

// metricLabels are the labels that are kept (REQ-OBS-100). Others are dropped.
var metricLabels = map[string]bool{"org": true, "project": true, "environment": true, "service": true, "release": true,
	"node": true, "replica": true, "volume": true, "domain": true, "status_class": true, "le": true, "policy": true, "state": true,
	"mountpoint": true, "roles": true}

var metricNameRe = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)

// platformMetrics are written by the server only. An agent cannot send them.
var platformMetrics = map[string]bool{"polipan_deployments_total": true, "polipan_deploy_duration_seconds": true,
	"polipan_backup_last_success_timestamp": true, "polipan_restore_check_last_result": true,
	"polipan_agent_heartbeat_age_seconds": true}

// Handler serves /ingest/metrics and /ingest/logs. It needs a verified client certificate: the listener has
// ClientAuth RequireAndVerifyClientCert.
func (in *Ingest) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /ingest/metrics", in.node(in.metrics))
	mux.HandleFunc("POST /ingest/logs", in.node(in.logs))
	return mux
}

func (in *Ingest) now() time.Time {
	if in.Clock != nil {
		return in.Clock.Now()
	}
	return time.Now()
}

func (in *Ingest) log() *slog.Logger {
	if in.Log != nil {
		return in.Log
	}
	return slog.Default()
}

// node checks the client certificate and finds the services of the node.
func (in *Ingest) node(next func(w http.ResponseWriter, r *http.Request, nodeID string, sc *nodeScope)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.VerifiedChains[0]) == 0 {
			http.Error(w, "client certificate required", http.StatusUnauthorized)
			return
		}
		leaf := r.TLS.VerifiedChains[0][0]
		nodeID := leaf.Subject.CommonName
		if !strings.HasPrefix(nodeID, "nod_") {
			http.Error(w, "the certificate is not a node certificate", http.StatusForbidden)
			return
		}
		if revoked, err := pki.IsRevoked(r.Context(), in.DB, pki.SerialHex(leaf)); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		} else if revoked {
			http.Error(w, "the certificate is revoked", http.StatusForbidden)
			return
		}
		sc, err := in.scope(r.Context(), nodeID)
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "unknown node", http.StatusForbidden)
			return
		}
		if err != nil {
			in.log().Warn("ingest: node scope", "node", nodeID, "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		next(w, r, nodeID, sc)
	}
}

// scope returns the services that have a placement on the node, with their org, project and environment.
func (in *Ingest) scope(ctx context.Context, nodeID string) (*nodeScope, error) {
	in.mu.Lock()
	if sc := in.cache[nodeID]; sc != nil && in.now().Sub(sc.at) < scopeTTL {
		in.mu.Unlock()
		return sc, nil
	}
	in.mu.Unlock()
	var deleted any
	if err := in.DB.QueryRow(ctx, `SELECT deleted_at FROM nodes WHERE id = $1`, nodeID).Scan(&deleted); err != nil {
		return nil, err
	}
	if deleted != nil {
		return nil, pgx.ErrNoRows
	}
	rows, err := in.DB.Query(ctx, `SELECT DISTINCT s.id, e.id, p.id, p.org_id FROM placements pl
		JOIN services s ON s.id = pl.service_id JOIN environments e ON e.id = s.environment_id JOIN projects p ON p.id = e.project_id
		WHERE pl.node_id = $1`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sc := &nodeScope{at: in.now(), svc: map[string]svcScope{}}
	for rows.Next() {
		var sid string
		var v svcScope
		if err := rows.Scan(&sid, &v.env, &v.project, &v.org); err != nil {
			return nil, err
		}
		sc.svc[sid] = v
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	in.mu.Lock()
	if in.cache == nil {
		in.cache = map[string]*nodeScope{}
	}
	in.cache[nodeID] = sc
	in.mu.Unlock()
	return sc, nil
}

// fresh reads the scope of a node again when the cached one is more than a second old. It returns nil when the cache
// is new (nothing can have changed) or the database failed. This is for a batch that names a service the cache does
// not know: the service was placed after the cache was made.
func (in *Ingest) fresh(ctx context.Context, nodeID string, old *nodeScope) *nodeScope {
	if in.now().Sub(old.at) < time.Second {
		return nil
	}
	in.refresh(nodeID)
	sc, err := in.scope(ctx, nodeID)
	if err != nil {
		return nil
	}
	return sc
}

// refresh forgets the cached scope of a node, so a service that was placed a moment ago is accepted.
func (in *Ingest) refresh(nodeID string) {
	in.mu.Lock()
	delete(in.cache, nodeID)
	in.mu.Unlock()
}

func (in *Ingest) lockFor(nodeID string) *sync.Mutex {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.locks == nil {
		in.locks = map[string]*sync.Mutex{}
	}
	l := in.locks[nodeID]
	if l == nil {
		l = &sync.Mutex{}
		in.locks[nodeID] = l
	}
	return l
}

// ---- metrics ----

func (in *Ingest) metrics(w http.ResponseWriter, r *http.Request, nodeID string, sc *nodeScope) {
	if !in.Backends.MetricsEnabled() {
		http.Error(w, "metrics storage is not set up", http.StatusServiceUnavailable)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	series, err := decodeRemoteWrite(body)
	if err != nil {
		http.Error(w, "bad remote write body", http.StatusBadRequest)
		return
	}
	text, dropped := in.cleanSeries(nodeID, sc, series)
	if dropped > 0 {
		if fresh := in.fresh(r.Context(), nodeID, sc); fresh != nil { // a service that was placed a moment ago
			text, dropped = in.cleanSeries(nodeID, fresh, series)
		}
		in.log().Debug("ingest: dropped series", "node", nodeID, "count", dropped)
	}
	if text == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	resp, err := in.Backends.do(r.Context(), "POST", in.Backends.MetricsURL, "/api/v1/import/prometheus", nil, "text/plain", strings.NewReader(text))
	if err != nil {
		http.Error(w, "metrics storage is not reachable", http.StatusServiceUnavailable)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if err := check(resp); err != nil {
		in.log().Warn("ingest: metrics store", "err", err)
		http.Error(w, "metrics storage refused the data", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// cleanSeries keeps the series Polipan defines, sets the tenant labels from the database, and gives back the text
// for the import. It counts the series it dropped.
func (in *Ingest) cleanSeries(nodeID string, sc *nodeScope, series []rwSeries) (string, int) {
	var sb strings.Builder
	dropped := 0
	maxTS := in.now().Add(maxFutureSkew).UnixMilli()
	for _, s := range series {
		name := s.Labels["__name__"]
		// The name goes into the import text as it is: only a plain metric name is allowed (no newline, brace or quote).
		if !strings.HasPrefix(name, "polipan_") || platformMetrics[name] || !metricNameRe.MatchString(name) {
			dropped++
			continue
		}
		labels := map[string]string{}
		for k, v := range s.Labels {
			if metricLabels[k] {
				labels[k] = v
			}
		}
		if sid := labels["service"]; sid != "" {
			v, ok := sc.svc[sid]
			if !ok {
				dropped++
				continue
			}
			labels["org"], labels["project"], labels["environment"] = v.org, v.project, v.env
		} else {
			delete(labels, "org")
			delete(labels, "project")
			delete(labels, "environment")
		}
		labels["node"] = nodeID
		for _, sm := range s.Samples {
			if sm.TS > maxTS {
				continue
			}
			promText(&sb, name, labels, sm)
		}
	}
	return sb.String(), dropped
}

// ---- logs ----

// BatchHeader is the header of a log batch: <epoch>.<n>.
const BatchHeader = "X-Polipan-Batch"

func parseBatch(h string) (epoch string, n int64, ok bool) {
	i := strings.LastIndex(h, ".")
	if i <= 0 || i == len(h)-1 {
		return "", 0, false
	}
	n, err := strconv.ParseInt(h[i+1:], 10, 64)
	if err != nil || n < 0 {
		return "", 0, false
	}
	return h[:i], n, true
}

func (in *Ingest) logs(w http.ResponseWriter, r *http.Request, nodeID string, sc *nodeScope) {
	if !in.Backends.LogsEnabled() {
		http.Error(w, "log storage is not set up", http.StatusServiceUnavailable)
		return
	}
	epoch, n, hasBatch := parseBatch(r.Header.Get(BatchHeader))
	lock := in.lockFor(nodeID)
	lock.Lock()
	defer lock.Unlock()
	if hasBatch {
		var lastEpoch string
		var last int64
		err := in.DB.QueryRow(r.Context(), `SELECT epoch, last_batch FROM telemetry_batches WHERE node_id = $1`, nodeID).Scan(&lastEpoch, &last)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if err == nil && lastEpoch == epoch && n <= last {
			w.WriteHeader(http.StatusNoContent) // the agent sent this batch before: it is stored already
			return
		}
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	out, dropped, err := in.cleanLogs(nodeID, sc, bytes.NewReader(raw))
	if err != nil {
		http.Error(w, "bad log batch", http.StatusBadRequest)
		return
	}
	if dropped > 0 {
		// The first lines of a new container can come before the scope cache knows its service. Look again before
		// the lines are given up: the agent counts a 204 as done and never sends them again.
		if fresh := in.fresh(r.Context(), nodeID, sc); fresh != nil {
			if out2, _, err := in.cleanLogs(nodeID, fresh, bytes.NewReader(raw)); err == nil {
				out = out2
			}
		}
	}
	if out.Len() > 0 {
		q := url.Values{}
		q.Set("_stream_fields", "org,project,environment,service,node")
		q.Set("_msg_field", "_msg")
		q.Set("_time_field", "_time")
		// The body is JSON lines, not a form: the content type must not be x-www-form-urlencoded.
		resp, err := in.Backends.do(r.Context(), "POST", in.Backends.LogsURL, "/insert/jsonline", q, "application/stream+json", bytes.NewReader(out.Bytes()))
		if err != nil {
			http.Error(w, "log storage is not reachable", http.StatusServiceUnavailable)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		if err := check(resp); err != nil {
			in.log().Warn("ingest: log store", "err", err)
			http.Error(w, "log storage refused the data", http.StatusServiceUnavailable)
			return
		}
	}
	if hasBatch {
		if _, err := in.DB.Exec(r.Context(), `INSERT INTO telemetry_batches (node_id, epoch, last_batch) VALUES ($1,$2,$3)
			ON CONFLICT (node_id) DO UPDATE SET epoch = EXCLUDED.epoch, last_batch = EXCLUDED.last_batch, updated_at = now()`, nodeID, epoch, n); err != nil {
			in.log().Warn("ingest: save batch", "node", nodeID, "err", err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// logKeys are the fields of a line that an agent may send. The server adds org, project, environment and node.
var logKeys = map[string]bool{"_time": true, "_msg": true, "service": true, "release": true, "container": true,
	"stream": true, "level": true, "replica": true}

func (in *Ingest) cleanLogs(nodeID string, sc *nodeScope, body io.Reader) (*bytes.Buffer, int, error) {
	out := &bytes.Buffer{}
	dropped := 0
	now := in.now()
	br := bufio.NewReaderSize(body, 256<<10)
	for {
		line, err := br.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 && len(line) <= maxLineBytes {
			var m map[string]any
			if json.Unmarshal(line, &m) != nil {
				return nil, 0, errors.New("a line is not JSON")
			}
			clean := map[string]any{}
			for k, v := range m {
				if logKeys[k] {
					clean[k] = v
				}
			}
			sid, _ := clean["service"].(string)
			v, ok := sc.svc[sid]
			if !ok {
				dropped++
			} else {
				clean["org"], clean["project"], clean["environment"], clean["node"] = v.org, v.project, v.env, nodeID
				if msg, _ := clean["_msg"].(string); len(msg) > maxMsgBytes {
					clean["_msg"] = msg[:maxMsgBytes] + " …[cut]"
				}
				if ts, _ := clean["_time"].(string); ts != "" {
					if t, perr := time.Parse(time.RFC3339Nano, ts); perr != nil || t.After(now.Add(maxFutureSkew)) {
						clean["_time"] = now.UTC().Format(time.RFC3339Nano)
					}
				}
				if rep, isNum := clean["replica"].(float64); isNum {
					clean["replica"] = strconv.Itoa(int(rep))
				}
				b, _ := json.Marshal(clean)
				out.Write(b)
				out.WriteByte('\n')
			}
		} else if len(line) > maxLineBytes {
			dropped++
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return out, dropped, nil
			}
			return nil, 0, fmt.Errorf("read: %w", err)
		}
	}
}
