// SPDX-License-Identifier: Apache-2.0

// Package cac is the config as code engine (07-config-as-code, F08): parse and validate a polipan.yaml,
// merge it per environment, plan it against the live state and apply it in order.
// It is not the process config of the server (that is the package config).
package cac

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Issue is one error of a file: where it is and what is wrong (REQ-CAC-007).
type Issue struct {
	Line    int    `json:"line"`
	Path    string `json:"path"`
	Message string `json:"message"`
	// Code is set for errors that have a name in the API, for example missing_secret.
	Code string `json:"code,omitempty"`
}

func (i Issue) String() string {
	return fmt.Sprintf("line %d, %s: %s", i.Line, i.Path, i.Message)
}

// File is a polipan.yaml after the schema check.
type File struct {
	Version       int                       `json:"version"`
	Project       string                    `json:"project"`
	Environments  map[string]*Environment   `json:"environments"`
	Services      map[string]map[string]any `json:"services"` // raw: the merge works on maps (07 section 3)
	Variables     map[string]Scalar         `json:"variables"`
	Secrets       []SecretRef               `json:"secrets"`
	Previews      *Previews                 `json:"previews"`
	BackupTargets []BackupTarget            `json:"backup_targets"`
	Alerts        *Alerts                   `json:"alerts"`
}

// Scalar is a plain value of a variable. Numbers and booleans become text.
type Scalar string

// UnmarshalJSON reads a string, a number or a boolean.
func (s *Scalar) UnmarshalJSON(b []byte) error {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*s = Scalar(scalarText(v))
	return nil
}

func scalarText(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t))
		}
		return fmt.Sprintf("%g", t)
	case nil:
		return ""
	}
	return fmt.Sprint(v)
}

// SecretRef is a secret name in the file. The value is never in the file.
type SecretRef struct {
	Name      string `json:"name"`
	Preview   bool   `json:"preview"`
	BuildTime bool   `json:"build_time"`
	File      string `json:"file"`
}

// UnmarshalJSON reads a name or an object.
func (s *SecretRef) UnmarshalJSON(b []byte) error {
	var name string
	if json.Unmarshal(b, &name) == nil {
		*s = SecretRef{Name: name}
		return nil
	}
	type plain SecretRef
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*s = SecretRef(p)
	return nil
}

// Environment is the block environments.<slug>.
type Environment struct {
	Kind       string                    `json:"kind"`
	Protected  *bool                     `json:"protected"`
	AutoDeploy *bool                     `json:"auto_deploy"`
	Deploy     map[string]any            `json:"deploy"`
	Variables  map[string]Scalar         `json:"variables"`
	Secrets    []SecretRef               `json:"secrets"`
	Services   map[string]map[string]any `json:"services"`
}

// Previews is the previews block. The fields are kept as the schema has them.
type Previews struct {
	Enabled         *bool             `json:"enabled"`
	BaseEnvironment string            `json:"base_environment"`
	MaxActive       *int              `json:"max_active"`
	TTL             string            `json:"ttl"`
	FromForks       *bool             `json:"from_forks"`
	NodeSelector    map[string]string `json:"node_selector"`
	Strategy        string            `json:"strategy"`
	SeedCommand     []string          `json:"seed_command"`
}

// BackupTarget is a backup target by name. Credentials come from a secret name.
type BackupTarget struct {
	Name              string         `json:"name"`
	Kind              string         `json:"kind"`
	Config            map[string]any `json:"config"`
	CredentialsSecret string         `json:"credentials_secret"`
}

// Alerts is the alerts block.
type Alerts struct {
	Channels []map[string]any `json:"channels"`
	Rules    []map[string]any `json:"rules"`
}

// Service is a service after the merge of one environment (07 section 3), typed.
type Service struct {
	Enabled               *bool               `json:"enabled"`
	Type                  string              `json:"type"`
	Source                *Source             `json:"source"`
	DB                    *DB                 `json:"db"`
	Ports                 []Port              `json:"ports"`
	Command               []string            `json:"command"`
	Entrypoint            []string            `json:"entrypoint"`
	Workdir               string              `json:"workdir"`
	Env                   map[string]EnvValue `json:"env"`
	Secrets               []SecretRef         `json:"secrets"`
	Domains               []Domain            `json:"domains"`
	Volumes               []Volume            `json:"volumes"`
	Files                 []FileMount         `json:"files"`
	Healthcheck           *Healthcheck        `json:"healthcheck"`
	Deploy                *Deploy             `json:"deploy"`
	Resources             *Resources          `json:"resources"`
	Replicas              *int                `json:"replicas"`
	Placement             map[string]any      `json:"placement"`
	Failover              string              `json:"failover"`
	AllowDataLossFailover *bool               `json:"allow_data_loss_failover"`
	StopGracePeriod       string              `json:"stop_grace_period"`
	RestartPolicy         string              `json:"restart_policy"`
	AutoDeploy            *bool               `json:"auto_deploy"`
	DependsOn             []string            `json:"depends_on"`
	Cron                  map[string]any      `json:"cron"`
	Backup                *Backup             `json:"backup"`
	RequestBodyLimit      string              `json:"request_body_limit"`
}

// Source is a git or an image source.
type Source struct {
	Git   *GitSource   `json:"git"`
	Image *ImageSource `json:"image"`
}

