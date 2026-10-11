// SPDX-License-Identifier: Apache-2.0

package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"

	"github.com/polipan/polipan/api/gen"
	"github.com/polipan/polipan/server/internal/logging"
	"github.com/polipan/polipan/server/internal/orchestrator"
	"github.com/polipan/polipan/server/internal/store"
)

// domainOptions are the per-domain settings that are stored in domains.options.
type domainOptions struct {
	MaxBodyBytes    *int      `json:"max_body_bytes,omitempty"`
	IPAllow         *[]string `json:"ip_allow,omitempty"`
	BasicAuthSecret *string   `json:"basic_auth_secret,omitempty"`
}

func (h *Handlers) domainJSON(d *store.Domain, ingressIPs []string) gen.Domain {
	tls := gen.DomainTlsStatus(d.TLSStatus)
	out := gen.Domain{Id: &d.ID, ServiceId: &d.ServiceID, Host: &d.Host, PathPrefix: &d.PathPrefix, ContainerPort: &d.ContainerPort,
		Https: &d.HTTPS, TlsStatus: &tls, TlsExpiresAt: d.TLSExpiresAt, TlsError: d.TLSError}
	if d.RedirectWWW != nil {
		v := gen.DomainRedirectWww(*d.RedirectWWW)
		out.RedirectWww = &v
	}
	if d.IngressNodeIDs != nil {
		out.IngressNodeIds = &d.IngressNodeIDs
	}
	// The user creates one A or AAAA record per ingress node (REQ-DOM-003).
	records := []struct {
		Name  *string `json:"name,omitempty"`
		Type  *string `json:"type,omitempty"`
		Value *string `json:"value,omitempty"`
	}{}
	for _, ip := range ingressIPs {
		typ := "A"
		if a, err := netip.ParseAddr(ip); err == nil && a.Is6() {
			typ = "AAAA"
		}
		records = append(records, struct {
			Name  *string `json:"name,omitempty"`
			Type  *string `json:"type,omitempty"`
			Value *string `json:"value,omitempty"`
		}{Name: ptr(d.Host), Type: ptr(typ), Value: ptr(ip)})
	}
	out.DnsRecords = &records
	return out
}

func (h *Handlers) ingressIPs(r *http.Request) []string {
	_, ips, err := h.Store.ReadyNodes(r.Context(), h.orgID(r))
	if err != nil {
		logging.From(r.Context()).Warn("read ingress nodes failed", "error", err)
	}
	return ips
}

// ListDomains lists the domains of a service.
func (h *Handlers) ListDomains(w http.ResponseWriter, r *http.Request, serviceID gen.ServiceId) {
	if _, err := h.Store.GetService(r.Context(), h.orgID(r), serviceID); fail(w, r, err) {
		return
	}
	list, err := h.Store.ListDomains(r.Context(), h.orgID(r), serviceID)
	if fail(w, r, err) {
		return
	}
	ips := h.ingressIPs(r)
	out := make([]gen.Domain, len(list))
	for i := range list {
		out[i] = h.domainJSON(&list[i], ips)
	}
	WriteJSON(w, http.StatusOK, out)
}

