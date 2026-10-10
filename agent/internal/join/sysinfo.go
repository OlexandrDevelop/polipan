// SPDX-License-Identifier: Apache-2.0

package join

import (
	"bufio"
	"context"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/polipan/polipan/agent/internal/version"
	"github.com/polipan/polipan/proto/agentv1"
)

// Mem returns total and available memory in bytes (best effort, Linux).
func Mem() (total, avail uint64) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fs := strings.Fields(sc.Text())
		if len(fs) < 2 {
			continue
		}
		v, _ := strconv.ParseUint(fs[1], 10, 64)
		switch fs[0] {
		case "MemTotal:":
			total = v * 1024
		case "MemAvailable:":
			avail = v * 1024
		}
	}
	return
}

// Disk returns total and used bytes of the filesystem of path.
func Disk(path string) (total, used uint64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0
	}
	total = st.Blocks * uint64(st.Bsize)
	used = total - st.Bfree*uint64(st.Bsize)
	return
}

// Load1 returns the one minute load average.
func Load1() float64 {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0
	}
	fs := strings.Fields(string(b))
	if len(fs) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(fs[0], 64)
	return v
}

// outboundIP returns the local address used for outgoing traffic. No packet is sent.
func outboundIP() string {
	c, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return ""
	}
	defer func() { _ = c.Close() }()
	if a, ok := c.LocalAddr().(*net.UDPAddr); ok {
		return a.IP.String()
	}
	return ""
}

// NodeInfo describes this node in the format of the agent protocol.
func NodeInfo(ctx context.Context, dockerVersion, caddyVersion, dataDir string, labels map[string]string) *agentv1.NodeInfo {
	host, _ := os.Hostname()
	mem, _ := Mem()
	disk, _ := Disk(dataDir)
	if disk == 0 {
		disk, _ = Disk("/")
	}
	return &agentv1.NodeInfo{
		Hostname: host, Os: runtime.GOOS, Arch: runtime.GOARCH,
		DockerVersion: dockerVersion, AgentVersion: version.Version, CaddyVersion: caddyVersion,
		CpuMillis: uint64(runtime.NumCPU()) * 1000, MemoryBytes: mem, DiskBytes: disk,
		PublicIp: outboundIP(), Labels: labels,
	}
}
