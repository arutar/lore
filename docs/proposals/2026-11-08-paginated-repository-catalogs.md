---
lep: 2026-11-08-paginated-repository-catalogs
title: Paginated repository catalogs
authors:
  - arutar
status: Draft
created: 2026-11-08
updated: 2026-11-08
discussion: pending
---

# Paginated repository catalogs

## Summary

This proposal adds opt-in cursor pagination to Lore's client-facing repository lists and the user-service registry list. A page has a server-enforced size limit, an opaque continuation token, and bounded metadata-loading concurrency. Existing callers retain the current full-list behavior. Browser, CLI, and anonymous clients can enumerate large catalogs without collecting every visible [partition](../glossary.md#partition) or starting one task per repository.

## Motivation

The client-facing [`RepositoryList`](../../lore-proto/proto/lore/repository/v1/repository.proto) request has no page fields. Its [versioned handler](../../lore-server/src/grpc/repository/v1/repository_list.rs) collects all candidate identifiers before streaming metadata responses and starts a task for every candidate. The [legacy unary handler](../../lore-server/src/grpc/handlers/repository_list.rs) does the same and then collects all records for one response.

The internal [`RepositoryCatalog`](../../lore-server/src/authnz/repository_catalog.rs) trait can page, but its user-service implementation collects the registry's complete stream before applying a local page. The outbound `lore.user.v1.RepositoryRegistryService.RepositoryList` request has no fields through which to request a page. The existing 100,000-repository ceiling limits one result but doesn't make memory or task count proportional to a caller-selected page.

A public catalog makes this cost reachable without an account. The [web client](2026-11-08-lore-web-client.md) also needs pages small enough for responsive navigation. Local post-processing can't provide either property when the upstream registry has already sent the full catalog.

## Goals / Non-Goals

### Goals

- Bound each opted-in list request by a configured maximum page size and metadata-loading concurrency limit.
- Pass pagination through Lore Server to the user-service registry instead of collecting the upstream catalog.
- Preserve existing behavior for callers that don't request pagination.
- Define continuation, empty-page, filtering, mixed-version, and catalog-mutation behavior.
- Cover the versioned streaming list, the legacy unary list, and the outbound registry stream.

### Non-Goals

- Changing repository visibility, grants, or anonymous authorization.
- Adding search, sorting choices, or a consistent snapshot across pages.
- Paginating branch, revision, tree, or other Lore APIs.
- Removing the existing full-list compatibility path in this proposal.

## Proposed Design

### Page requests and responses

`lore.repository.v1.RepositoryListRequest` gains `uint32 page_size = 2` and `string page_token = 3`. A zero or absent `page_size` selects the existing full-list behavior. A positive value requests at most the smaller of that value and the server's configured maximum. A page token without a positive page size is invalid.

The streaming `lore.repository.v1.RepositoryListResponse` gains `optional string next_page_token = 2`. Repository messages leave it absent. A paginated response ends with exactly one terminal message whose `repository` is absent and whose `next_page_token` is present. A nonempty token continues the list; a present empty token marks the end. Field presence distinguishes the terminal message from an ordinary repository record.

`lore.user.v1.RepositoryListRequest` gains `uint32 page_size = 1` and `string page_token = 2`. Its streaming response gains `optional string next_page_token = 3`, leaving the existing `permission = 2` unchanged, and follows the same terminal-marker contract. The legacy unary `RepositoryListRequest` gains `uint32 page_size = 1` and `string page_token = 2`; its response gains `string next_page_token = 2`, with an empty value marking the end.

The server rejects malformed, expired, oversized, or context-mismatched cursors with `INVALID_ARGUMENT`. It rejects unknown required cursor versions rather than interpreting them as another position.

### Ordering, filtering, and cursor scope

Each catalog uses repository ID as its stable ascending key. The registry applies authorization before taking a page from its policy store. Its cursor represents the last returned repository ID or an equivalent keyset position. Lore Server passes the registry cursor through its own opaque client cursor.

