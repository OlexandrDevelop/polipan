// SPDX-License-Identifier: Apache-2.0

package obs

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// LogLine is one stored log line.
type LogLine struct {
	Time          time.Time
	Message       string
	Level         string
	Stream        string
	Replica       *int
	ProjectID     string
	EnvironmentID string
	ServiceID     string
	ReleaseID     string
	NodeID        string
	Container     string
	// Match is false for a context line around a match.
	Match bool
}

// LogFilter is a log search. Users cannot send raw LogsQL: text and fields only (REQ-OBS-101).
type LogFilter struct {
	OrgID         string
	ProjectID     string
	EnvironmentID string
	ServiceID     string
	ReleaseID     string
	NodeID        string
	Level         string
	Stream        string
	// Text is free text: words, a phrase in double quotes, or /regex/.
	Text string
	From time.Time
	To   time.Time
	// Limit is the page size, Cursor the cursor of the last page, Context the lines before and after a match.
	Limit   int
	Cursor  string
	Context int
	// Retention cuts the start of the range (REQ-OBS-012). Zero means no cut.
	Retention time.Duration
}

var levelRe = regexp.MustCompile(`^(debug|info|warn|error|unknown)$`)

// quote makes a LogsQL string literal. The text of a user is only ever put inside one.
func quote(s string) string { return strconv.Quote(s) }

// textFilter turns the free text of a user into LogsQL filters (words, phrases, regex).
func textFilter(text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", nil
	}
	if len(text) > 500 {
		return "", fmt.Errorf("%w: the search text is too long", ErrBadQuery)
	}
	if len(text) >= 2 && strings.HasPrefix(text, "/") && strings.HasSuffix(text, "/") {
		re := text[1 : len(text)-1]
		if _, err := regexp.Compile(re); err != nil {
			return "", fmt.Errorf("%w: bad regex: %v", ErrBadQuery, err)
		}
		return "~" + quote(re), nil
	}
	var parts []string
	for _, tok := range splitTerms(text) {
		parts = append(parts, quote(tok))
	}
	return strings.Join(parts, " "), nil
}

// splitTerms splits on spaces. A part in double quotes is one phrase.
func splitTerms(s string) []string {
	var out []string
	var cur strings.Builder
	inQuote := false
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
			if !inQuote {
				flush()
			}
		case (r == ' ' || r == '\t') && !inQuote:
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

// streamAndFields is the part of the query without the text.
func (f LogFilter) streamAndFields() (string, error) {
	if f.OrgID == "" {
		return "", fmt.Errorf("%w: a log query needs an org", ErrBadQuery)
	}
	var sel []string
	for _, kv := range [][2]string{{"org", f.OrgID}, {"project", f.ProjectID}, {"environment", f.EnvironmentID}, {"service", f.ServiceID}, {"node", f.NodeID}} {
		if kv[1] == "" {
			continue
		}
		if !idRe.MatchString(kv[1]) {
			return "", fmt.Errorf("%w: bad %s id", ErrBadQuery, kv[0])
		}
		sel = append(sel, kv[0]+"="+quote(kv[1]))
	}
	q := "{" + strings.Join(sel, ",") + "}"
	if f.ReleaseID != "" {
		if !idRe.MatchString(f.ReleaseID) {
			return "", fmt.Errorf("%w: bad release id", ErrBadQuery)
		}
		q += " release:=" + quote(f.ReleaseID)
	}
	if f.Level != "" {
		if !levelRe.MatchString(f.Level) {
			return "", fmt.Errorf("%w: bad level", ErrBadQuery)
		}
		q += " level:=" + quote(f.Level)
	}
	if f.Stream != "" {
		if f.Stream != "stdout" && f.Stream != "stderr" {
			return "", fmt.Errorf("%w: bad stream", ErrBadQuery)
		}
		q += " stream:=" + quote(f.Stream)
	}
	return q, nil
}

// Query builds the LogsQL text of a search, newest first.
func (f LogFilter) Query() (string, error) {
	q, err := f.streamAndFields()
	if err != nil {
		return "", err
	}
	t, err := textFilter(f.Text)
	if err != nil {
		return "", err
	}
	if t != "" {
		q += " " + t
	}
	return q, nil
}

func parseLogLine(raw []byte) (LogLine, bool) {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return LogLine{}, false
	}
	str := func(k string) string { s, _ := m[k].(string); return s }
	ts, err := time.Parse(time.RFC3339Nano, str("_time"))
	if err != nil {
		return LogLine{}, false
	}
	l := LogLine{Time: ts.UTC(), Message: str("_msg"), Level: str("level"), Stream: str("stream"), ProjectID: str("project"),
		EnvironmentID: str("environment"), ServiceID: str("service"), ReleaseID: str("release"), NodeID: str("node"),
		Container: str("container"), Match: true}
	if r := str("replica"); r != "" {
		if n, err := strconv.Atoi(r); err == nil {
			l.Replica = &n
		}
	}
	return l, true
}

