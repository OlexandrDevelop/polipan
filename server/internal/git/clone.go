// SPDX-License-Identifier: Apache-2.0

package git

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/polipan/polipan/server/internal/ssrf"
)

// CloneRequest is one checkout of a commit.
type CloneRequest struct {
	URL      string // https://host/owner/name.git, ssh://git@host/owner/name.git or git@host:owner/name.git
	Branch   string // used when Commit is empty
	Commit   string // optional: the exact commit
	Username string // https auth (token clone)
	Password string // https auth, never logged
	SSHKey   string // deploy key (OpenSSH private key PEM), never logged
	Dir      string // an empty folder that receives the files
	Policy   ssrf.Policy
	Log      func(line string) // progress lines, may be nil
}

// Cloner checks out one commit of a repository. Tests use FakeCloner.
type Cloner interface {
	// Clone fetches the commit (or the branch head) into req.Dir and returns the commit id.
	Clone(ctx context.Context, req CloneRequest) (commit string, err error)
}

// CloneError is a clone failure with a message for the user (repo gone, token revoked, no branch).
type CloneError struct{ Msg string }

func (e *CloneError) Error() string { return "git clone failed: " + e.Msg }

// CLI clones with the git command line tool.
//
// SSRF: the host is resolved and checked first, then git connects only to that address
// (http.curloptResolve for https, the IP in the URL for ssh), so a second DNS answer cannot
// point somewhere else. Redirects are off. Only https, http and ssh are allowed.
// The token goes in through GIT_CONFIG_* environment variables, never in the URL or the args.
type CLI struct {
	Git string // default "git"
}

