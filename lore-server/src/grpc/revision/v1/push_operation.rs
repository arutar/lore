// SPDX-License-Identifier: MIT
use std::sync::Arc;
use std::time::Duration;
use std::time::SystemTime;
use std::time::UNIX_EPOCH;

use lore_base::runtime::LORE_CONTEXT;
use lore_base::types::Address;
use lore_base::types::BranchId;
use lore_base::types::Context;
use lore_base::types::Hash;
use lore_base::types::KeyType;
use lore_proto::lore::revision::v1 as rpc;
use lore_revision::branch;
use lore_revision::immutable;
use lore_revision::repository::RepositoryContext;
use lore_storage::mutable_store::ConditionalWrite;
use prost::Message;
use rpc::branch_push_operation::Processing;
use rpc::branch_push_operation::Rejected;
use rpc::branch_push_operation::State;
use rpc::branch_push_operation_service_server::BranchPushOperationService;
use tokio::sync::Mutex;
use tokio::sync::Semaphore;
use tonic::Request;
use tonic::Response;
use tonic::Status;

use super::branch_push;
use super::service::LoreRevisionV1Service;
use crate::grpc::extract_correlation_id;
use crate::grpc::get_repository;
use crate::grpc::get_user_id;
use crate::grpc::handlers::branch_push::PushAccess;
use crate::util::setup_execution;

// The receipt pointer refers to immutable protobuf bytes. A lease owns the
// right to finish only while its pointer still matches. A replacement worker
// cannot be overwritten by a delayed or disconnected former owner.
#[derive(Clone, PartialEq, Message)]
struct Record {
    #[prost(message, optional, tag = "1")]
    result: Option<rpc::BranchPushOperation>,
    #[prost(uint64, tag = "2")]
    lease_until: u64,
    #[prost(bytes = "bytes", tag = "3")]
    generation: bytes::Bytes,
}

const RENEW: Duration = Duration::from_secs(10);
const LEASE_SECONDS: u64 = 60;

fn now() -> Result<u64, Status> {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|v| v.as_secs())
        .map_err(internal)
}

fn internal(error: impl std::fmt::Display) -> Status {
    Status::internal(error.to_string())
}

pub(super) struct Workers {
    permits: Arc<Semaphore>,
}

impl Default for Workers {
    fn default() -> Self {
        Self {
            permits: Arc::new(Semaphore::new(32)),
        }
    }
}

