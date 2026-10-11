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

### Short address

`https://get.polipan.dev` sends you to the `install.sh` of the latest release. These two commands do the same:

```sh
curl -fsSL https://get.polipan.dev | sudo sh -s -- --domain panel.example.com
curl -fsSL https://github.com/polipan/polipan/releases/latest/download/install.sh | sudo sh -s -- --domain panel.example.com
```

To pin a version, download the `install.sh` of that release from GitHub and check its signature (see
[Release](../operations/release.md)).

### Install when the server is created (cloud-init)

Most hosting providers have a field named "user data" or "cloud-init" when you create a server. Paste the file
[`deploy/cloud-init/polipan.yaml`](https://github.com/polipan/polipan/blob/main/deploy/cloud-init/polipan.yaml)
there. First change `PANEL_DOMAIN` and `ACME_EMAIL` in the file.

When the server starts for the first time, it installs Polipan with the same script. After a few minutes:

- The log is in `/var/log/polipan-install.log`.
- The one-time link for the first user is in `/root/polipan-setup-link.txt`. Only root can read it. Open the link,
  make the first user and delete the file.

Options of the script: `--domain`, `--address`, `--acme-email`, `--version`, `--registry`, `--dir`, `--require-signature`,
`--allow-userns-sysctl`.
`--address HOST[:PORT]` sets the address of the panel when there is no domain (for example a forwarded port in a test VM).
Run `install.sh --help` for the full list.

## Builds from Git on Ubuntu 24.04

Builds from Git run in a rootless BuildKit container. It needs user namespaces. Ubuntu 24.04 limits them for all
programs that have no AppArmor profile (`kernel.apparmor_restrict_unprivileged_userns=1`).

The installer does not turn this limit off. It loads a small AppArmor profile, `polipan-buildkit`
(`/etc/apparmor.d/polipan-buildkit`). The profile allows user namespaces for the BuildKit container only. The container
is not privileged. The limit stays on for the rest of the server.

If the profile cannot be loaded (for example `apparmor_parser` is missing), the installer stops and tells you. Install the
package `apparmor` and run the script again. If you accept the risk, run the script with `--allow-userns-sysctl`.
It sets `kernel.apparmor_restrict_unprivileged_userns=0` for the whole server and saves it in
`/etc/sysctl.d/60-polipan-userns.conf`. Every program may then use user namespaces.

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
