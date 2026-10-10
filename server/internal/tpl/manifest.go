// SPDX-License-Identifier: Apache-2.0

// Package tpl is the template engine (C03): manifests, the safety validator, Compose import and
// the expansion of a template into services. It has no database code. The API turns a Plan into rows.
package tpl

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Manifest is a template (template.schema.json).
type Manifest struct {
	Slug        string                  `yaml:"slug" json:"slug"`
	Version     string                  `yaml:"version" json:"version"`
	Name        string                  `yaml:"name" json:"name"`
	Description string                  `yaml:"description,omitempty" json:"description,omitempty"`
	Icon        string                  `yaml:"icon,omitempty" json:"icon,omitempty"`
	Tags        []string                `yaml:"tags,omitempty" json:"tags,omitempty"`
	Inputs      []Input                 `yaml:"inputs,omitempty" json:"inputs,omitempty"`
	Services    map[string]ServiceEntry `yaml:"services" json:"services"`
}

// Input is a question to the user.
type Input struct {
	Name     string `yaml:"name" json:"name"`
	Label    string `yaml:"label,omitempty" json:"label,omitempty"`
	Type     string `yaml:"type" json:"type"` // string | number | bool | secret | domain
	Default  any    `yaml:"default,omitempty" json:"default,omitempty"`
	Required bool   `yaml:"required,omitempty" json:"required,omitempty"`
	Generate bool   `yaml:"generate,omitempty" json:"generate,omitempty"`
}

// ServiceEntry has the shape of one service of polipan.yaml (07-config-as-code). The keys that a
// template may use are listed here. Any other key is an error, and the unsafe ones are listed by Safety.
type ServiceEntry struct {
	Type        string         `yaml:"type,omitempty" json:"type,omitempty"`
	Source      *Source        `yaml:"source,omitempty" json:"source,omitempty"`
	DB          *DB            `yaml:"db,omitempty" json:"db,omitempty"`
	Ports       []Port         `yaml:"ports,omitempty" json:"ports,omitempty"`
	Command     []string       `yaml:"command,omitempty" json:"command,omitempty"`
	Entrypoint  []string       `yaml:"entrypoint,omitempty" json:"entrypoint,omitempty"`
	Env         map[string]any `yaml:"env,omitempty" json:"env,omitempty"`
	Secrets     []string       `yaml:"secrets,omitempty" json:"secrets,omitempty"`
	Domains     []Domain       `yaml:"domains,omitempty" json:"domains,omitempty"`
	Volumes     []Volume       `yaml:"volumes,omitempty" json:"volumes,omitempty"`
	Files       []File         `yaml:"files,omitempty" json:"files,omitempty"`
	Healthcheck map[string]any `yaml:"healthcheck,omitempty" json:"healthcheck,omitempty"`
	Deploy      map[string]any `yaml:"deploy,omitempty" json:"deploy,omitempty"`
	Resources   map[string]any `yaml:"resources,omitempty" json:"resources,omitempty"`
	Restart     string         `yaml:"restart_policy,omitempty" json:"restart_policy,omitempty"`
	StopGrace   string         `yaml:"stop_grace_period,omitempty" json:"stop_grace_period,omitempty"`
	DependsOn   []string       `yaml:"depends_on,omitempty" json:"depends_on,omitempty"`
}

// Source is the source of a template service. Only images are allowed in templates.
type Source struct {
	Image *struct {
		Name string `yaml:"name" json:"name"`
	} `yaml:"image,omitempty" json:"image,omitempty"`
}

// DB is the db block of a database service.
type DB struct {
	Engine         string `yaml:"engine" json:"engine"`
	Version        string `yaml:"version,omitempty" json:"version,omitempty"`
	ExposeHostPort int    `yaml:"expose_host_port,omitempty" json:"expose_host_port,omitempty"`
}

// Port is a port: a number or {container, protocol, host}.
type Port struct {
	Container int
	Protocol  string
	Host      int
}

// UnmarshalYAML reads a port as a number or as an object.
func (p *Port) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		return n.Decode(&p.Container)
	}
	var o struct {
		Container int    `yaml:"container"`
		Protocol  string `yaml:"protocol"`
		Host      int    `yaml:"host"`
	}
	if err := n.Decode(&o); err != nil {
		return err
	}
	p.Container, p.Protocol, p.Host = o.Container, o.Protocol, o.Host
	return nil
}

// MarshalYAML writes the short form when possible.
func (p Port) MarshalYAML() (any, error) {
	if p.Protocol == "" && p.Host == 0 {
		return p.Container, nil
	}
	return map[string]any{"container": p.Container, "protocol": p.Protocol, "host": p.Host}, nil
}

// MarshalJSON writes the port like MarshalYAML.
func (p Port) MarshalJSON() ([]byte, error) {
	if p.Protocol == "" && p.Host == 0 {
		return []byte(fmt.Sprint(p.Container)), nil
	}
	return []byte(fmt.Sprintf(`{"container":%d,"protocol":%q,"host":%d}`, p.Container, p.Protocol, p.Host)), nil
}

