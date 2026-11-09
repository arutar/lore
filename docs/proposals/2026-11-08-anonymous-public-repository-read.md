---
lep: 2026-11-08-anonymous-public-repository-read
title: Anonymous public repository read
authors:
  - arutar
status: Draft
created: 2026-11-08
updated: 2026-11-08
discussion: pending
---

# Anonymous public repository read

## Summary

This proposal adds an opt-in anonymous-read mode to an authenticated Lore Server and a matching anonymous bootstrap path to Lore clients. A deployment can mark selected repositories as publicly readable through its repository policy service. A caller without a bearer token can then read only those [partitions](../glossary.md#partition). Every write and privileged action still requires a verified caller and an explicit grant. Invalid or expired credentials don't fall back to anonymous access.

## Motivation

Partition-scoped gRPC services and repository HTTP routes currently reject requests without a bearer token in [`jwt_interceptor.rs`](../../lore-server/src/auth/jwt_interceptor.rs) and [`jwt_axum_middleware.rs`](../../lore-server/src/auth/jwt_axum_middleware.rs). The [versioned environment service](../../lore-proto/proto/lore/environment/v1/environment.proto) explicitly defines its contrasting discovery RPC as unauthenticated. The current design prevents a deployment from publishing a repository for anonymous cloning and browsing while keeping its other repositories private.

A client-side check happens even earlier. [`connect_impl`](../../lore-transport/src/connection.rs) resolves a stored identity when the environment advertises authentication, and [`auth_exchange_for_stored_identity`](../../lore-transport/src/auth/exchange.rs) returns `NotAuthenticated` when no identity exists. Server-only anonymous authorization therefore wouldn't make clone or catalog work.

A separate public server without authentication isn't a safe substitute. The [server configuration reference](../reference/lore-server-config.md) states that a server without `[server.auth]` gives every caller `read`, `write`, `obliterate`, and `presign` on every partition.

Public source and asset distribution needs a third option between full authentication and an unauthenticated allow-all server.

## Goals / Non-Goals

### Goals

- Let an operator enable anonymous access only on an otherwise authenticated deployment.
- Restrict anonymous callers to `read` on partitions explicitly marked `anonymous_read` by deployment policy.
- Support anonymous catalog, clone, tree, revision, diff, and content-read operations across gRPC, QUIC, and HTTP.
- Define client bootstrap when no credential exists and when a caller explicitly selects anonymous access.
- Preserve default deny for private repositories and every write or privileged action.
- Preserve the partition boundary and side-channel discipline in [system design §17](../explanation/system-design.md#17-access-model-data-isolation-and-multi-tenancy).
- Bound policy lookup latency, memory use, and revocation delay.

### Non-Goals

- Anonymous write, branch changes, locks, deletion, obliteration, presigning, or administration.
- Granting access based on repository metadata supplied by an untrusted client.
- Changing fragment, revision, token, or QUIC frame formats.
- Changing C API or SDK call signatures for explicit anonymous selection. An embedding that needs that selection requires a separate versioned API proposal; the no-credential automatic path remains available through existing calls.
- Administering public visibility. The [repository policy control plane](2026-11-08-repository-policy-control-plane.md) proposes that surface.

## Proposed Design

### Authenticated public-access policy

A new `[server.auth.public_access]` settings block is absent by default. Its `enabled` flag defaults to `false`. Enabling it requires an internal policy endpoint, a service certificate, and bounded timeout and cache settings. Startup rejects public-access settings without `[server.auth]`.

The policy endpoint answers whether a partition has `anonymous_read`. It requires mutual TLS with an explicit Lore Server identity and returns a verdict bound to the requested partition. It returns no private repository names, account data, or content. Only the deployment's trusted policy writer can mark a partition public.

A public-access decorator wraps the configured repository authorizer. Verified-token requests always use the wrapped authorizer. A request without a verified token receives exactly read access on a public partition and no access otherwise. Partition admission occurs before the handler; read-only action enforcement denies every write and privileged action in the action-check layer or handler.

### Environment advertisement and client bootstrap

The versioned `lore.environment.v1.Environment` message gains `bool anonymous_read = 4`. The legacy `Environment` message in `model.proto` gains `bool anonymous_read = 3` when that service is mounted. Both advertise the same server capability. Older clients ignore the new field. Environment discovery remains unauthenticated and doesn't reveal which partitions are public.

A client with no stored identity and no supplied credential checks this capability before attempting identity exchange. When present, it establishes a distinct anonymous connection with no bearer token and no user identity. This path covers repository-name resolution through `RepositoryGet`, catalog, and repository connection. Anonymous repository connections skip both stored-identity resolution and partition token exchange. Linked-repository connections preserve the anonymous selection. Without the capability, the client preserves the current `NotAuthenticated` result.

A new global `--anonymous` option explicitly requests this path and bypasses stored credentials. Combining it with supplied credentials is invalid rather than silently discarding them. Without it, the client refreshes or verifies a selected stored or supplied credential normally. A malformed, invalid, expired, or unauthorized supplied credential produces its existing error and never silently downgrades to anonymous access. An operator or script can therefore require authenticated identity even for a public repository.

Anonymous mode is an explicit connection-cache identity, not an empty username. The cache key distinguishes anonymous, stored-credential, and supplied-credential connections. An identity-less lookup can't reuse an authenticated connection through the URL fallback in [`connection.rs`](../../lore-transport/src/connection.rs), and an authenticated call can't inherit anonymous mode. Tests cover alternation between anonymous and authenticated calls in one process.

### Missing and invalid credentials on the server

When public access is enabled, partition-scoped gRPC, QUIC, and HTTP authentication layers accept an absent credential as an anonymous caller and pass it to partition authorization. The server rejects a present credential that fails verification. It never becomes an anonymous request.

Environment and unrelated non-partition services retain their existing authentication requirements. Only environment discovery, repository lookup, and repository catalog receive the anonymous behavior needed before selecting a partition. Repository lookup returns a record only after the anonymous partition read check; private names retain the existing non-disclosure response.

### Public catalog behavior

The client-facing `lore.repository.v1.RepositoryService.RepositoryList` accepts no-token requests when public access is enabled. The server then calls the outbound `lore.user.v1.RepositoryRegistryService.RepositoryList` without a bearer token. The registry returns only `anonymous_read` partitions with `read`. The legacy repository-list RPC, where mounted, follows the same catalog policy.

`RepositoryRegister` and `RepositoryUnregister` continue to require a verified caller. The baseline catalog doesn't infer public visibility from local storage. Deployments enabling anonymous access configure the user-service catalog. Private partitions never appear in anonymous responses.

The current registry request has no page fields, and both client-facing list handlers collect candidates and start metadata work for the full result. Anonymous catalog implementation therefore depends on [paginated repository catalogs](2026-11-08-paginated-repository-catalogs.md). Anonymous mode doesn't add a second unpaginated catalog path or weaken that proposal's bounds.

### Transport and linked-partition enforcement

The existing partition-access layer, QUIC session establishment, and HTTP middleware continue to decide reachability before handlers. Handler action checks consume the read-only grant, so storage puts, mutable writes, branch changes, locks, metadata writes, and destructive operations fail without special cases.

Cross-partition copies and links require read access to every involved partition. A public parent doesn't make a private linked repository readable.

Public-visibility lookups use a bounded per-partition cache with a short configured expiry. Before synchronous revision-graph or link traversal touches a newly discovered partition, an asynchronous phase authorizes it with a bounded, deduplicated policy lookup and warms the verdict. This phase follows links only from content the caller already authorized. Existing depth limits don't bound the number of sibling links, so each request also has a unique-partition ceiling, a policy-lookup ceiling, and an overall deadline. Deduplication occurs before policy calls. Exceeding a ceiling fails the operation with a resource-limit result without revealing unauthorized partition details; no unbounded prewalk is added.

Synchronous graph checks consult only warmed cache entries. A missing, expired, failed, or unresolved verdict denies that linked partition. Cold-cache tests cover public/public and public/private links and a wide link graph exceeding the unique-partition ceiling.

### Revocation and failure behavior

Making a repository private stops new anonymous access after the configured cache expiry. An anonymous v4 QUIC `SessionEntry` and its enumerated grants expire no later than the public verdict that authorized them. The legacy QUIC `Connect` path stores `PartitionGrants` in connection context outside that session lifecycle, so its grants and `authorized_sources` carry the same explicit expiry and require reauthorization. The server then closes or refuses the affected session or legacy connection before another operation. Per-connection authorized-partition and cross-partition-source caches can't extend anonymous access beyond that expiry. Authenticated sessions retain their existing token semantics.

Authenticated grants are unaffected by anonymous policy changes. A policy timeout, unavailable service, malformed response, expired session, or cache miss fails closed. Public access never falls back to Tier 1, allow-all, repository metadata, or a wildcard claim. Denials preserve existing non-disclosure behavior. Tests cover revocation during long-lived v4 and legacy QUIC connections and subsequent reconnects.

## Compatibility

- **Wire format** — N/A. Bearer tokens and QUIC frames keep their existing layout. Environment receives only an additive optional capability.
- **Client/server protocols** — Environment advertises anonymous-read capability. Client-facing repository list and outbound registry list gain defined no-token behavior only when enabled. Other registry RPCs are unchanged. Updated clients can create anonymous connections; older clients preserve their current `NotAuthenticated` behavior when no credential exists.
- **On-disk format** — N/A. Public-visibility state belongs to deployment policy, not repository history or `.lore` files.
- **CLI and public API** — The global `--anonymous` option is additive. Without it, no-credential commands can succeed anonymously against a capable server instead of returning `NotAuthenticated`. Supplied-credential errors don't change. No C API or SDK signature changes; embedding APIs expose the same anonymous-selection setting.
- **Configuration format** — `[server.auth.public_access]` is additive and disabled when absent. Existing configurations keep their behavior.
- **Policy interface** — The internal policy endpoint requires mutual TLS, a partition-bound request and response, expiry, and a versioned verdict contract.

## Non-Functional Considerations

- **Concurrency** — Cache fills and linked-partition authorization are deduplicated per partition with bounded concurrency.
- **Memory** — The cache has a maximum entry count and stores only partition identifiers, verdicts, and expiry. Linked discovery uses existing graph limits and doesn't buffer content.
- **Statelessness** — The cache is disposable process state. The external policy service remains authoritative.
- **Determinism** — Public access doesn't alter revision content or hashes. Policy changes affect access, not history.
- **Latency** — Authenticated requests add no policy call. Anonymous requests pay at most one bounded lookup per partition per cache-fill interval.
- **Backpressure** — Policy timeouts, concurrency caps, and deduplication prevent anonymous traffic from creating unbounded outbound calls.

## Migration Plan

N/A — no breaking changes, no migration required. Public access is opt-in. An operator first deploys the authenticated policy and user-service catalog, verifies public and private tests with an updated client, and then enables anonymous access. Older clients can continue authenticating as before.

## Security Considerations

This proposal intentionally introduces a caller with no verified identity. Its authority is smaller than any authenticated role: read on explicitly public partitions only. No anonymous identity is recorded as author, owner, or administrator because mutations require a verified caller and write grant.

Mutual TLS authenticates the policy channel. A compromised policy service could mark private partitions public, so its service identity, write APIs, audit, and emergency disable procedure are security-critical. Public-access startup logs identify the enabled mode without listing private partitions.

Anonymous endpoints can receive higher unauthenticated traffic. Transport rate limits, bounded policy calls, cache limits, and request limits apply. Authorization denials don't reveal private partition existence through detailed errors.

## Privacy Considerations

Public repository metadata and content become visible to anyone who can reach the deployment. This is the explicit purpose of the feature and requires an administrative decision per repository.

Anonymous requests create no account records or persistent user profiles. Ordinary network and audit logs may contain source addresses under the operator's policy. Public-policy cache entries contain only partition identifiers, verdicts, and expiry data.

## Risks and Assumptions

**Assumptions**

- **Assumption:** The deployment can provide a mutually authenticated policy endpoint and user-service catalog — *invalidated if:* public-visibility records exist only in repository metadata or an untrusted client. The feature must remain disabled.
- **Assumption:** Every partition-scoped entry point calls the common `RepositoryAuthorizer` described in the [OIDC authentication proposal](2026-08-20-oidc-oauth2-authentication.md) — *invalidated if:* a transport or RPC reaches storage without that call. That path needs enforcement and tests first.

**Risks**

- **Risk:** A client or transport path accidentally skips authentication or authorization — *mitigation:* add conformance tests over every public RPC family and transport for no-token public read, no-token private denial, supplied-token failure, and no-token write denial.
- **Risk:** A stale cache permits access after unpublication — *mitigation:* use short configured expiry, bound it at startup, and document maximum revocation delay.
- **Risk:** Cold-cache linked content fails or leaks across partitions — *mitigation:* preauthorize each discovered partition asynchronously, preserve graph limits, and test public/public and public/private combinations.
- **Risk:** Anonymous traffic exhausts the policy service — *mitigation:* deduplicate fills, cap cache and request concurrency, time out lookups, and fail closed without retry storms.

## Drawbacks

- Anonymous authorization introduces an online policy dependency that token-based authorization doesn't need.
- The public-policy cache expiry delays revocation.
- Client bootstrap and three server transports increase the anonymous conformance surface.

## Alternatives Considered

### A separate unauthenticated public server

Run another Lore Server without `[server.auth]` and publish selected repositories to it.

*Rejected because:* the current no-auth authorizer grants write, obliterate, and presign as well as read. Making this safe requires a restricted deployment mode anyway, while replication and storage separation add operational complexity.

### Anonymous read-only service mounting

Expose only read RPCs on a separate listener without authentication.

*Rejected because:* repository catalogs, linked partitions, content endpoints, and QUIC sessions don't share one service-mount boundary. A read-only listener alone can't decide which partitions are public.

### Signed public tokens issued without login

Give every anonymous caller a short-lived token carrying public resource grants.

*Rejected because:* it adds token issuance and exchange to anonymous clients and doesn't remove the need to define no-token behavior for existing unauthenticated entry points. It also hides a different identity flow inside a simple public read.

## Prior Art

GitHub documents [public repository visibility](https://docs.github.com/en/repositories/creating-and-managing-repositories/about-repositories#about-repository-visibility) with read access separated from authenticated write permissions. Docker's [Registry token authentication specification](https://docs.docker.com/registry/spec/auth/token/) likewise distinguishes `pull` and `push` actions on a named repository. This proposal applies that separation directly to Lore partitions without adding a parallel public storage path.

## Unresolved Questions

- Should the policy endpoint use a versioned gRPC contract or an internal HTTPS contract?
- What default cache expiry and maximum entry count should the shipped configuration recommend?
- Should operators have an explicit cache-invalidation action for emergency unpublication?
