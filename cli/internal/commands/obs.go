// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"fmt"
	"net/url"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/polipan/polipan/cli/internal/apiops"
)

func (a *app) obsCommands() []*cobra.Command {
	return []*cobra.Command{a.logsCmd(), a.metricsCmd(), a.eventsCmd()}
}

func (a *app) printLogLine(v any) {
	m, _ := v.(map[string]any)
	_, _ = fmt.Fprintf(a.out(), "%s %s %s\n", str(m, "time"), strings.ToUpper(str(m, "level")), str(m, "message"))
}

func (a *app) logsCmd() *cobra.Command {
	var follow bool
	var since string
	var limit int
	c := a.cmd("logs <service>", "Show the logs of a service (-f to follow)", "getServiceLogs,streamServiceLogs", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		id, err := a.serviceID(args[0])
		if err != nil {
			return err
		}
		if follow {
			resp, err := a.stream("streamServiceLogs", ids(id))
			if err != nil {
				return err
			}
			defer func() { _ = resp.Body.Close() }()
			f, _ := a.format()
			return readSSE(resp.Body, func(data string) {
				if f != "table" {
					_, _ = fmt.Fprintln(a.out(), data)
					return
				}
				v, err := decodeJSON([]byte(data))
				if err != nil {
					_, _ = fmt.Fprintln(a.out(), data)
					return
				}
				a.printLogLine(v)
			})
		}
		q := url.Values{}
		if since != "" {
			s, err := sinceTime(since)
			if err != nil {
				return err
			}
			q.Set("from", s)
		}
		if limit > 0 {
			q.Set("limit", itoa(limit))
		}
		items, err := a.list("getServiceLogs", apiops.Input{Path: []string{id}, Query: q})
		if err != nil {
			return err
		}
		return a.printLogs(items)
	})
	c.Flags().BoolVarP(&follow, "follow", "f", false, "keep the stream open")
	c.Flags().StringVar(&since, "since", "", "only lines newer than this, for example 1h")
	c.Flags().IntVar(&limit, "limit", 0, "number of lines")

	var service, level, sSince string
	var sLimit int
	search := a.listCmd("search <text>", "Search logs of many services", "searchLogs", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		q := url.Values{"q": {args[0]}}
		e, err := a.effective()
		if err != nil {
			return err
		}
		if e.Project != "" {
			pid, err := a.projectID("")
			if err != nil {
				return err
			}
			q.Set("project_id", pid)
			if e.Env != "" {
				eid, err := a.envID("")
				if err != nil {
					return err
				}
				q.Set("environment_id", eid)
			}
		}
		if service != "" {
			sid, err := a.serviceID(service)
			if err != nil {
				return err
			}
			q.Set("service_id", sid)
		}
		if level != "" {
			q.Set("level", level)
		}
		if sSince != "" {
			s, err := sinceTime(sSince)
			if err != nil {
				return err
			}
			q.Set("from", s)
		}
		if sLimit > 0 {
			q.Set("limit", itoa(sLimit))
		}
		items, err := a.list("searchLogs", apiops.Input{Query: q})
		if err != nil {
			return err
		}
		return a.printLogs(items)
	})
	search.Flags().StringVar(&service, "service", "", "limit to a service")
	search.Flags().StringVar(&level, "level", "", "debug, info, warn or error")
	search.Flags().StringVar(&sSince, "since", "24h", "only lines newer than this")
	search.Flags().IntVar(&sLimit, "limit", 0, "number of lines")
	c.AddCommand(search)
	return c
}

func (a *app) printLogs(items []any) error {
	if f, _ := a.format(); f == "table" {
		for _, it := range items {
			a.printLogLine(it)
		}
		return nil
	}
	return a.emit(items)
}

