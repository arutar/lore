//! Lock store kept in a file, so locks outlive a server restart.
//!
//! Lore Platform addition. Upstream ships an in-memory store (`mode = "local"`, lost on every
//! restart) and a `DynamoDB` one. A single self-hosted server needs neither a database nor AWS:
//! the locks sit in memory as in the local store and every change is written to a JSON file
//! beside the server's other data before the caller hears it succeeded.
//!
//! ```toml
//! [lock_store]
//! mode = "file"
//!
//! [plugins.file]
//! path = "/data/locks.json"
//! ```
//!
//! One mutex covers the map and the file: a batch is locked or released whole, as `DynamoDB`'s
//! transaction does, and the file is written by one request at a time, each writing the map as
//! it stands, so a later write never carries less than an earlier one. A change whose write fails
//! is undone and reported, so a caller is never told it holds a lock a restart would drop. The
//! file is replaced atomically (written beside it, flushed, renamed), so a crash leaves the old
//! list or the new one, never half of one. Locks number in the thousands at most, so writing the
//! whole list each time costs a few milliseconds.

use std::collections::HashMap;
use std::io::Write;
use std::path::Path;
use std::path::PathBuf;
use std::sync::Arc;

use async_trait::async_trait;
use lore_base::error::InvalidArguments;
use lore_base::error::LockNotFound;
use lore_base::error::LockNotOwned;
use lore_base::error::PluginConfigError;
use lore_base::error::PluginInitError;
use lore_base::types::Hash;
use lore_base::types::LockData;
use lore_base::types::LockResource;
use lore_revision::lock::LockError;
use lore_revision::lock::LockQuery;
use lore_revision::lock::LockStore;
use lore_revision::lore::BranchId;
use lore_revision::lore::RepositoryId;
use lore_revision::util;
use serde::Deserialize;
use serde::Serialize;
use tokio::sync::Mutex;
use tracing::info;
use tracing::warn;

use crate::plugins::LockStorePluginFactory;
use crate::plugins::PluginError;
use crate::plugins::PluginRegistry;

const PLUGIN_NAME: &str = "file";
const FILE_VERSION: u32 = 1;

#[derive(Clone, Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct FileLockStoreConfig {
    /// The JSON file the locks are kept in. Its folder must exist and be writable.
    pub path: PathBuf,
}

#[derive(Clone, Copy, Eq, Hash, PartialEq)]
struct LockKey {
    repository: RepositoryId,
    branch: BranchId,
    hash: Hash,
}

impl LockKey {
    fn of(repository: RepositoryId, resource: &LockResource) -> Self {
        Self {
            repository,
            branch: resource.branch,
            hash: resource.hash,
        }
    }
}

/// One lock as the file stores it.
#[derive(Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
struct StoredLock {
    repository: RepositoryId,
    branch: BranchId,
    hash: Hash,
    description: String,
    owner: String,
    locked_at: u64,
}

#[derive(Deserialize, Serialize)]
struct StoredFile {
    version: u32,
    locks: Vec<StoredLock>,
}

type Locks = HashMap<LockKey, LockData>;

pub struct FileLockStore {
    path: PathBuf,
    locks: Mutex<Locks>,
}

impl FileLockStore {
    /// Opens the store, reading the locks the file holds. A missing file is an empty store; a
    /// file that can't be read is an error rather than an empty store, so a damaged file never
    /// silently frees every lock.
    pub fn open(path: impl Into<PathBuf>) -> Result<Self, String> {
        let path = path.into();
        let locks = match std::fs::read(&path) {
            Ok(bytes) => parse(&bytes).map_err(|err| {
                format!(
                    "The lock file {} can't be read: {err}. Fix or remove it to start the server.",
                    path.display()
                )
            })?,
            Err(err) if err.kind() == std::io::ErrorKind::NotFound => Locks::new(),
            Err(err) => return Err(format!("Can't open the lock file {}: {err}", path.display())),
        };
        info!(path = %path.display(), locks = locks.len(), "Opened the lock file");
        Ok(Self {
            path,
            locks: Mutex::new(locks),
        })
    }

