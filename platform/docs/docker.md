# Running the server with Docker: scripts, arguments and settings

A reference for everything in the server bundle: what to run, which arguments and variables each
script takes, which ports and directories it uses. For a guided first installation (choosing a
name, Active Directory, HTTPS, clients) see [install.md](install.md).

- [The short version](#the-short-version)
- [What is in the bundle](#what-is-in-the-bundle)
- [setup.sh / setup.ps1](#setupsh--setupps1)
- [Settings (environment variables and .env)](#settings-environment-variables-and-env)
- [backup.sh / backup.ps1](#backupsh--backupps1)
- [restore.sh / restore.ps1](#restoresh--restoreps1)
- [test/e2e.sh](#teste2esh)
- [Ports](#ports)
- [The data directory](#the-data-directory)
- [Day-to-day commands](#day-to-day-commands)
- [Behind a proxy, or without internet access](#behind-a-proxy-or-without-internet-access)
- [Removing an installation](#removing-an-installation)
- [Without the scripts: plain docker compose](#without-the-scripts-plain-docker-compose)
- [Building the images yourself](#building-the-images-yourself)

## The short version

Requirements: Docker Engine with Compose v2 (Linux) or Docker Desktop (Windows), 2 CPU cores,
4 GB RAM, disk for your repositories. No other software on the host.

```sh
# Linux
tar -xzf lore-platform-server-<version>.tar.gz && cd lore-platform-server-<version>
LORE_DATA_ROOT=/srv/lore-data LORE_PUBLIC_HOST=lore.corp.local sh setup.sh
```

```powershell
# Windows, PowerShell, as the user that runs Docker Desktop
Expand-Archive lore-platform-server-<version>.zip -DestinationPath C:\lore
cd C:\lore\lore-platform-server-<version>
$env:LORE_DATA_ROOT = "D:\lore-data"
.\setup.ps1 -PublicHost lore.corp.local
```

Setup prints the WebUI address and the initial password of the `admin` account. Open
`http://lore.corp.local:8080` and sign in. Clients use `lore://lore.corp.local/<repository>`.

Everything you pass is saved in `.env`, so later a plain `sh setup.sh` (or `.\setup.ps1`)
re-applies the same configuration.

## What is in the bundle

| File | What it is |
|---|---|
| `setup.sh`, `setup.ps1` | Install, reconfigure, start. Safe to run any number of times |
| `backup.sh`, `backup.ps1` | Consistent backup of databases, repository contents, locks, keys and `.env` |
| `restore.sh`, `restore.ps1` | Restore a backup over the current installation, with automatic rollback on failure |
| `compose.yaml` | The five services; image versions are those of the bundle |
| `compose.caddy.yaml` | Added by setup when Caddy serves ports 80/443 itself (`LORE_DIRECT_CADDY`) |
| `compose.caddy-local.yaml` | Added for `localhost` / `*.internal` names with direct Caddy: a local CA instead of Let's Encrypt |
| `Caddyfile` | The entry point: WebUI, API and sign-in behind one address |
| `keycloak/` | Realm template, LDAP provider template, the script that keeps the realm in line with the settings |
| `lore/docker.toml` | Lore server configuration template (sign-in, lock store, lock enforcement) |
| `postgres/init/` | Creates the Keycloak database on the first start |
| `test/e2e.sh` | End-to-end self-test for a **test** installation |
| `VERSION` | Bundle version |

Setup never edits these files; what it generates goes to `<data>/generated/`.

## setup.sh / setup.ps1

```sh
sh setup.sh                          # all settings from the environment and .env
```

```powershell
.\setup.ps1 [-PublicHost NAME] [-Scheme http|https] [-HttpPort PORT] [-PublicUrl URL] [-AuthUrl URL] [-DirectCaddy] [-SkipBuild]
```

`setup.sh` takes no arguments: every setting is an environment variable (below). `setup.ps1`
accepts the most common ones as parameters, and reads all the others from the environment too:

| setup.ps1 parameter | Same as | Meaning |
|---|---|---|
| `-PublicHost NAME` | `LORE_PUBLIC_HOST` | Name or IP clients use to reach the server |
| `-Scheme http\|https` | `LORE_PUBLIC_SCHEME` | `http` (port 8080, no certificates) or `https` |
| `-HttpPort PORT` | `LORE_HTTP_PORT` | WebUI port in `http` mode, when 8080 is taken |
| `-PublicUrl URL` | `LORE_PUBLIC_URL` | Full WebUI address when it isn't `http://NAME:8080` / `https://NAME` |
| `-AuthUrl URL` | `LORE_AUTH_PUBLIC_URL` | Sign-in address when Keycloak has its own domain |
| `-DirectCaddy` | `LORE_DIRECT_CADDY=true` | Caddy listens on 80/443 and gets Let's Encrypt certificates |
| `-SkipBuild` | `LORE_SKIP_BUILD=true` | Start with the images already on the host, don't build or pull |

Each run:

1. On the first run only, creates `.env` (mode 600) with random secrets: `POSTGRES_PASSWORD`,
   `KEYCLOAK_ADMIN_PASSWORD`, `LORE_ADMIN_PASSWORD`, `LORE_SERVICE_CLIENT_SECRET`.
2. Saves the settings you passed to `.env`. A variable set in the environment wins over `.env`.
3. Generates `<data>/generated/lore.toml`, `lore-realm.json` and the Caddy snippets for your
   address, mail and directory settings.
4. Starts the containers and waits up to 4 minutes for every health check (`docker compose up -d --wait`).
   With `LORE_CONTROL_PLANE_IMAGE` and `LORE_SERVER_IMAGE` set (the bundle's defaults are its own
   version) it pulls the images; in a source checkout it builds them.
5. Brings Keycloak in line with the settings (addresses, LDAP, mail, the service client secret) and
   recreates the local `admin` account if it was deleted.
6. Prints the addresses, the initial `admin` password and the data directory.

Changing the name later: run setup again with the new `LORE_PUBLIC_HOST`; the saved URLs that
were derived from the old name are replaced.

## Settings (environment variables and .env)

Pass them to setup in the environment (`NAME=value sh setup.sh`, or `$env:NAME = "value"` before
`.\setup.ps1`). They are saved in `.env` next to the scripts; editing `.env` and re-running setup
works too.

**Address and network**

| Variable | Default | Meaning |
|---|---|---|
| `LORE_PUBLIC_HOST` | this computer's DNS name, or its IP if the name isn't a valid host name | Name clients use. Letters, digits, `-` and `.` only — **no `_`** (Keycloak can't parse it). Not `localhost` |
| `LORE_PUBLIC_SCHEME` | `http` | `http` or `https` |
| `LORE_HTTP_PORT` | `8080` | WebUI port in `http` mode. Change it when another program uses 8080; the address becomes `http://HOST:PORT` |
| `LORE_PUBLIC_URL` | `http://HOST:PORT` / `https://HOST` | WebUI address |
| `LORE_AUTH_PUBLIC_URL` | `<WebUI>/auth` | Sign-in (Keycloak) address |
| `LORE_PUBLIC_ADDRESS` | the WebUI address (`http`), the host name (`https`) | The site address Caddy serves; other names on port 8080 are redirected to it |
| `LORE_DIRECT_CADDY` | `false` | `true`: Caddy binds 80/443 and manages certificates |
| `LORE_PROXY_BIND` | `0.0.0.0` | Interface of port 8080; `127.0.0.1` behind your own reverse proxy on the same host |
| `LORE_NATIVE_PUBLIC_URL` | `lore://HOST:41337` | Address the WebUI shows for clients (Clone) |

**Data**

| Variable | Default | Meaning |
|---|---|---|
| `LORE_DATA_ROOT` | `./data` | Where everything is stored (see [The data directory](#the-data-directory)). Keep it outside the bundle directory, so an upgrade is "unpack the new version" |

**Active Directory** (optional; without it, only local accounts)

| Variable | Default | Meaning |
|---|---|---|
| `LORE_LDAP_URL` | — | `ldap://dc.corp.local:389` or `ldaps://dc.corp.local:636`; enables AD |
| `LORE_LDAP_BASE_DN` | — | Where users and groups are searched, e.g. `DC=corp,DC=local` |
| `LORE_LDAP_BIND_DN` | — | Read-only service account, e.g. `CN=svc-lore,OU=Service,DC=corp,DC=local` |
| `LORE_LDAP_BIND_PASSWORD` | — | Its password |
| `LORE_LDAP_USERNAME_ATTRIBUTE` | `sAMAccountName` | Login attribute |
| `LORE_LDAP_STARTTLS` | `false` | Upgrade `ldap://` to TLS |
| `LORE_LDAP_INSECURE_SKIP_VERIFY` | `false` | Don't verify the domain controller's certificate |
| `LORE_LDAP_TIMEOUT` | `60s` | LDAP request timeout (1s–1m) |
| `LORE_LDAP_REFRESH` | `5m` | How often groups and account status are re-read: removing someone from AD takes effect within this time |
| `LORE_LDAP_OUTAGE_GRACE` | `8h` | How long the last known groups are trusted while AD can't be reached; after that, directory users are refused |

**Mail** (optional; enables "forgot password" and email verification)

| Variable | Default | Meaning |
|---|---|---|
| `LORE_SMTP_HOST` | — | Mail server; enables mail |
| `LORE_SMTP_PORT` | `587` | Port |
| `LORE_SMTP_FROM` | — | Sender address (`Name <addr>` is accepted) |
| `LORE_SMTP_FROM_NAME` | `Lore` | Sender name |
| `LORE_SMTP_USERNAME`, `LORE_SMTP_PASSWORD` | — | Account, if the server needs one |
| `LORE_SMTP_STARTTLS` | `true` | STARTTLS (port 587) |
| `LORE_SMTP_SSL` | `false` | Implicit TLS (port 465) |

**Images**

| Variable | Default | Meaning |
|---|---|---|
| `LORE_CONTROL_PLANE_IMAGE` | `ghcr.io/arutar/lore-platform-control-plane:<bundle version>` | Control plane + WebUI image, e.g. from a mirror |
| `LORE_SERVER_IMAGE` | `ghcr.io/arutar/lore-platform-server:<bundle version>` | Lore server image |
| `LORE_KEYCLOAK_IMAGE` | `keycloak/keycloak:26.4` (Docker Hub) | Keycloak image, e.g. `quay.io/keycloak/keycloak:26.4` or a mirror |
| `LORE_SKIP_BUILD` | `false` | `true`: use the images already on the host, never build or pull (installations without internet access) |

**Generated by setup — keep them secret, don't change them by hand**

| Variable | What |
|---|---|
| `POSTGRES_PASSWORD` | Database password |
| `KEYCLOAK_ADMIN_PASSWORD` | Keycloak's own administrator (`master` realm) |
| `LORE_ADMIN_PASSWORD` | Initial password of the WebUI administrator `admin` |
| `LORE_SERVICE_CLIENT_SECRET` | The control plane's account at Keycloak |
| `SECURE_COOKIES`, `LORE_AD_HOST`, `LORE_AD_IP` | Derived from the settings above |

**For the backup script**

| Variable | Default | Meaning |
|---|---|---|
| `LORE_BACKUP_WITHOUT_SIGNING_KEY` | `false` | `true` if the token signing key is kept elsewhere and the backup may go without it |

## backup.sh / backup.ps1

```sh
sh backup.sh [DESTINATION] [NAME]
```

```powershell
.\backup.ps1 [-Destination DIR] [-Name NAME]
```

| Argument | Default | Meaning |
|---|---|---|
| `DESTINATION` / `-Destination` | `./backups` | Directory for backups |
| `NAME` / `-Name` | UTC date and time, `20261114-020000` | Backup name: letters, digits, `.`, `_`, `-` |

The backup is `DESTINATION/NAME/`: both databases (`lore.dump`, `keycloak.dump`), repository
contents, file locks and the token signing key (`storage.tar`, from `lore/` and
`control-plane/`), the settings (`env`, a copy of `.env`), `manifest.json` and `SHA256SUMS`. The services stop for the duration (seconds to minutes) and start again. The
directory is readable only by its owner — it contains secrets; copy it off the host.

It refuses to produce a backup that couldn't restore the server (empty storage, missing signing
key) and never leaves a half-written one behind (`NAME.partial` until it's complete).

Nightly at 02:00:

```cron
0 2 * * * cd /opt/lore-platform-server-0.2.7 && sh backup.sh /mnt/backup >> /var/log/lore-backup.log 2>&1
```

## restore.sh / restore.ps1

```sh
sh restore.sh BACKUP_DIR [--yes]
```

```powershell
.\restore.ps1 -Backup BACKUP_DIR
```

| Argument | Meaning |
|---|---|
| `BACKUP_DIR` / `-Backup` | A directory made by `backup.sh` or `backup.ps1` (either works on either system) |
| `--yes` | Don't ask for confirmation |

Restores the databases, repository contents, locks, the signing key and `.env` over the current
installation. The checksums are verified first; the current state is kept aside and put back if
anything fails. `.env` must exist (run setup on a new host first, then restore).

## test/e2e.sh

```sh
LORE_E2E=destroy-test-data bash test/e2e.sh
```

Installs (or reuses) the server, signs in through Keycloak, creates a repository, checks grants
in the WebUI, makes a backup, changes data, restores and checks the result. **It overwrites data:
run it only on a test installation**; without `LORE_E2E=destroy-test-data` it refuses to start.

| Variable | Meaning |
|---|---|
| `LORE_E2E=destroy-test-data` | Required: confirms this is a test installation |
| `LORE_E2E_POWERSHELL=true` | Use `setup.ps1` / `backup.ps1` / `restore.ps1` (Windows, from Git Bash) |
| `LORE_E2E_SKIP_SETUP=true` | Test the running installation without running setup |
| `LORE_E2E_BROWSER` | Chrome or Edge executable for the WebUI check |
| `LORE_E2E_REQUIRE_BROWSER=true` | Fail instead of skipping the WebUI check when no browser is found |

## Ports

| Port | Who connects | What |
|---|---|---|
| 8080/tcp (`LORE_HTTP_PORT`) | Browsers, clients (sign-in) | WebUI, API and Keycloak behind Caddy (HTTP mode) |
| 80/tcp, 443/tcp+udp | Browsers, clients | The same with `LORE_DIRECT_CADDY=true` (HTTPS) |
| 41337/tcp+udp | Clients (`lore`, Windows Explorer) | Lore server: clone, sync, commit, locks |
| 41339/tcp | Health checks | Lore server status; no need to open it in the firewall |

## The data directory

Everything that matters lives in `LORE_DATA_ROOT` (default `./data`):

| Path | Contents |
|---|---|
| `lore/` | Repository contents (Lore's storage) and `locks.json`, the file locks |
| `control-plane/` | Token signing key `token-exchange-signing.pem` |
| `postgres/` | PostgreSQL: control plane database (users, access, audit) and Keycloak's |
| `caddy/` | Certificates (HTTPS mode) |
| `generated/` | Configuration setup generates; recreated on every run |

`backup.sh` dumps the databases and archives `lore/` and `control-plane/`; `generated/` is
recreated by setup, and `caddy/` holds only certificates Caddy fetches again.

## Day-to-day commands

Run them in the bundle directory:

```sh
docker compose ps                      # are all services healthy
docker compose logs -f                 # all logs; add a name: control-plane, lore, keycloak, caddy, postgres
docker compose restart lore            # restart one service
docker compose down                    # stop everything (data stays)
sh setup.sh                            # start again with the saved settings
curl -fsS http://lore.corp.local:8080/healthz
```

Upgrade: `sh backup.sh`, unpack the new bundle next to the old one, copy `.env` over (and
`data/` if you didn't set `LORE_DATA_ROOT`), stop the old one with `docker compose down`, run
setup in the new directory. Database migrations run automatically.

## Behind a proxy, or without internet access

**Proxy.** The host needs the internet only to download images. Docker Engine takes the proxy
from its own service settings (`systemctl edit docker`, `HTTP_PROXY` / `HTTPS_PROXY`), Docker
Desktop from its settings. If your `~/.docker/config.json` also has a `proxies` section, Docker
passes that proxy into every container; the services are set up for it (they reach each other
and the server's own address directly, see `NO_PROXY` in `compose.yaml`), so nothing needs
changing.

Some corporate proxies block registries other than Docker Hub. The images come from Docker Hub
(`postgres`, `keycloak/keycloak`, `caddy`) and GitHub (`ghcr.io/arutar/...`); point
`LORE_KEYCLOAK_IMAGE`, `LORE_CONTROL_PLANE_IMAGE` or `LORE_SERVER_IMAGE` at a mirror if needed.

**No internet access at all.** Bring the images in a file. On a computer with internet access:

```sh
docker pull postgres:18-alpine
docker pull keycloak/keycloak:26.4
docker pull caddy:2-alpine
docker pull ghcr.io/arutar/lore-platform-control-plane:<version>
docker pull ghcr.io/arutar/lore-platform-server:<version>
docker save -o lore-images.tar postgres:18-alpine keycloak/keycloak:26.4 caddy:2-alpine \
    ghcr.io/arutar/lore-platform-control-plane:<version> ghcr.io/arutar/lore-platform-server:<version>
```

Copy `lore-images.tar` and the server bundle to the server, then:

```sh
docker load -i lore-images.tar
LORE_SKIP_BUILD=true LORE_PUBLIC_HOST=lore.corp.local sh setup.sh
```

## Removing an installation

```sh
docker compose down -v --remove-orphans   # stops and removes the containers
sudo rm -rf <data root> .env              # the data: owned partly by the services' users
```

The data directory holds files that belong to the services' own users (PostgreSQL's, for
example), so removing it needs `sudo` on Linux. Make a backup first if you may need the data.

## Without the scripts: plain docker compose

`setup` is the supported way: it generates the Keycloak realm and the Lore configuration for
your address, which `compose.yaml` mounts from `<data>/generated/`. After setup has run once,
`docker compose up -d` and `docker compose down` work on their own, because Compose reads the
same `.env`. To change settings, run setup again rather than editing `generated/`.

With direct Caddy (HTTPS), pass the same files setup uses:
`docker compose -f compose.yaml -f compose.caddy.yaml up -d`.

## Building the images yourself

In a clone of the repository, run the same scripts from `platform/deploy` without
`LORE_CONTROL_PLANE_IMAGE` / `LORE_SERVER_IMAGE`: setup builds both images on the host. The
first build compiles the Rust server and takes 10–30 minutes and several GB of memory.

To make your own bundle that points at your own registry:

```sh
bash platform/deploy/make-bundle.sh VERSION IMAGE_PREFIX OUTPUT_DIR
bash platform/deploy/make-bundle.sh 0.2.7 registry.corp.local/lore ./dist
```

The images: `docker build -f platform/Dockerfile .` (control plane and WebUI) and
`docker build -f lore-server/Dockerfile .` (Lore server), from the repository root.
