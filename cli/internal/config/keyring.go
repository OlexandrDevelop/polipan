// SPDX-License-Identifier: Apache-2.0

package config

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"
)

// Keyring stores one secret per account.
type Keyring interface {
	Get(account string) (string, error)
	Set(account, secret string) error
	Delete(account string) error
}

// ErrNoKeyring means no OS keyring is available.
var ErrNoKeyring = errors.New("no keyring available")

const service = "polipan"

// SystemKeyring uses secret-tool (libsecret) on Linux. The secret goes in on stdin, never in arguments.
// It returns nil when no keyring tool is found. Other systems use the file.
func SystemKeyring() Keyring {
	path, err := exec.LookPath("secret-tool")
	if err != nil {
		return nil
	}
	return &secretTool{path: path}
}

type secretTool struct{ path string }

func (s *secretTool) run(stdin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.path, args...)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", ErrNoKeyring
	}
	return out.String(), nil
}

func (s *secretTool) Get(account string) (string, error) {
	out, err := s.run("", "lookup", "service", service, "account", account)
	if err != nil || out == "" {
		return "", ErrNoKeyring
	}
	return out, nil
}

func (s *secretTool) Set(account, secret string) error {
	_, err := s.run(secret, "store", "--label=polipan", "service", service, "account", account)
	return err
}

func (s *secretTool) Delete(account string) error {
	_, err := s.run("", "clear", "service", service, "account", account)
	return err
}
