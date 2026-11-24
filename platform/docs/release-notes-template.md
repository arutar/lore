## Lore Platform {{VERSION}}

### Downloads

| File | For |
|---|---|
| `lore-platform-server-{{VERSION}}.tar.gz` / `.zip` | **Server.** Docker Compose bundle: unpack and run `setup.sh` (Linux) or `setup.ps1` (Windows). It pulls the images below; no source code or build tools needed. |
| `LoreClient-{{VERSION}}-x64.msi` | **Windows client**: CLI, Explorer integration, sign-in companion. Per-machine install. |
| `LoreClient-{{VERSION}}-win-x64.zip` | Windows client without an installer (portable / scripted installs). |
| `lore-client-{{VERSION}}-1.el9.x86_64.rpm` | **Linux client** for Rocky Linux and RHEL 9: CLI, menus and status icons in GNOME Files, Nemo and Caja, operation windows. `sudo dnf install ./lore-client-{{VERSION}}-1.el9.x86_64.rpm` |
| `lore-client_{{VERSION}}-1.ubuntu24.04_amd64.deb` | **Linux client** for Ubuntu 24.04. `sudo apt install ./lore-client_{{VERSION}}-1.ubuntu24.04_amd64.deb` |
| `lore-client_{{VERSION}}-1.debian13_amd64.deb` | **Linux client** for Debian 13. |
| `lore-client-dolphin…` (one per system above) | The same menus and status icons in KDE Dolphin; install it with `lore-client`. |
| `lore-cli-{{VERSION}}-x86_64-unknown-linux-gnu.tar.gz` | Linux CLI only, x86-64, for other distributions. |
| `lore-cli-{{VERSION}}-aarch64-unknown-linux-gnu.tar.gz` | Linux CLI, ARM64. |
| `SHA256SUMS` | Checksums of all files above: `sha256sum -c SHA256SUMS --ignore-missing`. |

Container images (pulled by the server bundle):

```
ghcr.io/{{OWNER}}/lore-platform-control-plane:{{VERSION}}
ghcr.io/{{OWNER}}/lore-platform-server:{{VERSION}}
```

### Install or upgrade the server

```sh
tar -xzf lore-platform-server-{{VERSION}}.tar.gz
cd lore-platform-server-{{VERSION}}
LORE_DATA_ROOT=/srv/lore-data LORE_PUBLIC_HOST=lore.corp.local sh setup.sh
# Windows: $env:LORE_DATA_ROOT="D:\lore-data"; .\setup.ps1 -PublicHost lore.corp.local
```

The first run generates the secrets and prints the WebUI address and the `admin` password.

Upgrading: unpack the new bundle, copy `.env` from the old directory (and `data/` if you keep data
there), then run `setup` again. Make a backup first (`sh backup.sh`).

- Every script, argument, variable, port and directory: https://github.com/{{REPOSITORY}}/blob/platform-v{{VERSION}}/platform/docs/docker.md
- Step-by-step guide (Active Directory, HTTPS, clients): https://github.com/{{REPOSITORY}}/blob/platform-v{{VERSION}}/platform/docs/install.md
