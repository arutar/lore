// SPDX-License-Identifier: MIT
//! Branch pushes that run as server-side operations (`lore.platform.v1`).
//!
//! The design and its alternatives are recorded in
//! `platform/docs/decisions/0001-push-operations.md`. In short: `Start` registers the push under a
//! client-chosen id and a worker verifies it in the background, `Get` reports its state. The
//! state lives in the mutable store under [`KeyType::PushJournal`], so any server instance can
//! answer and a crashed worker's operation is taken over by the next query.
//!
//! The journal holds two keys per operation:
//!
//! - the registration, pointing to an immutable [`Entry`] with the request;
//! - the receipt, holding either an inline [`Lease`] while a worker owns the push, or a pointer to
//!   an immutable [`Entry`] with the outcome once it is decided.
//!
//! The exact receipt value a worker last wrote is its fencing token: a worker can move the branch
//! or record an outcome only while the receipt still holds that value, so a worker that lost its
//! lease can never overwrite its successor.
use std::collections::HashMap;
use std::sync::Arc;
use std::sync::atomic::AtomicBool;
use std::sync::atomic::AtomicU8;
use std::sync::atomic::AtomicU64;
use std::sync::atomic::Ordering;
use std::time::Duration;
use std::time::SystemTime;
use std::time::UNIX_EPOCH;

use lore_base::runtime::LORE_CONTEXT;
use lore_base::types::Address;
use lore_base::types::BranchId;
use lore_base::types::Context;
use lore_base::types::Hash;
use lore_base::types::KeyType;
use lore_base::types::RepositoryId;
use lore_proto::lore::platform::v1 as rpc;
use lore_proto::lore::revision::v1 as revision_rpc;
use lore_revision::branch;
use lore_revision::immutable;
use lore_revision::repository::RepositoryContext;
use lore_revision::util::request_tracker::StoreRequestTracker;
use lore_storage::ReadConsistency;
use lore_storage::mutable_store::ConditionalWrite;
use prost::Message;
use rpc::branch_push_operation::Phase;
use rpc::branch_push_operation::Processing;
use rpc::branch_push_operation::Rejected;
use rpc::branch_push_operation::State;
use rpc::branch_push_operation_service_server::BranchPushOperationService;
use tokio::sync::Mutex;
use tokio::time::Instant;
use tokio_stream::StreamExt;
use tonic::Request;
use tonic::Response;
use tonic::Status;

use super::branch_push;
use super::service::LoreRevisionV1Service;
use crate::grpc::extract_correlation_id;
use crate::grpc::get_repository;
use crate::grpc::get_user_id;
use crate::grpc::handlers::branch_push::PushAccess;
use crate::grpc::handlers::branch_push::PushPhase;
use crate::util::core_hop::outliving_request;
use crate::util::setup_execution;

/// How many times a push is restarted after it stopped making progress, or its worker vanished,
/// before the operation is rejected.
pub const MAX_STALLS: u8 = 3;

/// How many times a push that failed with a transient storage error (slow down, unavailable) is
/// retried before the error becomes its outcome.
pub const MAX_RETRIES: u8 = 10;

/// How often [`push_status`](LoreRevisionV1Service::push_status) looks for journal entries past
/// their retention, per repository.
const SWEEP_INTERVAL: Duration = Duration::from_secs(3600);

/// How the server runs push operations. `[feature]` settings, see `config/default.toml`.
#[derive(Clone, Copy, Debug)]
pub struct PushOperationSettings {
    /// How long a verification may go without progress before the server restarts it. Progress
    /// is any store request the verification completes or any change of phase, so this bounds a
    /// single step, never the whole push.
    pub stall: Duration,
    /// How long a lease outlives its worker. Another server instance takes over an operation
    /// whose lease expired.
    pub lease: Duration,
    /// How often a worker renews its lease and publishes its progress.
    pub renew: Duration,
    /// How long a decided operation stays in the journal for clients to query.
    pub retention: Duration,
}

impl Default for PushOperationSettings {
    fn default() -> Self {
        Self {
            stall: Duration::from_secs(300),
            lease: Duration::from_secs(60),
            renew: Duration::from_secs(10),
            retention: Duration::from_secs(7 * 24 * 3600),
        }
    }
}

