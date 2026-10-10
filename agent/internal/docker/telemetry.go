// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Stats is one sample of the resource use of a container. It is read from the Docker stats API.
type Stats struct {
	Time         time.Time // when Docker took the sample
	CPUTotalNs   uint64    // total CPU time of the container, in nanoseconds
	MemoryBytes  uint64    // memory in use, without the page cache
	MemoryLimit  uint64
	NetRxBytes   uint64
	NetTxBytes   uint64
	BlkioBytes   uint64 // bytes read and written
	PreTime      time.Time
	PreCPUTotal  uint64 // the CPU time of the sample before (Docker keeps it for stream=false)
	OnlineCPUs   int
	SystemCPUNs  uint64
	PreSystemCPU uint64
}

// Stats reads one sample for the container.
func (c *HTTPClient) Stats(ctx context.Context, idOrName string) (*Stats, error) {
	var r struct {
		Read     string `json:"read"`
		PreRead  string `json:"preread"`
		CPUStats struct {
			CPUUsage struct {
				Total uint64 `json:"total_usage"`
			} `json:"cpu_usage"`
			System uint64 `json:"system_cpu_usage"`
			Online int    `json:"online_cpus"`
		} `json:"cpu_stats"`
		PreCPUStats struct {
			CPUUsage struct {
				Total uint64 `json:"total_usage"`
			} `json:"cpu_usage"`
			System uint64 `json:"system_cpu_usage"`
		} `json:"precpu_stats"`
		MemoryStats struct {
			Usage uint64            `json:"usage"`
			Limit uint64            `json:"limit"`
			Stats map[string]uint64 `json:"stats"`
		} `json:"memory_stats"`
		Networks map[string]struct {
			Rx uint64 `json:"rx_bytes"`
			Tx uint64 `json:"tx_bytes"`
		} `json:"networks"`
		Blkio struct {
			Bytes []struct {
				Op    string `json:"op"`
				Value uint64 `json:"value"`
			} `json:"io_service_bytes_recursive"`
		} `json:"blkio_stats"`
	}
	q := url.Values{"stream": {"false"}, "one-shot": {"true"}}
	if err := c.getJSON(ctx, "/containers/"+url.PathEscape(idOrName)+"/stats", q, &r); err != nil {
		return nil, err
	}
	s := &Stats{CPUTotalNs: r.CPUStats.CPUUsage.Total, MemoryLimit: r.MemoryStats.Limit, OnlineCPUs: r.CPUStats.Online,
		PreCPUTotal: r.PreCPUStats.CPUUsage.Total, SystemCPUNs: r.CPUStats.System, PreSystemCPU: r.PreCPUStats.System}
	s.Time, _ = time.Parse(time.RFC3339Nano, r.Read)
	s.PreTime, _ = time.Parse(time.RFC3339Nano, r.PreRead)
	// Like the docker CLI: the page cache is not counted as used memory.
	cache := r.MemoryStats.Stats["inactive_file"] // cgroup v2
	if v, ok := r.MemoryStats.Stats["total_inactive_file"]; ok {
		cache = v // cgroup v1
	}
	if r.MemoryStats.Usage > cache {
		s.MemoryBytes = r.MemoryStats.Usage - cache
	} else {
		s.MemoryBytes = r.MemoryStats.Usage
	}
	for _, n := range r.Networks {
		s.NetRxBytes += n.Rx
		s.NetTxBytes += n.Tx
	}
	for _, b := range r.Blkio.Bytes {
		if op := strings.ToLower(b.Op); op == "read" || op == "write" {
			s.BlkioBytes += b.Value
		}
	}
	return s, nil
}

// LogLine is one line of a container log.
type LogLine struct {
	Stream    string // stdout or stderr
	Time      time.Time
	Text      string // without the end of line
	Truncated bool   // the line was longer than LogOptions.MaxLine
}

// LogOptions selects what FollowLogs reads.
type LogOptions struct {
	// Since is the first time to read (inclusive). Zero = from the start.
	Since time.Time
	// Follow keeps the stream open for new lines.
	Follow bool
	// MaxLine is how many bytes of one line are kept. 0 = 1 MiB.
	MaxLine int
}

// FollowLogs calls fn for every line of the log. It returns when the stream ends (no Follow), the container
// stops (with Follow), fn returns an error, or ctx ends.
func (c *HTTPClient) FollowLogs(ctx context.Context, idOrName string, o LogOptions, fn func(LogLine) error) error {
	q := url.Values{"stdout": {"1"}, "stderr": {"1"}, "timestamps": {"1"}}
	if o.Follow {
		q.Set("follow", "1")
	}
	if !o.Since.IsZero() {
		q.Set("since", fmt.Sprintf("%d.%09d", o.Since.Unix(), o.Since.Nanosecond()))
	}
	resp, err := c.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(idOrName)+"/logs", q, nil, nil)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	return ReadLogStream(resp.Body, o.MaxLine, fn)
}

type partialLine struct {
	ts        time.Time
	buf       []byte
	truncated bool
	open      bool
}

// ReadLogStream reads the Docker multiplexed log stream (with timestamps) and calls fn for each line.
// A long line comes in several frames: they are joined, and only the first MaxLine bytes are kept.
func ReadLogStream(r io.Reader, maxLine int, fn func(LogLine) error) error {
	if maxLine <= 0 {
		maxLine = 1 << 20
	}
	br := bufio.NewReaderSize(r, 64<<10)
	parts := map[byte]*partialLine{1: {}, 2: {}}
	hdr := make([]byte, 8)
	for {
		if _, err := io.ReadFull(br, hdr); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return nil
			}
			return err
		}
		n := binary.BigEndian.Uint32(hdr[4:])
		payload := make([]byte, n)
		if _, err := io.ReadFull(br, payload); err != nil {
			return nil
		}
		st := hdr[0]
		p := parts[st]
		if p == nil {
			continue // stdin or unknown
		}
		// Every frame starts with its own timestamp and a space.
		ts := time.Time{}
		if i := bytes.IndexByte(payload, ' '); i > 0 {
			ts, _ = time.Parse(time.RFC3339Nano, string(payload[:i]))
			payload = payload[i+1:]
		}
		for len(payload) > 0 {
			if !p.open {
				p.open, p.ts = true, ts
			}
			chunk, end := payload, false
			if i := bytes.IndexByte(payload, '\n'); i >= 0 {
				chunk, payload, end = payload[:i], payload[i+1:], true
			} else {
				payload = nil
			}
			if room := maxLine - len(p.buf); len(chunk) > room {
				chunk, p.truncated = chunk[:max(room, 0)], true
			}
			p.buf = append(p.buf, chunk...)
			if !end {
				continue
			}
			name := "stdout"
			if st == 2 {
				name = "stderr"
			}
			text := strings.TrimSuffix(string(p.buf), "\r")
			l := LogLine{Stream: name, Time: p.ts, Text: text, Truncated: p.truncated}
			*p = partialLine{buf: p.buf[:0]}
			if err := fn(l); err != nil {
				return err
			}
		}
	}
}
