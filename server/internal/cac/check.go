// SPDX-License-Identifier: Apache-2.0

package cac

import (
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

var hostLabelRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// NormalizeHost lower-cases a host and checks the syntax like the domain API does (REQ-DOM-004).
func NormalizeHost(host string) (string, error) {
	host = strings.ToLower(strings.TrimSpace(host))
	switch {
	case host == "":
		return "", fmt.Errorf("host is required")
	case strings.Contains(host, "*"):
		return "", fmt.Errorf("wildcard hosts are not allowed")
	case len(host) > 253:
		return "", fmt.Errorf("host is too long")
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return "", fmt.Errorf("host must be a domain name, not an IP address")
	}
	labels := strings.Split(strings.TrimSuffix(host, "."), ".")
	if len(labels) < 2 {
		return "", fmt.Errorf("host must have at least two labels, for example app.example.com")
	}
	for _, l := range labels {
		if !hostLabelRe.MatchString(l) {
			return "", fmt.Errorf("host label %q is not valid", l)
		}
	}
	return strings.Join(labels, "."), nil
}

// envsOf lists the environments the static checks use: the ones in the file, or one anonymous environment.
func (f *File) envsOf() []string {
	if len(f.Environments) == 0 {
		return []string{""}
	}
	return sortedKeys(f.Environments)
}

func join(parts ...any) string {
	s := make([]string, len(parts))
	for i, p := range parts {
		s[i] = fmt.Sprint(p)
	}
	return strings.Join(s, ".")
}

// checkStatic runs the semantic checks that need no live state (07 section 6): an unknown service in depends_on
// or in an override, domain conflicts, a port that is not exposed, a strategy that does not fit the volumes,
// and the references inside the file.
func (p *Parsed) checkStatic() []Issue {
	f := p.File
	var out []Issue
	add := func(path, msg string) {
		out = append(out, Issue{Line: p.lineOf(strings.Split(path, ".")), Path: path, Message: msg})
	}
	if len(f.Services) == 0 {
		add("services", "the file needs at least one service")
	}
	for _, env := range sortedKeys(f.Environments) {
		for _, name := range sortedKeys(f.Environments[env].Services) {
			if _, ok := f.Services[name]; !ok {
				add(join("environments", env, "services", name), fmt.Sprintf("the service %q is not in services", name))
			}
		}
	}
	targets := map[string]bool{}
	for i, t := range f.BackupTargets {
		if targets[t.Name] {
			add(join("backup_targets", i, "name"), fmt.Sprintf("the backup target %q is named twice", t.Name))
		}
		targets[t.Name] = true
	}
	channels := map[string]bool{}
	if f.Alerts != nil {
		for i, c := range f.Alerts.Channels {
			name, _ := c["name"].(string)
			if channels[name] {
				add(join("alerts", "channels", i, "name"), fmt.Sprintf("the alert channel %q is named twice", name))
			}
			channels[name] = true
		}
		for i, r := range f.Alerts.Rules {
			list, _ := r["channels"].([]any)
			for j, c := range list {
				if name, _ := c.(string); !channels[name] {
					add(join("alerts", "rules", i, "channels", j), fmt.Sprintf("the alert channel %q is not in alerts.channels", name))
				}
			}
		}
	}
	domains := map[string]string{} // host + path -> where
	for _, env := range f.envsOf() {
		for _, name := range sortedKeys(f.Services) {
			eff, _ := f.EffectiveService(env, name)
			if eff.Disabled {
				continue
			}
			where := join("services", name)
			if env != "" && f.Environments[env].Services[name] != nil {
				where = join("environments", env, "services", name)
			}
			out = append(out, p.checkService(eff, where)...)
			for i, d := range eff.Service.Domains {
				host, err := NormalizeHost(d.Host)
				if err != nil {
					continue
				}
				prefix := d.PathPrefix
				if prefix == "" {
					prefix = "/"
				}
				key := host + prefix
				at := join(where, "domains", i, "host")
				if prev, dup := domains[key]; dup && prev != at {
					add(at, fmt.Sprintf("the domain %s%s is used twice in the file (first at %s)", host, prefix, prev))
				}
				domains[key] = at
			}
		}
	}
	return dedupe(out)
}

func dedupe(in []Issue) []Issue {
	seen := map[string]bool{}
	var out []Issue
	for _, i := range in {
		k := i.Path + "|" + i.Message
		if !seen[k] {
			seen[k] = true
			out = append(out, i)
		}
	}
	return out
}

// checkService checks one merged service. where is the path of the block that the file author edits.
func (p *Parsed) checkService(eff *Effective, where string) []Issue {
	f, s := p.File, &eff.Service
	var out []Issue
	add := func(path, msg string) {
		out = append(out, Issue{Line: p.lineOf(strings.Split(path, ".")), Path: path, Message: msg})
	}
	// where may be the base block while the bad field comes from there too. Fields are found by their base path.
	at := func(field string, idx ...any) string {
		path := where + "." + field
		for _, i := range idx {
			path += "." + fmt.Sprint(i)
		}
		if p.lineExists(path) {
			return path
		}
		return join("services", eff.Name, field) + suffix(idx)
	}
	for i, d := range s.DependsOn {
		switch _, ok := f.Services[d]; {
		case d == eff.Name:
			add(at("depends_on", i), "a service cannot depend on itself")
		case !ok:
			add(at("depends_on", i), fmt.Sprintf("depends_on names the service %q, which is not in services", d))
		}
	}
	exposed := map[int]bool{}
	for _, port := range s.Ports {
		exposed[port.Container] = true
	}
	for i, d := range s.Domains {
		if _, err := NormalizeHost(d.Host); err != nil {
			add(at("domains", i, "host"), err.Error())
		}
		switch {
		case d.Port != 0 && !exposed[d.Port]:
			add(at("domains", i, "port"), fmt.Sprintf("port %d is not exposed by the service. Add it to ports", d.Port))
		case d.Port == 0 && len(s.Ports) != 1:
			add(at("domains", i), "the domain needs a port because the service does not have exactly one port")
		}
	}
	vols := map[string]bool{}
	paths := map[string]bool{}
	singleWriter := false
	for i, v := range s.Volumes {
		if vols[v.Name] {
			add(at("volumes", i, "name"), fmt.Sprintf("the volume %q is named twice", v.Name))
		}
		if paths[v.Path] {
			add(at("volumes", i, "path"), fmt.Sprintf("two volumes use the path %q", v.Path))
		}
		vols[v.Name], paths[v.Path] = true, true
		if v.Kind != "tmpfs" && v.AccessMode != "shared" {
			singleWriter = true
		}
	}
	if strat := s.strategy(); singleWriter && (strat == "blue_green" || strat == "canary") && !eff.DeployFromEnv["strategy"] {
		add(at("deploy", "strategy"), fmt.Sprintf("the strategy %s is not allowed with a single-writer volume. Use recreate", strat))
	}
	if b := s.Backup; b != nil {
		if b.Volume != "" && !vols[b.Volume] {
			add(at("backup", "volume"), fmt.Sprintf("backup.volume names the volume %q, which the service does not have", b.Volume))
		}
		if s.Type == "database" && b.Volume != "" {
			add(at("backup", "volume"), "a database is backed up by its dump. Remove backup.volume")
		}
	}
	if s.Type != "database" && s.Source == nil {
		add(at("source"), "the service needs source.git or source.image")
	} else if s.Source != nil && s.Source.Git == nil && s.Source.Image == nil {
		add(at("source"), "source needs git or image")
	}
	if s.Type == "database" && s.Source != nil {
		add(at("source"), "a database has no source. Polipan runs the official image")
	}
	for k, v := range map[string]string{"stop_grace_period": s.StopGracePeriod} {
		if v == "" {
			continue
		}
		if _, err := Seconds(v); err != nil {
			add(at(k), err.Error())
		}
	}
	return out
}

func suffix(idx []any) string {
	var b strings.Builder
	for _, i := range idx {
		b.WriteString("." + fmt.Sprint(i))
	}
	return b.String()
}

// lineExists tells if the path is in the file.
func (p *Parsed) lineExists(path string) bool {
	n := p.lines
	if len(n.Content) > 0 {
		n = n.Content[0]
	}
	for _, seg := range strings.Split(path, ".") {
		_, next := step(n, seg)
		if next == nil {
			return false
		}
		n = next
	}
	return true
}

func (s *Service) strategy() string {
	if s.Deploy == nil {
		return ""
	}
	return s.Deploy.Strategy
}

var _ = strconv.Itoa