// Domain is a domain of a service.
type Domain struct {
	Host        string `yaml:"host" json:"host"`
	Port        int    `yaml:"port,omitempty" json:"port,omitempty"`
	PathPrefix  string `yaml:"path_prefix,omitempty" json:"path_prefix,omitempty"`
	HTTPS       *bool  `yaml:"https,omitempty" json:"https,omitempty"`
	RedirectWWW string `yaml:"redirect_www,omitempty" json:"redirect_www,omitempty"`
}

// Volume is a volume of a service.
type Volume struct {
	Name       string `yaml:"name" json:"name"`
	Path       string `yaml:"path" json:"path"`
	Kind       string `yaml:"kind,omitempty" json:"kind,omitempty"`
	AccessMode string `yaml:"access_mode,omitempty" json:"access_mode,omitempty"`
	SizeLimit  string `yaml:"size_limit,omitempty" json:"size_limit,omitempty"`
}

// File is a small config file mounted read-only.
type File struct {
	Path    string `yaml:"path" json:"path"`
	Content string `yaml:"content" json:"content"`
}

var (
	slugRe      = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)
	versionRe   = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	inputNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	svcNameRe   = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)
	inputTypes  = map[string]bool{"string": true, "number": true, "bool": true, "secret": true, "domain": true}
)

// Problem is one reason a manifest or a Compose file is refused.
type Problem struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

func (p Problem) String() string {
	if p.Path == "" {
		return p.Message
	}
	return p.Path + ": " + p.Message
}

// Problems is a list of problems. It is an error.
type Problems []Problem

func (ps Problems) Error() string {
	parts := make([]string, len(ps))
	for i, p := range ps {
		parts[i] = p.String()
	}
	return strings.Join(parts, "; ")
}

// UnsafeError is a manifest with unsafe settings (422 template_unsafe, REQ-TPL-005).
type UnsafeError struct{ Violations Problems }

func (e *UnsafeError) Error() string { return "template is not safe: " + e.Violations.Error() }

// ParseManifest reads a manifest from YAML (or JSON). Unknown keys are errors, except the unsafe
// keys: they are read as a raw document and listed by the safety validator first (AC-TPL-02).
func ParseManifest(data []byte) (*Manifest, error) {
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, Problems{{Message: "the manifest is not valid YAML: " + err.Error()}}
	}
	if v := SafetyOfManifest(raw); len(v) > 0 {
		return nil, &UnsafeError{Violations: v}
	}
	var m Manifest
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil && !errors.Is(err, io.EOF) {
		return nil, Problems{{Message: "the manifest is not valid: " + err.Error()}}
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Validate checks the rules of REQ-TPL-001 that the YAML shape does not check.
func (m *Manifest) Validate() error {
	var ps Problems
	add := func(path, format string, a ...any) {
		ps = append(ps, Problem{Path: path, Message: fmt.Sprintf(format, a...)})
	}
	if !slugRe.MatchString(m.Slug) {
		add("slug", "must match ^[a-z0-9-]{1,40}$")
	}
	if !versionRe.MatchString(m.Version) {
		add("version", "must be like 1.0.0")
	}
	if strings.TrimSpace(m.Name) == "" {
		add("name", "is required")
	}
	seen := map[string]bool{}
	for i, in := range m.Inputs {
		path := fmt.Sprintf("inputs[%d]", i)
		switch {
		case !inputNameRe.MatchString(in.Name):
			add(path+".name", "must match ^[a-z][a-z0-9_]*$")
		case seen[in.Name]:
			add(path+".name", "%s is used twice", in.Name)
		}
		seen[in.Name] = true
		if !inputTypes[in.Type] {
			add(path+".type", "must be string, number, bool, secret or domain")
		}
		if in.Generate && in.Type != "secret" {
			add(path+".generate", "only secret inputs can be generated")
		}
	}
	if len(m.Services) == 0 {
		add("services", "needs at least one service")
	}
	for _, name := range sortedKeys(m.Services) {
		s := m.Services[name]
		path := "services." + name
		if !svcNameRe.MatchString(name) {
			add(path, "the service name must match ^[a-z0-9-]{1,40}$")
		}
		switch s.Type {
		case "", "app", "worker", "database":
		default:
			add(path+".type", "templates can have app, worker and database services")
		}
		switch {
		case s.Type == "database" && s.DB == nil:
			add(path+".db", "is required for a database service")
		case s.Type != "database" && s.DB != nil:
			add(path+".db", "is only for a database service")
		case s.Type != "database" && (s.Source == nil || s.Source.Image == nil || s.Source.Image.Name == ""):
			add(path+".source.image.name", "is required (templates run images)")
		}
		for _, d := range s.DependsOn {
			if _, ok := m.Services[d]; !ok {
				add(path+".depends_on", "unknown service %s", d)
			}
		}
	}
	if len(ps) > 0 {
		return ps
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
