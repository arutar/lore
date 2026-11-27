// SPDX-License-Identifier: MIT
//! Push operations (`lore.platform.v1.BranchPushOperationService`): restarts after a stall,
//! retries after a transient failure, fencing of a superseded worker, no limit on how many run at
//! once, the journal sweep, and a restart after the journal was lost.
use std::sync::Arc;
use std::sync::atomic::AtomicUsize;
use std::sync::atomic::Ordering;
use std::time::Duration;

use lore_base::runtime::LORE_CONTEXT;
use lore_base::types::Hash;
use lore_base::types::KeyType;
use lore_proto::lore::platform::v1 as rpc;
use lore_revision::branch;
use lore_revision::branch::DEFAULT_HISTORY_STEP_SIZE;
use lore_revision::lore::BranchId;
use lore_revision::lore::RepositoryId;
use lore_revision::repository::RepositoryContext;
use lore_server::auth::jwt::AuthorizationToken;
use lore_server::grpc::revision::v1::push_operation::Lease;
use lore_server::grpc::revision::v1::push_operation::MAX_STALLS;
use lore_server::grpc::revision::v1::push_operation::PushOperationSettings;
use lore_server::grpc::revision::v1::push_operation::sweep;
use lore_server::grpc::revision::v1::service::LoreRevisionV1Service;
use lore_server::hooks::Hook;
use lore_server::hooks::HookContext;
use lore_server::hooks::HookDispatcher;
use lore_server::hooks::HookError;
use lore_server::hooks::HookPoint;
use lore_server::hooks::StatusCode;
use rand::random;
use rpc::branch_push_operation::State;
use rpc::branch_push_operation_service_server::BranchPushOperationService;
use tokio_stream::StreamExt;
use tonic::Request;

use super::branch_push::build_revision;
use super::branch_push::create_root_branch;
use super::branch_push::make_request;
use crate::grpc::push_protected::allow_all;
use crate::notification::testing::MockNotificationSender;
use crate::store::test_support::test_store_create;

/// What [`Scripted`] does on its `n`th call, from 0.
#[derive(Clone)]
enum Step {
    Pass,
    /// Never returns, as a verification stuck on a store request would.
    Hang,
    Fail(StatusCode),
    /// Waits for a permit of the semaphore.
    Gate(Arc<tokio::sync::Semaphore>),
    /// Waits until every push has reached the barrier.
    Barrier(Arc<tokio::sync::Barrier>),
}

/// A push pre-hook that stands in for the verification's behavior, one step per call.
struct Scripted {
    calls: AtomicUsize,
    script: Box<dyn Fn(usize) -> Step + Send + Sync>,
}

#[async_trait::async_trait]
impl Hook for Scripted {
    fn name(&self) -> &'static str {
        "scripted"
    }
    fn hook_points(&self) -> &'static [HookPoint] {
        &[HookPoint::BranchPush]
    }
    async fn pre_handler(&self, _: &HookContext) -> Result<(), HookError> {
        match (self.script)(self.calls.fetch_add(1, Ordering::SeqCst)) {
            Step::Pass => Ok(()),
            Step::Hang => std::future::pending().await,
            Step::Fail(status) => Err(HookError::Rejected {
                hook_name: "scripted".into(),
                message: "scripted failure".into(),
                status,
            }),
            Step::Gate(gate) => {
                let _permit = gate.acquire().await.unwrap();
                Ok(())
            }
            Step::Barrier(barrier) => {
                barrier.wait().await;
                Ok(())
            }
        }
    }
}

/// Settings scaled down so a test sees a stall, a lease expiry and a restart within a second.
fn fast() -> PushOperationSettings {
    PushOperationSettings {
        stall: Duration::from_millis(300),
        lease: Duration::from_secs(2),
        renew: Duration::from_millis(50),
        ..Default::default()
    }
}

struct Fixture {
    service: LoreRevisionV1Service,
    repo: Arc<RepositoryContext>,
    repository: RepositoryId,
    execution: Arc<lore_revision::interface::ExecutionContext>,
}

