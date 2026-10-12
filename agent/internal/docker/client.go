// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Docker Engine 25 and newer speak API 1.44. Docker 29 refuses clients older than 1.44.
const apiPrefix = "http://docker/v1.44"

// HTTPClient is the real Docker client. It speaks the Engine API over a unix socket.
type HTTPClient struct {
	hc     *http.Client
	socket string
	// IPNetwork is the network whose address is returned in Container.IP.
	IPNetwork string
}

var _ Client = (*HTTPClient)(nil)

// New creates a client for the given unix socket, for example /var/run/docker.sock.
func New(socket string) *HTTPClient {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
	}
	return &HTTPClient{hc: &http.Client{Transport: tr}, socket: socket, IPNetwork: "polipan"}
}

// do sends a request. A status of 400 or more becomes an error.
func (c *HTTPClient) do(ctx context.Context, method, path string, q url.Values, body any, hdr map[string]string) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	u := apiPrefix + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if resp.StatusCode >= 400 {
		defer func() { _ = resp.Body.Close() }()
		var m struct {
			Message string `json:"message"`
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		_ = json.Unmarshal(raw, &m)
		if m.Message == "" {
			m.Message = strings.TrimSpace(string(raw))
		}
		if resp.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, m.Message)
		}
		return nil, fmt.Errorf("docker: %s %s: status %d: %s", method, path, resp.StatusCode, m.Message)
	}
	return resp, nil
}

func (c *HTTPClient) getJSON(ctx context.Context, path string, q url.Values, out any) error {
	resp, err := c.do(ctx, http.MethodGet, path, q, nil, nil)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *HTTPClient) post(ctx context.Context, path string, q url.Values, body any) error {
	resp, err := c.do(ctx, http.MethodPost, path, q, body, nil)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.Body.Close()
}

// Ping returns the Docker server version.
func (c *HTTPClient) Ping(ctx context.Context) (string, error) {
	var v struct {
		Version string `json:"Version"`
	}
	if err := c.getJSON(ctx, "/version", nil, &v); err != nil {
		return "", err
	}
	return v.Version, nil
}

type networkInspect struct {
	IPAM struct {
		Config []struct {
			Subnet  string `json:"Subnet"`
			Gateway string `json:"Gateway"`
		} `json:"Config"`
	} `json:"IPAM"`
}

func gatewayOf(n networkInspect) string {
	for _, cfg := range n.IPAM.Config {
		if cfg.Gateway != "" {
			return cfg.Gateway
		}
		if _, ipn, err := net.ParseCIDR(cfg.Subnet); err == nil {
			ip := ipn.IP.To4()
			if ip != nil {
				g := make(net.IP, 4)
				copy(g, ip)
				g[3]++
				return g.String()
			}
		}
	}
	return ""
}

// NetworkIPs returns the addresses that containers use on a network.
func (c *HTTPClient) NetworkIPs(ctx context.Context, name string) (map[string]bool, error) {
	var n struct {
		Containers map[string]struct {
			IPv4Address string `json:"IPv4Address"`
		} `json:"Containers"`
	}
	if err := c.getJSON(ctx, "/networks/"+url.PathEscape(name), nil, &n); err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, ct := range n.Containers {
		if ip, _, err := net.ParseCIDR(ct.IPv4Address); err == nil {
			out[ip.String()] = true
		}
	}
	return out, nil
}

// NetworkSubnet returns the first subnet of a network, or "" when it has none.
func (c *HTTPClient) NetworkSubnet(ctx context.Context, name string) (string, error) {
	var n networkInspect
	if err := c.getJSON(ctx, "/networks/"+url.PathEscape(name), nil, &n); err != nil {
		return "", err
	}
	for _, cfg := range n.IPAM.Config {
		if cfg.Subnet != "" {
			return cfg.Subnet, nil
		}
	}
	return "", nil
}

