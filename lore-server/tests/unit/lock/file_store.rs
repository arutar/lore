// Lore Platform: the lock store kept in a file (`plugins::file_locks`).
use std::path::PathBuf;
use std::sync::Arc;

use lore_revision::lock::LockQuery;
use lore_revision::lock::LockStore;
use lore_revision::lock::util::assemble_resource_for_path;
use lore_revision::lore::BranchId;
use lore_revision::lore::RepositoryId;
use lore_server::plugins::file_locks::FileLockStore;

fn lock_file(name: &str) -> PathBuf {
    let folder =
        std::env::temp_dir().join(format!("lore-file-locks-{}-{name}", std::process::id()));
    let _ = std::fs::remove_dir_all(&folder);
    std::fs::create_dir_all(&folder).unwrap();
    folder.join("locks.json")
}

async fn held(store: &FileLockStore, repository: RepositoryId) -> Vec<(String, String)> {
    let mut locks: Vec<(String, String)> = store
        .query_locks(LockQuery::Repository(repository))
        .await
        .unwrap()
        .into_iter()
        .map(|lock| (lock.resource.description, lock.owner))
        .collect();
    locks.sort();
    locks
}

#[tokio::test]
async fn locks_survive_reopening_the_store() {
    let path = lock_file("reopen");
    let repository = RepositoryId::default();
    let branch = BranchId::default();
    {
        let store = FileLockStore::open(&path).unwrap();
        store
            .lock_resources(
                "anna",
                repository,
                &[
                    assemble_resource_for_path("art/tex.png", branch),
                    assemble_resource_for_path("b.txt", branch),
                ],
            )
            .await
            .unwrap();
        store
            .unlock_resources(
                "anna",
                true,
                repository,
                &[assemble_resource_for_path("b.txt", branch)],
            )
            .await
            .unwrap();
        store
            .lock_resources(
                "boris",
                repository,
                &[assemble_resource_for_path("c.txt", branch)],
            )
            .await
            .unwrap();
    }
    let reopened = FileLockStore::open(&path).unwrap();
    assert_eq!(
        held(&reopened, repository).await,
        vec![
            ("art/tex.png".into(), "anna".into()),
            ("c.txt".into(), "boris".into())
        ]
    );
    // The reopened store still enforces them.
    reopened
        .lock_resources(
            "boris",
            repository,
            &[assemble_resource_for_path("art/tex.png", branch)],
        )
        .await
        .expect_err("anna's lock outlived the restart");
}

#[tokio::test]
async fn a_missing_file_is_an_empty_store_and_a_damaged_one_is_refused() {
    let path = lock_file("damaged");
    let store = FileLockStore::open(&path).unwrap();
    assert!(held(&store, RepositoryId::default()).await.is_empty());
    std::fs::write(&path, b"{ not json").unwrap();
    let error = FileLockStore::open(&path)
        .err()
        .expect("a damaged file must not read as no locks");
    assert!(error.contains("can't be read"), "{error}");
}

#[tokio::test]
async fn a_batch_is_all_or_nothing() {
    let path = lock_file("batch");
    let store = FileLockStore::open(&path).unwrap();
    let repository = RepositoryId::default();
    let branch = BranchId::default();
    store
        .lock_resources(
            "anna",
            repository,
            &[assemble_resource_for_path("busy", branch)],
        )
        .await
        .unwrap();
    let error = store
        .lock_resources(
            "boris",
            repository,
            &[
                assemble_resource_for_path("free", branch),
                assemble_resource_for_path("busy", branch),
            ],
        )
        .await
        .expect_err("the batch is refused");
    assert_eq!(error.to_string(), "resource already locked");
    assert_eq!(
        held(&store, repository).await,
        vec![("busy".into(), "anna".into())]
    );
    // Releasing is all or nothing too: one lock that isn't boris's keeps both.
    store
        .lock_resources(
            "boris",
            repository,
            &[assemble_resource_for_path("free", branch)],
        )
        .await
        .unwrap();
    store
        .unlock_resources(
            "boris",
            true,
            repository,
            &[
                assemble_resource_for_path("free", branch),
                assemble_resource_for_path("busy", branch),
            ],
        )
        .await
        .expect_err("busy isn't boris's");
    assert_eq!(held(&store, repository).await.len(), 2);
    assert_eq!(
        held(&FileLockStore::open(&path).unwrap(), repository)
            .await
            .len(),
        2
    );
}

#[tokio::test(flavor = "multi_thread", worker_threads = 8)]
async fn concurrent_lockers_each_get_their_own_files_and_one_wins_a_shared_one() {
    let path = lock_file("concurrent");
    let store = Arc::new(FileLockStore::open(&path).unwrap());
    let repository = RepositoryId::default();
    let branch = BranchId::default();
    let mut tasks = Vec::new();
    for user in 0..50 {
        let store = store.clone();
        tasks.push(tokio::spawn(async move {
            let own = store
                .lock_resources(
                    &format!("user{user}"),
                    repository,
                    &[assemble_resource_for_path(
                        &format!("own/{user}.png"),
                        branch,
                    )],
                )
                .await
                .is_ok();
            let shared = store
                .lock_resources(
                    &format!("user{user}"),
                    repository,
                    &[assemble_resource_for_path("shared.png", branch)],
                )
                .await
                .is_ok();
            (own, shared)
        }));
    }
    let mut own = 0;
    let mut shared = 0;
    for task in tasks {
        let (got_own, got_shared) = task.await.unwrap();
        own += usize::from(got_own);
        shared += usize::from(got_shared);
    }
    assert_eq!(own, 50, "every user gets the file only they asked for");
    assert_eq!(shared, 1, "exactly one user gets the shared file");
    let in_memory = held(&store, repository).await;
    assert_eq!(in_memory.len(), 51);
    // The file holds exactly what memory does.
    assert_eq!(
        held(&FileLockStore::open(&path).unwrap(), repository).await,
        in_memory
    );
}

#[tokio::test]
async fn a_change_that_cant_be_saved_is_undone() {
    let path = lock_file("unwritable");
    let store = FileLockStore::open(&path).unwrap();
    let repository = RepositoryId::default();
    let branch = BranchId::default();
    store
        .lock_resources(
            "anna",
            repository,
            &[assemble_resource_for_path("kept", branch)],
        )
        .await
        .unwrap();
    // A folder where the temporary file goes makes the next write fail.
    let mut temporary = path.clone().into_os_string();
    temporary.push(".tmp");
    std::fs::create_dir(&temporary).unwrap();
    store
        .lock_resources(
            "anna",
            repository,
            &[assemble_resource_for_path("lost", branch)],
        )
        .await
        .expect_err("the write fails");
    store
        .unlock_resources(
            "anna",
            true,
            repository,
            &[assemble_resource_for_path("kept", branch)],
        )
        .await
        .expect_err("the write fails");
    assert_eq!(
        held(&store, repository).await,
        vec![("kept".into(), "anna".into())]
    );
}