fn processing() -> rpc::BranchPushOperation {
    rpc::BranchPushOperation {
        state: Some(State::Processing(Processing {})),
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

async fn put<M: Message>(repo: Arc<RepositoryContext>, message: &M) -> Result<Hash, Status> {
    let address = immutable::write(
        repo.clone(),
        Context::default(),
        message.encode_to_vec().into(),
        Default::default(),
    )
    .await
    .map_err(internal)?;
    repo.immutable_store().flush(true).await.map_err(internal)?;
    Ok(address.hash)
}

async fn read<M: Default + Message>(repo: Arc<RepositoryContext>, hash: Hash) -> Result<M, Status> {
    let data = immutable::read(
        repo,
        Address::zero_context_hash(hash),
        None,
        Default::default(),
    )
    .await
    .map_err(internal)?;
    M::decode(data).map_err(internal)
}

async fn swap(repo: &RepositoryContext, key: Hash, old: Hash, new: Hash) -> Result<Hash, Status> {
    let storage = store(repo)?;
    let previous = storage
        .clone()
        .compare_and_swap(repo.id, key, old, new, KeyType::PushJournal)
        .await
        .map_err(internal)?;
    storage.flush(true).await.map_err(internal)?;
    Ok(previous)
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

fn receipt_key(registration: Hash, head: Hash) -> Hash {
    let mut key =
        Hash::hash_buffer(&[b"lore/push/receipt/v1".as_slice(), registration.data()].concat());
    key.data_mut()[..2].copy_from_slice(&head.data()[..2]);
    key
}

#[derive(Clone)]
pub(crate) struct Receipt {
    key: Hash,
    // The content hash of the current lease doubles as the fencing token.
    lease: Arc<Mutex<Hash>>,
    generation: bytes::Bytes,
    committed: Arc<std::sync::atomic::AtomicBool>,
}

impl Receipt {
    async fn renew(&self, repo: Arc<RepositoryContext>) -> Result<bool, Status> {
        let mut lease = self.lease.lock().await;
        if self.committed.load(std::sync::atomic::Ordering::Relaxed) {
            return Ok(true);
        }
        let record = Record {
            result: None,
            lease_until: now()? + LEASE_SECONDS,
            generation: self.generation.clone(),
        };
        let next = put(repo.clone(), &record).await?;
        if swap(&repo, self.key, *lease, next).await? != *lease {
            return Ok(false);
        }
        *lease = next;
        Ok(true)
    }

    async fn finish(&self, repo: Arc<RepositoryContext>, state: State) -> Result<(), Status> {
        if self.committed.load(std::sync::atomic::Ordering::Relaxed)
            && !matches!(&state, State::Accepted(_))
        {
            return Ok(());
        }
        let result = Record {
            result: Some(rpc::BranchPushOperation { state: Some(state) }),
            ..Default::default()
        };
        let value = put(repo.clone(), &result).await?;
        let lease = self.lease.lock().await;
        swap(&repo, self.key, *lease, value).await?;
        Ok(())
    }

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
        let state = State::Accepted(rpc::BranchPushResponse {
            revision_signature: revision.into(),
            revision_number,
            fast_forward_merged: merged,
            message: None,
        });
        // A live worker may still supply a response hook message. After a crash,
        // the committed result is authoritative once this short lease expires.
        let result = Record {
            result: Some(rpc::BranchPushOperation { state: Some(state) }),
            lease_until: now()? + LEASE_SECONDS,
            ..Default::default()
        };
        let value = put(repo.clone(), &result).await?;
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
                self.committed
                    .store(true, std::sync::atomic::Ordering::Relaxed);
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

#[tonic::async_trait]
impl BranchPushOperationService for LoreRevisionV1Service {
    async fn start(
        &self,
        request: Request<rpc::StartBranchPushRequest>,
    ) -> Result<Response<rpc::BranchPushOperation>, Status> {
        let (metadata, extensions, start) = request.into_parts();
        let push = start
            .push
            .ok_or_else(|| Status::invalid_argument("missing push"))?;
        let request = Request::from_parts(metadata, extensions, ());
        crate::grpc::timeout_grpc(
            self.rpc_timeout,
            self.push_status(request, start.operation_id, Some(push)),
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
                None,
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
        start: Option<rpc::BranchPushRequest>,
    ) -> Result<Response<rpc::BranchPushOperation>, Status> {
        let repo_id = get_repository(request.metadata())?;
        if repo_id == Default::default() {
            return Err(Status::invalid_argument("expected a nonzero repository id"));
        }
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
        start: Option<rpc::BranchPushRequest>,
        repo_id: lore_base::types::RepositoryId,
        registration: Hash,
    ) -> Result<Response<rpc::BranchPushOperation>, Status> {
        PushAccess::of(&self.repository_authorizer, request.extensions(), repo_id).await?;
        if !self.mutable_store().supports_atomic_pair() {
            return Err(Status::unimplemented(
                "backend does not support persistent push operations",
            ));
        }
        let repo = Arc::new(RepositoryContext::new_server_context(
            self.immutable_store().clone(),
            self.mutable_store().clone(),
            repo_id,
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
                if registered.is_zero() {
                    let value = put(repo.clone(), &push).await?;
                    let previous = swap(&repo, registration, Hash::default(), value).await?;
                    if !previous.is_zero() && previous != value {
                        return Err(Status::already_exists("operation id already used"));
                    }
                } else if read::<rpc::BranchPushRequest>(repo.clone(), registered).await? != push {
                    return Err(Status::already_exists("operation id already used"));
                }
                push
            }
            None if registered.is_zero() => {
                return Err(Status::not_found("unknown push operation"));
            }
            None => read(repo.clone(), registered).await?,
        };
        let (head, _) = branch::mutable_key(
            repo.salt(),
            branch::LATEST,
            repo_id,
            BranchId::from(&push.id),
        );
        let key = receipt_key(registration, head);
        let current = pointer(&repo, key, KeyType::PushJournal).await?;
        if !current.is_zero() {
            let record: Record = read(repo.clone(), current).await?;
            if record.lease_until > now()? {
                return Ok(Response::new(processing()));
            }
            if let Some(result) = record.result {
                store(&repo)?.flush(true).await.map_err(internal)?;
                return Ok(Response::new(result));
            }
        }
        let permit = self
            .push_workers
            .permits
            .clone()
            .try_acquire_owned()
            .map_err(|error| Status::resource_exhausted(format!("push workers busy: {error}")))?;
        let receipt = Receipt {
            key,
            generation: uuid::Uuid::now_v7().as_bytes().to_vec().into(),
            committed: Arc::default(),
            lease: Arc::new(Mutex::new(current)),
        };
        if receipt.renew(repo.clone()).await? {
            let service = self.clone();
            let (metadata, extensions, ()) = request.into_parts();
            let mut worker_request = Request::from_parts(metadata, extensions, push);
            worker_request.extensions_mut().insert(receipt.clone());
            lore_base::lore_spawn!(async move {
                let _permit = permit;
                Box::pin(service.run_push(worker_request, repo, receipt)).await;
            });
        }
        Ok(Response::new(processing()))
    }

    async fn run_push(
        &self,
        worker_request: Request<rpc::BranchPushRequest>,
        repo: Arc<RepositoryContext>,
        receipt: Receipt,
    ) {
        let work = branch_push::handler(
            worker_request,
            self.repository_authorizer.clone(),
            self.immutable_store().clone(),
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
            loop {
                tokio::select! {
                    _ = &mut stopped => return Ok(()),
                    () = tokio::time::sleep(RENEW) => (),
                }
                if !receipt.renew(heartbeat_repo.clone()).await? {
                    return Err(Status::aborted("push lease reclaimed"));
                }
            }
        };
        tokio::pin!(heartbeat);
        let outcome = tokio::select! {
            result = work => {
                let _ = stop.send(());
                // Let an in-flight renewal publish its new fencing token before finish.
                if let Err(error) = heartbeat.await { tracing::warn!(%error, "push lease renewal failed"); }
                result
            }
            lost = &mut heartbeat => {
                if let Err(error) = lost { tracing::warn!(%error, "push lease renewal failed"); }
                return;
            }
        };
        let state = match outcome {
            Ok(result) => State::Accepted(result.into_inner()),
            Err(status) => State::Rejected(Rejected {
                code: status.code() as i32,
                message: status.message().to_owned(),
                details: status.details().to_vec().into(),
            }),
        };
        if let Err(error) = receipt.finish(repo, state).await {
            tracing::warn!(%error,"could not finalize push receipt");
        }
    }
}
