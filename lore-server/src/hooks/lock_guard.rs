//! Refuses a push that changes a file someone else has locked.
//!
//! Lore Platform addition. Upstream Lore treats locks as advisory: neither the client nor the
//! server looks at them when a revision is committed or pushed, so a lock only helps people who
//! check for it. Teams working on binary files that can't be merged need the lock to hold, so
//! this hook checks each push before the branch moves.
//!
//! ```toml
//! [hooks.lock_guard]
//! enabled = true
//! # optional
//! timeout_seconds = 30
//! history_limit = 512
//! ```
//!
//! The files a push changes are those that differ between the incoming revision and the point
//! where it left the branch: the branch head when the head is among its ancestors (an ordinary
//! push, a push after a sync merge), otherwise the nearest ancestor the two share (a push the
//! server fast-forward merges). A file changed only by others on the branch is not counted, so a
//! push isn't refused over someone else's locked work it merely doesn't have yet. Locks on other
//! branches don't count, nor does the pusher's own lock.
//!
//! The check reads nothing when nobody else holds a lock on the branch, which is the usual case.
//! If the revisions can't be read the hook lets the push through: the push itself then fails
//! or succeeds on its own checks, and a lock check never turns a store hiccup into a refusal.

use std::collections::HashMap;
use std::collections::HashSet;
use std::collections::VecDeque;
use std::sync::Arc;
use std::time::Duration;

use async_trait::async_trait;
use lore_base::types::Hash;
use lore_base::types::LockData;
use lore_revision::branch;
use lore_revision::diff;
use lore_revision::lock::LockQuery;
use lore_revision::lock::LockStore;
use lore_revision::repository::RepositoryContext;
use lore_revision::state::State;
use lore_revision::util::collect_stream::collect_stream_with_summary;
use serde::Deserialize;
use tracing::debug;
use tracing::warn;

use crate::hooks::Hook;
use crate::hooks::HookContext;
use crate::hooks::HookError;
use crate::hooks::HookFactory;
use crate::hooks::HookPoint;
use crate::hooks::HookRegistrationContext;
use crate::hooks::HookRegistry;
use crate::hooks::StatusCode;

const HOOK_NAME: &str = "lock_guard";
/// How many files a refusal names before it says "and N more".
const NAMED_FILES: usize = 10;

#[derive(Clone, Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct LockGuardConfig {
    /// How long the check may take. Diffing a very large revision takes a while.
    #[serde(default = "default_timeout_seconds")]
    timeout_seconds: u64,
    /// How many revisions to walk back looking for where the push left the branch. A push
    /// further behind than this is checked against the branch head instead.
    #[serde(default = "default_history_limit")]
    history_limit: usize,
}

fn default_timeout_seconds() -> u64 {
    30
}

fn default_history_limit() -> usize {
    512
}

struct Stores {
    immutable: Arc<dyn lore_storage::ImmutableStore>,
    mutable: Arc<dyn lore_storage::MutableStore>,
    locks: Arc<dyn LockStore>,
}

pub struct LockGuardHook {
    stores: Option<Arc<Stores>>,
    config: LockGuardConfig,
}

#[async_trait]
impl Hook for LockGuardHook {
    fn name(&self) -> &'static str {
        HOOK_NAME
    }

    fn hook_points(&self) -> &'static [HookPoint] {
        &[HookPoint::BranchPush]
    }

    fn pre_handler_timeout(&self) -> Option<Duration> {
        Some(Duration::from_secs(self.config.timeout_seconds))
    }

    async fn pre_handler(&self, ctx: &HookContext) -> Result<(), HookError> {
        let Some(stores) = &self.stores else {
            return Ok(());
        };
        let (Some(branch_id), Some(incoming)) = (ctx.branch(), ctx.revision()) else {
            return Ok(());
        };
        let repository_id = ctx.repository();
        let pusher = ctx.user().unwrap_or_default();

        let locks = match stores
            .locks
            .query_locks(LockQuery::RepositoryBranch(repository_id, branch_id))
            .await
        {
            Ok(locks) => locks,
            Err(err) => {
                warn!(
                    ?err,
                    "Lock guard couldn't read the locks; letting the push through"
                );
                return Ok(());
            }
        };
        let others: HashMap<String, LockData> = locks
            .into_iter()
            .filter(|lock| lock.owner != pusher)
            .map(|lock| (lock.resource.description.clone(), lock))
            .collect();
        if others.is_empty() {
            return Ok(());
        }

        let repository = Arc::new(RepositoryContext::new_server_context(
            stores.immutable.clone(),
            stores.mutable.clone(),
            repository_id,
            lore_revision::state::no_link_traversal(),
        ));
        let changed =
            match changed_paths(repository, branch_id, incoming, self.config.history_limit).await {
                Some(changed) => changed,
                None => return Ok(()),
            };

        let mut blocked: Vec<&LockData> = changed
            .iter()
            .filter_map(|path| others.get(path.as_str()))
            .collect();
        if blocked.is_empty() {
            return Ok(());
        }
        blocked.sort_by(|a, b| a.resource.description.cmp(&b.resource.description));
        blocked.dedup_by(|a, b| a.resource.description == b.resource.description);
        debug!(count = blocked.len(), "Lock guard refused a push");
        Err(HookError::rejected(
            HOOK_NAME,
            refusal(&blocked),
            StatusCode::FailedPrecondition,
        ))
    }
}