/// The server's push operations. Every push starts its verification at once, however many run:
/// the store, not a count of operations, is what limits the load, and it does so by asking
/// verifications to slow down.
pub(super) struct Workers {
    settings: PushOperationSettings,
    /// When each repository's journal was last swept.
    swept: parking_lot::Mutex<HashMap<RepositoryId, Instant>>,
}

impl Workers {
    pub(super) fn new(settings: PushOperationSettings) -> Self {
        Self {
            settings,
            swept: Default::default(),
        }
    }
}

impl Default for Workers {
    fn default() -> Self {
        Self::new(PushOperationSettings::default())
    }
}

/// An immutable journal record. Exactly one field is set.
#[derive(Clone, PartialEq, Message)]
struct Entry {
    #[prost(message, optional, tag = "1")]
    registration: Option<Registration>,
    #[prost(message, optional, tag = "2")]
    outcome: Option<Outcome>,
}

#[derive(Clone, PartialEq, Message)]
struct Registration {
    #[prost(message, optional, tag = "1")]
    push: Option<revision_rpc::BranchPushRequest>,
    #[prost(uint64, tag = "2")]
    created_ms: u64,
}

#[derive(Clone, PartialEq, Message)]
struct Outcome {
    #[prost(message, optional, tag = "1")]
    result: Option<rpc::BranchPushOperation>,
    /// Set when the branch moved but the worker may still add the response hooks' message.
    /// Queries report the push as running until then.
    #[prost(uint64, tag = "2")]
    lease_until_ms: u64,
    #[prost(uint64, tag = "3")]
    finished_ms: u64,
}

/// The receipt value while a worker owns the push, stored inline so that a renewal writes no
/// immutable data.
///
/// Layout: 8 bytes of [`LEASE_MAGIC`], the lease expiry in milliseconds since the epoch, a random
/// generation, the phase, the stall and retry counts, a released flag and the checked count. An
/// outcome pointer is a content hash; one that starts with the magic is a 2^-64 chance.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
#[lore_macro::test_pub]
struct Lease {
    until_ms: u64,
    generation: u64,
    phase: u8,
    stalls: u8,
    retries: u8,
    /// The worker gave the push up on purpose (a transient error), so taking it over is not a
    /// stall.
    released: bool,
    checked: u32,
}

const LEASE_MAGIC: [u8; 8] = *b"lpleas\x00\x01";

impl Lease {
    #[lore_macro::test_pub]
    fn encode(&self) -> Hash {
        let mut data = [0u8; 32];
        data[..8].copy_from_slice(&LEASE_MAGIC);
        data[8..16].copy_from_slice(&self.until_ms.to_be_bytes());
        data[16..24].copy_from_slice(&self.generation.to_be_bytes());
        data[24] = self.phase;
        data[25] = self.stalls;
        data[26] = self.retries;
        data[27] = u8::from(self.released);
        data[28..32].copy_from_slice(&self.checked.to_be_bytes());
        Hash::from(data)
    }

    #[lore_macro::test_pub]
    fn decode(value: &Hash) -> Option<Self> {
        let data = value.data();
        if data[..8] != LEASE_MAGIC {
            return None;
        }
        let u64_at =
            |at: usize| u64::from_be_bytes(data[at..at + 8].try_into().unwrap_or_default());
        Some(Self {
            until_ms: u64_at(8),
            generation: u64_at(16),
            phase: data[24],
            stalls: data[25],
            retries: data[26],
            released: data[27] != 0,
            checked: u32::from_be_bytes(data[28..32].try_into().unwrap_or_default()),
        })
    }

    fn processing(&self) -> rpc::BranchPushOperation {
        processing(
            Phase::try_from(i32::from(self.phase)).unwrap_or(Phase::Unspecified),
            u64::from(self.checked),
            u32::from(self.stalls) + u32::from(self.retries),
        )
    }
}

fn now_ms() -> Result<u64, Status> {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|v| v.as_millis() as u64)
        .map_err(internal)
}

fn ms(duration: Duration) -> u64 {
    duration.as_millis() as u64
}

fn internal(error: impl std::fmt::Display) -> Status {
    Status::internal(error.to_string())
}

fn processing(phase: Phase, checked: u64, restarts: u32) -> rpc::BranchPushOperation {
    rpc::BranchPushOperation {
        state: Some(State::Processing(Processing {
            phase: phase as i32,
            checked,
            restarts,
        })),
    }
}

