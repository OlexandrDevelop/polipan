// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

func addFrame(buf *bytes.Buffer, stream byte, s string) {
	h := make([]byte, 8)
	h[0] = stream
	binary.BigEndian.PutUint32(h[4:], uint32(len(s)))
	buf.Write(h)
	buf.WriteString(s)
}

func TestReadLogStream(t *testing.T) {
	var b bytes.Buffer
	addFrame(&b, 1, "2025-01-02T03:04:05.000000001Z hello\n")
	addFrame(&b, 2, "2025-01-02T03:04:06.000000000Z part one ")
	addFrame(&b, 1, "2025-01-02T03:04:07.000000000Z out\n")
	addFrame(&b, 2, "2025-01-02T03:04:06.000000001Z part two\n")
	var got []LogLine
	err := ReadLogStream(&b, 10, func(l LogLine) error { got = append(got, l); return nil })
	if err != nil || len(got) != 3 {
		t.Fatalf("err=%v lines=%v", err, got)
	}
	if got[0].Text != "hello" || got[0].Stream != "stdout" || got[0].Time.Nanosecond() != 1 {
		t.Fatalf("line 0: %+v", got[0])
	}
	if got[1].Text != "out" {
		t.Fatalf("line 1: %+v", got[1])
	}
	if !got[2].Truncated || got[2].Text != "part one p" || got[2].Stream != "stderr" || !strings.HasSuffix(got[2].Time.String(), "06 +0000 UTC") {
		t.Fatalf("line 2: %+v", got[2])
	}
}
