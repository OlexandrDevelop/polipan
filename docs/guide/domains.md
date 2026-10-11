# Domains and HTTPS

## Add a domain

Open a service, then **Domains**, then **Add domain**. Give the host, an optional path prefix and the container port.
Polipan shows the DNS records to create: one A or AAAA record for each ingress node.
A host and path pair can be used by one service only. A second use answers `409 domain_in_use`.
The host of the panel itself can not be used by a service.

## Certificates

When the DNS record is right, Polipan asks Let's Encrypt for a certificate (HTTP-01). Wildcard names need a DNS-01
provider. `tls_status` goes from `pending` to `issued`. If the order fails (for example the DNS points elsewhere),
the status is `failed` and the error text is shown in the screen and in the API. Fix the DNS and the order is tried again.

## Options

- `redirect_www`: redirect `www` to the apex name or the other way.
- `ip_allow`: allow only these IP addresses or CIDR ranges.
- `max_body_bytes`: limit the size of a request body.
- `basic_auth_secret`: protect the domain with a basic auth secret.

## Move a domain

**Move** gives a domain to another service of the same organization in one change (`POST /domains/{id}/move`).
There is no gap: the ingress is updated once. Moving into or out of a protected environment needs an admin.

## Stopped services

A domain of a stopped or not yet deployed service answers `503` with the Polipan page.