// EnsureNetwork creates the bridge network if it is missing. An existing network is not changed.
func (c *HTTPClient) EnsureNetwork(ctx context.Context, name, subnet string) (string, error) {
	var n networkInspect
	err := c.getJSON(ctx, "/networks/"+url.PathEscape(name), nil, &n)
	if err == nil {
		return gatewayOf(n), nil
	}
	if !isNotFound(err) {
		return "", err
	}
	body := map[string]any{"Name": name, "Driver": "bridge", "CheckDuplicate": true}
	if subnet != "" {
		cfg := map[string]string{"Subnet": subnet}
		// The lower half of a /24 is for the fixed addresses of the server (.2 Caddy, .10 up for workloads).
		// Docker gives its own addresses from the upper half only, so they never meet.
		if _, ipn, err := net.ParseCIDR(subnet); err == nil && ipn.IP.To4() != nil {
			if ones, bits := ipn.Mask.Size(); ones == 24 && bits == 32 {
				cfg["IPRange"] = fmt.Sprintf("%d.%d.%d.128/25", ipn.IP[0], ipn.IP[1], ipn.IP[2])
				// Without it Docker takes the gateway from the range: the bridge must have .1, the mesh address of the node.
				cfg["Gateway"] = fmt.Sprintf("%d.%d.%d.1", ipn.IP[0], ipn.IP[1], ipn.IP[2])
			}
		}
		body["IPAM"] = map[string]any{"Config": []map[string]string{cfg}}
	}
	if err := c.post(ctx, "/networks/create", nil, body); err != nil {
		return "", err
	}
	if err := c.getJSON(ctx, "/networks/"+url.PathEscape(name), nil, &n); err != nil {
		return "", err
	}
	return gatewayOf(n), nil
}

func isNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), ErrNotFound.Error())
}

// repoOf returns the repository part of an image reference, without tag and digest.
func repoOf(ref string) string {
	if i := strings.Index(ref, "@"); i >= 0 {
		ref = ref[:i]
	}
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		ref = ref[:i]
	}
	return ref
}

// PullImage pulls ref and returns repo@sha256:digest. registryAuth is the X-Registry-Auth
// value (see RegistryAuth), empty for a public image.
func (c *HTTPClient) PullImage(ctx context.Context, ref, registryAuth string) (string, error) {
	hdr := map[string]string{}
	if registryAuth != "" {
		hdr["X-Registry-Auth"] = registryAuth
	}
	resp, err := c.do(ctx, http.MethodPost, "/images/create", url.Values{"fromImage": {ref}}, nil, hdr)
	if err != nil {
		return "", err
	}
	dec := json.NewDecoder(resp.Body)
	for {
		var m struct {
			Error string `json:"error"`
		}
		if err := dec.Decode(&m); err != nil {
			_ = resp.Body.Close()
			if err == io.EOF {
				break
			}
			return "", err
		}
		if m.Error != "" {
			_ = resp.Body.Close()
			return "", fmt.Errorf("docker: pull %s: %s", ref, m.Error)
		}
	}
	var img struct {
		ID          string   `json:"Id"`
		RepoDigests []string `json:"RepoDigests"`
	}
	if err := c.getJSON(ctx, "/images/"+ref+"/json", nil, &img); err != nil {
		return "", err
	}
	repo := repoOf(ref)
	for _, d := range img.RepoDigests {
		r := repoOf(d)
		if r == repo || strings.HasSuffix(r, "/"+repo) || strings.HasSuffix(repo, "/"+r) {
			return d, nil
		}
	}
	if len(img.RepoDigests) > 0 {
		return img.RepoDigests[0], nil
	}
	return repo + "@" + img.ID, nil
}

// ImageRemove removes an image. A missing image is not an error.
func (c *HTTPClient) ImageRemove(ctx context.Context, ref string) error {
	resp, err := c.do(ctx, http.MethodDelete, "/images/"+ref, url.Values{"force": {"false"}}, nil, nil)
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return err
	}
	return resp.Body.Close()
}

// CreateAndStart creates and starts a container.
func (c *HTTPClient) CreateAndStart(ctx context.Context, spec ContainerSpec) (string, error) {
	body := createBody(spec)
	resp, err := c.do(ctx, http.MethodPost, "/containers/create", url.Values{"name": {spec.Name}}, body, nil)
	if err != nil {
		return "", err
	}
	var out struct {
		ID string `json:"Id"`
	}
	err = json.NewDecoder(resp.Body).Decode(&out)
	_ = resp.Body.Close()
	if err != nil {
		return "", err
	}
	if err := c.Start(ctx, out.ID); err != nil {
		return out.ID, err
	}
	return out.ID, nil
}