// applyDomainWrite copies the fields of a request into a domain after checking them.
// It writes the 422 answer and returns false when a field is bad.
func applyDomainWrite(w http.ResponseWriter, r *http.Request, d *store.Domain, body *gen.DomainWrite) bool {
	if body.Host != nil {
		host, err := normalizeHost(*body.Host)
		if err != nil {
			invalid(w, r, "host", err.Error())
			return false
		}
		if host != d.Host {
			// A new host needs a new certificate.
			d.TLSStatus, d.TLSError, d.TLSExpiresAt = "pending", nil, nil
		}
		d.Host = host
	}
	if body.PathPrefix != nil {
		if !validPathPrefix(*body.PathPrefix) {
			invalid(w, r, "path_prefix", "path_prefix must start with / and have no spaces, ? or #")
			return false
		}
		d.PathPrefix = *body.PathPrefix
	}
	if body.ContainerPort != nil {
		if *body.ContainerPort < 1 || *body.ContainerPort > 65535 {
			invalid(w, r, "container_port", "container_port must be from 1 to 65535")
			return false
		}
		d.ContainerPort = *body.ContainerPort
	}
	if body.Https != nil {
		d.HTTPS = *body.Https
	}
	if body.RedirectWww != nil {
		v := string(*body.RedirectWww)
		if v != "to_www" && v != "to_apex" {
			invalid(w, r, "redirect_www", "redirect_www must be to_www or to_apex")
			return false
		}
		d.RedirectWWW = &v
	}
	if body.IngressNodeIds != nil {
		d.IngressNodeIDs = *body.IngressNodeIds
	}
	var opts domainOptions
	_ = json.Unmarshal(d.Options, &opts)
	if body.MaxBodyBytes != nil {
		if *body.MaxBodyBytes < 1 {
			invalid(w, r, "max_body_bytes", "max_body_bytes must be 1 or more")
			return false
		}
		opts.MaxBodyBytes = body.MaxBodyBytes
	}
	if body.IpAllow != nil {
		for _, v := range *body.IpAllow {
			if _, err := netip.ParsePrefix(v); err != nil {
				if _, err := netip.ParseAddr(v); err != nil {
					invalid(w, r, "ip_allow", "ip_allow must have IP addresses or CIDR ranges")
					return false
				}
			}
		}
		opts.IPAllow = body.IpAllow
	}
	if body.BasicAuthSecret != nil {
		opts.BasicAuthSecret = body.BasicAuthSecret
	}
	d.Options, _ = json.Marshal(opts)
	return true
}

func (h *Handlers) domainInUse(w http.ResponseWriter, r *http.Request, d *store.Domain) {
	WriteError(w, r, http.StatusConflict, CodeDomainInUse, "this host and path are already used by a domain",
		map[string]any{"host": d.Host, "path_prefix": d.PathPrefix})
}

// domainsChanged tells the orchestrator. It starts no container and makes no release (AC-DOM-03).
// The domain row is already saved, so an error is logged and the answer stays successful.
func (h *Handlers) domainsChanged(r *http.Request, serviceID string) {
	if err := h.Orch.DomainsChanged(r.Context(), serviceID); err != nil {
		logging.From(r.Context()).Error("domains changed failed", "service_id", serviceID, "error", err)
	}
}

// CreateDomain adds a domain to a service. A used (host, path_prefix) gives 409 domain_in_use (AC-DOM-02).
func (h *Handlers) CreateDomain(w http.ResponseWriter, r *http.Request, serviceID gen.ServiceId) {
	var body gen.DomainWrite
	if !decodeJSON(w, r, &body) {
		return
	}
	s, err := h.Store.GetService(r.Context(), h.orgID(r), serviceID)
	if fail(w, r, err) {
		return
	}
	if denyProtected(w, r, "domain:manage", s.EnvProtected) {
		return
	}
	if h.denyManaged(w, r, serviceID, "domains") {
		return
	}
	if body.Host == nil {
		invalid(w, r, "host", "host is required")
		return
	}
	d := store.Domain{ServiceID: serviceID, PathPrefix: "/", HTTPS: true, TLSStatus: "pending", ContainerPort: configPort(s.Config)}
	if !applyDomainWrite(w, r, &d, &body) || h.refusePanelHost(w, r, d.Host) {
		return
	}
	if d.ContainerPort == 0 {
		invalid(w, r, "container_port", "container_port is required when the service has no port")
		return
	}
	created, err := h.Store.CreateDomain(r.Context(), d)
	if unique, _ := store.IsUnique(err); unique {
		h.domainInUse(w, r, &d)
		return
	}
	if fail(w, r, err) {
		return
	}
	h.domainsChanged(r, serviceID)
	WriteJSON(w, http.StatusCreated, h.domainJSON(created, h.ingressIPs(r)))
}

