// SPDX-License-Identifier: Apache-2.0

package ingress

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/polipan/polipan/proto/agentv1"
)

const (
	// AdminListen is the Caddy admin address inside the container. It is a unix socket, never TCP (REQ-AGENT-010).
	AdminListen = "unix//run/caddy-admin/admin.sock|0660"

	// AccessLogger is the name of the access logger of the server and of its log writer.
	AccessLogger = "access"

	defaultMaxBody = 100 << 20 // 100 MiB (REQ-DOM-008)

	letsEncryptCA = "https://acme-v02.api.letsencrypt.org/directory"
	zeroSSLCA     = "https://acme.zerossl.com/v2/DV90"

	stoppedPage = `<!doctype html><html><head><meta charset="utf-8"><title>Polipan</title></head>` +
		`<body style="font-family:sans-serif;text-align:center;padding-top:15vh">` +
		`<h1>Polipan</h1><p>Service is stopped or not deployed yet</p></body></html>`
)

// Options changes how BuildConfig works.
type Options struct {
	ACMECAURL        string // when set, the only ACME issuer is this directory URL
	ACMETrustedRoots string // PEM file (path inside the Caddy container) that trusts ACMECAURL
	MaxBodyBytes     int64  // request body limit, 0 = 100 MiB
	// DNS01 turns on wildcard certificates: a host with one label under a wildcard domain is served with the one
	// certificate of the wildcard (REQ-DOM-007, REQ-PREV-005). Nil: every host gets its own certificate.
	DNS01 *DNS01
}

// DNS01 is the DNS-01 provider for wildcard certificates.
type DNS01 struct {
	Provider  string         // the name of the Caddy DNS module, for example cloudflare
	Config    map[string]any // the fields of the module (the credentials)
	Wildcards []string       // for example *.preview.example.com
}

// ParseDNS01 reads the settings of the agent. An empty provider or an empty wildcard list gives nil.
func ParseDNS01(provider, config, wildcards string) (*DNS01, error) {
	var list []string
	for _, w := range strings.Split(wildcards, ",") {
		w = strings.ToLower(strings.TrimSpace(w))
		if w == "" {
			continue
		}
		if !strings.HasPrefix(w, "*.") || strings.Contains(w[2:], "*") || len(w) < 5 {
			return nil, errors.New("ingress: a wildcard domain looks like *.preview.example.com: " + w)
		}
		list = append(list, w)
	}
	if strings.TrimSpace(provider) == "" || len(list) == 0 {
		return nil, nil
	}
	d := &DNS01{Provider: strings.TrimSpace(provider), Wildcards: list, Config: map[string]any{}}
	if strings.TrimSpace(config) != "" {
		if err := json.Unmarshal([]byte(config), &d.Config); err != nil {
			return nil, errors.New("ingress: the DNS-01 config is not a JSON object")
		}
	}
	return d, nil
}

// wildcardOf gives the wildcard that covers a host: one label before the domain of the wildcard.
func (d *DNS01) wildcardOf(host string) string {
	if d == nil {
		return ""
	}
	for _, w := range d.Wildcards {
		suffix := w[1:] // .preview.example.com
		if strings.HasSuffix(host, suffix) && !strings.Contains(strings.TrimSuffix(host, suffix), ".") && len(host) > len(suffix) {
			return w
		}
	}
	return ""
}

type obj = map[string]any

