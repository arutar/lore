# MCP: letting AI agents read Lore

The Lore Platform server can answer the [Model Context Protocol](https://modelcontextprotocol.io)
(MCP), so an AI agent can look through repositories: list them, browse folders, read text files,
search a file, follow the history and download files. Claude Code, Cursor, Hermes and any other MCP
client work: you give the agent the server's MCP address and a personal token.

- **It only reads.** There is no tool that changes anything: no commit, no lock, no access change,
  no deletion. An agent that misunderstands an instruction, or reads a file written to mislead it,
  still can't damage anything.
- **Every request needs a token**, also for public repositories.
- **A token reads what its owner may read, never more**, and checks it on every call: when a
  person loses access to a repository, leaves a group or is switched off, their tokens follow at once.

Contents:

- [1. Turn MCP on](#1-turn-mcp-on)
- [2. Settings](#2-settings)
- [3. Who may use MCP](#3-who-may-use-mcp)
- [4. Tokens](#4-tokens)
- [5. Connect an agent](#5-connect-an-agent)
- [6. What the agent can do](#6-what-the-agent-can-do)
- [7. Access from outside your network](#7-access-from-outside-your-network)
- [8. Troubleshooting](#8-troubleshooting)

## 1. Turn MCP on

MCP is off after installation. The administrator of the server turns it on with setup, in one of
two modes:

| Mode | Who can reach `<WebUI address>/mcp` |
|---|---|
| `off` (default) | Nobody. There is no MCP endpoint, and the WebUI shows no MCP page |
| `internal` | Only clients whose address is in the networks you list; anyone else gets 404 |
| `on` | Anyone who can reach the WebUI, always with a token |

```sh
# Linux: only from these networks
sh setup.sh --mcp internal --mcp-networks "10.0.0.0/16;192.168.0.0/24"
# or from anywhere the WebUI is reachable
sh setup.sh --mcp on
# turn it off again
sh setup.sh --mcp off
```

```powershell
# Windows
.\setup.ps1 -Mcp internal -McpNetworks "10.0.0.0/16;192.168.0.0/24"
.\setup.ps1 -Mcp on
```

Networks are written in CIDR form (`10.0.0.0/16`, `192.168.0.0/24`, `fd00::/8`) and separated by
`;`. A single address (`10.1.2.3`) means that one host. `internal` without networks stops setup with
an error: there is no default, so you decide what "internal" means for your company.

Setup saves the mode and the networks in `.env` (`LORE_MCP`, `LORE_MCP_NETWORKS`). To change them,
run setup again with the new values. At the end setup prints the result:

```
MCP: internal (10.0.0.0/16;192.168.0.0/24): http://lore.corp.local:8080/mcp
```

The networks are checked against the client address the Caddy proxy in front of the control plane
sees. When another reverse proxy or load balancer sits in front of Caddy, Caddy sees that proxy's
address, not the person's: list the proxy's address, or use `on`. **Docker Desktop (checked on
Windows) replaces every client address with its own**, so `internal` works only on a Linux host; see
[Troubleshooting](#8-troubleshooting). A refused request is logged with the address the server saw:
`docker compose logs control-plane`.

## 2. Settings

All settings are environment variables that setup saves in `.env` next to the scripts. Pass them
to setup (`LORE_MCP_CONCURRENCY=16 sh setup.sh`, or `$env:LORE_MCP_CONCURRENCY = "16"` before
`.\setup.ps1`), or edit `.env` and run setup again. When you only edit `.env`,
`docker compose up -d control-plane` in the scripts' folder applies it too.

| Variable | Default | What it does |
|---|---|---|
| `LORE_MCP` | `off` | `off`, `internal` or `on`, see [above](#1-turn-mcp-on). Same as `--mcp` / `-Mcp` |
| `LORE_MCP_NETWORKS` | — | Networks `internal` answers: `10.0.0.0/16;192.168.0.0/24`. Required with `internal`. Same as `--mcp-networks` / `-McpNetworks` |
| `LORE_MCP_CONCURRENCY` | `8` | How many calls one token may run at the same time. A call beyond that gets "busy, try again" at once instead of waiting. `0` turns the limit off |
| `LORE_MCP_MAX_READ` | `1048576` (1 MB) | The most bytes of text one `read_text` call returns. A longer file is read in parts. From `4096` to `67108864` |
| `LORE_MCP_LINK_TTL` | `10m` | How long a link from `download_link` works, from `1m` to `24h` (`30m`, `2h`) |

**Why there is a limit on simultaneous calls, and when to change it.** Agents often run several
tools at once, and an agent caught in a loop can send them without end. The heaviest calls (a
folder listing in a big repository, reading a large file) each keep the Lore server busy for a
while. The limit keeps one runaway agent from slowing the server down for everyone else, including
the WebUI and the clients. 8 is plenty for an agent working normally. Raise it if your agents run
many calls in parallel and see "busy" often; lower it on a small server.

**Why `read_text` returns at most 1 MB.** What a tool returns goes into the model's context, and
1 MB of text is roughly 250–300 thousand tokens, more than most models hold. An agent reads a longer
file in parts (`offset`, `limit`) or searches it (`pattern`); a file it needs whole, it downloads
with `download_link`. Raise the value for models with very large contexts.

What isn't limited: how often a token may call. Every call belongs to a person and a token that
can be revoked, and the limits above already protect the server.

## 3. Who may use MCP

Everyone may, unless an administrator switches MCP off for them. The switch is the **MCP** column
in **Users and groups**:

| Row | Whose MCP it decides |
|---|---|
| A local account | That account's |
| An Active Directory person added personally | That person's, whatever their groups say |
| An Active Directory group | Its members' who have no personal row |

**The personal setting wins over groups.** For a person in Active Directory:

1. If they have a personal row, its switch decides: on is on even when their groups are off, off
   is off even when their groups are on.
2. Otherwise they may use MCP when at least one of the groups that admits them has it on.

Examples, with the groups *artists* (MCP off) and *engineers* (MCP on):

| Person | Personal row | Groups | MCP |
|---|---|---|---|
| Anna | — | artists | off |
| Mark | — | artists, engineers | on (one group is enough) |
| Lina | MCP on | artists | on (personal wins) |
| Pavel | MCP off | engineers | off (personal wins) |

A switch takes effect at once: the person's tokens stop working on their next call and their
download links stop working too, nothing needs to be revoked. The tokens come back to life when the
switch is turned on again. Someone without MCP doesn't see the MCP page.

Turning MCP off for everyone at once is `LORE_MCP=off` on the server.

What a token may read is decided like everything else in Lore: by the person's access to each
repository. MCP only reads, so an administrator's token reads like anyone else's, with no
administration through MCP.

## 4. Tokens

Each person creates their own tokens in the WebUI: **MCP** in the left menu (`<WebUI address>/mcp-tokens`) → **New token**.

- **Name**: for you, to tell tokens apart, such as "Claude Code on my laptop".
- **Expires**: never (the default), or after 30, 90 or 365 days.
- **Repositories**: every repository you may read (the default), or only the ones you choose. A
  token limited to some repositories can't see the others, even in the list.

The token is shown **once**, right after it's created, with ready-to-paste examples. Copy it then:
Lore keeps only a fingerprint (SHA-256) of it, so nobody, administrators included, can show it
again. If you lose it, revoke it and create a new one.

A token looks like `lore_mcp_` followed by 43 letters and digits. The prefix makes a leaked token
recognisable, also to secret scanners in code hosting services.

The list shows each token's start (`lore_mcp_7Hq2…`), what it may read, when it was created and when
it was last used. **Revoke** ends it at once, together with the download links it issued.

An administrator sees **Everyone's tokens** on the same page and can revoke any of them, for
example when a laptop is lost or someone leaves. Creating and revoking tokens, the MCP switches and
every download link are recorded in the **Audit log**.

Treat a token like a password: it reads what you may read. Don't commit it to a repository and don't
paste it into chats. A token that doesn't expire is convenient for an agent that runs for months;
revoke the tokens you no longer use.

## 5. Connect an agent

An agent needs two things: the address, `<WebUI address>/mcp` (shown on the MCP page with a copy
button), and the token in the `Authorization: Bearer <token>` header. The server speaks MCP's
Streamable HTTP transport.

**Claude Code**

```sh
claude mcp add --transport http lore https://lore.corp.local/mcp --header "Authorization: Bearer lore_mcp_..."
```

**Cursor and other clients with a JSON configuration** (`mcp.json` or similar):

```json
{
  "mcpServers": {
    "lore": {
      "url": "https://lore.corp.local/mcp",
      "headers": { "Authorization": "Bearer lore_mcp_..." }
    }
  }
}
```

**Hermes and any other agent.** Every client that connects to a remote MCP server over HTTP asks for
the same two things: the URL and a header. Give it the address above and the header
`Authorization: Bearer lore_mcp_...`, however the client names those fields. A model behind the
agent (DeepSeek, Claude, GPT and so on) doesn't matter to Lore.

**Clients that only start local (stdio) servers**, such as some desktop applications: the
[`mcp-remote`](https://www.npmjs.com/package/mcp-remote) bridge turns the remote server into a local
one. It needs Node.js.

```json
{
  "mcpServers": {
    "lore": {
      "command": "npx",
      "args": ["-y", "mcp-remote", "https://lore.corp.local/mcp", "--header", "Authorization:Bearer lore_mcp_..."]
    }
  }
}
```

Check: ask the agent "list the Lore repositories". It should call `list_repositories`.

With plain HTTP (`http://…:8080`) the token travels unencrypted. That is acceptable inside a trusted
network for a test; for real use, and always outside the company network, run the server with HTTPS
(see [install.md](install.md#7-https)).

## 6. What the agent can do

| Tool | What it returns |
|---|---|
| `list_repositories` | The repositories the token may read, with a filter, a page at a time |
| `get_repository` | Name, description, owner, public or not, dates |
| `list_files` | One folder of a repository at the latest or a given revision, a page at a time |
| `file_info` | A file's **metadata only**: size, whether it's text, text not in UTF-8 or binary, the revision, who changed it last and whether it's locked |
| `read_text` | The lines of a text file with their numbers |
| `download_link` | A short-lived link to download the file itself |
| `history` | Revisions, newest first, a page at a time |
| `revision_changes` | The files a revision added, changed, deleted, moved or copied, with counts by kind |
| `list_locks` | Locked files, who holds each lock and since when |

Repositories are named as in the WebUI (`game/hollow-keep`) or by ID. A private repository the token
can't read and one that doesn't exist get the same answer, so a token doesn't reveal which private
repositories exist.

### Reading files

`file_info` is the cheap first step: it reads only the start of the file to tell what it is. Then:

- **Text** → `read_text`.
- **Binary** (models, textures, archives, executables) → `download_link`; a model can't use the
  bytes anyway.

**How the type is decided**, from the first 8 KB of the file, the way git does it:

1. A zero byte anywhere in them → binary.
2. A UTF-8 or UTF-16 byte-order mark → text in that encoding.
3. Valid UTF-8, plain ASCII included → text.
4. Anything else → "text, not UTF-8".

**`read_text`** returns the lines with their numbers, which agents use to refer to them:

```
game/hollow-keep/source/player.cpp · revision 9f3c2a71… · 48211 bytes · utf-8
     1│ #include "player.h"
     2│
     3│ void Player::Jump() {
...
[More lines follow. Continue with offset=2001.]
```

- `offset` (from line, default 1) and `limit` (lines, default 2000, at most 10000) read a long file
  in parts. One call returns at most `LORE_MCP_MAX_READ` bytes; the last line says where to
  continue.
- `pattern` returns only the lines that contain the text, case-insensitively, with their numbers:
  "find ERROR in this 40 MB log" without reading the whole log into the model. With `regex: true`
  the pattern is a regular expression (RE2 syntax, which can't hang on a hostile expression). At
  most 200 matching lines are shown; the last line says how many there were.
- `encoding` reads text that isn't UTF-8. Lore doesn't guess encodings, because the same bytes are
  valid in several of them; the agent (or you) names it: `windows-1251` (Russian Windows),
  `ibm866` (DOS, also `cp866`), `koi8-r`, `koi8-u`, `iso-8859-5`, `windows-1252`, `iso-8859-1`,
  `utf-16le`, `utf-16be`, `shift_jis`, `gbk` and the other encodings browsers know. Without it, a
  file that isn't UTF-8 is refused with a hint instead of being shown as garbage.
- `revision` reads the file as it was in an earlier revision.

### Downloading files

`download_link` returns a link like `https://lore.corp.local/mcp/download/k2Jf…` and the time it
expires. Anyone can open it without a token, so an agent can pass it to `curl -o` or show it to you
to click, but:

- it downloads one file at one revision;
- it works for `LORE_MCP_LINK_TTL` (10 minutes by default);
- it stops working the moment the token is revoked, the person loses access to the repository or
  their MCP switch is turned off;
- it answers only from where MCP itself answers (`internal` networks);
- every link is recorded in the audit log ("Downloaded through MCP", the person and the path).

There is no size limit on a download.

## 7. Access from outside your network

`on` makes `/mcp` reachable wherever the WebUI is. Then:

- **Use HTTPS.** A token sent over plain HTTP can be read on the way.
- Every request still needs a token, there is no anonymous access, and tokens can't be guessed
  (256 random bits).
- Requests from web pages in a browser are refused (the `Origin` check), so a website someone opens
  can't use their browser to reach MCP.
- Consider tokens limited to the repositories an agent needs, and an expiry for tokens used on
  machines outside the company.
- To allow outside access only for some people, keep `on` and switch MCP off for everyone else (for
  example per group in **Users and groups**).

## 8. Troubleshooting

| What the agent or client reports | Why | What to do |
|---|---|---|
| 404 on `/mcp` | MCP is `off`, or in `internal` mode the client's address isn't in `LORE_MCP_NETWORKS` | Check `LORE_MCP` in `.env`; add the client's network and run setup again |
| 401 "An MCP token is required" | No `Authorization: Bearer` header, or it isn't a Lore MCP token | Check the client's header configuration |
| 401 "The token is invalid" / "was revoked or has expired" | Mistyped, revoked or expired token | Create a new token on the MCP page |
| 401 "The token's account is disabled…" | The person was disabled, deleted, or no longer admitted from Active Directory | — |
| 403 "An administrator has turned MCP off…" | The MCP switch is off for the person or all their groups | An administrator turns it on in **Users and groups** |
| 403 "Requests from web pages aren't accepted" | The request came from a browser page | Use an MCP client, not a web page |
| 503 "Active Directory can't be reached…" | The directory is down and the person's groups can't be checked | Wait for the directory, see `LORE_LDAP_OUTAGE_GRACE` in [docker.md](docker.md) |
| "Busy: this token already runs 8 calls at once" | The `LORE_MCP_CONCURRENCY` limit | Wait and retry; raise the limit if it happens in normal work |
| "…is text, but not UTF-8" | The file uses another encoding | Call `read_text` with `encoding`, such as `windows-1251` |
| "…is a binary file" | `read_text` on a binary file | Use `download_link` |
| "Repository not found, or this token can't read it" | Wrong name, no access, or the token is limited to other repositories | Check the name with `list_repositories` |
| 404 "The download link has expired or isn't valid" | The link is older than `LORE_MCP_LINK_TTL`, or the token, the access or the switch changed | Ask for a new link |
| `internal` answers 404 to everyone on a Windows host | Docker Desktop shows the proxy every connection as coming from its own address (such as `172.21.0.1`); the control plane log says `MCP request from 172.21.0.1 refused` | Run the server on Linux for `internal`, or use `on` with per-group switches. Don't add Docker's address to the networks: that lets everyone in |

Logs: `docker compose logs control-plane` in the scripts' folder. The audit log in the WebUI shows
token and switch changes and downloads.
