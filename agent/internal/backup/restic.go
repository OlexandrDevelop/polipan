// SPDX-License-Identifier: Apache-2.0

package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	pb "github.com/polipan/polipan/proto/agentv1"
)

// repo is one restic repository with its environment. Close removes the temporary files.
type repo struct {
	target   string // target id
	url      string
	env      []string
	args     []string // global restic options, for example -o sftp.command=...
	password string
	dir      string // temporary folder of this repository (key files)
	// learned is the known_hosts file that ssh fills on the first connection. Empty when the key is pinned.
	learned string
}

// firstHostKey returns the host key line that ssh saved on the first connection, or "".
func (rp *repo) firstHostKey() string {
	if rp.learned == "" {
		return ""
	}
	raw, err := os.ReadFile(rp.learned)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			return line
		}
	}
	return ""
}

func (rp *repo) Close() {
	if rp.dir != "" {
		_ = os.RemoveAll(rp.dir)
	}
}

var (
	sftpHostRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.\-]{0,251}[A-Za-z0-9])?$`)
	sftpUserRe = regexp.MustCompile(`^[A-Za-z0-9._][A-Za-z0-9._\-]{0,63}$`)
)

// openRepo makes the repository of a target: the restic URL, the environment and, for SFTP, the ssh command.
func (r *Runner) openRepo(t *pb.BackupTargetSpec, password string) (*repo, error) {
	if t == nil || t.Repository == "" {
		return nil, errors.New("the target has no repository")
	}
	rp := &repo{target: t.TargetId, url: t.Repository, password: password}
	cache := filepath.Join(r.tmp(), "restic-cache")
	rp.env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + r.tmp(), "RESTIC_CACHE_DIR=" + cache, "RESTIC_PASSWORD=" + password}
	for k, v := range t.Env {
		if k == "GOOGLE_APPLICATION_CREDENTIALS_JSON" {
			dir, err := os.MkdirTemp(r.tmp(), "pp-gcs-")
			if err != nil {
				return nil, err
			}
			rp.dir = dir
			p := filepath.Join(dir, "credentials.json")
			if err := os.WriteFile(p, []byte(v), 0o600); err != nil {
				return nil, err
			}
			rp.env = append(rp.env, "GOOGLE_APPLICATION_CREDENTIALS="+p)
			continue
		}
		rp.env = append(rp.env, k+"="+v)
	}
	if t.Kind == "sftp" || strings.HasPrefix(t.Repository, "sftp:") {
		if err := r.sftpSetup(rp, t); err != nil {
			rp.Close()
			return nil, err
		}
	}
	return rp, nil
}

// sftpSetup turns "sftp://user@host:port/abs/path" into a restic repository and an ssh command with a key file
// and a known_hosts file (trust on first use when no host key is given).
func (r *Runner) sftpSetup(rp *repo, t *pb.BackupTargetSpec) error {
	u, err := url.Parse(rp.url)
	if err != nil || u.Scheme != "sftp" || u.User == nil || u.Hostname() == "" {
		return errors.New("the sftp repository must look like sftp://user@host:port/path")
	}
	host, user, port := u.Hostname(), u.User.Username(), u.Port()
	if port == "" {
		port = "22"
	}
	if !sftpHostRe.MatchString(host) || !sftpUserRe.MatchString(user) || !regexp.MustCompile(`^[0-9]{1,5}$`).MatchString(port) {
		return errors.New("the sftp host, user or port is not valid")
	}
	if len(t.SshKey) == 0 {
		return errors.New("the sftp target needs a private key")
	}
	dir, err := os.MkdirTemp(r.tmp(), "pp-ssh-")
	if err != nil {
		return err
	}
	rp.dir = dir
	key := filepath.Join(dir, "id")
	if err := os.WriteFile(key, append(bytes.TrimRight(t.SshKey, "\n"), '\n'), 0o600); err != nil {
		return err
	}
	known := filepath.Join(dir, "known_hosts")
	strict := "accept-new"
	content := ""
	if t.SshKnownHost != "" {
		content, strict = strings.TrimSpace(t.SshKnownHost)+"\n", "yes"
	} else {
		rp.learned = known
	}
	if err := os.WriteFile(known, []byte(content), 0o600); err != nil {
		return err
	}
	rp.url = "sftp:" + user + "@" + host + ":" + u.Path
	cmd := fmt.Sprintf("ssh -p %s -i %s -o UserKnownHostsFile=%s -o StrictHostKeyChecking=%s -o IdentitiesOnly=yes -o BatchMode=yes %s@%s -s sftp",
		port, key, known, strict, user, host)
	rp.args = []string{"-o", "sftp.command=" + cmd}
	return nil
}

// resticError is a failed restic call.
type resticError struct {
	Args   []string
	Code   int
	Stderr string
}

func (e *resticError) Error() string {
	cmd := ""
	if len(e.Args) > 0 {
		cmd = e.Args[0] + ": "
	}
	return fmt.Sprintf("restic %sexit code %d: %s", cmd, e.Code, firstLine(e.Stderr))
}

// restic runs one restic command. stdin and stdout are optional.
func (r *Runner) restic(ctx context.Context, rp *repo, stdin io.Reader, stdout io.Writer, args ...string) error {
	cmd := r.command(ctx, rp, stdin, stdout, args...)
	var stderr limited
	stderr.max = 8 << 10
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return &resticError{Args: args, Code: ee.ExitCode(), Stderr: r.scrub(stderr.String())}
		}
		return fmt.Errorf("restic: %w", err)
	}
	return nil
}

func (r *Runner) command(ctx context.Context, rp *repo, stdin io.Reader, stdout io.Writer, args ...string) *exec.Cmd {
	full := append(append([]string{"-r", rp.url}, rp.args...), args...)
	cmd := exec.CommandContext(ctx, r.resticBin(), full...)
	cmd.Env = rp.env
	cmd.Stdin, cmd.Stdout = stdin, stdout
	return cmd
}

// ensureRepo initializes the repository when it does not exist yet.
func (r *Runner) ensureRepo(ctx context.Context, rp *repo) error {
	err := r.restic(ctx, rp, nil, io.Discard, "cat", "config")
	if err == nil {
		return nil
	}
	var re *resticError
	if errors.As(err, &re) && (re.Code == 10 || strings.Contains(re.Stderr, "Is there a repository") || strings.Contains(re.Stderr, "does not exist") ||
		strings.Contains(re.Stderr, "no such file") || strings.Contains(re.Stderr, "unable to open config file")) {
		if err := r.restic(ctx, rp, nil, io.Discard, "init", "--quiet"); err != nil {
			return fmt.Errorf("cannot create the repository: %w", err)
		}
		return nil
	}
	return err
}

// summary is the last message of "restic backup --json".
type summary struct {
	MessageType         string `json:"message_type"`
	SnapshotID          string `json:"snapshot_id"`
	TotalBytesProcessed int64  `json:"total_bytes_processed"`
	DataAdded           int64  `json:"data_added"`
}

// parseSummary reads the lines of "restic backup --json" and returns the summary.
func parseSummary(out []byte) (summary, bool) {
	var s summary
	found := false
	for _, line := range bytes.Split(out, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var m summary
		if json.Unmarshal(line, &m) == nil && m.MessageType == "summary" && m.SnapshotID != "" {
			s, found = m, true
		}
	}
	return s, found
}

// Snapshot is one entry of "restic snapshots --json".
type Snapshot struct {
	ID       string   `json:"id"`
	ShortID  string   `json:"short_id"`
	Time     string   `json:"time"`
	Paths    []string `json:"paths"`
	Hostname string   `json:"hostname"`
	Tags     []string `json:"tags"`
}

func (r *Runner) snapshots(ctx context.Context, rp *repo, args ...string) ([]Snapshot, error) {
	var out bytes.Buffer
	if err := r.restic(ctx, rp, nil, &out, append([]string{"snapshots", "--json"}, args...)...); err != nil {
		return nil, err
	}
	var list []Snapshot
	if err := json.Unmarshal(out.Bytes(), &list); err != nil {
		return nil, fmt.Errorf("cannot read the snapshot list: %w", err)
	}
	return list, nil
}

// forget applies the retention of the policy to the snapshots of the policy host (REQ-BKP-005).
func (r *Runner) forget(ctx context.Context, rp *repo, host string, daily, weekly, monthly uint32) error {
	if daily == 0 && weekly == 0 && monthly == 0 {
		return nil // no rule: keep everything
	}
	args := []string{"forget", "--prune", "--host", host, "--group-by", "host"}
	if daily > 0 {
		args = append(args, "--keep-daily", fmt.Sprint(daily))
	}
	if weekly > 0 {
		args = append(args, "--keep-weekly", fmt.Sprint(weekly))
	}
	if monthly > 0 {
		args = append(args, "--keep-monthly", fmt.Sprint(monthly))
	}
	return r.restic(ctx, rp, nil, io.Discard, args...)
}
