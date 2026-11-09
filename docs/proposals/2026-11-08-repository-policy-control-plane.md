---
lep: 2026-11-08-repository-policy-control-plane
title: Repository policy control plane for Lore
authors:
  - arutar
status: Draft
created: 2026-11-08
updated: 2026-11-08
discussion: pending
---

# Repository policy control plane for Lore

## Summary

This proposal adds a self-hosted repository policy control plane for Lore. A Go service stores users' repository grants, Active Directory group mappings, repository metadata, and administrative audit records in PostgreSQL. Keycloak federates local and Active Directory accounts and signs partition-scoped authorization tokens. The control plane implements Lore's existing user directory and repository registry contracts and supplies effective grants to a Keycloak provider extension. Authenticated internal policy endpoints preserve the [partition](../glossary.md#partition) access boundary.

## Motivation

[`lore.user.v1.UserService` and `RepositoryRegistryService`](../../lore-proto/proto/lore/user/v1/user.proto) define directory and catalog contracts, but not their policy store or administrator API. An operator must implement these contracts before a Tier 2 deployment can offer per-user repository catalogs and ownership.

The [OIDC authentication proposal](2026-08-20-oidc-oauth2-authentication.md) defines token exchange and directory surfaces, but explicitly leaves permission administration outside its scope. A team still needs account lifecycle operations, Active Directory group mappings, per-repository grants, and an auditable policy service.

Separate rules in a browser or another client would not govern direct Lore CLI calls. Manual per-user grants also become impractical when teams change often.

## Goals / Non-Goals

### Goals

- Store and evaluate per-repository grants for local and Active Directory users and groups.
- Implement the existing Lore user directory and repository registry contracts.
- Issue partition-scoped grants through Keycloak without moving token signing into the control plane.
- Provide authenticated administrator APIs for accounts, groups, repositories, grants, settings, and audit records.
- Support optional local registration and Active Directory federation through one OIDC identity.
- Make policy changes, revocation, and repository registration auditable and recoverable.
- Run the service in Linux containers with durable state and protected secrets.

### Non-Goals

- Defining the browser client. [Lore web client](2026-11-08-lore-web-client.md) covers it separately.
- Defining bulk file formats. [Bulk user and grant import and export](2026-11-08-bulk-user-and-grant-import-export.md) covers them separately.
- Defining anonymous server behavior. [Anonymous public repository read](2026-11-08-anonymous-public-repository-read.md) covers it separately.
- Changing revision, fragment, branch, or Lore on-disk formats.
- Implementing version-control operations directly against Lore storage.

## Proposed Design

### Service and trust boundaries

The control plane implements `UserService` and `RepositoryRegistryService` and exposes versioned administrator and internal policy APIs. PostgreSQL stores repository metadata, grants, group mappings, and append-only audit events. Keycloak remains the source of truth for credentials and Active Directory federation.

Internal network placement doesn't authenticate a caller. Lore Server, the Keycloak extension, and the control plane authenticate each internal policy connection with mutual TLS and explicit service identities. Certificate subjects map to the named caller and nothing else. A response is bound to the requested partition and, for token exchange, the authenticated subject.

The internal route families are:

- `/internal/v1/policy/partitions/{id}/anonymous-read` for the public-visibility verdict.
- `/internal/v1/policy/partitions/{id}/actions` for a verified subject's effective actions.
- `/internal/v1/health` for dependency and synchronization health.

The `/v1/` contract uses additive changes within a major version and rejects unknown enum values. A client that requires a newer contract fails rather than silently reducing privilege checks. Policy responses expire quickly and contain no repository content.

### Identity and registration

Keycloak provides local accounts, optional self-registration, email verification, and LDAP/LDAPS federation with Active Directory. The [Keycloak Server Administration Guide](https://www.keycloak.org/docs/latest/server_admin/) documents these identity-provider capabilities. Browser login uses Authorization Code with PKCE. The Lore CLI uses the existing OIDC device flow. A stable subject identifier connects an account to policy and Lore history.

Operators can enable self-registration without rebuilding the deployment; it is disabled by default. Self-registered users receive only the ordinary user role. A one-time bootstrap procedure creates the first administrator. Administrator APIs disable accounts, assign roles, invite users, and initiate password resets through Keycloak. Active Directory-managed attributes remain read-only.

Group mappings use Active Directory `objectGUID` or SID rather than display names. The control plane refreshes membership at login and on a bounded schedule. Removing an account or membership affects subsequent token exchanges only after the applicable membership refresh and effective-actions cache expiry; existing tokens retain access only until they expire. Configuration defines and health reporting exposes the resulting maximum revocation delay.

### Roles and repository policy

The global roles are administrator and user. Administrators manage deployment settings, accounts, groups, every repository, and audit records. Ordinary users access repositories named by their effective grants and public repositories. An administrator controls whether ordinary users may create repositories. Anonymous access is a policy subject, not an account role.

Repository grants name a local or Active Directory user, a local or Active Directory group, or a special `anonymous` or `authenticated` subject. Permission levels are ordered `none < read < write < owner`. Write includes read, and owner includes write and management of repository settings and grants. `none` records no grant and has no effect in an additive evaluation. The global administrator role has an administrative override.

Policy starts with default deny and combines grants additively. The highest applicable permission wins. The `anonymous` and `authenticated` subjects support read only. A public repository has `anonymous: read`; a private repository has no anonymous or authenticated baseline. Anonymous writes have no representation in the policy schema.

Every privileged administrator API checks the caller's global role or repository grant. An effective-access endpoint returns the direct grants and groups that produced a user's result.

### Registry and Keycloak token exchange

Repository registration records the creator as owner. Catalog responses contain only repositories the caller may list. Registration and unregistration are idempotent and reconcile failures between PostgreSQL and Lore Server. The registry implements the bounded cursor contract in [paginated repository catalogs](2026-11-08-paginated-repository-catalogs.md); the control plane doesn't emulate pages by collecting a complete authorization list.

For Tier 2 authorization, a Keycloak provider extension handles the dynamic partition scope described by the [OIDC authentication proposal](2026-08-20-oidc-oauth2-authentication.md). During token exchange, the extension resolves the requested partition and calls the authenticated effective-actions endpoint. Keycloak places the returned actions in the configured resource claim and signs the partition-scoped token. Lore Server verifies it with the existing `ResourceGrantsAuthorizer`.

The control plane neither mints nor signs tokens and holds no token-signing key. The extension applies a short cache only when configured. Failures deny exchange rather than falling back to broader Tier 1 access.

### Audit, durability, and recovery

Audit records include actor, time, action, target, and non-secret before-and-after values. Grant changes, visibility changes, and deployment-setting changes produce records in the same PostgreSQL transaction as the policy change. Repository registration and deletion use the reconciliation protocol below.

Account administration changes Keycloak state outside PostgreSQL, so a database transaction can't make the external operation and its audit event atomic. Each account operation first commits a durable pending record and idempotency key. A worker performs the Keycloak operation, then records its outcome and audit event. An unknown outcome remains pending while reconciliation queries by the idempotency key or other stable operation identity; retry never blindly repeats a successful invitation, disablement, or reset. Recovery eventually settles or flags every pending operation for administrator repair.

Grant writes use optimistic concurrency. Repository registration exposes pending, registered, and failed reconciliation states so an administrator can retry a partial operation. Health reporting distinguishes unavailable dependencies without disclosing secrets or private metadata.

Backup and restore cover PostgreSQL policy and Lore storage as one recovery procedure. A restore either uses a coordinated snapshot or runs reconciliation before opening writes. Schema upgrades require a backup. A downgrade runs only against a schema version it supports.

## Compatibility

- **Wire format** — N/A. No Lore fragment, revision, or QUIC layout changes.
- **Client/server protocols** — The control plane implements existing directory and registry messages without changing them. Administrator APIs use `/v1/` route families for users, groups, repositories, grants, effective access, settings, audit, and health. Internal `/internal/v1/policy/` requests carry a partition identifier and a verified subject where applicable, require mutual TLS, and return explicit expiry and action values. Mixed clients and servers reject unsupported major versions and unknown enum values.
- **On-disk format** — N/A for Lore repositories.
- **CLI and public API** — N/A. No Lore CLI command, C API, or SDK signature changes.
- **Control-plane state** — PostgreSQL migrations move schema versions forward transactionally. Backups record schema and Lore compatibility versions. A restored control plane reconciles registry state before accepting writes.
- **Configuration format** — New deployment configuration selects Keycloak, policy endpoints, service certificates, registration, and secrets. Existing Lore Server configurations remain valid.
- **Keycloak extension interface** — The extension and control plane use the same internal major version. Extension upgrades require a passing contract test against the pinned Keycloak and control-plane versions.

## Non-Functional Considerations

- **Concurrency** — Grant writes use transactions and optimistic concurrency. Registration is idempotent. Token exchanges and directory reads proceed concurrently.
- **Memory** — Catalogs and administrator lists are paginated. No operation buffers repository content or an unbounded membership set.
- **Statelessness** — Replicas keep no authoritative session state in process memory. PostgreSQL and Keycloak hold durable state. Caches are bounded and disposable.
- **Determinism** — Policy doesn't alter revision hashes or history. Repeating a grant write produces the same effective policy.
- **Latency** — Lore authorization remains local token verification after exchange. Only token exchange, administration, and catalog operations call the control plane.
- **Durability and recovery** — Policy and audit changes commit together. Backups and schema versions support coordinated recovery and reconciliation with Lore.
- **Backpressure** — Policy calls have bounded timeouts and concurrency. Failures deny new exchanges without retry storms.

## Migration Plan

N/A — no breaking Lore changes, no migration required. The deployment is additive. Initial installation creates an empty policy database and bootstraps one administrator. Existing Lore repositories register through a reconciliation run before receiving new grants.

## Security Considerations

The partition remains the access boundary. Browser and CLI operations use caller-scoped credentials. Mutual TLS prevents another container or network peer from requesting or altering policy answers without a service identity.

Keycloak's signing boundary prevents a compromised control plane from forging a token directly. It does not prevent unauthorized access if the control plane returns unauthorized actions that Keycloak then signs correctly. Administrator authentication, mutual TLS, audit, database protection, and restricted network access are therefore mandatory. Policy endpoint failures deny exchanges.

Registration endpoints require rate limits and email verification. Administrative HTTP endpoints require CSRF protection where cookies are used, TLS, and protection against cross-origin and scripting attacks. Logs and audit records exclude credentials, tokens, private keys, and secret configuration.

## Privacy Considerations

The control plane stores account identifiers, usernames, email addresses, group memberships, repository metadata, and administrative audit history. Active Directory attributes are limited to those required for login, display, and policy.

The [OIDC authentication proposal](2026-08-20-oidc-oauth2-authentication.md) documents that revision history records stable user identifiers and resolves display names separately. Account disablement stops future tokens without rewriting history. Operators can remove directory mappings and expire cached membership data. Audit and account-data retention are configurable.

## Risks and Assumptions

**Assumptions**

- **Assumption:** Keycloak supports the provider extension and dynamic-scope flow identified by the OIDC authentication proposal — *invalidated if:* the target Keycloak version can't pass a dynamic partition value to the extension during exchange. The deployment then needs a custom issuer outside this repository or another supported identity provider.
- **Assumption:** Active Directory is reachable over LDAPS and exposes stable group identifiers — *invalidated if:* the directory permits neither a suitable bind account nor stable attributes.
- **Assumption:** The deployment can issue and rotate internal service certificates — *invalidated if:* it can't authenticate policy callers and rotate credentials without downtime.

**Risks**

- **Risk:** Experimental Keycloak features change between versions — *mitigation:* pin and test Keycloak, run real exchange contract tests, and never fall back silently to Tier 1.
- **Risk:** Policy and Lore repository state diverge after partial failure — *mitigation:* use idempotent registration, expose reconciliation status, and provide repair operations.
- **Risk:** Cached membership extends revoked access — *mitigation:* use short token and cache lifetimes, refresh at login and exchange, and test revocation.
- **Risk:** Compromise of the policy service produces valid but unauthorized signed grants — *mitigation:* minimize service privileges, authenticate every internal call, audit grants, alert on policy changes, and support emergency Keycloak extension shutdown.

## Drawbacks

- Production deployment requires Keycloak, PostgreSQL, and internal certificate management.
- The Keycloak extension introduces a version-specific Java component.
- Short token lifetimes increase exchange traffic compared with broad, long-lived grants.

## Alternatives Considered

### Client-only authorization

Store access rules in a browser backend and let clients enforce them while backend operations use an administrative Lore credential.

*Rejected because:* direct CLI or SDK calls would bypass the policy, and a stolen administrative credential would expose every repository. The partition access boundary would no longer determine effective rights.

### A custom token-signing service in the control plane

Let the control plane accept an identity token and sign a partition-scoped token itself.

*Rejected because:* it adds direct token-forging capability to the policy service and violates the trust boundary selected by the OIDC authentication proposal. A malicious policy response remains dangerous under either design, but removing the signing key reduces the control plane's credential blast radius and preserves one token issuer.

### Global OIDC roles only

Use Tier 1 global `read`, `write`, and `admin` roles without per-repository grants.

*Rejected because:* Tier 1 can't express selected private repositories or different rights for different teams.

## Prior Art

GitHub documents [repository roles](https://docs.github.com/en/organizations/managing-user-access-to-your-organizations-repositories/managing-repository-roles/repository-roles-for-an-organization) that combine team and user grants. The useful pattern is one centralized policy for every transport, rather than separate browser-only rules.

The [OIDC authentication proposal](2026-08-20-oidc-oauth2-authentication.md) evaluates provider extensions and dynamic parameterized scopes. Keycloak's [Server Administration Guide](https://www.keycloak.org/docs/latest/server_admin/) documents LDAP federation and OIDC administration.

## Unresolved Questions

- Which Keycloak release and feature flags form the supported baseline?
- Which certificate issuer and rotation mechanism should the reference deployment use?
- What retention periods should deployment templates recommend for audit and directory mappings?
