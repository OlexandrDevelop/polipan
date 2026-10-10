// SPDX-License-Identifier: Apache-2.0

package cac

// The live state of a project, as the engine needs it to plan. The loader is in load.go.

// LiveVar is a variable row.
type LiveVar struct {
	ID        string
	Value     string
	Preview   bool
	BuildTime bool
	Managed   bool
}

// LiveSecret is a secret row (no value).
type LiveSecret struct {
	ID        string
	Name      string
	EnvID     string // empty: project scope
	ServiceID string // empty: not service scope
	Preview   bool
	BuildTime bool
	Managed   bool
}

// LiveDomain is a domain row.
type LiveDomain struct {
	ID           string
	Host         string
	PathPrefix   string
	Port         int
	HTTPS        bool
	RedirectWWW  string
	IngressNodes []string
	Options      map[string]any
}

// LiveVolume is a volume row.
type LiveVolume struct {
	ID         string
	Name       string
	Kind       string
	Path       string
	AccessMode string
	SizeLimit  int64
	Node       string `json:"-"`
}

// LivePolicy is a backup policy of a service.
type LivePolicy struct {
	ID            string
	Schedule      string
	TargetIDs     []string
	Retention     map[string]int
	CheckEnabled  bool
	CheckSchedule string
	CheckMode     string
	VolumeID      string
	PreHook       []string
	PostHook      []string
}

// LiveService is a service row with its parts.
type LiveService struct {
	ID            string
	Name          string
	Type          string
	SourceType    string
	DBEngine      string
	Config        map[string]any
	Failover      string
	AllowDataLoss bool
	Managed       []string
	// HasRelease is true when the service has a live release.
	HasRelease bool `json:"-"`
	Domains    []LiveDomain
	Volumes    []LiveVolume
	Vars       map[string]LiveVar
	Policies   []LivePolicy
}

// LiveEnv is an environment with its services.
type LiveEnv struct {
	ID        string
	Slug      string
	Name      string
	Kind      string
	Protected bool
	Settings  map[string]any
	Services  map[string]*LiveService
	Vars      map[string]LiveVar
}

// LiveTarget is a backup target of the org.
type LiveTarget struct {
	ID     string
	Name   string
	Kind   string
	Config map[string]any
}

// Live is the state of one project that the plan compares with the file.
type Live struct {
	ProjectID    string
	Slug         string
	Settings     map[string]any
	ConfigSource map[string]any
	Envs         map[string]*LiveEnv
	ProjectVars  map[string]LiveVar
	Secrets      []LiveSecret
	Targets      map[string]*LiveTarget
	// Connections maps the name of a Git connection to its id.
	Connections map[string]string
}

// HasSecret tells if a secret with the name exists in one of the scopes: the project, the environment, the service.
func (l *Live) HasSecret(name, envID, serviceID string) bool {
	return l.findSecret(name, envID, serviceID) != nil
}

func (l *Live) findSecret(name, envID, serviceID string) *LiveSecret {
	var best *LiveSecret
	for i := range l.Secrets {
		s := &l.Secrets[i]
		if s.Name != name {
			continue
		}
		switch {
		case s.ServiceID != "" && s.ServiceID == serviceID:
			return s
		case s.ServiceID == "" && s.EnvID != "" && s.EnvID == envID:
			best = s
		case s.ServiceID == "" && s.EnvID == "" && best == nil:
			best = s
		}
	}
	return best
}