/// The message a refused push shows. Clients match its first words, "Locked by another user".
fn refusal(blocked: &[&LockData]) -> String {
    let named: Vec<String> = blocked
        .iter()
        .take(NAMED_FILES)
        .map(|lock| format!("{} (locked by {})", lock.resource.description, lock.owner))
        .collect();
    let mut message = format!("Locked by another user: {}", named.join(", "));
    if blocked.len() > NAMED_FILES {
        message.push_str(&format!(" and {} more", blocked.len() - NAMED_FILES));
    }
    message.push_str(
        ". Ask them to release the lock, or remove your changes to these files, then push again.",
    );
    message
}

/// The paths `incoming` changes relative to where it left the branch, or `None` when the
/// revisions can't be read.
async fn changed_paths(
    repository: Arc<RepositoryContext>,
    branch_id: lore_revision::lore::BranchId,
    incoming: Hash,
    history_limit: usize,
) -> Option<HashSet<String>> {
    let head = branch::load_latest_local(repository.clone(), branch_id)
        .await
        .ok()
        .unwrap_or_default();
    let base = if head.is_zero() || head == incoming {
        head
    } else {
        branch_point(repository.clone(), head, incoming, history_limit).await
    };
    if base == incoming {
        return Some(HashSet::new());
    }
    let state_incoming = State::deserialize(repository.clone(), incoming)
        .await
        .ok()?;
    if base.is_zero() {
        // A first push onto an empty branch: no file in it can have been locked yet.
        return None;
    }
    let state_base = State::deserialize(repository.clone(), base).await.ok()?;
    let collected = collect_stream_with_summary(|tx| {
        diff::diff_revision_paths(repository.clone(), state_base, state_incoming, None, tx)
    })
    .await;
    let (_, changes) = match collected {
        Ok(collected) => collected,
        Err(err) => {
            warn!(
                ?err,
                "Lock guard couldn't diff the pushed revision; letting the push through"
            );
            return None;
        }
    };
    let mut paths = HashSet::new();
    for change in &changes {
        paths.insert(change.path().as_str().to_string());
        if let Some(from) = change.relocated_from() {
            paths.insert(from.as_str().to_string());
        }
    }
    Some(paths)
}

/// Where `incoming` left the branch: `head` itself when it is an ancestor of `incoming`,
/// otherwise the nearest ancestor of `incoming` that is also an ancestor of `head`. Falls back
/// to `head` when neither turns up within `limit` revisions, which errs towards checking more.
async fn branch_point(
    repository: Arc<RepositoryContext>,
    head: Hash,
    incoming: Hash,
    limit: usize,
) -> Hash {
    let incoming_ancestors = ancestors(repository.clone(), incoming, limit).await;
    if incoming_ancestors.contains(&head) {
        return head;
    }
    let head_ancestors: HashSet<Hash> = ancestors(repository, head, limit)
        .await
        .into_iter()
        .collect();
    incoming_ancestors
        .into_iter()
        .find(|revision| head_ancestors.contains(revision))
        .unwrap_or(head)
}

/// `start` and its ancestors through both parents, nearest first, at most `limit` of them.
async fn ancestors(repository: Arc<RepositoryContext>, start: Hash, limit: usize) -> Vec<Hash> {
    let mut seen = HashSet::from([start]);
    let mut order = Vec::new();
    let mut queue = VecDeque::from([start]);
    while let Some(revision) = queue.pop_front() {
        order.push(revision);
        if order.len() >= limit {
            break;
        }
        let Ok(state) = State::deserialize(repository.clone(), revision).await else {
            continue;
        };
        for parent in state.parents() {
            if !parent.is_zero() && seen.insert(parent) {
                queue.push_back(parent);
            }
        }
    }
    order
}

struct LockGuardFactory {
    stores: Option<Arc<Stores>>,
}

impl HookFactory for LockGuardFactory {
    fn name(&self) -> &'static str {
        HOOK_NAME
    }

    fn create(&self, config: &toml::Value) -> Result<Box<dyn Hook>, HookError> {
        let config: LockGuardConfig = config.clone().try_into().map_err(|err| {
            HookError::execution_failed(HOOK_NAME, format!("Invalid lock_guard settings: {err}"))
        })?;
        if self.stores.is_none() {
            warn!("lock_guard is enabled but the server has no lock store; pushes aren't checked");
        }
        Ok(Box::new(LockGuardHook {
            stores: self.stores.clone(),
            config,
        }))
    }
}

pub fn register(registry: &mut HookRegistry, ctx: &HookRegistrationContext) {
    let stores = match (&ctx.immutable_store, &ctx.mutable_store, &ctx.lock_store) {
        (Some(immutable), Some(mutable), Some(locks)) => Some(Arc::new(Stores {
            immutable: immutable.clone(),
            mutable: mutable.clone(),
            locks: locks.clone(),
        })),
        _ => None,
    };
    registry.register_hook(Box::new(LockGuardFactory { stores }));
}
