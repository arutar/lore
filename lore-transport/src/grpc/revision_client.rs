// SPDX-FileCopyrightText: 2026 Epic Games, Inc.
// SPDX-License-Identifier: MIT
use std::sync::Arc;
use std::sync::atomic::AtomicU64;

use lore_base::lore_debug;
use lore_base::types::BranchId;
use lore_base::types::BranchMetadata;
use lore_base::types::BranchPoint;
use lore_base::types::Context;
use lore_base::types::Hash;
use lore_base::types::RepositoryId;
use lore_proto::lore::model::v1 as model_v1;
use lore_proto::lore::platform::v1 as platform_v1;
use lore_proto::lore::platform::v1::branch_push_operation_service_client::BranchPushOperationServiceClient;
use lore_proto::lore::revision::v1 as revision_v1;
use lore_proto::lore::revision::v1::revision_service_client::RevisionServiceClient;
use tokio_stream::StreamExt;

use super::AuthorizedService;
use super::AuthzInterceptor;
use super::Channel;
use super::GRPCAuthRef;
use super::REVISION_LIST_STRATEGY_HEADER;
use super::RequestScopedCounter;
use super::grpc_retry;
use super::handle_error;
use crate::error::ProtocolError;
use crate::types::BranchListResponse;
use crate::types::BranchPushResponse;
use crate::types::BranchQueryResponse;
use crate::types::MetadataSetResult;
use crate::types::PushVerifyProgress;
use crate::types::RevisionItem;
use crate::types::RevisionListResponse;
use crate::types::RevisionListStart;

fn retryable_push_operation(status: &tonic::Status) -> bool {
    matches!(
        status.code(),
        tonic::Code::ResourceExhausted | tonic::Code::DeadlineExceeded
    ) || (status.code() == tonic::Code::Cancelled
        && status.message() == "Request handler timeout exceeded")
}

// An expired status query says nothing about the branch head. Preserve the
// legacy handler's timeout wording only on the synchronous fallback path.
fn push_query_error(id: Context, status: tonic::Status) -> ProtocolError {
    if status.code() == tonic::Code::Cancelled && retryable_push_operation(&status) {
        tonic::Status::deadline_exceeded(format!("Timed out checking push operation {id}")).into()
    } else {
        status.into()
    }
}

#[derive(Clone)]
pub struct RevisionService {
    client: RevisionServiceClient<AuthorizedService>,
    operations: BranchPushOperationServiceClient<AuthorizedService>,
    repository: RepositoryId,
    pub request_inflight: Arc<AtomicU64>,
}

impl RevisionService {
    pub fn new(channel: Channel, repository: RepositoryId, auth: GRPCAuthRef) -> Self {
        let interceptor = AuthzInterceptor { repository, auth };
        let client = RevisionServiceClient::with_interceptor(channel.clone(), interceptor.clone());
        let operations = BranchPushOperationServiceClient::with_interceptor(channel, interceptor);

        Self {
            client,
            operations,
            repository,
            request_inflight: Arc::new(AtomicU64::new(0)),
        }
    }

    pub async fn branch_create(
        &self,
        branch: BranchId,
        name: &str,
        category: &str,
        creator: &str,
        stack: &[BranchPoint],
    ) -> Result<Hash, ProtocolError> {
        lore_debug!("Creating remote branch {name} ({branch}) with stack {stack:?}");
        let _ = RequestScopedCounter::new(self.request_inflight.clone());

        let mut retry = grpc_retry();
        let response = loop {
            let request = revision_v1::BranchCreateRequest {
                id: branch.into(),
                name: name.to_string(),
                creator: Some(creator.to_string()),
                category: category.to_string(),
                stack: stack.iter().map(model_v1::BranchPoint::from).collect(),
            };

            let mut client = self.client.clone();

            match client.branch_create(request).await {
                Ok(response) => {
                    break response.into_inner();
                }
                Err(status) => {
                    handle_error(&mut retry, status).await?;
                }
            }
        };

        let branch_record = response
            .branch
            .ok_or_else(|| ProtocolError::internal("BranchCreate response missing branch"))?;
        Ok(Hash::from(branch_record.latest))
    }