// BuildConfig builds the full Caddy JSON from spec. The output is deterministic:
// the same spec in a different order gives the same bytes.
func BuildConfig(spec *agentv1.IngressSpec, opts Options) ([]byte, error) {
	if spec == nil {
		spec = &agentv1.IngressSpec{}
	}
	maxBody := opts.MaxBodyBytes
	if maxBody <= 0 {
		maxBody = defaultMaxBody
	}

	var redirects, mains []routeItem
	httpsHosts := map[string]bool{}
	plainHosts := map[string]bool{}
	for _, r := range spec.GetRoutes() {
		hosts := cleanHosts(r.GetHosts())
		if len(hosts) == 0 {
			continue
		}
		prefix, err := cleanPrefix(r.GetPathPrefix())
		if err != nil {
			return nil, err
		}
		scheme := "http"
		if r.GetHttps() {
			scheme = "https"
		}
		serve := hosts
		switch r.GetRedirectWww() {
		case "":
		case "to_www", "to_apex":
			var from []string
			serve, from = wwwSplit(hosts, r.GetRedirectWww() == "to_www")
			for i, f := range from {
				redirects = append(redirects, routeItem{
					key: f,
					route: obj{
						"match":    []any{obj{"host": []string{f}}},
						"handle":   []any{redirectHandler(scheme + "://" + serve[i] + "{http.request.uri}")},
						"terminal": true,
					},
				})
			}
			all := append(append([]string{}, serve...), from...)
			markHosts(r.GetHttps(), all, httpsHosts, plainHosts)
		default:
			return nil, errors.New("ingress: bad redirect_www: " + r.GetRedirectWww())
		}
		markHosts(r.GetHttps(), serve, httpsHosts, plainHosts)

		match := obj{"host": serve}
		if prefix != "/" {
			match["path"] = []string{prefix, prefix + "/*"}
		}
		mains = append(mains, routeItem{
			key:    strings.Join(serve, ",") + "|" + r.GetRouteId(),
			length: len(prefix),
			route: obj{
				"match":    []any{match},
				"handle":   handlers(r, maxBody),
				"terminal": true,
			},
		})
	}
	sort.SliceStable(redirects, func(i, j int) bool { return redirects[i].key < redirects[j].key })
	// Longer path prefixes first, so /api wins over /.
	sort.SliceStable(mains, func(i, j int) bool {
		if mains[i].length != mains[j].length {
			return mains[i].length > mains[j].length
		}
		return mains[i].key < mains[j].key
	})

	// The first route is not terminal. It runs the rest of the routes and then adds the upstream to the access
	// log line, so the value is known. Requests without a proxy get an empty upstream.
	routes := []any{obj{"handle": []any{obj{
		"handler": "log_append", "key": "upstream", "value": "{http.reverse_proxy.upstream.hostport}",
	}}}}
	// Plain HTTP for a host that has HTTPS goes to HTTPS (REQ-DOM-002). The server listens on
	// both ports, so Caddy does not add this redirect by itself.
	if len(httpsHosts) > 0 {
		hs := make([]string, 0, len(httpsHosts))
		for h := range httpsHosts {
			hs = append(hs, h)
		}
		sort.Strings(hs)
		routes = append(routes, obj{
			"match":    []any{obj{"host": hs, "protocol": "http"}},
			"handle":   []any{redirectHandler("https://{http.request.host}{http.request.uri}")},
			"terminal": true,
		})
	}
	for _, it := range redirects {
		routes = append(routes, it.route)
	}
	for _, it := range mains {
		routes = append(routes, it.route)
	}
	// Catch-all for unknown hosts. It has no host matcher, so Caddy never asks for a certificate for it.
	routes = append(routes, obj{"handle": []any{obj{
		"handler":     "static_response",
		"status_code": 404,
		"headers":     obj{"Content-Type": []string{"text/plain; charset=utf-8"}},
		"body":        "no such site",
	}}})

	server := obj{
		"listen": []string{":80", ":443"},
		"routes": routes,
		// Access logs go to the "access" logger. The agent reads them for the ingress metrics (12 section 7).
		"logs": obj{"default_logger_name": AccessLogger},
	}
	var skip []string
	for h := range plainHosts {
		if !httpsHosts[h] {
			skip = append(skip, h)
		}
	}
	// Hosts under a wildcard domain use the wildcard certificate: Caddy does not get a certificate for each of them.
	covered := map[string][]string{} // wildcard -> hosts
	for h := range httpsHosts {
		if w := opts.DNS01.wildcardOf(h); w != "" {
			covered[w] = append(covered[w], h)
		}
	}
	auto := obj{}
	if len(skip) > 0 {
		sort.Strings(skip)
		auto["skip"] = skip
	}
	var skipCerts []string
	for _, hs := range covered {
		skipCerts = append(skipCerts, hs...)
	}
	if len(skipCerts) > 0 {
		sort.Strings(skipCerts)
		auto["skip_certificates"] = skipCerts
	}
	if len(auto) > 0 {
		server["automatic_https"] = auto
	}
	if spec.GetMetricsEnabled() {
		server["metrics"] = obj{}
	}

	apps := obj{"http": obj{"servers": obj{"polipan": server}}}
	if len(httpsHosts) > 0 {
		subjects := make([]string, 0, len(httpsHosts))
		for h := range httpsHosts {
			if opts.DNS01.wildcardOf(h) == "" {
				subjects = append(subjects, h)
			}
		}
		sort.Strings(subjects)
		var policies []any
		var wild []string
		for w := range covered {
			wild = append(wild, w)
		}
		sort.Strings(wild)
		if len(wild) > 0 {
			// The wildcard names are managed by the DNS-01 issuer. The more specific policy comes first.
			policies = append(policies, obj{"subjects": wild, "issuers": dnsIssuers(spec, opts)})
		}
		if len(subjects) > 0 {
			policies = append(policies, obj{"subjects": subjects, "issuers": issuers(spec, opts)})
		}
		tls := obj{"automation": obj{"policies": policies}}
		if len(wild) > 0 {
			tls["certificates"] = obj{"automate": wild}
		}
		apps["tls"] = tls
	}

	cfg := obj{
		"admin":   obj{"listen": AdminListen},
		"storage": obj{"module": "file_system", "root": "/data"},
		// Access lines are written once, by their own log, so they do not mix with the Caddy messages.
		"logging": obj{"logs": obj{
			"default": obj{
				"writer":  obj{"output": "stdout"},
				"encoder": obj{"format": "json"},
				"level":   "INFO",
				"exclude": []string{"http.log.access"},
			},
			AccessLogger: obj{
				"writer":  obj{"output": "stdout"},
				"encoder": obj{"format": "json"},
				"level":   "INFO",
				"include": []string{"http.log.access"},
			},
		}},
		"apps": apps,
	}
	return json.Marshal(cfg) // map keys are sorted by encoding/json
}