    /// Writes `locks` to the file, replacing it whole.
    async fn save(&self, locks: &Locks) -> Result<(), LockError> {
        let mut stored: Vec<StoredLock> = locks
            .iter()
            .map(|(key, lock)| StoredLock {
                repository: key.repository,
                branch: key.branch,
                hash: key.hash,
                description: lock.resource.description.clone(),
                owner: lock.owner.clone(),
                locked_at: lock.locked_at,
            })
            .collect();
        // A stable order keeps the file readable and its backups comparable.
        stored.sort_by(|a, b| {
            (a.repository.to_string(), &a.description).cmp(&(b.repository.to_string(), &b.description))
        });
        let bytes = serde_json::to_vec_pretty(&StoredFile {
            version: FILE_VERSION,
            locks: stored,
        })
        .map_err(|err| LockError::internal(format!("Can't encode the locks: {err}")))?;
        let path = self.path.clone();
        tokio::task::spawn_blocking(move || write_atomically(&path, &bytes))
            .await
            .map_err(|err| LockError::internal(format!("Writing the lock file stopped: {err}")))?
            .map_err(|err| {
                warn!(?err, "Writing the lock file failed");
                LockError::internal(format!("Can't save the locks: {err}"))
            })
    }
}

fn parse(bytes: &[u8]) -> Result<Locks, String> {
    let file: StoredFile = serde_json::from_slice(bytes).map_err(|err| err.to_string())?;
    if file.version != FILE_VERSION {
        return Err(format!("unknown version {}", file.version));
    }
    Ok(file
        .locks
        .into_iter()
        .map(|lock| {
            let key = LockKey {
                repository: lock.repository,
                branch: lock.branch,
                hash: lock.hash,
            };
            let data = LockData {
                resource: LockResource {
                    branch: lock.branch,
                    hash: lock.hash,
                    description: lock.description,
                },
                owner: lock.owner,
                locked_at: lock.locked_at,
            };
            (key, data)
        })
        .collect())
}

fn write_atomically(path: &Path, bytes: &[u8]) -> std::io::Result<()> {
    let mut temporary = path.as_os_str().to_owned();
    temporary.push(".tmp");
    let temporary = PathBuf::from(temporary);
    {
        let mut file = std::fs::File::create(&temporary)?;
        file.write_all(bytes)?;
        file.sync_all()?;
    }
    std::fs::rename(&temporary, path)?;
    // The rename itself is durable once the folder is flushed. Windows can't open a folder this
    // way and makes the rename durable on its own.
    #[cfg(unix)]
    if let Some(folder) = path.parent() {
        std::fs::File::open(folder)?.sync_all()?;
    }
    Ok(())
}

#[async_trait]
impl LockStore for FileLockStore {
    async fn lock_resources(
        &self,
        owner_id: &str,
        repository: RepositoryId,
        resources: &[LockResource],
    ) -> Result<Vec<LockData>, LockError> {
        let mut locks = self.locks.lock().await;
        // All or nothing: refuse before changing anything if another owner holds any of them.
        for resource in resources {
            if let Some(lock) = locks.get(&LockKey::of(repository, resource))
                && lock.owner != owner_id
            {
                return Err(LockError::internal("resource already locked"));
            }
        }
        let timestamp = util::time::timestamp();
        let mut added = Vec::new();
        for resource in resources {
            let key = LockKey::of(repository, resource);
            // Already held by the caller (or listed twice): kept as it is, as the local store does.
            if locks.contains_key(&key) {
                continue;
            }
            let lock = LockData {
                resource: resource.clone(),
                owner: owner_id.to_string(),
                locked_at: timestamp,
            };
            locks.insert(key, lock.clone());
            added.push((key, lock));
        }
        if added.is_empty() {
            return Ok(Vec::new());
        }
        if let Err(err) = self.save(&locks).await {
            for (key, _) in &added {
                locks.remove(key);
            }
            return Err(err);
        }
        Ok(added.into_iter().map(|(_, lock)| lock).collect())
    }