// GitSource is source.git.
type GitSource struct {
	Connection string            `json:"connection"`
	Repo       string            `json:"repo"`
	Branch     string            `json:"branch"`
	Path       string            `json:"path"`
	Builder    string            `json:"builder"`
	Dockerfile string            `json:"dockerfile"`
	BuildArgs  map[string]string `json:"build_args"`
	Commit     string            `json:"commit"`
}

// ImageSource is source.image.
type ImageSource struct {
	Name           string `json:"name"`
	RegistrySecret string `json:"registry_secret"`
}

// DB is the db block of a database service.
type DB struct {
	Engine         string `json:"engine"`
	Version        string `json:"version"`
	ExposeHostPort int    `json:"expose_host_port"`
}

// Port is a port: a number or an object.
type Port struct {
	Container int    `json:"container"`
	Protocol  string `json:"protocol"`
	Host      int    `json:"host"`
}

// UnmarshalJSON reads a number or an object.
func (p *Port) UnmarshalJSON(b []byte) error {
	var n int
	if json.Unmarshal(b, &n) == nil {
		*p = Port{Container: n, Protocol: "tcp"}
		return nil
	}
	type plain Port
	var q plain
	if err := json.Unmarshal(b, &q); err != nil {
		return err
	}
	if q.Protocol == "" {
		q.Protocol = "tcp"
	}
	*p = Port(q)
	return nil
}

// EnvValue is a value of env: a scalar or an object with flags.
type EnvValue struct {
	Value     string `json:"value"`
	Preview   bool   `json:"preview"`
	BuildTime bool   `json:"build_time"`
}

// UnmarshalJSON reads a scalar or an object.
func (e *EnvValue) UnmarshalJSON(b []byte) error {
	var obj struct {
		Value     Scalar `json:"value"`
		Preview   bool   `json:"preview"`
		BuildTime bool   `json:"build_time"`
	}
	if strings.HasPrefix(strings.TrimSpace(string(b)), "{") {
		if err := json.Unmarshal(b, &obj); err != nil {
			return err
		}
		*e = EnvValue{Value: string(obj.Value), Preview: obj.Preview, BuildTime: obj.BuildTime}
		return nil
	}
	var s Scalar
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	*e = EnvValue{Value: string(s)}
	return nil
}

// Domain is a domain of a service.
type Domain struct {
	Host            string   `json:"host"`
	Port            int      `json:"port"`
	PathPrefix      string   `json:"path_prefix"`
	HTTPS           *bool    `json:"https"`
	RedirectWWW     string   `json:"redirect_www"`
	IngressNodes    []string `json:"ingress_nodes"`
	MaxBody         string   `json:"max_body"`
	IPAllow         []string `json:"ip_allow"`
	BasicAuthSecret string   `json:"basic_auth_secret"`
}

// Volume is a volume of a service.
type Volume struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Kind       string `json:"kind"`
	AccessMode string `json:"access_mode"`
	SizeLimit  string `json:"size_limit"`
	Node       string `json:"node"`
}

// FileMount is a small file mounted read-only.
type FileMount struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// Healthcheck is the health check.
type Healthcheck struct {
	Type               string   `json:"type"`
	Path               string   `json:"path"`
	Port               int      `json:"port"`
	HostHeader         string   `json:"host_header"`
	Command            []string `json:"command"`
	ExpectedStatus     string   `json:"expected_status"`
	Interval           string   `json:"interval"`
	Timeout            string   `json:"timeout"`
	HealthyThreshold   int      `json:"healthy_threshold"`
	UnhealthyThreshold int      `json:"unhealthy_threshold"`
	StartPeriod        string   `json:"start_period"`
	DeployTimeout      string   `json:"deploy_timeout"`
	RestartOnUnhealthy *bool    `json:"restart_on_unhealthy"`
}

// Deploy is the deploy block.
type Deploy struct {
	Strategy                   string         `json:"strategy"`
	DrainSeconds               *int           `json:"drain_seconds"`
	RetainStopped              *int           `json:"retain_stopped"`
	AllowOverlap               *bool          `json:"allow_overlap"`
	AllowDowntimeOnLowCapacity *bool          `json:"allow_downtime_on_low_capacity"`
	PreDeploy                  []string       `json:"pre_deploy"`
	PostDeploy                 []string       `json:"post_deploy"`
	Canary                     map[string]any `json:"canary"`
}

// Resources are the limits.
type Resources struct {
	CPU               string `json:"cpu"`
	Memory            string `json:"memory"`
	MemoryReservation string `json:"memory_reservation"`
}

// Backup is the backup block of a service.
type Backup struct {
	Schedule     string         `json:"schedule"`
	Targets      []string       `json:"targets"`
	Volume       string         `json:"volume"`
	Retention    map[string]int `json:"retention"`
	RestoreCheck *RestoreCheck  `json:"restore_check"`
	PreHook      []string       `json:"pre_hook"`
	PostHook     []string       `json:"post_hook"`
}

// RestoreCheck is the restore check of a backup.
type RestoreCheck struct {
	Enabled  *bool  `json:"enabled"`
	Schedule string `json:"schedule"`
	Mode     string `json:"mode"`
}