// initialConfig is the first config of the container: only the admin socket.
func initialConfig() []byte {
	b, _ := json.Marshal(obj{"admin": obj{"listen": AdminListen}})
	return b
}

type routeItem struct {
	key    string
	length int
	route  obj
}

func markHosts(https bool, hosts []string, httpsHosts, plainHosts map[string]bool) {
	for _, h := range hosts {
		if https {
			httpsHosts[h] = true
		} else {
			plainHosts[h] = true
		}
	}
}

func redirectHandler(location string) obj {
	return obj{
		"handler":     "static_response",
		"status_code": 308,
		"headers":     obj{"Location": []string{location}},
	}
}

// wwwSplit returns the hosts that are served and the hosts that redirect to them (same index).
func wwwSplit(hosts []string, toWWW bool) (serve, from []string) {
	seen := map[string]bool{}
	for _, h := range hosts {
		apex := strings.TrimPrefix(h, "www.")
		s, f := apex, "www."+apex
		if toWWW {
			s, f = f, apex
		}
		if seen[s] {
			continue
		}
		seen[s] = true
		serve = append(serve, s)
		from = append(from, f)
	}
	return serve, from
}

func handlers(r *agentv1.Route, maxBody int64) []any {
	ups := append([]*agentv1.Upstream{}, r.GetUpstreams()...)
	if len(ups) == 0 {
		return []any{obj{
			"handler":     "static_response",
			"status_code": 503,
			"headers": obj{
				"Content-Type":  []string{"text/html; charset=utf-8"},
				"Cache-Control": []string{"no-store"},
			},
			"body": stoppedPage,
		}}
	}
	sort.SliceStable(ups, func(i, j int) bool { return ups[i].GetAddress() < ups[j].GetAddress() })
	dial := make([]any, 0, len(ups))
	weights := make([]int, 0, len(ups))
	weighted := false
	for _, u := range ups {
		dial = append(dial, obj{"dial": u.GetAddress()})
		w := int(u.GetWeight())
		if w > 0 {
			weighted = true
		} else {
			w = 100
		}
		weights = append(weights, w)
	}
	policy := obj{"policy": "round_robin"}
	if weighted {
		policy = obj{"policy": "weighted_round_robin", "weights": weights}
	}
	proxy := obj{
		"handler":        "reverse_proxy",
		"upstreams":      dial,
		"flush_interval": -1, // WebSocket and SSE pass without buffering
		"load_balancing": obj{"selection_policy": policy},
		"headers": obj{"request": obj{"set": obj{
			"X-Forwarded-For":   []string{"{http.request.remote.host}"},
			"X-Forwarded-Proto": []string{"{http.request.scheme}"},
			"X-Forwarded-Host":  []string{"{http.request.host}"},
		}}},
	}
	if d := r.GetStreamCloseDelaySeconds(); d > 0 {
		// After a config load, WebSockets and other streams to the old containers stay open for drain_seconds (AC-DEPLOY-09).
		proxy["stream_close_delay"] = fmt.Sprintf("%ds", d)
	}
	return []any{
		obj{
			"handler": "encode",
			"encodings": obj{
				"zstd": obj{},
				"gzip": obj{},
			},
			"prefer": []string{"zstd", "gzip"},
			// Only text-like types are compressed, so event streams stay unbuffered.
			"match": obj{"headers": obj{"Content-Type": []string{
				"text/html*", "text/plain*", "text/css*", "text/javascript*", "text/xml*",
				"application/json*", "application/javascript*", "application/xml*",
				"application/wasm*", "image/svg+xml*",
			}}},
		},
		obj{"handler": "request_body", "max_size": maxBody},
		proxy,
	}
}

