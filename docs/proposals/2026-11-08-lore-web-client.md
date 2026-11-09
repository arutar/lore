---
lep: 2026-11-08-lore-web-client
title: Lore web client for repositories and administration
authors:
  - arutar
status: Draft
created: 2026-11-08
updated: 2026-11-08
discussion: pending
---

# Lore web client for repositories and administration

## Summary

This proposal adds a responsive React browser client and a Go web gateway for Lore. Users can browse repository catalogs, trees, branches, revisions, diffs, locks, and file content. Administrators can manage the accounts, groups, repositories, grants, registration settings, and audit records exposed by the [repository policy control plane](2026-11-08-repository-policy-control-plane.md). The browser presents caller-specific operations but never becomes the authorization authority.

## Motivation

Lore exposes full functionality through the Lore CLI and SDKs, as the [project overview](../../README.md) describes. The [roadmap](../roadmap.md) commits to open-sourcing a web client, but this tree doesn't contain its source or a self-hosted package. A team that needs a browser home and an administration interface today must build or package one itself.

Repository trees, histories, user lists, and grants can be large. A browser client that loads them eagerly becomes unresponsive. A web backend that uses an administrative Lore credential would also bypass per-user policy unless every operation reimplemented it correctly.

## Goals / Non-Goals

### Goals

- Provide fast catalog, repository, revision, diff, lock, and download views.
- Provide administration views for the policy control plane's users, groups, repositories, grants, settings, health, and audit records.
- Use the caller's identity and effective policy for every browser and Lore operation.
- Bound browser and gateway memory through pagination, lazy loading, and streaming.
- Package the client and gateway for self-hosted Linux container deployment.

### Non-Goals

- Defining repository policy or identity storage. The [repository policy control plane](2026-11-08-repository-policy-control-plane.md) owns them.
- Defining bulk file formats. [Bulk user and grant import and export](2026-11-08-bulk-user-and-grant-import-export.md) owns them.
- Adding code review, change requests, issue tracking, or repository forks.
- Implementing a second version-control engine against Lore storage.

## Proposed Design

### Browser and gateway responsibilities

The React client handles navigation, presentation, accessible interaction, and request cancellation. The Go gateway exposes versioned browser APIs, validates the caller's OIDC session, and calls the policy control plane and Lore gRPC services. It never parses CLI output or modifies Lore storage directly.

For Tier 2 Lore calls, the gateway uses RFC 8693 to exchange the caller's OIDC credential for a token scoped to the selected partition, then presents that token to Lore. Any exchange-token cache is bounded and keyed by remote, subject, credential fingerprint, and partition; entries honor token and policy expiry. A failed exchange denies the operation without falling back to a privileged credential. Administrative operations require the same policy checks as direct control-plane calls. Hiding an unauthorized browser action improves presentation but provides no security boundary.

The browser receives no privileged service token and no token-signing key. A secure, HTTP-only, same-site cookie carries only an opaque session identifier. The identifier references a bounded, expiring session record in shared PostgreSQL storage, so every gateway replica applies the same logout and expiry behavior. The gateway encrypts stored OIDC credentials and deletes the session record at logout. Browser APIs use `/web/v1/` and reject unsupported major versions.

### Repository views

The catalog provides search, public/private filters, and server-side pagination through [paginated repository catalogs](2026-11-08-paginated-repository-catalogs.md). Repository pages load tree entries on demand and provide branches, revision history, text diffs, binary metadata, lock state, file downloads, and clone instructions.

Large files and diffs have explicit display limits. Downloads stream through the gateway without buffering the complete file in browser or gateway memory. Users can cancel navigation and downloads without leaving abandoned gateway work.

### Administration views

Administration pages cover account and group search, role assignment, invitations and disablement, repository settings, grant editing, effective-access inspection, registration settings, service health, and audit search. Destructive actions require confirmation and display the resulting audit event.

An effective-access view identifies whether a direct grant, group grant, public baseline, or administrator override produced the result. Active Directory-managed fields are read-only.

Bulk import and export actions appear only after the corresponding control-plane APIs exist under the separate bulk proposal.

### Deployment and failure behavior

The browser client builds to static assets served by the gateway or reverse proxy. The container runs as a non-root user and exposes health checks. Public configuration contains only URLs and feature flags that are safe to expose.

A failed Lore or policy dependency produces a bounded error and degraded health, not a privileged fallback. Private metadata doesn't appear in error responses or client logs.

## Compatibility

- **Wire format** — N/A. No Lore wire layout changes.
- **Client/server protocols** — The gateway consumes existing Lore and policy protocols. `/web/v1/session`, `/web/v1/login`, and `/web/v1/oidc/callback` form the session contract; `/web/v1/catalog` and `/web/v1/repositories/{id}/` route families cover tree, branches, revisions, diffs, locks, and downloads. `/web/v1/admin/` route families cover users, groups, grants, settings, health, and audit. List responses carry cursor and limit fields; errors carry stable versioned codes. Unsupported major versions fail rather than changing shape silently.
- **On-disk format** — N/A for Lore repositories. Shared session records use the gateway's versioned PostgreSQL schema and migrations.
- **CLI and public API** — N/A. No Lore CLI command, C API, or SDK signature changes.
- **Browser configuration** — Static deployment configuration is versioned with the client. Safe public values include service URLs and feature flags; secrets are prohibited.
- **Container image** — Image versions pin compatible client, gateway, policy, and Keycloak-extension contracts.

