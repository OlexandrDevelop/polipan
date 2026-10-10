// SPDX-License-Identifier: Apache-2.0

// Package config reads and writes the CLI config file (docs/spec/06-cli.md, section 1).
// Tokens go to the OS keyring when one is available, else to the file with mode 0600.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Context is one server with its login.
type Context struct {
	Name string `yaml:"name"`
	URL  string `yaml:"url"`
	// Token is set only when the keyring is not used.
	Token string `yaml:"token,omitempty"`
	// TokenInKeyring is true when the token is stored in the OS keyring.
	TokenInKeyring bool `yaml:"token_in_keyring,omitempty"`
	// Project and Env are optional defaults.
	Project string `yaml:"project,omitempty"`
	Env     string `yaml:"env,omitempty"`
}

// File is the content of config.yaml.
type File struct {
	CurrentContext string    `yaml:"current_context,omitempty"`
	Contexts       []Context `yaml:"contexts,omitempty"`
}

// DefaultPath returns ~/.config/polipan/config.yaml (or under XDG_CONFIG_HOME).
func DefaultPath(getenv func(string) string) (string, error) {
	if dir := getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "polipan", "config.yaml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "polipan", "config.yaml"), nil
}

// Load reads the file. A missing file gives an empty config.
func Load(path string) (*File, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &File{}, nil
	}
	if err != nil {
		return nil, err
	}
	var f File
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("config file %s is not valid: %w", path, err)
	}
	return &f, nil
}

// Save writes the file with mode 0600. The folder gets mode 0700.
func Save(path string, f *File) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := yaml.Marshal(f)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Find returns the context with this name.
func (f *File) Find(name string) *Context {
	for i := range f.Contexts {
		if f.Contexts[i].Name == name {
			return &f.Contexts[i]
		}
	}
	return nil
}

// Upsert adds or replaces a context.
func (f *File) Upsert(c Context) {
	if old := f.Find(c.Name); old != nil {
		*old = c
		return
	}
	f.Contexts = append(f.Contexts, c)
}

// Remove deletes a context.
func (f *File) Remove(name string) {
	out := f.Contexts[:0]
	for _, c := range f.Contexts {
		if c.Name != name {
			out = append(out, c)
		}
	}
	f.Contexts = out
	if f.CurrentContext == name {
		f.CurrentContext = ""
	}
}