    pub async fn branch_delete(&self, branch: BranchId) -> Result<(), ProtocolError> {
        lore_debug!("Deleting remote branch {}", branch);
        let _ = RequestScopedCounter::new(self.request_inflight.clone());

        let mut retry = grpc_retry();
        let _response = loop {
            let request = revision_v1::BranchDeleteRequest { id: branch.into() };

            let mut client = self.client.clone();

            match client.branch_delete(request).await {
                Ok(response) => {
                    break response.into_inner();
                }
                Err(status) => {
                    handle_error(&mut retry, status).await?;
                }
            }
        };

        Ok(())
    }

    pub async fn branch_list(&self) -> Result<BranchListResponse, ProtocolError> {
        self.branch_list_with_limit(None)
            .await
            .and_then(|response| {
                response.ok_or_else(|| {
                    ProtocolError::internal("unbounded branch listing exceeded a limit")
                })
            })
    }

    pub async fn branch_list_bounded(
        &self,
        limit: usize,
    ) -> Result<Option<BranchListResponse>, ProtocolError> {
        self.branch_list_with_limit(Some(limit)).await
    }

    async fn branch_list_with_limit(
        &self,
        limit: Option<usize>,
    ) -> Result<Option<BranchListResponse>, ProtocolError> {
        lore_debug!("List branches");
        let _ = RequestScopedCounter::new(self.request_inflight.clone());

        let mut retry = grpc_retry();
        let list = loop {
            let request = revision_v1::BranchListRequest {
                creator: None,
                include_deleted: false,
            };

            let mut client = self.client.clone();

            match client.branch_list(request).await {
                Ok(response) => match collect_branch_list(response.into_inner(), limit).await {
                    Ok(response) => break response,
                    Err(status) => {
                        handle_error(&mut retry, status).await?;
                    }
                },
                Err(status) => {
                    handle_error(&mut retry, status).await?;
                }
            }
        };

        Ok(list)
    }

    pub async fn branch_query(
        &self,
        branch: Option<BranchId>,
        name: Option<&str>,
    ) -> Result<BranchQueryResponse, ProtocolError> {
        lore_debug!(
            "Query branch: id[{:?}] / name[{:?}] in repository {}",
            branch,
            name,
            self.repository
        );
        let _ = RequestScopedCounter::new(self.request_inflight.clone());

        let mut retry = grpc_retry();
        let branch_record = loop {
            let query = if let Some(branch) = branch {
                revision_v1::branch_get_request::Query::Id(branch.into())
            } else {
                revision_v1::branch_get_request::Query::Name(name.unwrap_or_default().to_string())
            };
            let request = revision_v1::BranchGetRequest { query: Some(query) };

            let mut client = self.client.clone();

            match client.branch_get(request).await {
                Ok(response) => match response.into_inner().branch {
                    Some(branch) => break branch,
                    None => {
                        return Err(ProtocolError::internal("BranchGet response missing branch"));
                    }
                },
                Err(status) => {
                    handle_error(&mut retry, status).await?;
                }
            }
        };

        let mut id = Context::from(branch_record.id);
        if id.is_zero() && branch.is_some() {
            id = branch.unwrap_or_default();
        }
        let latest = branch_record.latest.into();
        let metadata = branch_record.metadata.into();
        let deleted = branch_record.deleted;
        lore_debug!(
            "Query branch: id[{:?}] / name[{:?}] in repository {} complete: ID {} latest {} metadata {}",
            branch,
            name,
            self.repository,
            id,
            latest,
            metadata,
        );

        Ok(BranchQueryResponse {
            id,
            latest,
            metadata,
            deleted,
        })
    }