/// Whether a failed push may succeed when run again unchanged: the store asked to slow down or
/// was unreachable.
#[lore_macro::test_pub]
fn transient(status: &Status) -> bool {
    matches!(
        status.code(),
        tonic::Code::ResourceExhausted | tonic::Code::Unavailable
    )
}

/// The wait before retry `attempt` (from 1) of a transiently failed push.
#[lore_macro::test_pub]
fn retry_delay(attempt: u8) -> Duration {
    Duration::from_secs((1u64 << attempt.min(6)).min(60))
}

fn phase_of(phase: PushPhase) -> Phase {
    match phase {
        PushPhase::Prepare => Phase::Prepare,
        PushPhase::Verify => Phase::Verify,
        PushPhase::FastForwardMerge => Phase::Merge,
        PushPhase::Commit => Phase::Commit,
        PushPhase::Respond => Phase::Respond,
    }
}

fn store(repo: &RepositoryContext) -> Result<Arc<dyn lore_storage::MutableStore>, Status> {
    repo.try_mutable_store_arc()
        .ok_or_else(|| Status::internal("push store is read-only"))
}

async fn pointer(repo: &RepositoryContext, key: Hash, kind: KeyType) -> Result<Hash, Status> {
    match store(repo)?.load(repo.id, key, kind).await {
        Ok(value) => Ok(value),
        Err(error) if error.is_address_not_found() => Ok(Hash::default()),
        Err(error) => Err(internal(error)),
    }
}

/// Writes `entry` and returns its content hash.
///
/// Journal writes take the stores' own write-back, as the branch head and the pushed fragments
/// do, rather than a flush. A flush writes every dirty bucket of the server's store, so right
/// after a large upload it held a push for many seconds. A crash that loses a journal write loses
/// the branch move it records too; the client then starts the operation again.
async fn put(repo: Arc<RepositoryContext>, entry: &Entry) -> Result<Hash, Status> {
    let address = immutable::write(
        repo.clone(),
        Context::default(),
        entry.encode_to_vec().into(),
        Default::default(),
    )
    .await
    .map_err(internal)?;
    Ok(address.hash)
}

async fn read(repo: Arc<RepositoryContext>, hash: Hash) -> Result<Entry, Status> {
    let data = immutable::read(
        repo,
        Address::zero_context_hash(hash),
        None,
        Default::default(),
    )
    .await
    .map_err(internal)?;
    Entry::decode(data).map_err(internal)
}

/// Swaps a journal value, returning the previous one. See [`put`] for why it doesn't flush.
async fn swap(repo: &RepositoryContext, key: Hash, old: Hash, new: Hash) -> Result<Hash, Status> {
    store(repo)?
        .compare_and_swap(repo.id, key, old, new, KeyType::PushJournal)
        .await
        .map_err(internal)
}

fn operation_key(user: &str, id: &[u8]) -> Result<Hash, Status> {
    if id.len() != 16 || id.iter().all(|byte| *byte == 0) {
        return Err(Status::invalid_argument(
            "expected a nonzero 16-byte operation id",
        ));
    }
    Ok(Hash::hash_buffer(
        &[b"lore/push/request/v1", user.as_bytes(), id].concat(),
    ))
}

/// The receipt shares the branch head key's first two bytes, which the local store's paired
/// write needs to keep both in one bucket.
fn receipt_key(registration: Hash, head: Hash) -> Hash {
    let mut key =
        Hash::hash_buffer(&[b"lore/push/receipt/v1".as_slice(), registration.data()].concat());
    key.data_mut()[..2].copy_from_slice(&head.data()[..2]);
    key
}

/// What a running push has done so far, read by its worker to tell progress from a stall.
#[derive(Default)]
pub(crate) struct Progress {
    phase: AtomicU8,
    steps: AtomicU64,
    /// Shared with the verification's tree walk, which counts its store requests in it.
    pub(crate) store_requests: Arc<StoreRequestTracker>,
}

impl Progress {
    pub(crate) fn enter(&self, phase: PushPhase) {
        self.phase
            .store(phase_of(phase) as i32 as u8, Ordering::Relaxed);
    }

    /// Counts `count` units of work that the store request tracker does not see.
    pub(crate) fn advance(&self, count: u64) {
        self.steps.fetch_add(count, Ordering::Relaxed);
    }

    fn checked(&self) -> u64 {
        self.steps.load(Ordering::Relaxed)
            + self.store_requests.lookups()
            + self.store_requests.metadata_reads()
    }