// Clone implements Cloner.
func (c CLI) Clone(ctx context.Context, req CloneRequest) (string, error) {
	if req.Commit != "" && !ValidCommit(req.Commit) {
		return "", &CloneError{"the commit id is not valid"}
	}
	if req.Commit == "" && (req.Branch == "" || strings.HasPrefix(req.Branch, "-") || strings.ContainsAny(req.Branch, " \t\n:~^?*[\\")) {
		return "", &CloneError{"the branch name is not valid"}
	}
	host, port, err := ssrf.HostPort(req.URL)
	if err != nil {
		return "", &CloneError{err.Error()}
	}
	addrs, err := req.Policy.Resolve(ctx, host)
	if err != nil {
		return "", &CloneError{err.Error()}
	}
	ip := addrs[0]

	tmp, err := os.MkdirTemp("", "pp-git-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)

	cfg := map[string]string{
		"protocol.allow":          "never",
		"protocol.https.allow":    "always",
		"protocol.http.allow":     "always",
		"protocol.ssh.allow":      "always",
		"http.followRedirects":    "false",
		"credential.helper":       "",
		"core.hooksPath":          "/dev/null",
		"advice.detachedHead":     "false",
		"init.defaultBranch":      "main",
		"fetch.recurseSubmodules": "false",
		"http.lowSpeedLimit":      "1000",
		"http.lowSpeedTime":       "60",
		"gc.auto":                 "0",
	}
	remote := req.URL
	env := []string{
		"HOME=" + tmp, "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"PATH=" + os.Getenv("PATH"), "GIT_ASKPASS=/bin/false", "SSH_ASKPASS=/bin/false",
	}
	u, perr := url.Parse(req.URL)
	isSSH := perr != nil || u.Scheme == "ssh" || !strings.Contains(req.URL, "://")
	if isSSH {
		if req.SSHKey == "" {
			return "", &CloneError{"an ssh URL needs a deploy key"}
		}
		keyFile := filepath.Join(tmp, "key")
		if err := os.WriteFile(keyFile, []byte(strings.TrimSpace(req.SSHKey)+"\n"), 0o600); err != nil {
			return "", err
		}
		user, path := "git", ""
		if perr == nil && u.Scheme == "ssh" {
			if u.User != nil {
				user = u.User.Username()
			}
			path = strings.TrimPrefix(u.Path, "/")
		} else {
			left, p, _ := strings.Cut(req.URL, ":")
			path = p
			if at := strings.LastIndex(left, "@"); at >= 0 {
				user = left[:at]
			}
		}
		if port == "" {
			port = "22"
		}
		remote = "ssh://" + url.User(user).String() + "@" + hostLiteral(ip) + ":" + port + "/" + path
		// The host key is not known before the first clone: accept it (trust on first use)
		// and check the name the user gave with HostKeyAlias.
		env = append(env, "GIT_SSH_COMMAND=ssh -F /dev/null -i "+keyFile+" -o IdentitiesOnly=yes -o BatchMode=yes"+
			" -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile="+filepath.Join(tmp, "known_hosts")+
			" -o HostKeyAlias="+host+" -o ConnectTimeout=20")
	} else {
		if port == "" {
			port = "443"
			if u.Scheme == "http" {
				port = "80"
			}
		}
		cfg["http.curloptResolve"] = host + ":" + port + ":" + hostLiteral(ip)
		if req.Password != "" {
			user := req.Username
			if user == "" {
				user = "polipan"
			}
			cfg["http.extraHeader"] = "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+req.Password))
		}
	}
	env = append(env, gitConfigEnv(cfg)...)

	git := c.Git
	if git == "" {
		git = "git"
	}
	run := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, git, args...)
		cmd.Dir = req.Dir
		cmd.Env = env
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		err := cmd.Run()
		text := out.String()
		if req.Log != nil {
			sc := bufio.NewScanner(strings.NewReader(text))
			for sc.Scan() {
				if l := strings.TrimSpace(sc.Text()); l != "" {
					req.Log("git: " + l)
				}
			}
		}
		return strings.TrimSpace(text), err
	}
	if _, err := run("init", "-q", "."); err != nil {
		return "", fmt.Errorf("git init: %w", err)
	}
	if _, err := run("remote", "add", "origin", remote); err != nil {
		return "", fmt.Errorf("git remote: %w", err)
	}
	if req.Commit != "" {
		if _, err := run("fetch", "-q", "--depth", "1", "--no-tags", "origin", req.Commit); err != nil {
			// Some servers do not allow a fetch by commit id: fetch the branch and look for it.
			if req.Branch == "" {
				return "", cloneFailed(ctx, err)
			}
			if out, err := run("fetch", "-q", "--no-tags", "origin", "+refs/heads/"+req.Branch+":refs/remotes/origin/"+req.Branch); err != nil {
				return "", cloneFailed(ctx, fmt.Errorf("%w: %s", err, lastLine(out)))
			}
		}
		if out, err := run("checkout", "-q", "--detach", req.Commit); err != nil {
			return "", &CloneError{"commit " + req.Commit + " not found: " + lastLine(out)}
		}
	} else {
		if out, err := run("fetch", "-q", "--depth", "1", "--no-tags", "origin", "+refs/heads/"+req.Branch+":refs/remotes/origin/"+req.Branch); err != nil {
			return "", cloneFailed(ctx, fmt.Errorf("%w: %s", err, lastLine(out)))
		}
		if out, err := run("checkout", "-q", "--detach", "refs/remotes/origin/"+req.Branch); err != nil {
			return "", &CloneError{"checkout failed: " + lastLine(out)}
		}
	}
	commit, err := run("rev-parse", "HEAD")
	if err != nil || !ValidCommit(commit) {
		return "", &CloneError{"cannot read the commit id"}
	}
	_ = os.RemoveAll(filepath.Join(req.Dir, ".git"))
	return commit, nil
}

func cloneFailed(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	msg := err.Error()
	if strings.Contains(msg, "Authentication failed") || strings.Contains(msg, "403") || strings.Contains(msg, "Permission denied") {
		msg = "access denied (the token or deploy key is not valid any more): " + msg
	} else if strings.Contains(msg, "not found") || strings.Contains(msg, "404") || strings.Contains(msg, "couldn't find remote ref") {
		msg = "repository or branch not found: " + msg
	}
	return &CloneError{msg}
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		s = s[i+1:]
	}
	return s
}

func hostLiteral(ip netip.Addr) string {
	if ip.Is6() {
		return "[" + ip.String() + "]"
	}
	return ip.String()
}

// gitConfigEnv passes config values through the environment (GIT_CONFIG_COUNT), so tokens
// never show in the process list.
func gitConfigEnv(cfg map[string]string) []string {
	keys := make([]string, 0, len(cfg))
	for k := range cfg {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := []string{"GIT_CONFIG_COUNT=" + strconv.Itoa(len(keys))}
	for i, k := range keys {
		n := strconv.Itoa(i)
		out = append(out, "GIT_CONFIG_KEY_"+n+"="+k, "GIT_CONFIG_VALUE_"+n+"="+cfg[k])
	}
	return out
}

// IsCloneError tells if err is a clone failure for the user.
func IsCloneError(err error) bool {
	var ce *CloneError
	return errors.As(err, &ce)
}
