// SPDX-License-Identifier: Apache-2.0

//go:build linux

package reconcile

import (
	"fmt"
	"hash/fnv"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

const (
	tmpfsMagic = 0x01021994
	xfsMagic   = 0x58465342
)

// isTmpfs returns an error when dir is not on tmpfs. Secret files are only written to memory.
func isTmpfs(dir string) error {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return err
	}
	if st.Type != tmpfsMagic {
		return fmt.Errorf("%s is not on tmpfs, secret files are not written to disk", dir)
	}
	return nil
}

// xfsQuota sets an XFS project quota with the xfs_quota tool. It needs XFS mounted with prjquota.
type xfsQuota struct{}

func (xfsQuota) Apply(dir string, limit uint64) (bool, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil || st.Type != xfsMagic {
		return false, err
	}
	mnt, opts := mountOf(dir)
	if mnt == "" || !(strings.Contains(opts, "prjquota") || strings.Contains(opts, "pquota")) {
		return false, nil
	}
	bin, err := exec.LookPath("xfs_quota")
	if err != nil || strings.ContainsAny(dir, " '\"\\") {
		return false, nil
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(dir))
	id := 100000 + h.Sum32()%900000
	for _, c := range []string{fmt.Sprintf("project -s -p %s %d", dir, id), fmt.Sprintf("limit -p bhard=%d %d", limit, id)} {
		if out, err := exec.Command(bin, "-x", "-c", c, mnt).CombinedOutput(); err != nil {
			return false, fmt.Errorf("xfs_quota: %w: %s", err, strings.TrimSpace(string(out)))
		}
	}
	return true, nil
}

// mountOf returns the mount point and the options of the file system that holds dir.
func mountOf(dir string) (mountPoint, options string) {
	b, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return "", ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		// id parent major:minor root mount-point options [optional...] - fstype source super-options
		f := strings.Fields(line)
		sep := -1
		for i, v := range f {
			if v == "-" {
				sep = i
				break
			}
		}
		if len(f) < 6 || sep < 0 || sep+3 >= len(f) {
			continue
		}
		mp := f[4]
		if (dir == mp || strings.HasPrefix(dir, strings.TrimSuffix(mp, "/")+"/")) && len(mp) >= len(mountPoint) {
			mountPoint, options = mp, f[5]+","+f[sep+3]
		}
	}
	return mountPoint, options
}