## Non-Functional Considerations

- **Concurrency** — Independent repository views load concurrently. Cancellable requests propagate cancellation to gateway work.
- **Memory** — Lists are paginated, trees are lazy, diffs are bounded, and downloads stream. Nothing buffers a whole repository or large file.
- **Statelessness** — Gateway replicas keep no authoritative repository, policy, or session state in process memory. Shared PostgreSQL session records support every replica, expire at a configured deadline, and are deleted at logout. Browser preferences are non-authoritative and disposable.
- **Determinism** — The client presents Lore results without changing revision content or history.
- **Latency** — Catalog and tree requests fetch only visible pages. Metadata and partition-token caches are short-lived and bounded, keyed by caller, credential fingerprint, and policy or partition scope, and never shared across authorization boundaries.
- **Observability** — Health and error responses identify failed dependencies without exposing private paths, credentials, or tokens.

## Migration Plan

N/A — no breaking changes, no migration required. The web client is additive. Users can continue with the Lore CLI and existing deployments.

## Security Considerations

Login, OIDC callback, and anonymous public-catalog routes have explicit unauthenticated bootstrap contracts and rate limits. Every other gateway route checks the caller session and required grant; repository routes also exchange for the selected partition token before calling Lore. Administrative routes don't rely on hidden buttons or client-supplied role claims without server verification. Download routes authorize the partition before streaming content and apply content-disposition and content-type protections.

The gateway limits request sizes, pagination, diff sizes, concurrent streams, sessions, and timeouts. Cookies, CSRF defenses, content security policy, output encoding, and dependency scanning address common browser threats. Tokens and private repository data stay out of browser and gateway logs.

## Privacy Considerations

The client displays account identifiers, email addresses, group membership, repository paths, and audit records only to callers with the required access. Browser caches don't persist private repository content after logout. Administrative exports and diagnostics exclude credentials and tokens.

## Risks and Assumptions

**Assumptions**

- **Assumption:** Existing Lore gRPC surfaces cover the required repository operations — *invalidated if:* a tree, diff, or history operation lacks a bounded remote API. That operation needs a separate protocol proposal rather than a storage workaround.
- **Assumption:** The policy control plane provides stable versioned administrator contracts — *invalidated if:* browser-specific policy rules are needed. Those rules belong in the policy proposal rather than the browser.

**Risks**

- **Risk:** Large repository views overload the browser or gateway — *mitigation:* paginate, virtualize lists, bound diffs, stream content, propagate cancellation, and test beyond every configured limit.
- **Risk:** Metadata caching leaks data between callers — *mitigation:* include caller and policy scope in cache keys, clear sessions at logout, and test private/public transitions.
- **Risk:** The planned upstream web client later overlaps this implementation — *mitigation:* keep policy APIs independent from presentation and evaluate migrating the frontend to the upstream package when its source becomes available.

## Drawbacks

- The gateway adds another public HTTP surface that must enforce caller policy correctly.
- Maintaining a second browser client may duplicate upstream presentation work when Epic publishes its web client.
- Streaming and cancellation require end-to-end testing across browser, gateway, and Lore APIs.

## Alternatives Considered

### Package or extend Epic's planned web client

Use the web client named in the roadmap instead of creating another frontend.

*Rejected for this proposal because:* its source and self-hosted package aren't present in the current tree, so its APIs and administration capabilities can't be evaluated or extended today. The policy control plane remains reusable if the upstream client later satisfies the repository views.

### Browser-only access checks

Check roles and grants in React and call Lore through a shared privileged gateway credential.

*Rejected because:* forged requests could bypass browser checks, and compromise of the shared credential would expose every repository. Server-side partition authorization must govern every operation.

### Server-rendered pages without a browser API

Render repository and administration pages entirely in the Go gateway.

*Rejected because:* interactive trees, history navigation, grant editing, and cancellable downloads would require repeated full-page requests or a separate local API anyway. The versioned gateway API also permits a future upstream frontend to reuse the same operations.

## Prior Art

GitHub and GitLab provide repository browsing and administration through web APIs while their transport endpoints enforce the same repository policy. This proposal borrows the shared-policy property, not their change-request or issue-tracking scope.

The [low-level revision graph API proposal](2026-10-23-low-level-revision-graph-api.md) is relevant when evaluating bounded tree and history operations for the gateway.

## Unresolved Questions

- Which existing or planned Epic web-client components can replace the frontend once published?
- Which tree and history APIs need separate pagination proposals?
- What default file-preview and diff limits should the deployment template use?