func createBody(spec ContainerSpec) map[string]any {
	var env []string
	for k, v := range spec.Env {
		env = append(env, k+"="+v)
	}
	exposed := map[string]struct{}{}
	pb := map[string][]map[string]string{}
	for _, p := range spec.Ports {
		proto := p.Proto
		if proto == "" {
			proto = "tcp"
		}
		key := strconv.Itoa(p.ContainerPort) + "/" + proto
		exposed[key] = struct{}{}
		if p.HostPort > 0 {
			pb[key] = append(pb[key], map[string]string{"HostIp": p.HostIP, "HostPort": strconv.Itoa(p.HostPort)})
		}
	}
	host := map[string]any{
		"Binds":        spec.Binds,
		"PortBindings": pb,
		"NanoCpus":     spec.NanoCPUs,
		"Memory":       spec.MemoryBytes,
		"CapAdd":       spec.CapAdd,
		"CapDrop":      spec.CapDrop,
		"Dns":          spec.DNS,
		"DnsSearch":    dnsSearch(spec.DNS),
		"DnsOptions":   dnsOptions(spec.DNS),
	}
	if spec.RestartPolicy != "" {
		host["RestartPolicy"] = map[string]any{"Name": spec.RestartPolicy}
	}
	if len(spec.Mounts) > 0 {
		var ms []map[string]any
		for _, m := range spec.Mounts {
			e := map[string]any{"Type": m.Type, "Target": m.Target, "ReadOnly": m.ReadOnly}
			if m.Source != "" {
				e["Source"] = m.Source
			}
			if m.Type == "tmpfs" && m.TmpfsSize > 0 {
				e["TmpfsOptions"] = map[string]any{"SizeBytes": m.TmpfsSize}
			}
			ms = append(ms, e)
		}
		host["Mounts"] = ms
	}
	if spec.Network != "" {
		host["NetworkMode"] = spec.Network
	}
	body := map[string]any{
		"Image":        spec.Image,
		"Env":          env,
		"Labels":       spec.Labels,
		"ExposedPorts": exposed,
		"HostConfig":   host,
	}
	if len(spec.Cmd) > 0 {
		body["Cmd"] = spec.Cmd
	}
	if len(spec.Entrypoint) > 0 {
		body["Entrypoint"] = spec.Entrypoint
	}
	if spec.StopTimeout > 0 {
		body["StopTimeout"] = spec.StopTimeout
	}
	if spec.Network != "" && spec.Network != "host" && (len(spec.Aliases) > 0 || spec.IP != "") {
		ep := map[string]any{}
		if len(spec.Aliases) > 0 {
			ep["Aliases"] = spec.Aliases
		}
		if spec.IP != "" {
			ep["IPAMConfig"] = map[string]any{"IPv4Address": spec.IP}
		}
		body["NetworkingConfig"] = map[string]any{"EndpointsConfig": map[string]any{spec.Network: ep}}
	}
	return body
}

func (c *HTTPClient) Start(ctx context.Context, id string) error {
	return c.post(ctx, "/containers/"+url.PathEscape(id)+"/start", nil, nil)
}

func (c *HTTPClient) Stop(ctx context.Context, id string, grace int) error {
	return c.post(ctx, "/containers/"+url.PathEscape(id)+"/stop", url.Values{"t": {strconv.Itoa(grace)}}, nil)
}

func (c *HTTPClient) Restart(ctx context.Context, id string, grace int) error {
	return c.post(ctx, "/containers/"+url.PathEscape(id)+"/restart", url.Values{"t": {strconv.Itoa(grace)}}, nil)
}

func (c *HTTPClient) SetRestartPolicy(ctx context.Context, id, policy string) error {
	body := map[string]any{"RestartPolicy": map[string]any{"Name": policy}}
	return c.post(ctx, "/containers/"+url.PathEscape(id)+"/update", nil, body)
}

