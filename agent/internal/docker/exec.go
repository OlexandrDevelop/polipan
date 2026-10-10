// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Exec creates an exec in the container and attaches to it over a raw connection.
func (c *HTTPClient) Exec(ctx context.Context, idOrName string, o ExecOptions) (ExecConn, error) {
	body := map[string]any{"AttachStdin": true, "AttachStdout": true, "AttachStderr": true, "Tty": o.Tty, "Cmd": o.Cmd}
	resp, err := c.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(idOrName)+"/exec", nil, body, nil)
	if err != nil {
		return nil, err
	}
	var created struct {
		ID string `json:"Id"`
	}
	err = json.NewDecoder(resp.Body).Decode(&created)
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}

	// The start call takes over the connection, so it is sent on its own socket.
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", c.socket)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	raw, _ := json.Marshal(map[string]any{"Detach": false, "Tty": o.Tty})
	req, _ := http.NewRequest(http.MethodPost, apiPrefix+"/exec/"+url.PathEscape(created.ID)+"/start", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "tcp")
	if err := req.Write(conn); err != nil {
		_ = conn.Close()
		return nil, err
	}
	br := bufio.NewReader(conn)
	sr, err := http.ReadResponse(br, req) //nolint:bodyclose // the connection stays open for the exec stream
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if sr.StatusCode != http.StatusSwitchingProtocols && sr.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(sr.Body, 1<<12))
		_ = conn.Close()
		return nil, fmt.Errorf("docker: exec start: status %d: %s", sr.StatusCode, strings.TrimSpace(string(msg)))
	}
	x := &execConn{c: c, id: created.ID, conn: conn, br: br, tty: o.Tty, stderr: o.Stderr}
	if o.Rows > 0 && o.Cols > 0 {
		_ = x.Resize(ctx, o.Rows, o.Cols)
	}
	return x, nil
}

type execConn struct {
	c    *HTTPClient
	id   string
	conn net.Conn
	br   *bufio.Reader
	tty  bool
	left uint32 // bytes left in the current frame (no tty)
	// stderr, when set, receives the stderr frames. They are not returned by Read.
	stderr io.Writer
	stream byte // stream of the current frame
}

// Read returns output. Without a tty Docker sends frames with an 8 byte header.
func (x *execConn) Read(p []byte) (int, error) {
	if x.tty {
		return x.br.Read(p)
	}
	for {
		for x.left == 0 {
			var hdr [8]byte
			if _, err := io.ReadFull(x.br, hdr[:]); err != nil {
				if errors.Is(err, io.ErrUnexpectedEOF) {
					err = io.EOF
				}
				return 0, err
			}
			x.stream = hdr[0]
			x.left = binary.BigEndian.Uint32(hdr[4:])
		}
		if x.stderr != nil && x.stream == 2 {
			n, err := io.CopyN(x.stderr, x.br, int64(x.left))
			x.left -= uint32(n)
			if err != nil {
				if errors.Is(err, io.EOF) {
					err = io.EOF
				}
				return 0, err
			}
			continue
		}
		if uint32(len(p)) > x.left {
			p = p[:x.left]
		}
		n, err := x.br.Read(p)
		x.left -= uint32(n)
		return n, err
	}
}

func (x *execConn) Write(p []byte) (int, error) { return x.conn.Write(p) }

func (x *execConn) CloseWrite() error {
	if cw, ok := x.conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return x.conn.Close()
}

func (x *execConn) Resize(ctx context.Context, rows, cols uint32) error {
	q := url.Values{"h": {strconv.Itoa(int(rows))}, "w": {strconv.Itoa(int(cols))}}
	return x.c.post(ctx, "/exec/"+url.PathEscape(x.id)+"/resize", q, nil)
}

// Wait asks Docker for the exit code. The process can need a moment to end after the output closed.
func (x *execConn) Wait(ctx context.Context) (int, error) {
	for i := 0; ; i++ {
		var r struct {
			Running  bool `json:"Running"`
			ExitCode int  `json:"ExitCode"`
		}
		if err := x.c.getJSON(ctx, "/exec/"+url.PathEscape(x.id)+"/json", nil, &r); err != nil {
			return -1, err
		}
		if !r.Running || i >= 50 {
			return r.ExitCode, nil
		}
		select {
		case <-ctx.Done():
			return -1, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (x *execConn) Close() error { return x.conn.Close() }
