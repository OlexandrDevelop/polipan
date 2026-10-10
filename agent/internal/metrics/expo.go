// SPDX-License-Identifier: Apache-2.0

// Package metrics serves the node, container, volume, health, agent and ingress metrics as Prometheus text
// on a local address. The names and labels are fixed by 12-observability, section 2 (REQ-OBS-100).
package metrics

import (
	"bytes"
	"math"
	"sort"
	"strconv"
	"strings"
)

type sample struct {
	suffix string // _bucket, _sum, _count or empty
	labels string // already formatted: {a="b",c="d"}
	value  float64
}

type family struct {
	name, help, typ string
	samples         []sample
}

// exposition builds the text format. A family is written once, with one HELP and one TYPE line.
type exposition struct {
	fams []*family
	idx  map[string]*family
}

func newExposition() *exposition { return &exposition{idx: map[string]*family{}} }

func (e *exposition) fam(name, help, typ string) *family {
	f := e.idx[name]
	if f == nil {
		f = &family{name: name, help: help, typ: typ}
		e.idx[name] = f
		e.fams = append(e.fams, f)
	}
	return f
}

// add adds one sample. kv are label name and value pairs, in the order they are written.
func (e *exposition) add(name, help, typ string, v float64, kv ...string) {
	f := e.fam(name, help, typ)
	f.samples = append(f.samples, sample{labels: formatLabels(kv), value: v})
}

// addHistogram adds the bucket, sum and count samples of one histogram series.
func (e *exposition) addHistogram(name, help string, bounds []float64, counts []uint64, sum float64, count uint64, kv ...string) {
	f := e.fam(name, help, "histogram")
	var cum uint64
	for i, b := range bounds {
		cum += counts[i]
		f.samples = append(f.samples, sample{suffix: "_bucket", labels: formatLabels(append(kv[:len(kv):len(kv)], "le", formatFloat(b))), value: float64(cum)})
	}
	f.samples = append(f.samples,
		sample{suffix: "_bucket", labels: formatLabels(append(kv[:len(kv):len(kv)], "le", "+Inf")), value: float64(count)},
		sample{suffix: "_sum", labels: formatLabels(kv), value: sum},
		sample{suffix: "_count", labels: formatLabels(kv), value: float64(count)})
}

func (e *exposition) bytes() []byte {
	var b bytes.Buffer
	for _, f := range e.fams {
		b.WriteString("# HELP " + f.name + " " + f.help + "\n")
		b.WriteString("# TYPE " + f.name + " " + f.typ + "\n")
		// Sorted, so the output does not depend on map order.
		ss := append([]sample(nil), f.samples...)
		if f.typ != "histogram" {
			sort.SliceStable(ss, func(i, j int) bool { return ss[i].labels < ss[j].labels })
		}
		for _, s := range ss {
			b.WriteString(f.name + s.suffix + s.labels + " " + formatFloat(s.value) + "\n")
		}
	}
	return b.Bytes()
}

func formatLabels(kv []string) string {
	if len(kv) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteByte('{')
	for i := 0; i+1 < len(kv); i += 2 {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(kv[i] + `="` + escapeLabel(kv[i+1]) + `"`)
	}
	b.WriteByte('}')
	return b.String()
}

var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

func escapeLabel(s string) string { return labelEscaper.Replace(s) }

func formatFloat(v float64) string {
	switch {
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsNaN(v):
		return "NaN"
	}
	if math.Abs(v) < 1e21 {
		return strconv.FormatFloat(v, 'f', -1, 64) // no exponent: 5120000, not 5.12e+06
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}
