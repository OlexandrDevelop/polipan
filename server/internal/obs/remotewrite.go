// SPDX-License-Identifier: Apache-2.0

package obs

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/golang/snappy"
	"google.golang.org/protobuf/encoding/protowire"
)

// Sample and series of a Prometheus remote write request (prompb.WriteRequest). Only what Polipan needs is decoded.
type rwSample struct {
	Value float64
	TS    int64 // milliseconds
}

type rwSeries struct {
	Labels  map[string]string
	Samples []rwSample
}

const maxSeriesPerRequest = 50000

// decodeRemoteWrite reads a snappy compressed remote write body.
func decodeRemoteWrite(body []byte) ([]rwSeries, error) {
	raw, err := snappy.Decode(nil, body)
	if err != nil {
		return nil, fmt.Errorf("remote write: %w", err)
	}
	var out []rwSeries
	for len(raw) > 0 {
		num, typ, n := protowire.ConsumeTag(raw)
		if n < 0 {
			return nil, protowire.ParseError(n)
		}
		raw = raw[n:]
		if num == 1 && typ == protowire.BytesType {
			b, n := protowire.ConsumeBytes(raw)
			if n < 0 {
				return nil, protowire.ParseError(n)
			}
			raw = raw[n:]
			ts, err := decodeSeries(b)
			if err != nil {
				return nil, err
			}
			out = append(out, ts)
			if len(out) > maxSeriesPerRequest {
				return nil, errors.New("remote write: too many series in one request")
			}
			continue
		}
		n = protowire.ConsumeFieldValue(num, typ, raw)
		if n < 0 {
			return nil, protowire.ParseError(n)
		}
		raw = raw[n:]
	}
	return out, nil
}

func decodeSeries(b []byte) (rwSeries, error) {
	s := rwSeries{Labels: map[string]string{}}
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return s, protowire.ParseError(n)
		}
		b = b[n:]
		switch {
		case num == 1 && typ == protowire.BytesType:
			v, n := protowire.ConsumeBytes(b)
			if n < 0 {
				return s, protowire.ParseError(n)
			}
			b = b[n:]
			name, value, err := decodeLabel(v)
			if err != nil {
				return s, err
			}
			s.Labels[name] = value
		case num == 2 && typ == protowire.BytesType:
			v, n := protowire.ConsumeBytes(b)
			if n < 0 {
				return s, protowire.ParseError(n)
			}
			b = b[n:]
			sm, err := decodeSample(v)
			if err != nil {
				return s, err
			}
			s.Samples = append(s.Samples, sm)
		default:
			n = protowire.ConsumeFieldValue(num, typ, b)
			if n < 0 {
				return s, protowire.ParseError(n)
			}
			b = b[n:]
		}
	}
	return s, nil
}

func decodeLabel(b []byte) (name, value string, err error) {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return "", "", protowire.ParseError(n)
		}
		b = b[n:]
		if typ == protowire.BytesType && (num == 1 || num == 2) {
			v, n := protowire.ConsumeBytes(b)
			if n < 0 {
				return "", "", protowire.ParseError(n)
			}
			b = b[n:]
			if num == 1 {
				name = string(v)
			} else {
				value = string(v)
			}
			continue
		}
		n = protowire.ConsumeFieldValue(num, typ, b)
		if n < 0 {
			return "", "", protowire.ParseError(n)
		}
		b = b[n:]
	}
	return name, value, nil
}

func decodeSample(b []byte) (rwSample, error) {
	var s rwSample
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return s, protowire.ParseError(n)
		}
		b = b[n:]
		switch {
		case num == 1 && typ == protowire.Fixed64Type:
			v, n := protowire.ConsumeFixed64(b)
			if n < 0 {
				return s, protowire.ParseError(n)
			}
			b = b[n:]
			s.Value = math.Float64frombits(v)
		case num == 2 && typ == protowire.VarintType:
			v, n := protowire.ConsumeVarint(b)
			if n < 0 {
				return s, protowire.ParseError(n)
			}
			b = b[n:]
			s.TS = int64(v)
		default:
			n = protowire.ConsumeFieldValue(num, typ, b)
			if n < 0 {
				return s, protowire.ParseError(n)
			}
			b = b[n:]
		}
	}
	return s, nil
}

// encodeRemoteWrite makes a snappy compressed remote write body. The agent side does not need it: tests do.
func encodeRemoteWrite(series []rwSeries) []byte {
	var raw []byte
	for _, s := range series {
		var ts []byte
		names := make([]string, 0, len(s.Labels))
		for k := range s.Labels {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			var l []byte
			l = protowire.AppendTag(l, 1, protowire.BytesType)
			l = protowire.AppendString(l, k)
			l = protowire.AppendTag(l, 2, protowire.BytesType)
			l = protowire.AppendString(l, s.Labels[k])
			ts = protowire.AppendTag(ts, 1, protowire.BytesType)
			ts = protowire.AppendBytes(ts, l)
		}
		for _, sm := range s.Samples {
			var b []byte
			b = protowire.AppendTag(b, 1, protowire.Fixed64Type)
			b = protowire.AppendFixed64(b, math.Float64bits(sm.Value))
			b = protowire.AppendTag(b, 2, protowire.VarintType)
			b = protowire.AppendVarint(b, uint64(sm.TS))
			ts = protowire.AppendTag(ts, 2, protowire.BytesType)
			ts = protowire.AppendBytes(ts, b)
		}
		raw = protowire.AppendTag(raw, 1, protowire.BytesType)
		raw = protowire.AppendBytes(raw, ts)
	}
	return snappy.Encode(nil, raw)
}

// promText writes a series in the text format of the Prometheus import of VictoriaMetrics.
func promText(sb *strings.Builder, name string, labels map[string]string, s rwSample) {
	sb.WriteString(name)
	if len(labels) > 0 {
		keys := make([]string, 0, len(labels))
		for k := range labels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		sb.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString(k)
			sb.WriteString(`="`)
			sb.WriteString(escapeLabel(labels[k]))
			sb.WriteByte('"')
		}
		sb.WriteByte('}')
	}
	sb.WriteByte(' ')
	sb.WriteString(strconv.FormatFloat(s.Value, 'g', -1, 64))
	sb.WriteByte(' ')
	sb.WriteString(strconv.FormatInt(s.TS, 10))
	sb.WriteByte('\n')
}

func escapeLabel(v string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return r.Replace(v)
}
