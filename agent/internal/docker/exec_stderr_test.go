// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"io"
	"testing"
)

func muxFrame(stream byte, text string) []byte {
	hdr := make([]byte, 8)
	hdr[0] = stream
	binary.BigEndian.PutUint32(hdr[4:], uint32(len(text)))
	return append(hdr, text...)
}

// The backup code reads the dump from stdout and keeps stderr apart (F10): a dump must not hold error text.
func TestExecConnSplitsStderr(t *testing.T) {
	var wire bytes.Buffer
	wire.Write(muxFrame(1, "dump part one "))
	wire.Write(muxFrame(2, "pg_dump: warning"))
	wire.Write(muxFrame(1, "part two"))
	var errs bytes.Buffer
	x := &execConn{br: bufio.NewReader(&wire), stderr: &errs}
	out, err := io.ReadAll(x)
	if err != nil || string(out) != "dump part one part two" || errs.String() != "pg_dump: warning" {
		t.Fatalf("out %q err %v stderr %q", out, err, errs.String())
	}
	// Without a stderr writer both streams come together, like before.
	var wire2 bytes.Buffer
	wire2.Write(muxFrame(1, "a"))
	wire2.Write(muxFrame(2, "b"))
	y := &execConn{br: bufio.NewReader(&wire2)}
	if out, _ := io.ReadAll(y); string(out) != "ab" {
		t.Fatalf("combined %q", out)
	}
}