The existing `creator` filter still applies after identifier retrieval. A page can therefore contain fewer records than requested, including zero repository records followed by a nonempty continuation token. Clients continue until the terminal empty token rather than treating a short or empty page as the end.

A cursor is bounded, opaque, integrity-protected, and time-limited. It binds the normalized filters, caller or anonymous catalog identity, cursor version, and upstream position. Every page reevaluates current repository policy; possessing a cursor grants no access and doesn't resurrect a revoked repository.

Pages don't form a consistent snapshot. Repository creation, deletion, renaming, or policy changes between requests can change page membership. Stable ID ordering prevents duplicates during insertion-only traversal, but the contract permits omissions or repeats when records move into or out of the authorized set. Clients reconcile by repository ID.

### Bounded server work

For a paginated user-service request, Lore Server retrieves only one bounded registry page at a time. It loads metadata with a fixed concurrency cap no greater than the page-size cap. It doesn't create a task for every visible repository or collect later pages. Response streaming and cancellation stop unfinished metadata work.

The registry applies the same page bound in its database query. It returns only page data and its continuation position, not a separate full authorization list. The existing full-list path and its configured ceiling remain for compatibility, but new browser and anonymous catalog clients use pagination.

### Mixed versions and client behavior

Protocol buffers make the new fields additive. An old client sends no page size and receives the existing response shape from a new server. A new registry accepts that request from an old Lore Server and retains full-list behavior.

A new client that requests pagination requires the terminal marker. If an old server ignores the page fields, the client stops after at most one record beyond the requested limit, cancels the stream, and reports `NotSupported`. It doesn't collect or render the unbounded remainder. A new client can use unary legacy pagination only when the response obeys the requested limit and continuation contract.

A new Lore Server applies the same upstream bound when calling an old registry. It cancels after one record beyond the requested upstream limit, returns `NotSupported`, and issues no client continuation token for the failed request. The client cursor never treats a rejected upstream response as a valid page.

## Compatibility

- **Wire format** — No QUIC storage frames, fragments, revisions, or tokens change. The protobuf additions use the new field numbers in Proposed Design and retain existing field numbers and stream cardinality.
- **Client/server protocols** — The versioned client and registry list requests gain `page_size` and `page_token`; their streaming responses gain optional `next_page_token` and a terminal-message rule for opted-in requests. The legacy unary request and response gain equivalent page fields. Zero page size preserves old behavior. Mixed-version detection and failure follow Proposed Design.
- **On-disk format** — N/A. Cursors and page state don't enter repository history or `.lore` files.
- **CLI and public API** — Existing full-list calls and command syntax retain their behavior. A new `lore_repository_list_page` C API entry point uses a new argument type containing the URL, page size, and cursor and a new terminal event carrying the next cursor. A caller linked to an old library fails symbol resolution rather than silently invoking a full list. The local service carries this as a new command under the version handshake in [ADR-00025](../developing/decisions/00025-service-version-compatibility.md), so incompatible client and service builds reject the call. SDKs expose pagination through additive methods rather than changing existing list-call signatures.
- **Cursor format** — Cursor encoding is private to the issuing service, versioned, bounded, integrity-protected, and expiring. Clients treat it as opaque. A service can reject cursors from another deployment or software version.

## Non-Functional Considerations

- **Concurrency** — Metadata loads have a fixed per-request cap. Cancellation drops pending work, and independent pages can proceed concurrently.
- **Memory** — An opted-in request holds at most one bounded registry page and one bounded metadata page. A mixed-version failure retains at most one record beyond the requested limit before cancellation.
- **Statelessness** — Continuation state travels in the cursor. Servers keep no per-client catalog snapshot or cursor table.
- **Determinism** — Repository ID orders each page. Concurrent catalog and policy changes have the explicitly weak cross-page semantics above and don't alter revision content.
- **Latency** — A request performs one registry page lookup and at most one metadata lookup per record in that page. Creator filtering can return an empty page without scanning later pages.
- **Backpressure** — Page size, cursor size and lifetime, request time, and metadata concurrency have configured ceilings. Slow consumers bound in-flight work through the existing stream channel.