func issuers(spec *agentv1.IngressSpec, opts Options) []any {
	email := spec.GetAcmeEmail()
	acme := func(ca string) obj {
		o := obj{"module": "acme"}
		if ca != "" {
			o["ca"] = ca
		}
		if email != "" {
			o["email"] = email
		}
		return o
	}
	if opts.ACMECAURL != "" {
		o := acme(opts.ACMECAURL)
		if opts.ACMETrustedRoots != "" {
			o["trusted_roots_pem_files"] = []string{opts.ACMETrustedRoots}
		}
		return []any{o}
	}
	// ZeroSSL through the ACME module: Caddy creates the EAB itself from the email.
	zero := acme(zeroSSLCA)
	if spec.GetAcmeCa() == "zerossl" {
		return []any{zero, acme(letsEncryptCA)}
	}
	return []any{acme(letsEncryptCA), zero}
}

// dnsIssuers is the issuer of the wildcard certificates: the ACME issuer with the DNS-01 challenge of the provider.
func dnsIssuers(spec *agentv1.IngressSpec, opts Options) []any {
	provider := obj{"name": opts.DNS01.Provider}
	for k, v := range opts.DNS01.Config {
		if k != "name" {
			provider[k] = v
		}
	}
	var out []any
	for _, i := range issuers(spec, opts) {
		o := i.(obj)
		o["challenges"] = obj{"dns": obj{"provider": provider}}
		out = append(out, o)
	}
	return out
}

func cleanHosts(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, h := range in {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

func cleanPrefix(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" || p == "/" {
		return "/", nil
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	p = strings.TrimRight(p, "/")
	if strings.ContainsAny(p, "*?") {
		return "", errors.New("ingress: bad path_prefix: " + p)
	}
	return p, nil
}