    fn snapshot(&self) -> (u8, u64) {
        (self.phase.load(Ordering::Relaxed), self.checked())
    }
}

#[derive(Clone)]
pub(crate) struct Receipt {
    key: Hash,
    /// The receipt value this worker last wrote: its fencing token.
    lease: Arc<Mutex<Hash>>,
    /// The generation and counters this worker's leases carry.
    base: Lease,
    settings: PushOperationSettings,
    committed: Arc<AtomicBool>,
    pub(crate) progress: Arc<Progress>,
}

impl Receipt {
    fn next_lease(&self, until_ms: u64) -> Lease {
        let (phase, checked) = self.progress.snapshot();
        Lease {
            until_ms,
            phase,
            checked: u32::try_from(checked).unwrap_or(u32::MAX),
            ..self.base
        }
    }

    /// Extends the lease and publishes the progress. `false` when another worker took the push
    /// over.
    async fn renew(&self, repo: Arc<RepositoryContext>) -> Result<bool, Status> {
        let until = now_ms()? + ms(self.settings.lease);
        self.replace(&repo, self.next_lease(until)).await
    }

    /// Ends this worker's lease so that the next query restarts the push after `delay`. A
    /// `released` lease does not count as a stall when taken over.
    async fn release(
        &self,
        repo: &RepositoryContext,
        delay: Duration,
        lease: Lease,
    ) -> Result<bool, Status> {
        let until = now_ms()? + ms(delay);
        self.replace(
            repo,
            Lease {
                until_ms: until,
                ..lease
            },
        )
        .await
    }

    async fn replace(&self, repo: &RepositoryContext, next: Lease) -> Result<bool, Status> {
        let mut lease = self.lease.lock().await;
        if self.committed.load(Ordering::Relaxed) {
            return Ok(true);
        }
        let next = next.encode();
        if swap(repo, self.key, *lease, next).await? != *lease {
            return Ok(false);
        }
        *lease = next;
        Ok(true)
    }

    async fn finish(&self, repo: Arc<RepositoryContext>, state: State) -> Result<(), Status> {
        if self.committed.load(Ordering::Relaxed) && !matches!(&state, State::Accepted(_)) {
            return Ok(());
        }
        let value = put(repo.clone(), &outcome(state, 0)?).await?;
        let lease = self.lease.lock().await;
        swap(&repo, self.key, *lease, value).await?;
        Ok(())
    }

    /// Moves the branch from `expected` to `revision` and records the push as accepted, both or
    /// neither. Returns the head found, `expected` when the branch moved.
    pub(crate) async fn commit(
        &self,
        repo: Arc<RepositoryContext>,
        branch_id: BranchId,
        expected: Hash,
        revision: Hash,
        revision_number: u64,
        merged: bool,
    ) -> Result<Hash, Status> {
        let (key, kind) = branch::mutable_key(repo.salt(), branch::LATEST, repo.id, branch_id);
        let state = State::Accepted(revision_rpc::BranchPushResponse {
            revision_signature: revision.into(),
            revision_number,
            fast_forward_merged: merged,
            message: None,
        });
        // A live worker may still supply a response hook message. After a crash,
        // the committed result is authoritative once this short lease expires.
        let lease_until = now_ms()? + ms(self.settings.lease);
        let value = put(repo.clone(), &outcome(state, lease_until)?).await?;
        let mut lease = self.lease.lock().await;
        let storage = store(&repo)?;
        loop {
            let committed = storage
                .clone()
                .compare_and_swap_pair(
                    repo.id,
                    [
                        ConditionalWrite {
                            key,
                            kind,
                            expected,
                            value: revision,
                        },
                        ConditionalWrite {
                            key: self.key,
                            kind: KeyType::PushJournal,
                            expected: *lease,
                            value,
                        },
                    ],
                )
                .await
                .map_err(internal)?;
            if committed {
                *lease = value;
                self.committed.store(true, Ordering::Relaxed);
                return Ok(expected);
            }
            if pointer(&repo, self.key, KeyType::PushJournal).await? != *lease {
                return Err(Status::aborted(
                    "push operation already finalized or reclaimed",
                ));
            }
            let current = pointer(&repo, key, kind).await?;
            if current != expected {
                return Ok(current);
            }
        }
    }
}