    async fn query_locks(&self, query: LockQuery) -> Result<Vec<LockData>, LockError> {
        let locks = self.locks.lock().await;
        let selected = |key: &LockKey, lock: &LockData| match &query {
            LockQuery::Repository(repository) => key.repository == *repository,
            LockQuery::RepositoryBranch(repository, branch) => {
                key.repository == *repository && key.branch == *branch
            }
            LockQuery::RepositoryBranchDescription(repository, branch, description) => {
                key.repository == *repository
                    && key.branch == *branch
                    && lock.resource.description == *description
            }
            LockQuery::OwnerRepository(owner, repository) => {
                key.repository == *repository && lock.owner == *owner
            }
            LockQuery::OwnerRepositoryBranch(owner, repository, branch) => {
                key.repository == *repository && key.branch == *branch && lock.owner == *owner
            }
            LockQuery::HashRepositoryBranch(hash, repository, branch) => {
                key.repository == *repository && key.branch == *branch && key.hash == *hash
            }
            _ => false,
        };
        if !matches!(
            query,
            LockQuery::Repository(..)
                | LockQuery::RepositoryBranch(..)
                | LockQuery::RepositoryBranchDescription(..)
                | LockQuery::OwnerRepository(..)
                | LockQuery::OwnerRepositoryBranch(..)
                | LockQuery::HashRepositoryBranch(..)
        ) {
            return Err(InvalidArguments {
                reason: "unsupported lock query".into(),
            }
            .into());
        }
        Ok(locks
            .iter()
            .filter(|(key, lock)| selected(key, lock))
            .map(|(_, lock)| lock.clone())
            .collect())
    }

    async fn check_locks_status(
        &self,
        repository: RepositoryId,
        resources: &[LockResource],
    ) -> Result<Vec<LockData>, LockError> {
        let locks = self.locks.lock().await;
        Ok(resources
            .iter()
            .filter_map(|resource| locks.get(&LockKey::of(repository, resource)).cloned())
            .collect())
    }

    async fn unlock_resources(
        &self,
        owner_id: &str,
        validate_user: bool,
        repository: RepositoryId,
        resources: &[LockResource],
    ) -> Result<Vec<LockResource>, LockError> {
        let mut locks = self.locks.lock().await;
        // All or nothing, as the trait promises: check every resource before releasing any.
        for resource in resources {
            match locks.get(&LockKey::of(repository, resource)) {
                None => return Err(LockNotFound.into()),
                Some(lock) if validate_user && lock.owner != owner_id => {
                    return Err(LockNotOwned.into());
                }
                Some(_) => {}
            }
        }
        let mut removed = Vec::new();
        for resource in resources {
            let key = LockKey::of(repository, resource);
            if let Some(lock) = locks.remove(&key) {
                removed.push((key, lock));
            }
        }
        if let Err(err) = self.save(&locks).await {
            locks.extend(removed);
            return Err(err);
        }
        Ok(resources.to_vec())
    }
}

pub struct FileLockStorePluginFactory;

impl FileLockStorePluginFactory {
    fn config(&self, config: &toml::Value) -> Result<FileLockStoreConfig, PluginError> {
        config.clone().try_into().map_err(|err| {
            PluginError::from(PluginConfigError {
                plugin_name: PLUGIN_NAME.to_string(),
                message: format!("Invalid file lock store settings (expected `path`): {err}"),
            })
        })
    }
}

impl LockStorePluginFactory for FileLockStorePluginFactory {
    fn name(&self) -> &'static str {
        PLUGIN_NAME
    }

    fn validate_config(&self, config: &toml::Value) -> Result<(), PluginError> {
        self.config(config).map(|_| ())
    }

    fn create(&self, config: &toml::Value) -> Result<Arc<dyn LockStore>, PluginError> {
        let config = self.config(config)?;
        let store = FileLockStore::open(config.path).map_err(|message| {
            PluginError::from(PluginInitError {
                plugin_name: PLUGIN_NAME.to_string(),
                message,
            })
        })?;
        Ok(Arc::new(store))
    }
}

pub fn register(registry: &mut PluginRegistry) {
    registry.register_lock_store_plugin(Box::new(FileLockStorePluginFactory));
}