## Migration Plan

N/A — no breaking changes, no migration required. Deploy the registry before Lore Server so paginated requests reach a capable upstream, then update clients. A client receiving an old full-list response stops within its bound and reports `NotSupported`. Rolling back removes the opt-in fields from new requests; existing full-list callers are unaffected.

## Security Considerations

Authorization runs before the registry emits each page and again under the current policy for every continuation. A cursor isn't a bearer credential and contains no grant that the server trusts. Integrity protection and context binding prevent editing or swapping cursor positions, while bounded length and expiry limit resource use.

Anonymous cursors identify only the anonymous catalog context, not a person. Private repository IDs can't appear in an anonymous page or cursor plaintext. Denials and malformed-cursor responses don't reveal whether a private repository exists.

## Privacy Considerations

Cursors can contain a repository position and filter context. Their encoding avoids exposing private identifiers to the caller, and logs exclude cursor payloads. Expiry limits the lifetime of that navigation state. Pagination doesn't add account data, repository content, or new audit records.

## Risks and Assumptions

**Assumptions**

- **Assumption:** The user-service policy store can filter authorized repositories and seek by repository ID in one bounded query — *invalidated if:* it can only compute the full authorized set. That implementation needs a different indexed policy representation before enabling pagination.
- **Assumption:** Repository IDs remain unique and aren't reused — *invalidated if:* a registry can associate the same ID with a different repository. Such a registry violates the existing repository identity contract and must reject the conflict.

**Risks**

- **Risk:** A filtered page appears complete because it has fewer records than requested — *mitigation:* require the explicit terminal marker and test empty intermediate pages.
- **Risk:** An old upstream ignores pagination and consumes resources before the client detects it — *mitigation:* cancel after one excess record, keep no accumulated list, and deploy the registry first.
- **Risk:** Policy changes produce confusing page membership — *mitigation:* document weak cross-page consistency, reauthorize every page, and reconcile client entries by repository ID.
- **Risk:** Cursor forgery or replay crosses caller or filter contexts — *mitigation:* authenticate cursors, bind their context, expire them, and never derive access from cursor contents.

## Drawbacks

- Streaming pagination needs a terminal message distinct from repository messages.
- Creator filtering can require several requests before displaying a record.
- Weak cross-page consistency can require client reconciliation after policy or catalog changes.

## Alternatives Considered

### Collect and page inside Lore Server

Keep the registry stream unchanged and divide its complete result into client pages.

*Rejected because:* upstream memory and transfer still grow with the full visible catalog. The current code already follows this shape internally and can't satisfy bounded anonymous or browser enumeration.

### Replace streaming list RPCs with unary pages

Introduce unary request-response RPCs and remove or bypass the existing streams.

*Rejected because:* additive fields preserve current callers and streaming backpressure. A parallel RPC set would duplicate catalog behavior and require a broader client migration.

### Return a consistent catalog snapshot

Create a server-held snapshot and page through it with a snapshot identifier.

*Rejected because:* it adds durable or expiring per-client state, cleanup, and snapshot storage. Repository browsing doesn't require that cost, and current-policy checks on every page provide clearer revocation behavior.

## Prior Art

The existing [`RepositoryCatalog`](../../lore-server/src/authnz/repository_catalog.rs) trait already uses page size and continuation tokens, and the legacy authentication catalog exposes the same pattern through `LookupUserPermissions`. This proposal extends that contract through the user-service registry instead of adding a browser-specific mechanism.

## Unresolved Questions

- What default and maximum page sizes should the reference configuration use?
- Which cursor integrity and expiry mechanism should Lore Server and the Go control plane share?
- When should the compatibility full-list path become deprecated?
