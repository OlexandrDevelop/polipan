// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// cpuTimes is the first line of /proc/stat, in clock ticks.
type cpuTimes struct{ busy, total uint64 }

// nodeStats is what the collector reads from /proc and the file system.
type nodeStats struct {
	cpu       cpuTimes
	cores     int
	memUsed   uint64
	load1     float64
	diskUsed  uint64
	diskTotal uint64
}

// readProc reads /proc under root (root is "/" on a real node, a temp folder in tests).
func readProc(root string) (nodeStats, error) {
	var s nodeStats
	f, err := os.Open(filepath.Join(root, "proc/stat"))
	if err != nil {
		return s, err
	}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fs := strings.Fields(sc.Text())
		if len(fs) < 5 || !strings.HasPrefix(fs[0], "cpu") {
			continue
		}
		if fs[0] != "cpu" {
			s.cores++
			continue
		}
		var v []uint64
		for _, x := range fs[1:] {
			n, _ := strconv.ParseUint(x, 10, 64)
			v = append(v, n)
		}
		for _, n := range v[:min(len(v), 8)] { // user nice system idle iowait irq softirq steal
			s.cpu.total += n
		}
		idle := v[3]
		if len(v) > 4 {
			idle += v[4]
		}
		s.cpu.busy = s.cpu.total - idle
	}
	_ = f.Close()
	if s.cpu.total == 0 {
		return s, errors.New("metrics: no cpu line in /proc/stat")
	}
	if s.cores == 0 {
		s.cores = 1
	}

	mem, err := os.ReadFile(filepath.Join(root, "proc/meminfo"))
	if err != nil {
		return s, err
	}
	var total, avail uint64
	for _, line := range strings.Split(string(mem), "\n") {
		fs := strings.Fields(line)
		if len(fs) < 2 {
			continue
		}
		kb, _ := strconv.ParseUint(fs[1], 10, 64)
		switch fs[0] {
		case "MemTotal:":
			total = kb * 1024
		case "MemAvailable:":
			avail = kb * 1024
		}
	}
	if total > avail {
		s.memUsed = total - avail
	}

	la, err := os.ReadFile(filepath.Join(root, "proc/loadavg"))
	if err != nil {
		return s, err
	}
	if fs := strings.Fields(string(la)); len(fs) > 0 {
		s.load1, _ = strconv.ParseFloat(fs[0], 64)
	}
	return s, nil
}