    #[allow(clippy::too_many_arguments)]
    pub async fn branch_push(
        &self,
        operation_id: Context,
        allow_legacy: &std::sync::atomic::AtomicBool,
        branch: BranchId,
        revision: Hash,
        force: bool,
        fast_forward_merge: bool,
        progress: &(dyn Fn(PushVerifyProgress) + Send + Sync),
    ) -> Result<BranchPushResponse, ProtocolError> {
        lore_debug!("Pushing branch: {} at {}", branch, revision);
        let _ = RequestScopedCounter::new(self.request_inflight.clone());

        let request = revision_v1::BranchPushRequest {
            id: branch.into(),
            revision_signature: revision.into(),
            force,
            fast_forward_merge,
        };
        let mut operations = self.operations.clone();
        let start = platform_v1::StartBranchPushRequest {
            operation_id: operation_id.into(),
            push: Some(request.clone()),
        };
        let mut retry = grpc_retry();
        let started = loop {
            match operations.start(start.clone()).await {
                Err(status) if retryable_push_operation(&status) => {
                    allow_legacy.store(false, std::sync::atomic::Ordering::Relaxed);
                    if !retry.wait().await {
                        return Err(push_query_error(operation_id, status));
                    }
                }
                result => break result,
            }
        };
        let response = match started {
            Ok(operation) => {
                allow_legacy.store(false, std::sync::atomic::Ordering::Relaxed);
                let mut operation = operation.into_inner();
                let mut interval = std::time::Duration::from_millis(40);
                let mut reported = None;
                loop {
                    use platform_v1::branch_push_operation::State;
                    match operation.state {
                        Some(State::Accepted(result)) => break result,
                        Some(State::Rejected(error)) => {
                            let status = tonic::Status::with_details(
                                tonic::Code::from_i32(error.code),
                                error.message,
                                error.details,
                            );
                            return Err(status.into());
                        }
                        Some(State::Processing(state)) => {
                            let now = PushVerifyProgress {
                                phase: u32::try_from(state.phase).unwrap_or_default(),
                                checked: state.checked,
                                restarts: state.restarts,
                            };
                            // The server bounds the operation: it restarts a stalled verification and
                            // rejects one that keeps stalling, so the client waits for its answer.
                            if reported != Some(now) {
                                reported = Some(now);
                                progress(now);
                            }
                            tokio::time::sleep(interval).await;
                            interval = (interval * 2).min(std::time::Duration::from_secs(1));
                            operation = match operations
                                .get(platform_v1::GetBranchPushOperationRequest {
                                    operation_id: operation_id.into(),
                                })
                                .await
                            {
                                Ok(reply) => {
                                    // Each failure run gets the whole retry budget: a long push
                                    // may meet several short outages.
                                    retry = grpc_retry();
                                    reply.into_inner()
                                }
                                Err(status)
                                    if retryable_push_operation(&status) && retry.wait().await =>
                                {
                                    continue;
                                }
                                Err(status) => return Err(push_query_error(operation_id, status)),
                            };
                        }
                        None => {
                            return Err(ProtocolError::internal(
                                "Missing branch push operation state",
                            ));
                        }
                    }
                }
            }
            Err(status)
                if status.code() == tonic::Code::Unimplemented
                    && allow_legacy.load(std::sync::atomic::Ordering::Relaxed) =>
            {
                let mut retry = grpc_retry();
                loop {
                    match self.client.clone().branch_push(request.clone()).await {
                        Ok(response) => break response.into_inner(),
                        Err(status) => handle_error(&mut retry, status).await?,
                    }
                }
            }
            Err(status) => {
                // A lost Start reply may already have registered the operation.
                allow_legacy.store(false, std::sync::atomic::Ordering::Relaxed);
                return Err(status.into());
            }
        };

        Ok(BranchPushResponse {
            fast_forward_merged: response.fast_forward_merged,
            revision: response.revision_signature.into(),
            revision_number: response.revision_number,
            message: response.message,
        })
    }

