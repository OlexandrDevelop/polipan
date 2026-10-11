# C04 – Domains and automatic HTTPS

## Summary

A user adds a domain to a service. Caddy on the ingress nodes serves it and gets a TLS certificate automatically. Polipan also offers a default domain so a new app works at once.

## User stories

- **US-DOM-01** As a developer, I add `app.example.com` to my service and get HTTPS with no manual step.
- **US-DOM-02** As a developer, I see what DNS record to create and whether it is correct.
- **US-DOM-03** As an admin, I set a default domain suffix so every new service gets a URL.
- **US-DOM-04** As a developer, I redirect `www` to the apex domain (or the reverse).

## Rules

- **REQ-DOM-001** (MUST) A Domain belongs to one service and has `host`, `path_prefix` (default `/`), `container_port`, `https` (default true), optional `redirect_www`, optional `ingress_node_ids`. The pair (`host`, `path_prefix`) is globally unique.
- **REQ-DOM-002** (MUST) Caddy obtains and renews certificates by ACME (Let's Encrypt, ZeroSSL fallback). HTTP requests redirect to HTTPS. Certificate storage is Caddy's own storage on each ingress node (the agent keeps it in `/var/lib/polipan-agent/caddy`).
- **REQ-DOM-003** (MUST) Domain status (`tls_status`: `pending`, `issued`, `failed`) and the last error are reported by the agent and shown in the UI. The UI shows the DNS records to create (A/AAAA to the ingress node IPs).
- **REQ-DOM-004** (MUST) Before accepting a domain, Polipan checks the syntax and that the host is not already in use. It does not require DNS to be ready.
- **REQ-DOM-005** (MUST) Domain changes are applied by a new desired state for the ingress nodes and one atomic Caddy `POST /load` ([REQ-AGENT-007](../09-multi-server.md#requirements)). Adding or removing a domain does not restart containers and does not create a Release. (The domain list is still stored in the next Release snapshot.)
- **REQ-DOM-006** (SHOULD) Default domain: org setting `default_domain_suffix` (for example `apps.example.com`). A service gets `<service>-<env>-<project>.<suffix>`, at most 63 chars per label (long names are cut and get a 4-char hash).
- **REQ-DOM-007** (MUST) Wildcard domains are only for the preview wildcard setting ([F03](F03-preview-environments.md)) and need a DNS-01 provider. The provider is set on the ingress node, not in the org settings, so the DNS credentials never leave the node: the agent settings `POLIPAN_AGENT_DNS01_PROVIDER`, `POLIPAN_AGENT_DNS01_CONFIG` (JSON) and `POLIPAN_AGENT_WILDCARD_DOMAINS` (for example `*.preview.example.com`), and a Caddy image (`POLIPAN_AGENT_CADDY_IMAGE`) that has the DNS module of the provider. A host with one label under such a wildcard is served by the wildcard certificate. Every other host keeps its own certificate.
- **REQ-DOM-008** (MUST) Caddy adds the headers `X-Forwarded-For`, `X-Forwarded-Proto`, `X-Forwarded-Host`. WebSocket and SSE pass through. Request body limit default 100 MiB, configurable per service.
- **REQ-DOM-009** (SHOULD) Optional per-domain settings: basic auth (credentials stored as a secret), IP allow-list, custom response headers, gzip/zstd (default on).
- **REQ-DOM-010** (MUST) A stopped or never-deployed service returns 503 with a short Polipan page, not a Caddy error.
- **REQ-DOM-011** (MUST) Rate limits of the ACME CA are respected: failures back off (1 h, 6 h, 24 h) and show the CA message.

## States

`tls_status`: `pending → issued`, `pending → failed → pending` (retry), `issued → pending` (renewal failing is shown as a warning, the old certificate stays valid until expiry).

## Edge cases

- Domain used by two services: second add fails 409 `domain_in_use`.
- DNS points to a different server: ACME fails, `tls_status = failed`, message "DNS does not point to an ingress node". Existing services are not affected.
- Domain moved between services: delete from the first, add to the second. A single request can move it (`POST /domains/{id}/move`), applied in one `/load`.
- Ingress node `down`: other ingress nodes serve (see [09](../09-multi-server.md#8-ingress-with-many-nodes)).
- Cloudflare proxy in front: HTTP-01 may fail. The UI explains how to use DNS-01.

## Touch points

API: `/services/{id}/domains`, `/domains/{id}`. CLI: `polipan domain add|rm|ls`. Config: `services.<name>.domains` ([07](../07-config-as-code.md)). UI: Service → Domains.

## Acceptance criteria

- **AC-DOM-01** Given DNS points to an ingress node, When the user adds a domain, Then `tls_status` becomes `issued` and `https://host` serves the app with a valid certificate. (REQ-DOM-001, REQ-DOM-002)
- **AC-DOM-02** Given a domain already used by another service, When added, Then 409 `domain_in_use`. (REQ-DOM-001, REQ-DOM-004)
- **AC-DOM-03** Given a running service, When a domain is added, Then no container restarts and no Release is created. (REQ-DOM-005)
- **AC-DOM-04** Given a stopped service, When its domain is requested, Then the response is 503 with the Polipan page. (REQ-DOM-010)
- **AC-DOM-05** Given DNS is wrong, When the ACME order fails, Then `tls_status = failed` and the error text is visible in the UI and API. (REQ-DOM-003)

## Out of scope

Automatic DNS record creation ([Q-03](../15-open-questions.md)). TCP/UDP (non-HTTP) public routing (host ports are available for databases, see [C02](C02-databases.md)).