async fn fixture(
    settings: PushOperationSettings,
    pushes: std::ops::RangeInclusive<usize>,
    script: impl Fn(usize) -> Step + Send + Sync + 'static,
) -> Fixture {
    let hooks = HookDispatcher::new(
        vec![(
            "scripted".into(),
            Box::new(Scripted {
                calls: AtomicUsize::new(0),
                script: Box::new(script),
            }),
        )],
        Duration::from_secs(60),
        Duration::from_secs(60),
    );
    let (immutable, mutable, execution) = test_store_create().await.unwrap();
    let repository = random::<RepositoryId>();
    let mut notifications = MockNotificationSender::new();
    notifications
        .expect_branch_pushed()
        .times(pushes)
        .returning(|_, _, _, _, _| ());
    let service = LoreRevisionV1Service::new(
        immutable.clone(),
        None,
        mutable.clone(),
        Arc::new(notifications),
        Arc::new(hooks),
        allow_all(),
        DEFAULT_HISTORY_STEP_SIZE,
        Default::default(),
        None,
        Duration::from_secs(10),
        std::num::NonZeroUsize::MIN,
        super::branch_list::unshared_reads(),
    )
    .with_push_operations(settings);
    let repo = Arc::new(RepositoryContext::new_server_context(
        immutable,
        mutable,
        repository,
        lore_revision::state::no_link_traversal(),
    ));
    Fixture {
        service,
        repo,
        repository,
        execution,
    }
}

fn operation_id() -> bytes::Bytes {
    uuid::Uuid::now_v7().as_bytes().to_vec().into()
}

fn start(
    repository: RepositoryId,
    branch: BranchId,
    revision: Hash,
    id: &bytes::Bytes,
) -> Request<rpc::StartBranchPushRequest> {
    make_request(repository, branch, revision, false, false).map(|push| {
        rpc::StartBranchPushRequest {
            operation_id: id.clone(),
            push: Some(push),
        }
    })
}

fn get(
    repository: RepositoryId,
    branch: BranchId,
    id: &bytes::Bytes,
) -> Request<rpc::GetBranchPushOperationRequest> {
    make_request(repository, branch, Hash::default(), false, false).map(|_| {
        rpc::GetBranchPushOperationRequest {
            operation_id: id.clone(),
        }
    })
}

/// Polls the operation as a client does until it is decided. Returns the decision and the most
/// restarts any `Processing` reported.
async fn decided(
    fixture: &Fixture,
    branch: BranchId,
    id: &bytes::Bytes,
    within: Duration,
) -> (State, u32) {
    let mut restarts = 0;
    tokio::time::timeout(within, async {
        loop {
            let reply = fixture
                .service
                .get(get(fixture.repository, branch, id))
                .await
                .unwrap()
                .into_inner();
            match reply.state {
                Some(State::Processing(processing)) => {
                    restarts = restarts.max(processing.restarts);
                    tokio::time::sleep(Duration::from_millis(20)).await;
                }
                Some(state) => return (state, restarts),
                None => panic!("an operation without a state"),
            }
        }
    })
    .await
    .expect("the operation was decided in time")
}

async fn latest(repo: &Arc<RepositoryContext>, branch: BranchId) -> Hash {
    branch::load_latest(repo.clone(), branch)
        .await
        .unwrap_or_default()
}

#[test]
fn lease_round_trips_through_its_inline_value() {
    let lease = Lease {
        until_ms: 1_800_000_000_123,
        generation: 0x0123_4567_89ab_cdef,
        phase: 2,
        stalls: 1,
        retries: 3,
        released: true,
        checked: 4_000_000_000,
    };
    assert_eq!(Lease::decode(&lease.encode()), Some(lease));
    // An outcome pointer is a content hash, never mistaken for a lease.
    assert_eq!(Lease::decode(&Hash::hash_buffer(b"outcome")), None);
}