fn outcome(state: State, lease_until_ms: u64) -> Result<Entry, Status> {
    Ok(Entry {
        registration: None,
        outcome: Some(Outcome {
            result: Some(rpc::BranchPushOperation { state: Some(state) }),
            lease_until_ms,
            finished_ms: now_ms()?,
        }),
    })
}

/// The request a status query answers.
enum Call {
    /// `Start`, with its push, which a malformed request may lack.
    Start(Option<revision_rpc::BranchPushRequest>),
    Get,
}

/// Why a worker stopped before its push returned.
enum Stop {
    /// Another worker owns the push now.
    Lost,
    /// The push made no progress for the stall timeout.
    Stalled,
}

#[tonic::async_trait]
impl BranchPushOperationService for LoreRevisionV1Service {
    async fn start(
        &self,
        request: Request<rpc::StartBranchPushRequest>,
    ) -> Result<Response<rpc::BranchPushOperation>, Status> {
        let (metadata, extensions, start) = request.into_parts();
        let request = Request::from_parts(metadata, extensions, ());
        crate::grpc::timeout_grpc(
            self.rpc_timeout,
            self.push_status(request, start.operation_id, Call::Start(start.push)),
        )
        .await
    }

    async fn get(
        &self,
        request: Request<rpc::GetBranchPushOperationRequest>,
    ) -> Result<Response<rpc::BranchPushOperation>, Status> {
        let (metadata, extensions, query) = request.into_parts();
        crate::grpc::timeout_grpc(
            self.rpc_timeout,
            self.push_status(
                Request::from_parts(metadata, extensions, ()),
                query.operation_id,
                Call::Get,
            ),
        )
        .await
    }
}

impl LoreRevisionV1Service {
    async fn push_status(
        &self,
        request: Request<()>,
        id: bytes::Bytes,
        call: Call,
    ) -> Result<Response<rpc::BranchPushOperation>, Status> {
        let repo_id = get_repository(request.metadata())?;
        if repo_id == Default::default() {
            return Err(Status::invalid_argument("expected a nonzero repository id"));
        }
        // Access first: nothing about the request is read for a caller who may not push.
        PushAccess::of(&self.repository_authorizer, request.extensions(), repo_id).await?;
        let start = match call {
            Call::Start(push) => {
                Some(push.ok_or_else(|| Status::invalid_argument("missing push"))?)
            }
            Call::Get => None,
        };
        let user = get_user_id(request.extensions());
        let registration = operation_key(&user, &id)?;
        let execution = setup_execution(
            module_path!(),
            extract_correlation_id(&request).unwrap_or_default(),
            user,
        );
        LORE_CONTEXT
            .scope(
                execution,
                self.push_status_scoped(request, start, repo_id, registration),
            )
            .await
    }