// UpdateDomain changes a domain.
func (h *Handlers) UpdateDomain(w http.ResponseWriter, r *http.Request, domainID gen.DomainId) {
	var body gen.DomainWrite
	if !decodeJSON(w, r, &body) {
		return
	}
	d, protected, err := h.Store.GetDomain(r.Context(), h.orgID(r), domainID)
	if fail(w, r, err) {
		return
	}
	if denyProtected(w, r, "domain:manage", protected) {
		return
	}
	if h.denyManaged(w, r, d.ServiceID, "domains") {
		return
	}
	if !applyDomainWrite(w, r, d, &body) || h.refusePanelHost(w, r, d.Host) {
		return
	}
	updated, err := h.Store.UpdateDomain(r.Context(), *d)
	if unique, _ := store.IsUnique(err); unique {
		h.domainInUse(w, r, d)
		return
	}
	if fail(w, r, err) {
		return
	}
	h.domainsChanged(r, d.ServiceID)
	WriteJSON(w, http.StatusOK, h.domainJSON(updated, h.ingressIPs(r)))
}

// DeleteDomain removes a domain.
func (h *Handlers) DeleteDomain(w http.ResponseWriter, r *http.Request, domainID gen.DomainId) {
	d, protected, err := h.Store.GetDomain(r.Context(), h.orgID(r), domainID)
	if fail(w, r, err) {
		return
	}
	if denyProtected(w, r, "domain:manage", protected) {
		return
	}
	if h.denyManaged(w, r, d.ServiceID, "domains") {
		return
	}
	if fail(w, r, h.Store.DeleteDomain(r.Context(), domainID)) {
		return
	}
	h.domainsChanged(r, d.ServiceID)
	w.WriteHeader(http.StatusNoContent)
}

// refusePanelHost answers 409 domain_in_use for the host of the panel and its www or apex partner,
// so a service can never take the traffic of the panel (logins, session cookies).
func (h *Handlers) refusePanelHost(w http.ResponseWriter, r *http.Request, host string) bool {
	if !orchestrator.IsPanelHost(h.Settings.ExternalURL, host) {
		return false
	}
	WriteError(w, r, http.StatusConflict, CodeDomainInUse, "this host is used by the panel", map[string]any{"host": host})
	return true
}

// MoveDomain gives a domain to another service of the org in one change. The source and the target
// environment must both allow the caller (protected environments need an admin), and neither service may
// have its domains set by polipan.yaml.
func (h *Handlers) MoveDomain(w http.ResponseWriter, r *http.Request, domainID gen.DomainId) {
	var body gen.MoveDomainJSONBody
	if !decodeJSON(w, r, &body) {
		return
	}
	d, protected, err := h.Store.GetDomain(r.Context(), h.orgID(r), domainID)
	if fail(w, r, err) {
		return
	}
	target, err := h.Store.GetService(r.Context(), h.orgID(r), body.TargetServiceId)
	if errors.Is(err, store.ErrNotFound) {
		invalid(w, r, "target_service_id", "the target service does not exist")
		return
	}
	if fail(w, r, err) {
		return
	}
	// The guard checked the project of the domain. The target service can be in another project: check the role of
	// that project, its protected flag and the project limit of an API token there too.
	if denyProtected(w, r, "domain:manage", protected) || h.denyInProject(w, r, target.ProjectID, "domain:manage", target.EnvProtected) {
		return
	}
	if h.denyManaged(w, r, d.ServiceID, "domains") || h.denyManaged(w, r, target.ID, "domains") {
		return
	}
	if target.ID == d.ServiceID {
		WriteJSON(w, http.StatusOK, h.domainJSON(d, h.ingressIPs(r)))
		return
	}
	moved, from, err := h.Store.MoveDomain(r.Context(), h.orgID(r), domainID, target.ID, configPort(target.Config))
	if fail(w, r, err) {
		return
	}
	h.domainsChanged(r, from)
	h.domainsChanged(r, target.ID)
	WriteJSON(w, http.StatusOK, h.domainJSON(moved, h.ingressIPs(r)))
}