#[tokio::test]
async fn stalled_push_is_restarted_and_completes() {
    let fixture = fixture(
        fast(),
        1..=1,
        |n| if n == 0 { Step::Hang } else { Step::Pass },
    )
    .await;
    let run = async {
        let branch = create_root_branch(&fixture.repo, "main").await;
        let revision = build_revision(&fixture.repo, Hash::default(), 1).await;
        let id = operation_id();
        fixture
            .service
            .start(start(fixture.repository, branch, revision, &id))
            .await
            .unwrap();
        let (state, restarts) = decided(&fixture, branch, &id, Duration::from_secs(10)).await;
        assert!(
            matches!(&state, State::Accepted(result) if Hash::from(result.revision_signature.clone()) == revision),
            "{state:?}"
        );
        assert!(restarts >= 1, "the restart is reported to the client");
        assert_eq!(latest(&fixture.repo, branch).await, revision);
    };
    Box::pin(LORE_CONTEXT.scope(fixture.execution.clone(), run)).await;
}

#[tokio::test]
async fn push_that_keeps_stalling_is_rejected() {
    let fixture = fixture(fast(), 0..=0, |_| Step::Hang).await;
    let run = async {
        let branch = create_root_branch(&fixture.repo, "main").await;
        let revision = build_revision(&fixture.repo, Hash::default(), 1).await;
        let id = operation_id();
        fixture
            .service
            .start(start(fixture.repository, branch, revision, &id))
            .await
            .unwrap();
        let (state, restarts) = decided(&fixture, branch, &id, Duration::from_secs(20)).await;
        let State::Rejected(rejected) = state else {
            panic!("a push that never progresses must be rejected: {state:?}");
        };
        assert_eq!(rejected.code, tonic::Code::DeadlineExceeded as i32);
        assert!(
            rejected.message.contains("saved locally"),
            "{}",
            rejected.message
        );
        assert_eq!(restarts, u32::from(MAX_STALLS));
        assert!(latest(&fixture.repo, branch).await.is_zero());
    };
    Box::pin(LORE_CONTEXT.scope(fixture.execution.clone(), run)).await;
}

#[tokio::test]
async fn transient_failure_is_retried_rather_than_returned() {
    let fixture = fixture(fast(), 1..=1, |n| {
        if n == 0 {
            Step::Fail(StatusCode::ResourceExhausted)
        } else {
            Step::Pass
        }
    })
    .await;
    let run = async {
        let branch = create_root_branch(&fixture.repo, "main").await;
        let revision = build_revision(&fixture.repo, Hash::default(), 1).await;
        let id = operation_id();
        fixture
            .service
            .start(start(fixture.repository, branch, revision, &id))
            .await
            .unwrap();
        let (state, restarts) = decided(&fixture, branch, &id, Duration::from_secs(10)).await;
        assert!(matches!(state, State::Accepted(_)), "{state:?}");
        assert_eq!(restarts, 1);
        assert_eq!(latest(&fixture.repo, branch).await, revision);
    };
    Box::pin(LORE_CONTEXT.scope(fixture.execution.clone(), run)).await;
}

#[tokio::test]
async fn refusal_is_returned_without_a_retry() {
    let fixture = fixture(fast(), 0..=0, |_| Step::Fail(StatusCode::PermissionDenied)).await;
    let run = async {
        let branch = create_root_branch(&fixture.repo, "main").await;
        let revision = build_revision(&fixture.repo, Hash::default(), 1).await;
        let id = operation_id();
        fixture
            .service
            .start(start(fixture.repository, branch, revision, &id))
            .await
            .unwrap();
        let (state, restarts) = decided(&fixture, branch, &id, Duration::from_secs(5)).await;
        assert!(
            matches!(&state, State::Rejected(rejected) if rejected.code == tonic::Code::PermissionDenied as i32),
            "{state:?}"
        );
        assert_eq!(restarts, 0);
    };
    Box::pin(LORE_CONTEXT.scope(fixture.execution.clone(), run)).await;
}