// runQuery runs a LogsQL query and reads the lines.
func (b *Backends) runQuery(ctx context.Context, query string, start, end time.Time, limit int) ([]LogLine, error) {
	if !b.LogsEnabled() {
		return nil, ErrUnavailable
	}
	form := url.Values{}
	form.Set("query", query)
	form.Set("limit", strconv.Itoa(limit))
	if !start.IsZero() {
		form.Set("start", start.UTC().Format(time.RFC3339Nano))
	}
	if !end.IsZero() {
		form.Set("end", end.UTC().Format(time.RFC3339Nano))
	}
	resp, err := b.do(ctx, "POST", b.LogsURL, "/select/logsql/query", nil, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := check(resp); err != nil {
		return nil, err
	}
	var out []LogLine
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		if l, ok := parseLogLine(sc.Bytes()); ok {
			out = append(out, l)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return out, nil
}

// LogPage is a page of a search.
type LogPage struct {
	Items      []LogLine
	NextCursor string
}

// rangeOf cuts the range of a filter to the retention of the org.
func (f LogFilter) rangeOf(now time.Time) (start, end time.Time) {
	start, end = f.From, f.To
	if f.Retention > 0 {
		if min := now.Add(-f.Retention); start.IsZero() || start.Before(min) {
			start = min
		}
	}
	if f.Cursor != "" {
		if ns, err := strconv.ParseInt(f.Cursor, 10, 64); err == nil {
			c := time.Unix(0, ns-1).UTC()
			if end.IsZero() || c.Before(end) {
				end = c
			}
		}
	}
	return start, end
}

// Search finds log lines, newest first, with a cursor for the next page. Lines older than the retention are not
// returned (AC-OBS-07).
func (b *Backends) Search(ctx context.Context, now time.Time, f LogFilter) (*LogPage, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	if f.Context < 0 {
		f.Context = 0
	}
	if f.Context > 20 {
		f.Context = 20
	}
	q, err := f.Query()
	if err != nil {
		return nil, err
	}
	start, end := f.rangeOf(now)
	lines, err := b.runQuery(ctx, q+" | sort by (_time desc) | limit "+strconv.Itoa(f.Limit), start, end, f.Limit)
	if err != nil {
		return nil, err
	}
	page := &LogPage{Items: lines}
	if len(lines) == f.Limit && len(lines) > 0 {
		page.NextCursor = strconv.FormatInt(lines[len(lines)-1].Time.UnixNano(), 10)
	}
	if f.Context > 0 && strings.TrimSpace(f.Text) != "" && len(lines) > 0 {
		if page.Items, err = b.withContext(ctx, f, now, lines); err != nil {
			return nil, err
		}
	}
	return page, nil
}

// withContext adds the lines before and after each match, from the same container.
func (b *Backends) withContext(ctx context.Context, f LogFilter, now time.Time, matches []LogLine) ([]LogLine, error) {
	base, err := f.streamAndFields()
	if err != nil {
		return nil, err
	}
	start, _ := f.rangeOf(now)
	type key struct {
		t         int64
		container string
		msg       string
	}
	seen := map[key]bool{}
	all := make([]LogLine, 0, len(matches)*(2*f.Context+1))
	add := func(l LogLine) {
		k := key{l.Time.UnixNano(), l.Container, l.Message}
		if !seen[k] {
			seen[k] = true
			all = append(all, l)
		}
	}
	for _, m := range matches {
		add(m)
	}
	n := strconv.Itoa(f.Context)
	for _, m := range matches {
		same := base
		if m.Container != "" {
			same += " container:=" + quote(m.Container)
		}
		before, err := b.runQuery(ctx, same+" | sort by (_time desc) | limit "+n, start, m.Time.Add(-time.Nanosecond), f.Context)
		if err != nil {
			return nil, err
		}
		after, err := b.runQuery(ctx, same+" | sort by (_time) | limit "+n, m.Time.Add(time.Nanosecond), time.Time{}, f.Context)
		if err != nil {
			return nil, err
		}
		for _, l := range append(before, after...) {
			l.Match = false
			add(l)
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Time.After(all[j].Time) })
	return all, nil
}

// Tail follows new lines of a filter until ctx ends. Lines come once, in time order. Duplicates of the short
// overlap with the start are dropped.
func (b *Backends) Tail(ctx context.Context, f LogFilter, fn func(LogLine) error) error {
	if !b.LogsEnabled() {
		return ErrUnavailable
	}
	q, err := f.Query()
	if err != nil {
		return err
	}
	form := url.Values{}
	form.Set("query", q)
	form.Set("start_offset", "3s")
	resp, err := b.do(ctx, "GET", b.LogsURL, "/select/logsql/tail", form, "", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := check(resp); err != nil {
		return err
	}
	seen := map[string]time.Time{}
	var last time.Time
	br := bufio.NewReaderSize(resp.Body, 64<<10)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			if l, ok := parseLogLine(line); ok {
				k := l.Time.Format(time.RFC3339Nano) + "|" + l.Container + "|" + l.Message
				if _, dup := seen[k]; !dup {
					seen[k] = l.Time
					if l.Time.After(last) {
						last = l.Time
					}
					for sk, st := range seen { // keep the memory small: forget lines older than 10 s
						if last.Sub(st) > 10*time.Second {
							delete(seen, sk)
						}
					}
					if ferr := fn(l); ferr != nil {
						return ferr
					}
				}
			}
		}
		if err != nil {
			if ctx.Err() != nil || err == io.EOF {
				return ctx.Err()
			}
			return fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
	}
}

// Recent returns the last n lines of a filter, oldest first, for the start of a live tail.
func (b *Backends) Recent(ctx context.Context, now time.Time, f LogFilter, n int) ([]LogLine, error) {
	f.Limit, f.Cursor, f.Context = n, "", 0
	page, err := b.Search(ctx, now, f)
	if err != nil {
		return nil, err
	}
	out := page.Items
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}
