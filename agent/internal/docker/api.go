// SPDX-License-Identifier: Apache-2.0

// Package docker talks to the Docker Engine API over the unix socket.
// Client is the interface the rest of the agent uses, so tests can use a fake.
package docker

import (
	"context"
	"time"
)

// PortBinding publishes a container port on the host.
type PortBinding struct {
	HostIP        string // empty = all addresses
	HostPort      int
	ContainerPort int
	Proto         string // tcp or udp
}

// ContainerSpec is what the agent asks Docker to create.
type ContainerSpec struct {
	Name          string
	Image         string
	Cmd           []string
	Entrypoint    []string
	Env           map[string]string
	Labels        map[string]string
	Network       string   // user-defined network name, or "host"
	Aliases       []string // network aliases on Network
	Binds         []string // host:container[:ro]
	Ports         []PortBinding
	RestartPolicy string // always | on-failure | no | unless-stopped
	NanoCPUs      int64
	MemoryBytes   int64
	StopTimeout   int // seconds
	CapAdd        []string
	DNS           []string // DNS servers for the container
}

// Container is the state of one container.
type Container struct {
	ID        string
	Name      string
	Image     string
	ImageID   string
	State     string // created | running | exited | removing | dead | paused | restarting
	ExitCode  int
	StartedAt time.Time
	Labels    map[string]string
	IP        string // address on the polipan network, empty if none
	Health    string // Docker health state, empty if no health check
}

// Event is a container event from Docker.
type Event struct {
	ContainerID string
	Name        string
	Action      string // start | die | oom | stop | destroy | restart
	ExitCode    int
	Labels      map[string]string
	Time        time.Time
}

// Client is the part of the Docker Engine API that the agent needs.
type Client interface {
	// Ping returns the Docker server version.
	Ping(ctx context.Context) (string, error)
	// EnsureNetwork creates the bridge network if it is missing. It returns the gateway address.
	EnsureNetwork(ctx context.Context, name, subnet string) (gateway string, err error)
	// PullImage pulls ref and returns the immutable reference repo@sha256:digest.
	PullImage(ctx context.Context, ref, registryAuth string) (digestRef string, err error)
	// CreateAndStart creates and starts a container. A container with the same name must not exist.
	CreateAndStart(ctx context.Context, spec ContainerSpec) (id string, err error)
	Start(ctx context.Context, id string) error
	Stop(ctx context.Context, id string, graceSeconds int) error
	Restart(ctx context.Context, id string, graceSeconds int) error
	// Remove removes the container. force kills it first.
	Remove(ctx context.Context, id string, force bool) error
	// List returns all containers (also stopped) that have every label pair in labels.
	List(ctx context.Context, labels map[string]string) ([]Container, error)
	// Inspect returns one container by id or name. A missing container returns ErrNotFound.
	Inspect(ctx context.Context, idOrName string) (*Container, error)
	// Logs returns the last tail lines of stdout and stderr, as text.
	Logs(ctx context.Context, idOrName string, tail int) (string, error)
	// Events streams container events until ctx ends.
	Events(ctx context.Context) (<-chan Event, error)
}