    async fn push_status_scoped(
        &self,
        request: Request<()>,
        start: Option<revision_rpc::BranchPushRequest>,
        repo_id: RepositoryId,
        registration: Hash,
    ) -> Result<Response<rpc::BranchPushOperation>, Status> {
        if !self.mutable_store().supports_atomic_pair() {
            return Err(Status::unimplemented(
                "backend does not support persistent push operations",
            ));
        }
        let settings = self.push_workers.settings;
        let repo = Arc::new(RepositoryContext::new_server_context(
            self.immutable_store().clone(),
            self.mutable_store().clone(),
            repo_id,
            lore_revision::state::no_link_traversal(),
        ));
        let registered = pointer(&repo, registration, KeyType::PushJournal).await?;
        let push = match start {
            Some(push) => {
                if push.id.len() != 16
                    || push.id.iter().all(|b| *b == 0)
                    || push.revision_signature.len() != 32
                    || push.revision_signature.iter().all(|b| *b == 0)
                {
                    return Err(Status::invalid_argument("invalid branch or revision"));
                }
                self.sweep_journal(repo.clone());
                if registered.is_zero() {
                    let entry = Entry {
                        registration: Some(Registration {
                            push: Some(push.clone()),
                            created_ms: now_ms()?,
                        }),
                        outcome: None,
                    };
                    let value = put(repo.clone(), &entry).await?;
                    let previous = swap(&repo, registration, Hash::default(), value).await?;
                    if !previous.is_zero() && registered_push(&repo, previous).await? != push {
                        return Err(Status::already_exists("operation id already used"));
                    }
                } else if registered_push(&repo, registered).await? != push {
                    return Err(Status::already_exists("operation id already used"));
                }
                push
            }
            None if registered.is_zero() => {
                return Err(Status::not_found("unknown push operation"));
            }
            None => registered_push(&repo, registered).await?,
        };
        let (head, _) = branch::mutable_key(
            repo.salt(),
            branch::LATEST,
            repo_id,
            BranchId::from(&push.id),
        );
        let key = receipt_key(registration, head);
        let current = pointer(&repo, key, KeyType::PushJournal).await?;
        let now = now_ms()?;
        let mut carried = Lease::default();
        if let Some(lease) = Lease::decode(&current) {
            if lease.until_ms > now {
                return Ok(Response::new(lease.processing()));
            }
            carried = Lease {
                stalls: lease.stalls + u8::from(!lease.released),
                ..lease
            };
        } else if !current.is_zero() {
            let outcome = read(repo.clone(), current)
                .await?
                .outcome
                .ok_or_else(|| Status::internal("push receipt holds no outcome"))?;
            if outcome.lease_until_ms > now {
                return Ok(Response::new(processing(Phase::Respond, 0, 0)));
            }
            return outcome
                .result
                .map(Response::new)
                .ok_or_else(|| Status::internal("push outcome is empty"));
        }
        if carried.stalls > MAX_STALLS {
            let state = State::Rejected(Rejected {
                code: tonic::Code::DeadlineExceeded as i32,
                message: format!(
                    "The server stopped making progress verifying this push {} times; the \
                     revision is saved locally. Push again, or ask the server administrator to \
                     check the server's storage.",
                    carried.stalls
                ),
                details: Default::default(),
            });
            let value = put(repo.clone(), &outcome(state, 0)?).await?;
            if swap(&repo, key, current, value).await? == current {
                return Ok(Response::new(
                    read(repo, value)
                        .await?
                        .outcome
                        .and_then(|o| o.result)
                        .unwrap_or_default(),
                ));
            }
            return Ok(Response::new(carried.processing()));
        }
        let receipt = Receipt {
            key,
            lease: Arc::new(Mutex::new(current)),
            base: Lease {
                generation: rand::random(),
                released: false,
                ..carried
            },
            settings,
            committed: Arc::default(),
            progress: Arc::default(),
        };
        receipt.progress.enter(PushPhase::Prepare);
        if receipt.renew(repo.clone()).await? {
            if carried.stalls > 0 || carried.retries > 0 {
                tracing::info!(
                    stalls = carried.stalls,
                    retries = carried.retries,
                    "Restarting a push operation"
                );
            }
            let service = self.clone();
            let (metadata, extensions, ()) = request.into_parts();
            let mut worker_request = Request::from_parts(metadata, extensions, push);
            worker_request.extensions_mut().insert(receipt.clone());
            // The push runs past this request, which ends with the reply below.
            lore_base::lore_spawn!(outliving_request(async move {
                Box::pin(service.run_push(worker_request, repo, receipt)).await;
            }));
        }
        Ok(Response::new(carried.processing()))
    }

