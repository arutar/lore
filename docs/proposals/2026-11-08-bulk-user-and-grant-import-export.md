---
lep: 2026-11-08-bulk-user-and-grant-import-export
title: Bulk user and grant import and export
authors:
  - arutar
status: Draft
created: 2026-11-08
updated: 2026-11-08
discussion: pending
---

# Bulk user and grant import and export

## Summary

This proposal defines versioned CSV and JSON formats for bulk administration of local users and repository grants in the [repository policy control plane](2026-11-08-repository-policy-control-plane.md). Administrators can validate an import before applying it, repeat it without duplicating state, inspect row-level errors, and export the same schemas. Credentials, Active Directory passwords, OIDC secrets, and tokens never enter these formats.

## Motivation

Assigning repository access one user at a time doesn't scale when a team joins a project or an administrator reorganizes several repositories. Repeating manual operations also makes it difficult to review a proposed change or reproduce it in another deployment.

An unversioned spreadsheet format creates a different problem: ambiguous columns, partial application, and silent handling of unknown values can produce broad unintended grants. Bulk administration needs a stable external contract and explicit failure behavior.

## Goals / Non-Goals

### Goals

- Import and export local users, group memberships, and repository grants in CSV and JSON.
- Validate the complete input before changing accounts or policy.
- Support dry-run, row-level diagnostics, confirmation, and repeatable application.
- Version schemas and define unknown-field, duplicate-row, and mixed-version behavior.
- Keep credentials and Active Directory-managed attributes out of import and export.
- Audit every applied batch and recover from partial infrastructure failures.

### Non-Goals

- Defining the underlying account or grant model. The [repository policy control plane](2026-11-08-repository-policy-control-plane.md) owns it.
- Importing passwords, password hashes, tokens, private keys, or Active Directory accounts into the directory itself.
- Defining the browser presentation of import and export.
- Synchronizing arbitrary identity-provider configuration.

## Proposed Design

### Schema envelopes and versions

JSON documents use an object with required `schema` and `items` fields. The supported values are `lore.users/v1` and `lore.grants/v1`. CSV files carry the same value in a required `schema` column on every row. Required columns have stable names; column order doesn't affect meaning.

A missing or unsupported major schema version rejects the entire document. Unknown fields, columns, actions, subject types, and permission values reject their rows and prevent application. This avoids silently ignoring a misspelled security-sensitive value. A future compatible addition requires an explicit schema revision and reader support.

### User schema

User items contain `username`, `email`, `displayName`, `globalRole`, `groups`, and `action`. CSV represents `groups` as a semicolon-separated list of stable group identifiers; JSON uses an array. Actions are `upsert` and `disable`.

`upsert` creates a local account or updates mutable local attributes and group memberships. Account creation uses a Keycloak invitation or reset flow. `disable` disables the local account and prevents future token issuance. Active Directory-managed accounts and attributes are read-only and produce a validation error when an import attempts to change them.

### Grant schema

Grant items contain `repository`, `subjectType`, `subject`, `permission`, and `action`. Subject types are `localUser`, `adUser`, `localGroup`, `adGroup`, `anonymous`, and `authenticated`. Subject identifiers use username or email for users and `objectGUID` or SID for Active Directory groups. Actions are `upsert` and `remove`.

Permissions follow the control-plane hierarchy. `anonymous` and `authenticated` accept `read` or `none` only. `remove` deletes the matching grant rather than adding an additive deny rule. Unknown repositories, subjects, or invalid combinations fail validation.

### Validation and application

Import first parses and validates the complete bounded document. Dry-run resolves accounts, groups, repositories, duplicates, and effective changes without writing anything. It reports row number, stable error code, and non-secret field name for every failure. Error codes are versioned with the schema; message text is presentation.

An administrator confirms a valid dry-run to apply the batch. Confirmation binds the document digest, the reviewed effective diff, and the base account and policy versions. A concurrent change to any affected account or grant invalidates the confirmation and requires a new dry-run; the service doesn't revalidate into a different effective change under the old approval.

Identical duplicate rows collapse into one deterministic action. Rows naming the same account or grant with different actions, roles, attributes, memberships, or permissions reject the batch. Repeating a document produces the same final state and no duplicate accounts, memberships, or grants.

Application uses bounded transactional chunks. A database error rolls back its chunk. The import job records completed chunks so retry doesn't repeat them. An atomic job or chunk lease with a fencing token gives one worker write ownership; a stale worker can't commit after another replica takes over. External Keycloak operations carry stable idempotency keys, and reconciliation resolves an uncertain external outcome before retry. Jobs expose pending, validating, awaiting confirmation, applying, completed, failed, and cancelled states.

### Export and audit

Exports use the same schema envelopes and stable fields accepted by import. Keyset-paginated source queries feed streaming CSV or JSON serialization into protected temporary storage or the response, with bounded rows in flight. Cancellation stops the next source query and serialization. Configured output-size and time ceilings terminate an export with a explicit truncated-result error rather than presenting a partial file as complete. Exports contain no credentials, tokens, password hashes, LDAP secrets, or OIDC client secrets. Spreadsheet-facing CSV export escapes cells that could be interpreted as formulas.

Each applied batch records its administrator, time, schema, content digest, item counts, and per-item outcome in the append-only audit log. The digest identifies the imported document without storing unnecessary personal-data copies.

## Compatibility