func (c *HTTPClient) Remove(ctx context.Context, id string, force bool) error {
	resp, err := c.do(ctx, http.MethodDelete, "/containers/"+url.PathEscape(id), url.Values{"force": {strconv.FormatBool(force)}}, nil, nil)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

type netSettings struct {
	Networks map[string]struct {
		IPAddress string `json:"IPAddress"`
	} `json:"Networks"`
}

func (c *HTTPClient) ipOf(ns netSettings) string {
	if n, ok := ns.Networks[c.IPNetwork]; ok {
		return n.IPAddress
	}
	return ""
}

// List returns all containers with every label pair in labels.
func (c *HTTPClient) List(ctx context.Context, labels map[string]string) ([]Container, error) {
	var lf []string
	for k, v := range labels {
		if v == "" {
			lf = append(lf, k)
		} else {
			lf = append(lf, k+"="+v)
		}
	}
	filters, _ := json.Marshal(map[string][]string{"label": lf})
	q := url.Values{"all": {"1"}}
	if len(lf) > 0 {
		q.Set("filters", string(filters))
	}
	var items []struct {
		ID              string            `json:"Id"`
		Names           []string          `json:"Names"`
		Image           string            `json:"Image"`
		ImageID         string            `json:"ImageID"`
		State           string            `json:"State"`
		Labels          map[string]string `json:"Labels"`
		NetworkSettings netSettings       `json:"NetworkSettings"`
	}
	if err := c.getJSON(ctx, "/containers/json", q, &items); err != nil {
		return nil, err
	}
	out := make([]Container, 0, len(items))
	for _, it := range items {
		name := ""
		if len(it.Names) > 0 {
			name = strings.TrimPrefix(it.Names[0], "/")
		}
		out = append(out, Container{ID: it.ID, Name: name, Image: it.Image, ImageID: it.ImageID,
			State: it.State, Labels: it.Labels, IP: c.ipOf(it.NetworkSettings)})
	}
	return out, nil
}

// Inspect returns one container. A missing one gives ErrNotFound.
func (c *HTTPClient) Inspect(ctx context.Context, idOrName string) (*Container, error) {
	var r struct {
		ID           string `json:"Id"`
		Name         string `json:"Name"`
		Image        string `json:"Image"`
		RestartCount int    `json:"RestartCount"`
		State        struct {
			Status    string `json:"Status"`
			ExitCode  int    `json:"ExitCode"`
			StartedAt string `json:"StartedAt"`
			Health    *struct {
				Status string `json:"Status"`
			} `json:"Health"`
		} `json:"State"`
		Config struct {
			Image  string            `json:"Image"`
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
		NetworkSettings netSettings `json:"NetworkSettings"`
	}
	if err := c.getJSON(ctx, "/containers/"+url.PathEscape(idOrName)+"/json", nil, &r); err != nil {
		return nil, err
	}
	ct := &Container{ID: r.ID, Name: strings.TrimPrefix(r.Name, "/"), Image: r.Config.Image, ImageID: r.Image,
		State: r.State.Status, ExitCode: r.State.ExitCode, Labels: r.Config.Labels, IP: c.ipOf(r.NetworkSettings),
		RestartCount: r.RestartCount}
	if t, err := time.Parse(time.RFC3339Nano, r.State.StartedAt); err == nil && t.Year() > 1 {
		ct.StartedAt = t
	}
	if r.State.Health != nil {
		ct.Health = r.State.Health.Status
	}
	return ct, nil
}

// Logs returns the last tail lines of stdout and stderr.
func (c *HTTPClient) Logs(ctx context.Context, idOrName string, tail int) (string, error) {
	q := url.Values{"stdout": {"1"}, "stderr": {"1"}, "tail": {strconv.Itoa(tail)}}
	resp, err := c.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(idOrName)+"/logs", q, nil, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	return demux(resp.Body)
}

// demux reads the Docker multiplexed stream: 8 byte header (stream, 0,0,0, size big endian) and a payload.
func demux(r io.Reader) (string, error) {
	var out bytes.Buffer
	br := bufio.NewReader(r)
	for {
		hdr := make([]byte, 8)
		if _, err := io.ReadFull(br, hdr); err != nil {
			if err == io.EOF {
				return out.String(), nil
			}
			return out.String(), err
		}
		n := binary.BigEndian.Uint32(hdr[4:])
		if _, err := io.CopyN(&out, br, int64(n)); err != nil {
			return out.String(), err
		}
	}
}

// Events streams container events until ctx ends.
func (c *HTTPClient) Events(ctx context.Context) (<-chan Event, error) {
	filters, _ := json.Marshal(map[string][]string{
		"type":  {"container"},
		"event": {"start", "die", "oom", "stop", "destroy", "restart"},
	})
	resp, err := c.do(ctx, http.MethodGet, "/events", url.Values{"filters": {string(filters)}}, nil, nil) //nolint:bodyclose // the body is closed when the event stream ends
	if err != nil {
		return nil, err
	}
	ch := make(chan Event, 64)
	go func() {
		defer close(ch)
		defer func() { _ = resp.Body.Close() }()
		dec := json.NewDecoder(resp.Body)
		for {
			var e struct {
				Action string `json:"Action"`
				Actor  struct {
					ID         string            `json:"ID"`
					Attributes map[string]string `json:"Attributes"`
				} `json:"Actor"`
				TimeNano int64 `json:"timeNano"`
			}
			if err := dec.Decode(&e); err != nil {
				return
			}
			code, _ := strconv.Atoi(e.Actor.Attributes["exitCode"])
			ev := Event{ContainerID: e.Actor.ID, Name: e.Actor.Attributes["name"], Action: e.Action,
				ExitCode: code, Labels: e.Actor.Attributes, Time: time.Unix(0, e.TimeNano)}
			select {
			case ch <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

// dnsSearch gives a container that uses the agent DNS the search domain of the mesh. Docker does not forward a name
// with one label (svc-abc) to the agent, but it forwards svc-abc.polipan.internal. With ndots:1 (see dnsOptions)
// the resolver asks for that name first.
func dnsSearch(servers []string) []string {
	if len(servers) == 0 {
		return nil
	}
	return []string{"polipan.internal"}
}

func dnsOptions(servers []string) []string {
	if len(servers) == 0 {
		return nil
	}
	return []string{"ndots:1"}
}
