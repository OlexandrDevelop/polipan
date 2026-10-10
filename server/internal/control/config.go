// SPDX-License-Identifier: Apache-2.0

package control

import (
	pb "github.com/polipan/polipan/proto/agentv1"
)

// Defaults of the glossary (01-glossary, Defaults).
const (
	defaultDrainSeconds   = 30
	defaultStopGrace      = 30
	defaultRetainStopped  = 1
	defaultRetainReleases = 10
	defaultHCInterval     = 5
	defaultHCTimeout      = 3
	defaultHCHealthy      = 3
	defaultHCUnhealthy    = 3
	defaultHCStartPeriod  = 30
	defaultDeployTimeout  = 300
)

// healthConfig is config.health.
type healthConfig struct {
	Type               string   `json:"type"`
	Port               uint32   `json:"port"`
	Path               string   `json:"path"`
	Host               string   `json:"host"`
	Command            []string `json:"command"`
	ExpectedStatus     []uint32 `json:"expected_status"` // [min, max], or one status
	Interval           int      `json:"interval"`
	Timeout            int      `json:"timeout"`
	HealthyThreshold   int      `json:"healthy_threshold"`
	UnhealthyThreshold int      `json:"unhealthy_threshold"`
	StartPeriod        *int     `json:"start_period"`
	DeployTimeout      int      `json:"deploy_timeout"` // seconds
	RestartOnUnhealthy *bool    `json:"restart_on_unhealthy"`
}

// serviceConfig is the image service config (services.config and releases.config_snapshot).
type serviceConfig struct {
	Image         string        `json:"image"`
	Port          uint32        `json:"port"`
	HostPort      uint32        `json:"host_port"`
	Command       []string      `json:"command"`
	Entrypoint    []string      `json:"entrypoint"`
	RestartPolicy string        `json:"restart_policy"`
	Health        *healthConfig `json:"health"`
	Strategy      string        `json:"strategy"`
	Resources     *struct {
		CPUMillis   uint32 `json:"cpu_millis"`
		MemoryBytes uint64 `json:"memory_bytes"`
	} `json:"resources"`
	PreDeploy       []string `json:"pre_deploy"`
	PostDeploy      []string `json:"post_deploy"`
	StopGracePeriod *int     `json:"stop_grace_period"`
	RetainStopped   *int     `json:"retain_stopped"`
	Replicas        int      `json:"replicas"`
	Placement       struct {
		Nodes  []string          `json:"nodes"`
		Labels map[string]string `json:"labels"`
		Spread *bool             `json:"spread"`
	} `json:"placement"`
	StrategyOptions struct {
		DrainSeconds               *int  `json:"drain_seconds"`
		AllowDowntimeOnLowCapacity bool  `json:"allow_downtime_on_low_capacity"`
		AllowOverlap               *bool `json:"allow_overlap"` // default true (F01)
	} `json:"strategy_options"`
}

// healthSpec returns the health check of the config (F05). Without a check, a service with a port gets
// tcp on the port (REQ-HC-003). A service without a port gets the "running" check: healthy when the
// container has run for start_period without a restart.
func (c serviceConfig) healthSpec() *pb.HealthCheckSpec {
	h := c.Health
	if h == nil {
		h = &healthConfig{}
	}
	typ, port := h.Type, c.Port
	if h.Port != 0 {
		port = h.Port
	}
	if typ == "" {
		typ = "tcp"
		if port == 0 {
			typ = "running"
		}
	}
	hc := &pb.HealthCheckSpec{
		Type: typ, Path: h.Path, Port: port, Command: h.Command, Host: h.Host,
		ExpectedStatusMin: 200, ExpectedStatusMax: 399,
		IntervalSeconds:    uint32(orInt(h.Interval, defaultHCInterval)),
		TimeoutSeconds:     uint32(orInt(h.Timeout, defaultHCTimeout)),
		HealthyThreshold:   uint32(orInt(h.HealthyThreshold, defaultHCHealthy)),
		UnhealthyThreshold: uint32(orInt(h.UnhealthyThreshold, defaultHCUnhealthy)),
		StartPeriodSeconds: defaultHCStartPeriod,
		RestartOnUnhealthy: h.RestartOnUnhealthy == nil || *h.RestartOnUnhealthy,
	}
	if typ == "http" && hc.Path == "" {
		hc.Path = "/"
	}
	if h.StartPeriod != nil {
		hc.StartPeriodSeconds = uint32(*h.StartPeriod)
	}
	switch len(h.ExpectedStatus) {
	case 1:
		hc.ExpectedStatusMin, hc.ExpectedStatusMax = h.ExpectedStatus[0], h.ExpectedStatus[0]
	case 2:
		hc.ExpectedStatusMin, hc.ExpectedStatusMax = h.ExpectedStatus[0], h.ExpectedStatus[1]
	}
	return hc
}

func orInt(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}

func orIntPtr(v *int, def int) int {
	if v != nil && *v >= 0 {
		return *v
	}
	return def
}

// deployTimeout is health.deploy_timeout in seconds (default 300).
func (c serviceConfig) deployTimeout() uint32 {
	if c.Health != nil && c.Health.DeployTimeout > 0 {
		return uint32(c.Health.DeployTimeout)
	}
	return defaultDeployTimeout
}

func (c serviceConfig) drainSeconds() int {
	return orIntPtr(c.StrategyOptions.DrainSeconds, defaultDrainSeconds)
}
func (c serviceConfig) allowOverlap() bool {
	return c.StrategyOptions.AllowOverlap == nil || *c.StrategyOptions.AllowOverlap
}
func (c serviceConfig) stopGraceSeconds() int { return orIntPtr(c.StopGracePeriod, defaultStopGrace) }
func (c serviceConfig) retainStopped() int    { return orIntPtr(c.RetainStopped, defaultRetainStopped) }
func (c serviceConfig) resourceSpec() *pb.Resources {
	if c.Resources == nil || (c.Resources.CPUMillis == 0 && c.Resources.MemoryBytes == 0) {
		return nil
	}
	return &pb.Resources{CpuLimitMillis: c.Resources.CPUMillis, MemoryLimitBytes: c.Resources.MemoryBytes}
}

// replicas is the number of copies of the service (default 1).
func (c serviceConfig) replicas() int {
	if c.Replicas > 0 {
		return c.Replicas
	}
	return 1
}

// spread is placement.spread (default true).
func (c serviceConfig) spread() bool { return c.Placement.Spread == nil || *c.Placement.Spread }