    async fn run_push(
        &self,
        worker_request: Request<revision_rpc::BranchPushRequest>,
        repo: Arc<RepositoryContext>,
        receipt: Receipt,
    ) {
        let settings = receipt.settings;
        let work = branch_push::handler(
            worker_request,
            self.repository_authorizer.clone(),
            self.immutable_store().clone(),
            self.local_tier(),
            self.mutable_store().clone(),
            self.notification().clone(),
            self.hook_dispatcher(),
            self.history_step_size(),
            self.acceleration,
            &self.instrument_provider,
        );
        let (stop, mut stopped) = tokio::sync::oneshot::channel::<()>();
        let heartbeat_repo = repo.clone();
        let heartbeat = async {
            let mut seen = receipt.progress.snapshot();
            let mut changed = Instant::now();
            loop {
                tokio::select! {
                    _ = &mut stopped => return Ok(()),
                    () = tokio::time::sleep(settings.renew) => (),
                }
                let now = receipt.progress.snapshot();
                if now != seen {
                    seen = now;
                    changed = Instant::now();
                } else if changed.elapsed() >= settings.stall {
                    return Err(Stop::Stalled);
                }
                match receipt.renew(heartbeat_repo.clone()).await {
                    Ok(true) => (),
                    Ok(false) => return Err(Stop::Lost),
                    Err(error) => {
                        tracing::warn!(%error, "push lease renewal failed");
                        return Err(Stop::Lost);
                    }
                }
            }
        };
        tokio::pin!(heartbeat);
        let outcome = tokio::select! {
            result = work => {
                let _ = stop.send(());
                // Let an in-flight renewal publish its new fencing token before finish.
                let _ = heartbeat.await;
                result
            }
            stop = &mut heartbeat => {
                if let Err(Stop::Stalled) = stop {
                    let (phase, checked) = receipt.progress.snapshot();
                    tracing::warn!(
                        phase,
                        checked,
                        stall_seconds = settings.stall.as_secs(),
                        "Push verification made no progress; releasing it for a restart"
                    );
                    // Expire the lease now rather than in `lease`, so the next query restarts it.
                    let lease = receipt.next_lease(0);
                    if let Err(error) = receipt.release(&repo, Duration::ZERO, lease).await {
                        tracing::warn!(%error, "could not release a stalled push");
                    }
                }
                return;
            }
        };
        let state = match outcome {
            Ok(result) => State::Accepted(result.into_inner()),
            Err(status) if transient(&status) && receipt.base.retries < MAX_RETRIES => {
                let retries = receipt.base.retries + 1;
                let delay = retry_delay(retries);
                tracing::info!(%status, retries, ?delay, "Push hit a transient failure; retrying");
                let lease = Lease {
                    retries,
                    released: true,
                    ..receipt.next_lease(0)
                };
                if let Err(error) = receipt.release(&repo, delay, lease).await {
                    tracing::warn!(%error, "could not release a push for a retry");
                }
                return;
            }
            Err(status) => State::Rejected(Rejected {
                code: status.code() as i32,
                message: status.message().to_owned(),
                details: status.details().to_vec().into(),
            }),
        };
        if let Err(error) = receipt.finish(repo, state).await {
            tracing::warn!(%error, "could not finalize push receipt");
        }
    }

    /// Removes this repository's journal entries past their retention, at most once per
    /// [`SWEEP_INTERVAL`]. Runs in the background; a failure is retried at the next interval.
    fn sweep_journal(&self, repo: Arc<RepositoryContext>) {
        let workers = self.push_workers.clone();
        {
            let mut swept = workers.swept.lock();
            let now = Instant::now();
            if swept
                .get(&repo.id)
                .is_some_and(|last| now.duration_since(*last) < SWEEP_INTERVAL)
            {
                return;
            }
            swept.insert(repo.id, now);
        }
        lore_base::lore_spawn!(outliving_request(async move {
            match sweep(repo, workers.settings.retention).await {
                Ok(0) => (),
                Ok(removed) => tracing::info!(removed, "Removed expired push journal entries"),
                Err(error) => tracing::warn!(%error, "Could not sweep the push journal"),
            }
        }));
    }
}

async fn registered_push(
    repo: &Arc<RepositoryContext>,
    value: Hash,
) -> Result<revision_rpc::BranchPushRequest, Status> {
    read(repo.clone(), value)
        .await?
        .registration
        .and_then(|registration| registration.push)
        .ok_or_else(|| Status::internal("push registration holds no request"))
}

/// Removes the journal entries of `repo` that were decided, abandoned or registered more than
/// `retention` ago. Returns how many it removed.
#[lore_macro::test_pub]
async fn sweep(repo: Arc<RepositoryContext>, retention: Duration) -> Result<u64, Status> {
    let cutoff = now_ms()?.saturating_sub(ms(retention));
    let mut entries = store(&repo)?
        .list(repo.id, KeyType::PushJournal, ReadConsistency::Strong)
        .await
        .map_err(internal)?;
    let mut removed = 0;
    while let Some((key, value)) = entries.next().await {
        let stamp = match Lease::decode(&value) {
            Some(lease) => lease.until_ms,
            None => match read(repo.clone(), value).await {
                Ok(Entry {
                    registration: Some(registration),
                    ..
                }) => registration.created_ms,
                Ok(Entry {
                    outcome: Some(outcome),
                    ..
                }) => outcome.finished_ms.max(outcome.lease_until_ms),
                Ok(_) | Err(_) => continue,
            },
        };
        if stamp < cutoff && swap(&repo, key, value, Hash::default()).await? == value {
            removed += 1;
        }
    }
    Ok(removed)
}
