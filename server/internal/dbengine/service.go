// SPDX-License-Identifier: Apache-2.0

package dbengine

import (
	"crypto/rand"
	"math/big"
)

// EnvEntry is a variable or a secret of a service. A secret has no value here when it is generated.
type EnvEntry struct {
	Name   string
	Value  string
	Secret bool
}

// ServiceSpec is what a new database service needs (REQ-DB-002, REQ-DB-003).
type ServiceSpec struct {
	Engine   string
	Version  string
	Config   map[string]any
	Env      []EnvEntry // DB_USER, DB_NAME, DB_PASSWORD and the variables of the image
	DataPath string
}

// NewService makes the config and the variables of a database service. The password is the value
// of the secret DB_PASSWORD. An empty password means a generated one.
func (e *Engine) NewService(version, password string) (*ServiceSpec, error) {
	if version == "" {
		version = e.DefaultTag
	}
	image, err := e.ImageRef(version)
	if err != nil {
		return nil, err
	}
	if password == "" {
		if password, err = GeneratePassword(PasswordLength); err != nil {
			return nil, err
		}
	}
	cfg := map[string]any{
		"image": image, "port": e.Port, "strategy": "recreate", "restart_policy": "always",
		"health": map[string]any{"type": "tcp", "port": e.Port, "start_period": 20},
	}
	if len(e.Command) > 0 {
		cmd := make([]any, len(e.Command))
		for i, c := range e.Command {
			cmd[i] = c
		}
		cfg["command"] = cmd
	}
	env := []EnvEntry{
		{Name: "DB_USER", Value: DefaultUser(e.Name)},
		{Name: "DB_NAME", Value: DefaultName(e.Name)},
		{Name: "DB_PASSWORD", Value: password, Secret: true},
	}
	for _, v := range e.Env {
		env = append(env, EnvEntry{Name: v.Name, Value: v.Value})
	}
	return &ServiceSpec{Engine: e.Name, Version: version, Config: cfg, Env: env, DataPath: e.DataPath}, nil
}

const alphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// GeneratePassword makes a random password of letters and digits (it needs no escaping in URLs).
func GeneratePassword(n int) (string, error) {
	out := make([]byte, n)
	max := big.NewInt(int64(len(alphabet)))
	for i := range out {
		v, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		out[i] = alphabet[v.Int64()]
	}
	return string(out), nil
}