    pub async fn revision_list(
        &self,
        signature: impl Into<RevisionListStart>,
    ) -> Result<RevisionListResponse, ProtocolError> {
        let _counter = RequestScopedCounter::new(self.request_inflight.clone());

        let signature = signature.into();

        let mut retry = grpc_retry();
        let response = loop {
            let start = match signature.clone() {
                RevisionListStart::Identifier(ident) => {
                    revision_v1::revision_list_request::Start::Identifier(
                        model_v1::RevisionIdentifier {
                            branch_id: ident.branch.into(),
                            number: ident.number,
                        },
                    )
                }
                RevisionListStart::Signature(sig) => {
                    revision_v1::revision_list_request::Start::Signature(sig.into())
                }
            };
            let request = revision_v1::RevisionListRequest { start: Some(start) };

            let mut client = self.client.clone();

            match client.revision_list(request).await {
                Ok(response) => {
                    if let Some(strategy) = response.metadata().get(REVISION_LIST_STRATEGY_HEADER) {
                        lore_debug!(
                            "Revision list strategy: {}",
                            strategy.to_str().unwrap_or("unknown")
                        );
                    }
                    break response.into_inner();
                }
                Err(status) => handle_error(&mut retry, status).await?,
            }
        };

        let revision_v1::RevisionListResponse {
            items,
            signature_forward,
            signature_backward,
        } = response;
        Ok(RevisionListResponse {
            items: items
                .into_iter()
                .map(|item| RevisionItem {
                    number: item.number,
                    signature: item.signature.into(),
                    metadata: item.metadata.into(),
                    state: item.state,
                })
                .collect(),
            next_revision: signature_backward.map(Into::into).unwrap_or_default(),
            previous_revision: signature_forward.map(Into::into).unwrap_or_default(),
        })
    }

    pub async fn branch_metadata_get(&self, branch: BranchId) -> Result<Hash, ProtocolError> {
        lore_debug!("Getting branch metadata for {}", branch);
        let _ = RequestScopedCounter::new(self.request_inflight.clone());

        let mut retry = grpc_retry();
        let response = loop {
            let request = revision_v1::BranchMetadataGetRequest { id: branch.into() };

            let mut client = self.client.clone();

            match client.branch_metadata_get(request).await {
                Ok(response) => {
                    break response.into_inner();
                }
                Err(status) => {
                    handle_error(&mut retry, status).await?;
                }
            }
        };

        Ok(response.metadata.into())
    }

    pub async fn branch_metadata_set(
        &self,
        branch: BranchId,
        expected: Hash,
        new: Hash,
    ) -> Result<MetadataSetResult, ProtocolError> {
        lore_debug!("Setting branch metadata for {}", branch);
        let _ = RequestScopedCounter::new(self.request_inflight.clone());

        let mut retry = grpc_retry();
        let response = loop {
            let request = revision_v1::BranchMetadataSetRequest {
                id: branch.into(),
                expected: expected.into(),
                updated: new.into(),
            };

            let mut client = self.client.clone();

            match client.branch_metadata_set(request).await {
                Ok(response) => {
                    break response.into_inner();
                }
                Err(status) => {
                    handle_error(&mut retry, status).await?;
                }
            }
        };

        // v1 signals CAS miss in-band: response.metadata == request.updated on hit, otherwise it is the unchanged current pointer
        let current_hash: Hash = response.metadata.into();
        let success = current_hash == new;

        Ok(MetadataSetResult {
            success,
            current_hash,
        })
    }
}

fn branch_metadata_from_v1(branch: model_v1::Branch) -> BranchMetadata {
    BranchMetadata {
        id: BranchId::from(branch.id),
        name: branch.name,
        category: branch.category,
        latest: branch.latest.into(),
        creator: branch.creator,
        created: branch.created,
        stack: branch.stack.into_iter().map(BranchPoint::from).collect(),
    }
}

#[lore_macro::test_pub]
async fn collect_branch_list(
    mut stream: impl tokio_stream::Stream<Item = Result<revision_v1::BranchListResponse, tonic::Status>>
    + Unpin,
    limit: Option<usize>,
) -> Result<Option<BranchListResponse>, tonic::Status> {
    let mut entries = Vec::new();
    while let Some(item) = stream.next().await {
        if let Some(branch) = item?.branch {
            if limit.is_some_and(|limit| entries.len() == limit) {
                return Ok(None);
            }
            entries.push(branch_metadata_from_v1(branch));
        }
    }
    Ok(Some(BranchListResponse { list: entries }))
}
