# Windows client checks

What people rely on when they work through Explorer, checked against a live installation:
a commit reaches the server, a lock holds, and nothing is lost on Sync, Revert or a conflict.
Run these after merging upstream Lore, after changing the Companion, the Explorer extension or
the server's lock handling, and before releasing a client.

## What is checked

**Companion scenarios** (`LoreCompanion.Tests/CompanionScenarioTests.cs`) call every Companion
operation the way its window does, while a second person works on the same repository through
`lore.exe`. Where the window stops to ask (files staged, or changed both here and on the server),
a test checks that nothing was changed before the answer, which is what Cancel leaves, and then
presses each answer the way its button does. Each test gets its own repository. After each step a test compares the files on
disk, what the working copy reports, and what the server holds (read through the second
person's working copy).

| Test | What must hold |
|---|---|
| CloneCommitSyncAndHistory | Clone brings the files; Status sees new files; Commit sends the revision to the server; Sync brings others' changes; History lists revisions |
| CommitOfAFolderFindsEditsAndDeletesWithoutAStatusFirst | a folder commit takes edits, deletions and new files |
| CommitOfOneFileLeavesTheOtherChangesAlone | only the selected file is committed; other changes stay on disk |
| CommitWithNothingChangedSaysSo | a clear message, nothing recorded |
| CommitAfterSomeoneChangedOtherFilesIsMergedByTheServer | changes to different files from two people both reach the server |
| StageThenUnstageKeepsTheChange | Unstage removes the mark and keeps the change |
| SyncWithStagedFilesAsksAndSyncWithoutCommittingKeepsTheChanges | Sync with staged files asks first and changes nothing until answered; "Sync without committing" downloads and leaves the changes in the files, unstaged |
| SyncWithStagedFilesAsksAndCommitAndSyncSendsThem | "Commit and sync" commits the staged files, downloads, and sends the commit |
| ChangedHereAndOnTheServerTakeTheServersVersion | Sync names the file changed on both sides, downloads nothing, touches nothing; "Take the server's version" discards mine and downloads |
| ChangedHereAndOnTheServerKeepMyVersion | "Keep my version" puts mine on the server and still brings the other person's other changes |
| ChangedHereAndOnTheServerKeepMyVersionMergesChangesToDifferentLines | when the two changes can be merged, "Keep my version" keeps both |
| AChoiceAppliesOnlyToTheFilesTheUserSaw | an answer given for other files asks again and changes nothing |
| RevertRestoresEditedStagedAndDeletedFiles | Revert restores the selection only (edited, staged, deleted files) |
| RevertOfANewFile | a file the server never had stays in place (Lore has no version to go back to) and the result says so |
| LockBlocksTheOtherPersonUntilReleased | a lock stops the other person until it's released; the refusal names the holder |
| CommitIsRefusedBeforeAnythingIsRecordedWhenSomeoneElseHoldsTheLock | nothing is committed or left staged; unlocked files can still be committed |
| TheServerRefusesAPushThatChangesSomeoneElsesLockedFile | the server enforces locks even for clients that don't check them, and still accepts pushes that leave the locked file alone |
| LocksSurviveAServerRestart | locks outlive a restart and are still enforced (needs a restart command) |
| ConflictOnCommitKeepMyVersion | a commit whose file someone else changed meanwhile is merged; where Lore can't merge, Commit asks; "Keep my version" sends mine |
| BinaryConflictKeepsAUsableFileAndTakesTheServersVersion | a binary file never holds conflict markers; "Take the server's version" works on it |
| ConflictLeftForLaterIsAskedAgainAndResolvedFromTheMenu | after Cancel the conflict stays, Sync and Commit ask again, Lore > Resolve conflicts settles it |
| ResolveWithoutConflictsSaysSo | a clear message |

**Explorer integration** (`explorer/Test-Explorer.ps1`) drives the same operations through the
real Explorer context menu and the Companion window (UI Automation), and after every step also
checks the overlay icon Explorer would draw on every file and folder.

The server's lock store has its own unit tests in `lore-server/tests/unit/lock/file_store.rs`
(restart, all-or-nothing batches, 50 concurrent users, a write that fails).

## Running

On a Windows machine with the Lore client installed, in the desktop session of a user whose saved
Lore sign-in (Lore > Accounts) is an administrator of the installation:

```powershell
pwsh -File platform/windows/tests/Run-ClientTests.ps1 `
    -Web http://lore.example.org:8080 `
    -EnvFile D:\lore\platform\deploy\.env `
    -RestartCommand "docker restart deploy-lore-1" `
    -Explorer
```

- `-EnvFile` is read for `LORE_ADMIN_PASSWORD` (or set `LORE_LIVE_ADMIN_PASSWORD`); the password
  is never printed.
- `-RestartCommand` is optional; without it the restart checks are skipped.
- Build the client first (`Build-Client.ps1`): the tests use the fork's Lore library from
  `target/release`, as the installed client does; the upstream one from NuGet can't sign in over
  plain HTTP.
- `-Explorer` adds the Explorer run (it closes open Companion windows while it works).
- The tests create repositories named `companion-<time>/...` and `explorer-<time>/...` and a
  temporary local user, which is deleted at the end.

`dotnet test` without these settings reports the scenario tests as inconclusive, so CI stays green
where no installation is reachable.

## After merging upstream Lore

1. `cargo test -p lore-server` — upstream's tests plus the lock store's.
2. Rebuild the server image and run the deployment end-to-end test, `platform/deploy/test/e2e.sh`.
3. Build and install the client (`Build-Client.ps1`, `Build-Msi.ps1`), then run
   `Run-ClientTests.ps1 -Explorer` as above.

What the scenarios pin down about Lore itself, so a change upstream shows up as a failing test
rather than in someone's lost work:

- `commit` is local; the Companion pushes after it.
- Lore refuses to sync while files are staged, and refuses the whole sync when a file changed both
  locally and on the server (the Companion asks what to do in both cases).
- Syncing a working copy whose commits diverged from the server merges them: a clean merge is
  committed, a conflicting one leaves `~mine`, `~theirs` and `~base` copies and writes conflict
  markers into the file (binary files too, hence the Companion's fix-up).
- In a sync merge, Lore's own `resolve mine` / `resolve theirs` mean the opposite sides, so the
  Companion writes the chosen version itself and marks the file resolved.
- A staged file can't be reset until it's unstaged.
