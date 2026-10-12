// SPDX-License-Identifier: Apache-2.0

// Package docker talks to the Docker Engine API over the unix socket.
// Client is the interface the rest of the agent uses, so tests can use a fake.
package docker

import (
	"context"
	"io"
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
	Mounts        []Mount  // volume, bind and tmpfs mounts (HostConfig.Mounts)
	Ports         []PortBinding
	RestartPolicy string // always | on-failure | no | unless-stopped
	NanoCPUs      int64
	MemoryBytes   int64
	StopTimeout   int // seconds
	CapAdd        []string
	CapDrop       []string // capabilities removed from the container
	DNS           []string // DNS servers for the container
	IP            string   // fixed IPv4 address on Network (must be inside the subnet of the network). Empty = Docker picks.
}

// Mount is one entry of HostConfig.Mounts.
type Mount struct {
	Type      string // volume | bind | tmpfs
	Source    string // volume name or host path, empty for tmpfs
	Target    string // path in the container
	ReadOnly  bool
	TmpfsSize int64 // tmpfs only, 0 = no limit
}

// Volume is a named Docker volume.
type Volume struct {
	Name       string
	Mountpoint string // host path of the data
	Labels     map[string]string
}

// ExecOptions are the options of a command run in a running container.
type ExecOptions struct {
	Cmd        []string
	Tty        bool
	Rows, Cols uint32
	// Stderr, when set, gets the stderr of the command. Read then returns stdout only (no tty).
	Stderr io.Writer
}

// ExecConn is an open exec session. Read gives the output (stdout and stderr together),
// Write sends input. Read returns io.EOF when the command ends.
type ExecConn interface {
	io.Reader
	io.Writer
	// CloseWrite ends the input (like Ctrl-D on a closed pipe).
	CloseWrite() error
	// Resize changes the terminal size.
	Resize(ctx context.Context, rows, cols uint32) error
	// Wait returns the exit code after the output has ended.
	Wait(ctx context.Context) (int, error)
	// Close ends the session.
	Close() error
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
	// RestartCount is how often Docker restarted the container (set by Inspect only).
	RestartCount int
	// RestartPolicy is the Docker restart policy: always | unless-stopped | on-failure | no (set by Inspect only).
	RestartPolicy string
}

// MountUse is one persistent mount (volume or bind) of a container, as Docker reports it.
type MountUse struct {
	Type   string // volume | bind
	Name   string // volume name, empty for a bind
	Source string // host path of the data
	RW     bool   // false = the container cannot write it
}

// MountedContainer is a container with its persistent mounts.
type MountedContainer struct {
	ID     string
	Name   string
	State  string
	Labels map[string]string
	Mounts []MountUse
}

// MountLister lists the persistent mounts of every container on the host: also stopped ones and
// the ones that Polipan does not manage (the volume guard, 08 section 7a).
type MountLister interface {
	MountedContainers(ctx context.Context) ([]MountedContainer, error)
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
	// SetRestartPolicy changes the restart policy (always | no | ...) of an existing container.
	SetRestartPolicy(ctx context.Context, id, policy string) error
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
	// VolumeCreate creates a named volume. An existing volume is returned as it is.
	VolumeCreate(ctx context.Context, name string, labels map[string]string) (*Volume, error)
	// VolumeInspect returns a volume. A missing volume returns ErrNotFound.
	VolumeInspect(ctx context.Context, name string) (*Volume, error)
	// VolumeList returns the volumes that have every label pair in labels.
	VolumeList(ctx context.Context, labels map[string]string) ([]Volume, error)
	// VolumeRemove removes a volume and its data. A missing volume returns ErrNotFound.
	VolumeRemove(ctx context.Context, name string) error
	// ImageRemove removes an image by reference. An image that a container uses is refused (the error says so).
	ImageRemove(ctx context.Context, ref string) error
	// Exec starts a command in a running container and attaches to it.
	Exec(ctx context.Context, idOrName string, opts ExecOptions) (ExecConn, error)
}
