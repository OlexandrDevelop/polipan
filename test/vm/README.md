# Test VM for Polipan

A small Ubuntu 24.04 virtual machine on your laptop. Polipan is installed in it with the real `deploy/install.sh`.
You can test the panel on a real Linux host without any risk for your laptop.

- No sudo and no root. The VM runs as your user with QEMU.
- The VM ports are open on `127.0.0.1` only. Nobody else on the network can reach them.
- Size: 2 CPUs, 2 GB memory, a disk of at most 10 GB that grows only as it is used.

## Ports (all on 127.0.0.1)

| What | Address |
| --- | --- |
| Panel (HTTP) | http://127.0.0.1:8088 |
| HTTPS of the VM | https://127.0.0.1:8443 |
| SSH | 127.0.0.1:2222 |

## Start and stop

```sh
test/vm/vm.sh up        # start the VM (the panel is ready after about 1 minute)
test/vm/vm.sh down      # stop the VM (clean power off, the data stays)
test/vm/vm.sh status    # is it running, the ports, the real size on disk
test/vm/vm.sh ssh       # open a shell in the VM (user polipan, with a key made by the script)
```

When the laptop restarts, the VM does not start by itself. Run `up` again. When the VM starts, Polipan starts by itself.
To start the VM at host boot, see the next part.

## Start the VM at host boot

The unit `test/vm/polipan-vm.service` runs `vm.sh up` at boot and `vm.sh down` at shutdown. It is for the car_driver
stack. It expects the repository in `~/Work/Personal/Polipan`. Change the paths in the unit if it is elsewhere.

```sh
mkdir -p ~/.config/systemd/user
cp test/vm/polipan-vm.service ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now polipan-vm.service
loginctl enable-linger "$USER"     # the user manager starts at boot, without a login
```

Check it with `systemctl --user status polipan-vm.service` and `test/vm/vm.sh status`.
`systemctl --user restart polipan-vm.service` stops and starts the VM. If `enable-linger` is refused, ask an
administrator to run it. Without linger the VM starts only after you log in.

## Open the panel and log in

1. Run `test/vm/vm.sh status` and check that "Panel answers: yes".
2. Get the one-time setup link:
   ```sh
   test/vm/vm.sh ssh "sudo sh -c 'cd /opt/polipan && docker compose exec -T server /polipan-server setup-url'"
   ```
   It looks like `http://127.0.0.1:8088/setup?token=...`.
3. Open the link in the browser on your laptop. Make the first user (email and password). This is the owner.
4. Next time open http://127.0.0.1:8088 and log in with this user.

There is no default user and no default password. The setup link works only once. After the first user exists, it is gone.

## Reset to a fresh panel

```sh
test/vm/vm.sh reset
```

This deletes all Polipan data in the VM (users, projects, apps, volumes) and installs again from the images that are
already in the VM. It prints a new setup link. The Ubuntu system and Docker stay. It takes about 1 minute.

To install a new build of your code (it runs `make images`, copies the images and runs `install.sh` again):

```sh
test/vm/vm.sh install
```

Running `install` on a VM that already has Polipan is an upgrade: the data stays.

## Destroy the VM and get the disk back

```sh
test/vm/vm.sh destroy         # deletes the VM disk, keys and logs. The Ubuntu image (about 600 MB) stays for the next VM.
test/vm/vm.sh destroy --all   # also deletes the Ubuntu image. Nothing stays in ~/.cache/polipan-vm.
```

Everything of the VM is in `~/.cache/polipan-vm`. Nothing else on your laptop is changed.

## How much disk does it use

Measured after the full test, with Polipan installed and a clean panel:

| Part | Size |
| --- | --- |
| VM disk (copy on write, maximum 10 GB) | about 2.1 GB |
| Ubuntu 24.04 image (cache, read only) | 0.6 GB |
| Total | about 2.7 GB |

`test/vm/vm.sh status` shows the real numbers. Most of the VM disk is Docker and the Polipan images. The disk gets
smaller when the VM frees blocks (`fstrim` runs by itself every week). To give the space back at once, run `destroy`.

## Create a new VM from nothing

```sh
test/vm/vm.sh create    # downloads Ubuntu (checks the sha256), makes the disk, starts, installs Docker with cloud-init
test/vm/vm.sh install   # builds and installs Polipan; prints the setup link
```

You need `qemu-system-x86_64`, `qemu-img`, `genisoimage` (or `xorriso`), `curl`, `ssh` and Docker on the laptop.
If `/dev/kvm` is not usable, QEMU runs without it. That works, but it is much slower. The script says so.
You can change the ports and the size with the variables `POLIPAN_VM_SSH_PORT`, `POLIPAN_VM_HTTP_PORT`,
`POLIPAN_VM_HTTPS_PORT`, `POLIPAN_VM_DISK`, `POLIPAN_VM_MEMORY` and `POLIPAN_VM_CPUS`.

## The checks

```sh
test/vm/vm.sh verify            # all stages, in order. It makes a test project "VM check" and test apps.
test/vm/vm.sh verify reboot     # one stage
test/vm/vm.sh verify-ui         # the web app in a headless browser (login, dashboard, deploy view, both themes, phone width)
test/vm/vm.sh reset             # after the checks: remove the test data
```

Stages of `verify`: `setup deploy gitbuild bluegreen rollback stopstart restart-server restart-agent reboot upgrade https`.
They check the first user, an image app, a build from a public Git repository (rootless BuildKit with the AppArmor profile of the installer), blue-green updates with 20 requests per second and no failed request,
one-click rollback, stop and start, restart of the server and of the agent, a reboot of the whole VM, an upgrade by
running `install.sh` again, and HTTPS.

What the checks cannot do:

- A real domain and a real Let's Encrypt certificate. The VM has no public name. HTTPS is tested with a local test
  ACME server (Pebble) and the name `secure.test`, which the check puts in `/etc/hosts` of the VM. The certificate
  comes from Pebble, so the browser does not trust it.
- A panel with a domain (`--domain`). The VM panel uses the plain HTTP address `127.0.0.1:8088`
  (`install.sh --address 127.0.0.1:8088`).

## Known limits

- The first start after `create` needs the internet (Ubuntu packages and the Docker images of Polipan).
