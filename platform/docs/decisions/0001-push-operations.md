---
status: accepted
date: 2026-11-20
---

# ADR-0001: Push as a server-side operation the client polls

Decisions this fork makes about the Lore protocol and server live here, apart from upstream's
`docs/developing/decisions`, whose numbering they must not take.

## Context and Problem Statement

`lore push` sends the fragments of a revision, then asks the server to move the branch with one
`lore.revision.v1.RevisionService/BranchPush` call. Before the branch moves, the server checks that
every fragment the revision adds is stored. For a large revision that check takes longer than one
request may last. The server's `request_handler_timeout_seconds` (50 s by default, kept below the
load balancer's limit) cancels it. The client then cannot tell whether the branch moved. A 74 GB
commit failed this way every time.

The server has to verify for as long as verification takes, the client has to learn the outcome
for certain, and neither may wait forever on a verification that has stopped.

## Decision Drivers

- No request bounds how long verification may take.
- A lost reply, a reconnect or a restarted server doesn't lose the outcome or move the branch twice.
- A verification that stops making progress ends, with a clear message, without cutting off one
  that is still working.
- The fork's additions don't collide with upstream's protocol or storage when the fork merges
  upstream.
- The server accepts every client that connects. The storage, not a count of operations, limits
  the load.

## Considered Options

1. Raise `request_handler_timeout_seconds` and the load balancer's limit.
2. Keep the call open and stream keep-alive messages until the outcome.
3. Add an idempotency key to `BranchPush`, and let the client repeat it after a timeout.
4. A push operation: `Start` registers it and returns, a worker verifies, the client polls `Get`.

## Decision Outcome

Chosen option 4, because it is the only one with no limit on verification time that still gives a
certain outcome after a lost reply. Options 1 and 2 keep one request open as long as the
verification and only move the limit: a proxy, a client network change or a server restart still
loses the answer. Option 3 makes the client repeat the verification from the start after every
timeout, so a push that never fits in one request never completes.

### How it works

- `lore.platform.v1.BranchPushOperationService` (`lore-proto/proto/lore/platform/v1/
  push_operation.proto`) has `Start` and `Get`. The client generates a 16-byte operation id and keeps
  it across retries. `Start` with the same id and the same push answers the current state;
  `ALREADY_EXISTS` refuses the same id with a different push.
- The state lives in the mutable store under `KeyType::PushJournal`, so any server instance answers.
  The registration key holds the request. The receipt key holds either an inline lease while a
  worker owns the push, or the outcome once it is decided.
- A worker renews its lease every 10 s, and the lease lasts 60 s. If the worker dies, the next
  `Get` after the lease expires starts the push again. A renewal needs no fsync: a lease lost in a
  crash only lets the next query take over sooner.
- The exact lease value a worker last wrote is its fencing token. The worker moves the branch and
  records `Accepted` in one paired compare-and-swap (`MutableStore::compare_and_swap_pair`) that
  expects that value. A worker that lost its lease can therefore neither move the branch nor
  overwrite its successor's outcome.
- **Progress, not time, bounds a push.** The worker publishes its phase and the store requests it
  has completed with every renewal. `Get` returns them, the CLI prints them, and the Windows client
  shows them in the Commit window. If neither changes for `push_operation_stall_seconds`
  (default 300 s), the worker releases the push for a restart. After three restarts the push is
  rejected with `DEADLINE_EXCEEDED`, and the message says the revision is saved locally. A long
  verification that keeps checking fragments is never cut off.
- A transient failure (`RESOURCE_EXHAUSTED` for "slow down", `UNAVAILABLE`) restarts the push
  after a pause of 2, 4, 8 … 60 s, up to ten times, instead of becoming the outcome.
- Every push starts verifying at once. A cap on concurrent operations is the wrong layer: the store
  already throttles load by asking verifications to slow down, and those pauses no longer fail a
  push.
- Journal entries are removed `push_operation_retention_days` (default 7) after they were decided
  or abandoned, by a sweep that runs at most hourly per repository when a push starts.
- A client falls back to `BranchPush` only when `Start` answers `UNIMPLEMENTED`: a server without
  this service, or one whose mutable store cannot write a pair (`remote` mode). Such a server logs a
  warning at start.

### Why a paired compare-and-swap

Moving the branch and recording the outcome must happen together. Repeating the push after a crash
does not give the same answer. If another push landed on top meanwhile, the repeat sees a diverged
branch and would report a refusal for a push that succeeded. If the server had merged the push onto
a moved branch, the repeat would merge it a second time. Writing an intent first and confirming it
from the branch history afterwards avoids the pair, but it needs a history walk for every recovery
and still races with later pushes. The local store writes the pair in one bucket: the receipt key
shares the branch key's first two bytes. DynamoDB uses `TransactWriteItems`.

### Namespace

The service is in `lore.platform.v1` and the key type is 200, far from upstream's range. An
upstream change to `lore.revision.v1` or a new upstream key type can't collide with them, and
merges from upstream don't touch these files. The key type is not in the C header
(`lore-capi/build.rs` strips it), and `KeyType::try_from` does not decode it, so no raw storage
request can reach the journal. Version 0.2.7 used `lore.revision.v1` and key type 8. Installations
were reinstalled for 0.2.8, so 0.2.8 keeps no alias for them.

The problem and this design are to be offered upstream (EpicGames/lore). If upstream adopts its own
solution, the fork moves to it.

### Consequences

- Good: a push of any size completes or fails with a certain outcome, and a lost reply costs
  nothing.
- Good: the client shows what the server is doing, and a stalled verification ends on its own.
- Bad: the mutable store API gains `compare_and_swap_pair`, which every backend that wants push
  operations has to implement. `remote` mode doesn't, and keeps the 50 s limit.
- Bad: the server keeps state per push for a week. A push costs two immutable blobs and two
  mutable keys.