/// A worker whose lease expired while it was blocked is taken over. The new worker decides the
/// push; the old one, released afterwards, cannot overwrite that decision.
#[tokio::test]
async fn superseded_worker_cannot_overwrite_the_outcome() {
    let gate = Arc::new(tokio::sync::Semaphore::new(0));
    let first = gate.clone();
    let settings = PushOperationSettings {
        lease: Duration::from_millis(300),
        // Neither renews nor sees a stall while the test runs.
        renew: Duration::from_secs(60),
        stall: Duration::from_secs(600),
        ..Default::default()
    };
    let fixture = fixture(settings, 1..=2, move |n| {
        if n == 0 {
            Step::Gate(first.clone())
        } else {
            Step::Pass
        }
    })
    .await;
    let run = async {
        let branch = create_root_branch(&fixture.repo, "main").await;
        let revision = build_revision(&fixture.repo, Hash::default(), 1).await;
        let id = operation_id();
        fixture
            .service
            .start(start(fixture.repository, branch, revision, &id))
            .await
            .unwrap();
        tokio::time::sleep(Duration::from_millis(400)).await;
        let (decision, _) = decided(&fixture, branch, &id, Duration::from_secs(5)).await;
        assert!(matches!(decision, State::Accepted(_)), "{decision:?}");

        gate.add_permits(1);
        tokio::time::sleep(Duration::from_millis(500)).await;
        let (after, _) = decided(&fixture, branch, &id, Duration::from_secs(5)).await;
        assert_eq!(after, decision, "the superseded worker changed the outcome");
        assert_eq!(latest(&fixture.repo, branch).await, revision);
    };
    Box::pin(LORE_CONTEXT.scope(fixture.execution.clone(), run)).await;
}

/// Every push starts its verification at once: all of them wait at one barrier, which only
/// opens when the last arrives. A cap on concurrent pushes would leave them waiting.
#[tokio::test]
async fn pushes_are_not_limited_in_number() {
    const PUSHES: usize = 48;
    let barrier = Arc::new(tokio::sync::Barrier::new(PUSHES));
    let shared = barrier.clone();
    let fixture = fixture(
        PushOperationSettings::default(),
        PUSHES..=PUSHES,
        move |_| Step::Barrier(shared.clone()),
    )
    .await;
    let run = async {
        let mut pushes = Vec::new();
        for n in 0..PUSHES {
            let branch = create_root_branch(&fixture.repo, &format!("branch-{n}")).await;
            let revision = build_revision(&fixture.repo, Hash::default(), 1).await;
            let id = operation_id();
            fixture
                .service
                .start(start(fixture.repository, branch, revision, &id))
                .await
                .unwrap();
            pushes.push((branch, revision, id));
        }
        for (branch, revision, id) in &pushes {
            let (state, _) = decided(&fixture, *branch, id, Duration::from_secs(20)).await;
            assert!(matches!(state, State::Accepted(_)), "{state:?}");
            assert_eq!(latest(&fixture.repo, *branch).await, *revision);
        }
    };
    Box::pin(LORE_CONTEXT.scope(fixture.execution.clone(), run)).await;
}

#[tokio::test]
async fn sweep_removes_operations_past_their_retention() {
    let fixture = fixture(fast(), 1..=1, |_| Step::Pass).await;
    let run = async {
        let branch = create_root_branch(&fixture.repo, "main").await;
        let revision = build_revision(&fixture.repo, Hash::default(), 1).await;
        let id = operation_id();
        fixture
            .service
            .start(start(fixture.repository, branch, revision, &id))
            .await
            .unwrap();
        decided(&fixture, branch, &id, Duration::from_secs(5)).await;

        assert_eq!(
            sweep(fixture.repo.clone(), Duration::from_secs(3600))
                .await
                .unwrap(),
            0,
            "a recent operation is kept"
        );
        tokio::time::sleep(Duration::from_millis(5)).await;
        assert_eq!(
            sweep(fixture.repo.clone(), Duration::ZERO).await.unwrap(),
            2,
            "the registration and the receipt"
        );
        let error = fixture
            .service
            .get(get(fixture.repository, branch, &id))
            .await
            .unwrap_err();
        assert_eq!(error.code(), tonic::Code::NotFound);
    };
    Box::pin(LORE_CONTEXT.scope(fixture.execution.clone(), run)).await;
}