- **Wire format** — N/A for Lore protocols. Control-plane import APIs accept bounded CSV or JSON bodies and return versioned job and error resources.
- **Client/server protocols** — `/v1/imports/users`, `/v1/imports/grants`, and `/v1/exports/` create or retrieve jobs and documents. Dry-run and confirmation are distinct operations. Unsupported schema major versions fail validation. Export errors distinguish cancellation and truncation from a complete document.
- **On-disk format** — N/A for Lore repositories. Import jobs and audit records use the control-plane schema and migrations.
- **CLI and public API** — N/A for the Lore CLI and SDKs. The CSV and JSON schemas are new control-plane public formats with the versioning rules in Proposed Design.
- **Error contract** — Validation error codes are stable within a schema major version. Clients present unknown codes as generic failures rather than ignoring them.

## Non-Functional Considerations

- **Concurrency** — Import jobs use bounded chunks, optimistic policy versions, and fenced worker leases. A conflicting administrator change invalidates the reviewed confirmation and requires a new dry-run before further application.
- **Memory** — Parser input, rows per chunk, group memberships, export rows in flight, and job retention are bounded. Imports and exports stream to protected temporary storage or the response rather than accumulating in process memory.
- **Statelessness** — Durable import-job state lives in PostgreSQL. A replica can resume a chunk only after acquiring its fenced lease.
- **Determinism** — The same document and base state produce the same validation result and final policy regardless of chunk boundaries.
- **Durability** — Chunks and their audit outcomes commit together. Retry skips committed chunks, and fencing prevents a stale worker from committing.
- **Backpressure** — Concurrent jobs, file size, chunk size, export size, and processing time have configured limits.

## Migration Plan

N/A — no breaking changes, no migration required. The first schema versions are `lore.users/v1` and `lore.grants/v1`. New major versions require separate import and export support rather than reinterpreting existing files.

## Security Considerations

Only administrators can create, confirm, or download bulk jobs. The confirmation step binds the applied content digest to the validated digest, so a file can't be replaced after review. Stored uploads have restricted permissions and expire after the configured retention period.

Validation prevents unexpected privilege escalation through unknown actions, duplicate conflicts, special subjects, or spreadsheet formulas. Exports never include credentials. Audit and error responses avoid echoing sensitive field contents.

## Privacy Considerations

User files contain identifiers, email addresses, names, roles, and group memberships. Grant files reveal repository access relationships. Access to both requires administrative authorization, encrypted transport, and protected temporary storage.

Expired uploads are deleted. Audit retains the content digest and outcomes rather than a full source-file copy. Export and deletion events are auditable.

## Risks and Assumptions

**Assumptions**

- **Assumption:** Usernames, emails, and stable group identifiers uniquely resolve import subjects — *invalidated if:* a deployment has ambiguous directory mappings. Such rows must fail rather than selecting an arbitrary subject.
- **Assumption:** Administrators can review dry-run changes before applying them — *invalidated if:* automation requires unattended imports. That use case needs separately scoped API credentials and policy restrictions.

**Risks**

- **Risk:** A valid-looking file assigns broader access than its reviewer expected — *mitigation:* show effective additions and removals in dry-run, bind confirmation to the digest, and require explicit special-subject validation.
- **Risk:** A retry duplicates completed work — *mitigation:* record content digest and committed chunks, use idempotent upserts, and test retries at every chunk boundary.
- **Risk:** CSV formulas execute when an export opens in spreadsheet software — *mitigation:* prefix formula-leading cells and test all supported export fields.
- **Risk:** Import jobs retain personal data longer than required — *mitigation:* expire uploads, store digests in audit, and test deletion.

## Drawbacks

- Two formats and their schemas require ongoing compatibility tests.
- Dry-run resolution adds a second pass over every row before any change can apply.
- Chunk recovery makes import jobs durable state that operators must monitor and expire.

## Alternatives Considered

### Keycloak Admin CSV export alone

Export accounts from Keycloak and leave repository grants in a separate manual interface.

*Rejected because:* identity-provider export doesn't include Lore grants, stable effective-policy reconciliation, or a common review and recovery flow. It can also expose identity data that this proposal intentionally excludes.

### Unversioned CSV with best-effort application

Accept common column names, skip invalid rows, and apply valid rows immediately.

*Rejected because:* skipped unknown permissions and partial application can leave unintended grants with no reliable review or rollback. Stable schemas and validation-before-write provide a safer administrative contract.

### Direct SQL import

Load users and grants into PostgreSQL through administrator SQL scripts.

*Rejected because:* it bypasses Keycloak account lifecycle operations, policy invariants, idempotency, audit, and service-level authorization. It also couples an external procedure to internal schema details.

## Prior Art

SCIM 2.0 provides standardized user and group provisioning, but it doesn't define Lore repository grants or the requested spreadsheet workflow. The formats in this proposal remain deliberately smaller and control-plane-specific.

GitHub's [repository role model](https://docs.github.com/en/organizations/managing-user-access-to-your-organizations-repositories/managing-repository-roles/repository-roles-for-an-organization) provides a useful subject and permission reference, but its export formats aren't Lore-compatible.

## Unresolved Questions

- Should unattended imports use signed job confirmations or a separately scoped API credential?
- What upload size, chunk size, and retention defaults should the reference deployment use?
- Should exports include effective group-derived grants or only directly stored grants?
