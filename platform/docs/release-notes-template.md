## Lore Platform {{VERSION}}

### Downloads

| File | For |
|---|---|
| `lore-platform-server-{{VERSION}}.tar.gz` / `.zip` | **Server.** Docker Compose bundle: unpack and run `setup.sh` (Linux) or `setup.ps1` (Windows). It pulls the images below; no source code or build tools needed. |
| `LoreClient-{{VERSION}}-x64.msi` | **Windows client**: CLI, Explorer integration, sign-in companion. Per-machine install. |
| `LoreClient-{{VERSION}}-win-x64.zip` | Windows client without an installer (portable / scripted installs). |
| `lore-cli-{{VERSION}}-x86_64-unknown-linux-gnu.tar.gz` | Linux CLI, x86-64. |
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
sh setup.sh            # first install: generates secrets and prints the admin password
```

Upgrading: unpack the new bundle, copy `.env` from the old directory (and `data/` if you keep data
there), then run `setup` again. Make a backup first (`sh backup.sh`).

Full guide: https://github.com/{{REPOSITORY}}/blob/main/platform/docs/install.md