/// A replayed Start returns the recorded outcome even after another push moved the branch, the
/// worker outlives the Start request, and the operation is private to the user who started it.
#[tokio::test]
async fn replayed_push_returns_its_receipt_after_a_subsequent_push() {
    let gate = Arc::new(tokio::sync::Semaphore::new(0));
    let first = gate.clone();
    let fixture = fixture(PushOperationSettings::default(), 2..=2, move |n| {
        if n == 0 {
            Step::Gate(first.clone())
        } else {
            Step::Pass
        }
    })
    .await;
    let run = async {
        let branch = create_root_branch(&fixture.repo, "main").await;
        let first = build_revision(&fixture.repo, Hash::default(), 1).await;
        let second = build_revision(&fixture.repo, first, 2).await;
        let id = operation_id();
        let reply = fixture
            .service
            .start(start(fixture.repository, branch, first, &id))
            .await
            .unwrap()
            .into_inner();
        assert!(matches!(reply.state, Some(State::Processing(_))));
        gate.add_permits(1);
        let (state, _) = decided(&fixture, branch, &id, Duration::from_secs(5)).await;
        assert!(matches!(state, State::Accepted(_)), "{state:?}");

        let other = operation_id();
        fixture
            .service
            .start(start(fixture.repository, branch, second, &other))
            .await
            .unwrap();
        decided(&fixture, branch, &other, Duration::from_secs(5)).await;

        let mut other_user = get(fixture.repository, branch, &id);
        other_user.extensions_mut().insert(AuthorizationToken {
            user_id: "another-user".into(),
            ..Default::default()
        });
        assert_eq!(
            fixture.service.get(other_user).await.unwrap_err().code(),
            tonic::Code::NotFound
        );
        let replay = fixture
            .service
            .start(start(fixture.repository, branch, first, &id))
            .await
            .unwrap()
            .into_inner()
            .state;
        assert!(
            matches!(&replay, Some(State::Accepted(result)) if Hash::from(result.revision_signature.clone()) == first),
            "{replay:?}"
        );
        let error = fixture
            .service
            .start(start(fixture.repository, branch, second, &id))
            .await
            .unwrap_err();
        assert_eq!(error.code(), tonic::Code::AlreadyExists);
    };
    Box::pin(LORE_CONTEXT.scope(fixture.execution.clone(), run)).await;
}

/// Journal writes don't flush the store, so a server crash can lose an operation. The client then
/// starts it again under the same id: the new worker moves the branch, and the worker that lost
/// its journal entry can't.
#[tokio::test]
async fn push_whose_journal_was_lost_starts_again_under_the_same_id() {
    let gate = Arc::new(tokio::sync::Semaphore::new(0));
    let first = gate.clone();
    let fixture = fixture(PushOperationSettings::default(), 1..=1, move |n| {
        if n == 0 {
            Step::Gate(first.clone())
        } else {
            Step::Pass
        }
    })
    .await;
    let run = async {
        let branch = create_root_branch(&fixture.repo, "main").await;
        let revision = build_revision(&fixture.repo, Hash::default(), 1).await;
        let id = operation_id();
        fixture
            .service
            .start(start(fixture.repository, branch, revision, &id))
            .await
            .unwrap();

        let store = fixture.repo.try_mutable_store_arc().unwrap();
        let mut entries = store
            .clone()
            .list(
                fixture.repository,
                KeyType::PushJournal,
                lore_storage::ReadConsistency::Strong,
            )
            .await
            .unwrap();
        while let Some((key, value)) = entries.next().await {
            store
                .clone()
                .compare_and_swap(
                    fixture.repository,
                    key,
                    value,
                    Hash::default(),
                    KeyType::PushJournal,
                )
                .await
                .unwrap();
        }
        assert_eq!(
            fixture
                .service
                .get(get(fixture.repository, branch, &id))
                .await
                .unwrap_err()
                .code(),
            tonic::Code::NotFound
        );

        fixture
            .service
            .start(start(fixture.repository, branch, revision, &id))
            .await
            .unwrap();
        let (state, _) = decided(&fixture, branch, &id, Duration::from_secs(5)).await;
        assert!(matches!(state, State::Accepted(_)), "{state:?}");
        assert_eq!(latest(&fixture.repo, branch).await, revision);

        gate.add_permits(1);
        tokio::time::sleep(Duration::from_millis(200)).await;
        let (state, _) = decided(&fixture, branch, &id, Duration::from_secs(5)).await;
        assert!(
            matches!(state, State::Accepted(_)),
            "the lost worker overwrote the outcome: {state:?}"
        );
    };
    Box::pin(LORE_CONTEXT.scope(fixture.execution.clone(), run)).await;
}