func (a *app) metricsCmd() *cobra.Command {
	var node, metrics, since, step string
	c := a.cmd("metrics [service] [--node N]", "Show metrics of a service or a node", "getServiceMetrics,getNodeMetrics", rangeArgs(0, 1), func(cmd *cobra.Command, args []string) error {
		q := url.Values{"metrics": strings.Split(metrics, ",")}
		if since != "" {
			s, err := sinceTime(since)
			if err != nil {
				return err
			}
			q.Set("from", s)
		}
		if step != "" {
			q.Set("step", step)
		}
		var r *Response
		var err error
		switch {
		case node != "" && len(args) == 0:
			id, nerr := a.nodeID(node)
			if nerr != nil {
				return nerr
			}
			r, err = a.call("getNodeMetrics", apiops.Input{Path: []string{id}, Query: q})
		case node == "" && len(args) == 1:
			id, serr := a.serviceID(args[0])
			if serr != nil {
				return serr
			}
			r, err = a.call("getServiceMetrics", apiops.Input{Path: []string{id}, Query: q})
		default:
			return usageErr("give one service or --node")
		}
		if err != nil {
			return err
		}
		v, err := decodeJSON(r.Body)
		if err != nil {
			return err
		}
		if f, _ := a.format(); f != "table" {
			return a.emit(v)
		}
		m, _ := v.(map[string]any)
		series, _ := m["series"].([]any)
		tw := tabwriter.NewWriter(a.out(), 0, 8, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "METRIC\tLAST\tMIN\tMAX\tAVG\tTREND")
		for _, s := range series {
			sm, _ := s.(map[string]any)
			vals := pointValues(sm["points"])
			if len(vals) == 0 {
				_, _ = fmt.Fprintf(tw, "%s\t-\t-\t-\t-\t\n", str(sm, "metric"))
				continue
			}
			lo, hi, sum := vals[0], vals[0], 0.0
			for _, x := range vals {
				if x < lo {
					lo = x
				}
				if x > hi {
					hi = x
				}
				sum += x
			}
			_, _ = fmt.Fprintf(tw, "%s\t%.4g\t%.4g\t%.4g\t%.4g\t%s\n", str(sm, "metric"), vals[len(vals)-1], lo, hi, sum/float64(len(vals)), sparkline(vals))
		}
		return tw.Flush()
	})
	c.Flags().StringVar(&node, "node", "", "show a node instead of a service")
	c.Flags().StringVar(&metrics, "metric", "cpu,memory", "metrics, comma separated")
	c.Flags().StringVar(&since, "since", "1h", "time range")
	c.Flags().StringVar(&step, "step", "", "step, for example 60s")
	return c
}

func pointValues(p any) []float64 {
	pts, _ := p.([]any)
	var out []float64
	for _, pt := range pts {
		pair, _ := pt.([]any)
		if len(pair) != 2 {
			continue
		}
		if n, ok := pair[1].(interface{ Float64() (float64, error) }); ok {
			f, _ := n.Float64()
			out = append(out, f)
		}
	}
	return out
}

func sparkline(vals []float64) string {
	const bars = "▁▂▃▄▅▆▇█"
	runes := []rune(bars)
	if len(vals) > 40 {
		step := float64(len(vals)) / 40
		var s []float64
		for i := 0; i < 40; i++ {
			s = append(s, vals[int(float64(i)*step)])
		}
		vals = s
	}
	lo, hi := vals[0], vals[0]
	for _, v := range vals {
		if v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
	}
	var b strings.Builder
	for _, v := range vals {
		i := 0
		if hi > lo {
			i = int((v - lo) / (hi - lo) * float64(len(runes)-1))
		}
		b.WriteRune(runes[i])
	}
	return b.String()
}

func (a *app) eventsCmd() *cobra.Command {
	var topics []string
	c := a.cmd("events", "Print platform events as they happen", "streamEvents", noArgs, func(cmd *cobra.Command, _ []string) error {
		q := url.Values{}
		if len(topics) > 0 {
			q["topics"] = topics
		}
		resp, err := a.stream("streamEvents", apiops.Input{Query: q})
		if err != nil {
			return err
		}
		defer func() { _ = resp.Body.Close() }()
		return readSSE(resp.Body, func(data string) { _, _ = fmt.Fprintln(a.out(), data) })
	})
	c.Flags().StringSliceVar(&topics, "topics", nil, "topics to listen to, comma separated")
	return c
}

var _ = time.Second
