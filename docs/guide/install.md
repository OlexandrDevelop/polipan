# Install

Polipan needs one Linux server with systemd, root access and a public IP. Docker is installed for you if it is missing.

## What you need

- A server (x86-64 or ARM64) with 2 CPU cores and 4 GB RAM or more.
- A domain name for the panel, for example `panel.example.com`. Its DNS A record must point to the server.
  Without a domain the panel runs over plain HTTP. Use this only for a test.
- Open ports: 80 and 443 (web), 9443 (agents of other servers), UDP 51820 (the mesh between servers).

## Install the control plane

```sh
curl -fsSL https://github.com/polipan/polipan/releases/latest/download/install.sh | sudo sh -s -- --domain panel.example.com
```

The script does these steps:

1. Checks the checksums of the files of the release. If `cosign` is installed, it also checks the signature of the images.
   Add `--require-signature` to stop when it cannot check.
2. Installs Docker, nftables and WireGuard tools if they are missing.
3. Creates the master key in `/etc/polipan/master.key`. **Back this file up.** Without it the secrets cannot be read.
4. Starts PostgreSQL, the server, the registry, BuildKit and the metric and log stores with Docker Compose in `/opt/polipan`.
5. Installs the agent of this server and joins it to the panel.
6. Prints a one-time link. Open it and create the first user. This user is the owner of the organization.

Options of the script: `--domain`, `--address`, `--acme-email`, `--version`, `--registry`, `--dir`, `--require-signature`.
`--address HOST[:PORT]` sets the address of the panel when there is no domain (for example a forwarded port in a test VM).
Run `install.sh --help` for the full list.

## Add another server

In the panel open **Nodes** and press **Add node**. The panel shows one install command with a join token.
The token works once and for one hour. Run the command on the new server as root. The node appears as `pending`.
An admin accepts it. See [Servers (nodes)](nodes.md).

## Check the install

- Open `https://panel.example.com` and log in.
- `curl https://panel.example.com/healthz` answers `ok`.
- On the server: `docker compose -f /opt/polipan/docker-compose.yml ps` shows all services as running.

## Back up the control plane

Turn on the control-plane backup policy as the first step after login. See [Backups](../operations/backups.md).
Keep a copy of `/etc/polipan/master.key` in a safe place that is not the same server.
