using System.Text.RegularExpressions;
using LoreVcs;
using LoreVcs.Types;
using LoreVcs.Types.Args;
using LoreVcs.Types.Enums;
using LoreVcs.Types.Events;

namespace LoreCompanion;

/// <summary>What an operation reports when it finishes: a summary for the window (null: the
/// operation's usual text) and whether it needs the user's attention.</summary>
public sealed record OperationOutcome(string? Summary = null, bool Warning = false);

/// <summary>A failure whose message is already written for the user and is shown as it is.</summary>
public class CompanionException(string message, Exception? inner = null) : Exception(message, inner);

/// <summary>The operation stopped, before changing what the choice is about, because only the
/// user can say how to go on. The window lists <see cref="Files"/> with a button per way forward
/// and runs the operation again with <see cref="OperationRequest.Choice"/> and the same files.
/// Kind "staged": files are staged for commit and Lore can't sync while they are ("commit" or
/// "unstage"). Kind "both": files changed both here and on the server ("mine" or "theirs");
/// <see cref="Merged"/> says Lore already merged and left them in conflict.</summary>
public sealed class ChoiceRequired(string kind, IReadOnlyList<string> files, string message, bool merged = false) : CompanionException(message)
{
    public string Kind { get; } = kind;
    public IReadOnlyList<string> Files { get; } = files;
    public bool Merged { get; } = merged;
}

/// <summary>One file as the working copy's status reports it.</summary>
public sealed record FileState(string Path, bool Staged, bool Conflicted, bool Merged, bool Added = false, bool Deleted = false);

/// <summary>A file the Commit window offers: <paramref name="Change"/> is "added", "deleted" or
/// "modified"; <paramref name="LockedBy"/> names someone else holding a lock on it, or is empty.</summary>
public sealed record CommitCandidate(string Path, string Change, bool Staged, string LockedBy);

// Step and Restarts describe the server's verification in the "verify" phase, where Complete
// counts the checks it has done.
public sealed record CommitProgress(string Phase, ulong Complete = 0, ulong Total = 0, ulong Bytes = 0, string Step = "", uint Restarts = 0);

// The server's progress verifying a push, this fork's own lorelib event
// (LORE_EVENT_FORK_BRANCH_PUSH_VERIFY_PROGRESS in lore.h). The LoreVcs package doesn't know the
// event: it hands it over with its tag as a number and no way to its data, so this reads the
// fields from the native event the package holds, at the layout lore.h gives.
internal static class ServerProgressEvent
{
    public const uint Tag = 65536;
    private static readonly System.Reflection.FieldInfo? Native = typeof(LoreEventFFI).GetField(
        "loreEvent", System.Reflection.BindingFlags.Instance | System.Reflection.BindingFlags.NonPublic);

    public static CommitProgress? Read(LoreEventFFI loreEvent)
    {
        if ((uint)loreEvent.Tag != Tag || Native?.GetValue(loreEvent) is not System.Reflection.Pointer pointer) return null;
        IntPtr data;
        unsafe { data = (IntPtr)System.Reflection.Pointer.Unbox(pointer); }
        if (data == IntPtr.Zero) return null;
        // The tag, padding, then the data: repository (16 bytes), branch (16), then phase,
        // restarts and checked.
        var phase = (uint)System.Runtime.InteropServices.Marshal.ReadInt32(data, 8 + 32);
        if (phase == 0) return null;
        var restarts = (uint)System.Runtime.InteropServices.Marshal.ReadInt32(data, 8 + 36);
        var checkedCount = (ulong)System.Runtime.InteropServices.Marshal.ReadInt64(data, 8 + 40);
        var step = phase switch { 1 => "preparing", 2 => "checking fragments", 3 => "merging", 4 => "updating the branch", 5 => "running server hooks", _ => "" };
        return new CommitProgress("verify", checkedCount, Step: step, Restarts: restarts);
    }
}

public static class LoreOperations
{
    public static async Task<OperationOutcome> ExecuteAsync(OperationRequest request, Action<string> report, CancellationToken cancellationToken, Action<CommitProgress>? progress = null)
    {
        cancellationToken.ThrowIfCancellationRequested();
        return await ExecuteCore(request, report, progress);
    }

    private static bool RequiresAuthentication(LoreError error) =>
        error.ReturnCode == 16 || error.Message.Contains("Not authenticated", StringComparison.OrdinalIgnoreCase)
        || error.Message.Contains("MissingToken", StringComparison.OrdinalIgnoreCase)
        || error.Message.Contains("missing token", StringComparison.OrdinalIgnoreCase);

    private static async Task<OperationOutcome> ExecuteCore(OperationRequest request, Action<string> report, Action<CommitProgress>? progress)
    {
        try
        {
            // A choice answers a question Sync asked (Commit asks through Sync too).
            if (request.Choice.Length > 0) return await Sync(request, report);
            return request.Operation switch
            {
                CompanionOperation.Clone => await Clone(request, report),
                CompanionOperation.Sync => await Sync(request, report),
                CompanionOperation.Commit => await Commit(request, report, progress),
                CompanionOperation.Status => await Done(Status(request, report, true)),
                CompanionOperation.Stage => await Done(Stage(request, report)),
                CompanionOperation.Unstage => await Unstage(request, report),
                CompanionOperation.Revert => await Revert(request, report),
                CompanionOperation.Resolve => await Resolve(request, report),
                CompanionOperation.History => await Done(History(request, report)),
                CompanionOperation.Lock => await Done(ChangeLock(request, report, true)),
                CompanionOperation.Unlock => await Done(ChangeLock(request, report, false)),
                _ => throw new InvalidDataException("Operation isn't supported"),
            };
        }
        finally
        {
            // Explorer's icons follow what happened, also when the operation stopped half-way.
            if (request.Operation is not (CompanionOperation.Status or CompanionOperation.History or CompanionOperation.Clone))
            {
                try { await Status(request, report, false); } catch (LoreError) { }
            }
        }
    }

    private static async Task<OperationOutcome> Done(Task work)
    {
        await work;
        return new OperationOutcome();
    }

    private static async Task<OperationOutcome> Clone(OperationRequest request, Action<string> report)
    {
        using var globals = new LoreGlobalArgs { RepositoryPath = request.TargetPath, Identity = SelectedIdentity(request) };
        using var args = new LoreRepositoryCloneArgs { RepositoryUrl = request.RepositoryUrl };
        await Lore.RepositoryClone(globals, args).Callback(LogCallback(report)).WaitAsync();
        // Hidden at once: a status, which also hides it, comes only with the first other
        // operation, and an empty repository has nothing else to show in the folder.
        HideMetadata(request.TargetPath);
        report("Clone completed");
        return new OperationOutcome();
    }

    // Sync downloads what others pushed and sends the commits not sent yet. Where Lore can't go on
    // without a decision it stops before touching those files and asks (ChoiceRequired); the run
    // with the answer goes through the same steps:
    //  1. a merge left conflicts: keep my version or the server's for each file;
    //  2. files are staged (Lore refuses to sync then): commit them, or unstage them (their changes
    //     stay in the files);
    //  3. a file changed both here and on the server (Lore refuses the whole sync): keep mine
    //     (commit it; the sync then merges, and where both versions can't be merged mine wins), or
    //     take the server's (my changes to it are discarded, after the window's confirmation);
    //  4. the sync merged diverged commits with conflicts: as in 1;
    //  5. send what's committed.
    // A choice applies only to the files the user saw; any other file is asked about again.
    private static async Task<OperationOutcome> Sync(OperationRequest request, Action<string> report)
    {
        var notes = new List<string>();
        var warning = false;

        var conflicts = (await ReadStatus(request)).Where(file => file.Conflicted).Select(file => file.Path).ToList();
        if (conflicts.Count > 0)
        {
            if (!Confirmed(request, "mine", conflicts) && !Confirmed(request, "theirs", conflicts)) throw MergeConflict(conflicts);
            notes.Add(await KeepVersion(request, conflicts, request.Choice == "mine", report));
        }

        var staged = (await ReadStatus(request)).Where(file => file.Staged && !file.Merged).Select(file => file.Path).ToList();
        if (staged.Count > 0)
        {
            if (Confirmed(request, "commit", staged))
            {
                await CommitLocally(request, staged, request.Message, report);
                notes.Add($"Committed {Names(staged)}.");
            }
            else if (Confirmed(request, "unstage", staged))
            {
                await UnstagePaths(request, staged.Select(path => FullPath(request, path)).ToArray(), report);
                notes.Add($"{Names(staged)}: no longer staged, the changes are still in the files.");
            }
            else
            {
                throw new ChoiceRequired("staged", staged, "Lore can't sync while files are staged for commit.");
            }
        }

        for (var attempt = 0; ; attempt++)
        {
            var changedHere = await TrySync(request, report);
            if (changedHere is null) break;
            if (attempt == 0 && Confirmed(request, "theirs", changedHere))
            {
                await Discard(request, changedHere, report);
                notes.Add($"Your changes to {Names(changedHere)} were discarded; the server's version was downloaded.");
            }
            else if (attempt == 0 && Confirmed(request, "mine", changedHere))
            {
                await CommitLocally(request, changedHere, $"Keep my version of {Names(changedHere)}", report);
            }
            else
            {
                throw new ChoiceRequired("both", changedHere, "Nothing was downloaded: these files changed both here and on the server.");
            }
        }
        report("Sync completed");

        conflicts = (await ReadStatus(request)).Where(file => file.Conflicted).Select(file => file.Path).ToList();
        if (conflicts.Count > 0)
        {
            KeepUsableBinaries(request, conflicts, report);
            if (!Confirmed(request, "mine", conflicts)) throw MergeConflict(conflicts);
            notes.Add(await KeepVersion(request, conflicts, keepMine: true, report));
        }

        // A clean merge of local commits with the server's is committed by Sync; send it, and any
        // commit not sent yet.
        try
        {
            if (await Push(request, report)) notes.Add("Your commits were sent to the server.");
        }
        catch (CompanionException error)
        {
            notes.Add(error.Message);
            warning = true;
        }
        return new OperationOutcome(notes.Count == 0 ? null : "Repository synchronized. " + string.Join(" ", notes), warning);
    }

    private static ChoiceRequired MergeConflict(List<string> files) =>
        new("both", files, "You and someone else changed these files, and Lore couldn't merge the changes.", merged: true);

    // Whether the user chose <paramref name="choice"/> for every one of <paramref name="files"/>.
    private static bool Confirmed(OperationRequest request, string choice, IReadOnlyCollection<string> files) =>
        request.Choice == choice && files.All(file => request.Files.Contains(file, PathText.Comparer));

    // Runs a sync. Null when it went through; the files Lore refused it for when a file changed
    // both here and on the server (then nothing was downloaded or changed).
    private static async Task<List<string>?> TrySync(OperationRequest request, Action<string> report)
    {
        var changedHere = new List<string>();
        using var globals = Globals(request);
        using var args = new LoreRevisionSyncArgs();
        try
        {
            await Lore.RevisionSync(globals, args).Callback((loreEvent, _) =>
            {
                LogCallback(report)(loreEvent, _);
                var text = loreEvent.Tag == LoreEventTag.LOG ? loreEvent.GetData<LoreLogEventDataFFI>().Message : null;
                var match = text is null ? null : LocalChangeLine.Match(text);
                if (match is { Success: true }) changedHere.Add(PathText.Native(match.Groups[1].Value.Trim()));
            }).WaitAsync();
            return null;
        }
        catch (LoreError error) when (error.Message.Contains("Local modifications prevent", StringComparison.OrdinalIgnoreCase)
            || error.Message.Contains("local changes", StringComparison.OrdinalIgnoreCase) || changedHere.Count > 0)
        {
            // Lore names the file in a log line that not every build passes on; failing that, the
            // files changed here are the candidates.
            if (changedHere.Count == 0)
            {
                try { changedHere.AddRange((await ReadStatus(request, scan: true)).Select(file => file.Path)); } catch (LoreError) { }
            }
            if (changedHere.Count == 0) throw;
            return changedHere.Distinct(PathText.Comparer).ToList();
        }
    }

    private static readonly Regex LocalChangeLine = new(@"File has local changes:\s*(.+?)(\s+\(incoming|$)", RegexOptions.IgnoreCase);

    /// <summary>What a commit of the selection would record, for the Commit window to list: the
    /// changed files under the selected paths and the files staged anywhere (a commit records every
    /// staged file). Conflicted files are left out, as Commit refuses them until they're resolved.
    /// It reads only the working copy; the window asks the server for locks separately
    /// (<see cref="LockedByOthers"/>) and marks them when they arrive.</summary>
    public static async Task<List<CommitCandidate>> CommitCandidates(OperationRequest request)
    {
        List<FileState> status;
        try
        {
            status = await ReadStatus(request, scan: true);
        }
        catch (LoreError error) when (error.ReturnCode == 80) // Lore AddressNotFound: a cold clone can lack metadata blocks.
        {
            status = await ReadStatus(request, scan: true, offline: false);
        }
        var isSelected = StatusPathSelection(request);
        return status.Where(file => !file.Conflicted && (file.Staged || isSelected(file.Path)))
            .OrderBy(file => file.Path, PathText.Comparer)
            .Select(file => new CommitCandidate(file.Path, file.Added ? "added" : file.Deleted ? "deleted" : "modified", file.Staged, ""))
            .ToList();
    }

    private static string RelativePath(OperationRequest request, string path) =>
        Path.IsPathRooted(path) ? Path.GetRelativePath(request.RepositoryRoot(), path) : PathText.Native(path);

    // Commit stages the selection, refuses if another user holds a lock on any file it would
    // commit (before anything is recorded, so nothing has to be undone), then commits and sends.
    // With nothing new to commit it sends commits an earlier push couldn't. When someone changed
    // the same files meanwhile the server can't take the commit as it is; Sync then merges it with
    // theirs and sends the result, asking first where both changed the same file.
    private static async Task<OperationOutcome> Commit(OperationRequest request, Action<string> report, Action<CommitProgress>? progress)
    {
        progress?.Invoke(new("prepare"));
        var stagedBefore = new List<string>();
        var staged = await PrepareCommit(request, report, stagedBefore);
        if (staged.Count == 0)
        {
            if (await Push(request, report, progress: progress)) return new OperationOutcome("Nothing new to commit; your earlier commits were sent to the server.");
            throw new CompanionException("There is nothing to commit: the selected files have no changes.");
        }

        // A check ahead of the server's own, which refuses the push of a file someone else has
        // locked: it spares a local commit that can't be sent. It is only a convenience, so a server
        // slow to answer doesn't hold the commit; the server's check still applies.
        var lookup = LockedByOthers(request, staged);
        var blocked = new List<(string Path, string Owner, DateTime? Since)>();
        try
        {
            if (await Task.WhenAny(lookup, Task.Delay(LockCheckWait)) == lookup) blocked = await lookup;
            else report("The server didn't list locks in time; it checks them when the commit is sent");
        }
        catch (LoreError error) when (!RequiresAuthentication(error))
        {
            report($"Couldn't check locks ({error.Message.Split('\n')[0]}); the server checks them when the commit is sent");
        }
        if (blocked.Count > 0)
        {
            try { await RestoreStage(request, stagedBefore, report); } catch (LoreError) { /* the refusal below matters more */ }
            throw new CompanionException($"Nothing was committed: {LockList(blocked)}. Ask them to release the lock, or leave these files out of the commit.");
        }

        progress?.Invoke(new("commit"));
        using (var globals = Globals(request, eventIntervalMs: progress is null ? 0u : 250u))
        using (var args = new LoreRevisionCommitArgs { Message = request.Message })
        {
            var log = LogCallback(report);
            await Lore.RevisionCommit(globals, args).Callback((entry, context) =>
            {
                log(entry, context);
                if (progress is null || entry.Tag != LoreEventTag.REVISION_COMMIT_PROGRESS) return;
                var count = entry.GetData<LoreRevisionCommitProgressEventDataFFI>().Count;
                progress(new("commit", count.FileCount, count.DiscoveryComplete ? count.FileTotal : 0, count.BytesTransferred));
            }).WaitAsync();
        }
        report("Commit completed");
        try
        {
            await Push(request, report, committed: true, progress: progress);
        }
        catch (CompanionException error) when (error.InnerException is LoreError inner && BehindTheServer(inner.Message))
        {
            progress?.Invoke(new("sync"));
            report("Someone changed the same files meanwhile; merging with the server");
            OperationOutcome merged;
            try
            {
                merged = await Sync(request, report);
            }
            catch (ChoiceRequired question)
            {
                throw new ChoiceRequired(question.Kind, question.Files, "Your revision is saved here but not sent yet: " + char.ToLowerInvariant(question.Message[0]) + question.Message[1..], question.Merged);
            }
            return new OperationOutcome("Revision created, merged with the changes others sent meanwhile, and sent to the server."
                + (merged.Warning && merged.Summary is { } note ? " " + note : ""), merged.Warning);
        }
        progress?.Invoke(new("sync"));
        return await CatchUp(request, report);
    }

    // Stages exactly <paramref name="files"/> and commits them locally (Sync sends the commit),
    // unless someone else holds a lock on one of them.
    private static async Task CommitLocally(OperationRequest request, List<string> files, string message, Action<string> report)
    {
        var blocked = await LockedByOthers(request, files);
        if (blocked.Count > 0)
        {
            throw new CompanionException($"Nothing was committed or downloaded: {LockList(blocked)}. Ask them to release the lock, or take the server's version.");
        }
        using (var globals = Globals(request))
        using (var args = new LoreFileStageArgs { Paths = files.Select(path => FullPath(request, path)).ToArray(), Scan = true })
        {
            await Lore.FileStage(globals, args).Callback(LogCallback(report)).WaitAsync();
        }
        using (var globals = Globals(request))
        using (var args = new LoreRevisionCommitArgs { Message = message })
        {
            await Lore.RevisionCommit(globals, args).Callback(LogCallback(report)).WaitAsync();
        }
        report("Commit completed");
    }

    // When others pushed changes to other files meanwhile, the server merges this commit with
    // theirs, and the merged revision exists only on the server; without a sync this working copy
    // can't even read its own status. Sync brings it (and the others' changes).
    private static async Task<OperationOutcome> CatchUp(OperationRequest request, Action<string> report)
    {
        try
        {
            using var globals = Globals(request);
            using var args = new LoreRevisionSyncArgs();
            await Lore.RevisionSync(globals, args).Callback(LogCallback(report)).WaitAsync();
            return new OperationOutcome();
        }
        catch (LoreError error) when (!RequiresAuthentication(error))
        {
            return new OperationOutcome($"Revision created and sent to the server. Others changed files meanwhile; run Lore Sync to get their changes ({error.Message.Split('\n')[0]}).", true);
        }
    }

    // A commit is local until it's pushed: without this the server, the WebUI and everyone else
    // never see it. If someone pushed meanwhile, the server fast-forwards when the changes don't
    // overlap; otherwise the push fails, the commit stays local and Sync merges and sends it.
    // Returns whether anything was sent.
    private static async Task<bool> Push(OperationRequest request, Action<string> report, bool committed = false, Action<CommitProgress>? progress = null)
    {
        using var globals = Globals(request, eventIntervalMs: progress is null ? 0u : 250u);
        using var args = new LoreBranchPushArgs { FastForwardMerge = true };
        progress?.Invoke(new("push"));
        var alreadyPushed = false;
        try
        {
            await Lore.BranchPush(globals, args).Callback((loreEvent, _) =>
            {
                LogCallback(report)(loreEvent, _);
                if (loreEvent.Tag == LoreEventTag.BRANCH_PUSH_REVISION_PUSH_BEGIN) progress?.Invoke(new("verify"));
                if (progress is not null && ServerProgressEvent.Read(loreEvent) is { } server) progress(server);
                else if (progress is not null && loreEvent.Tag == LoreEventTag.BRANCH_PUSH_FRAGMENT_PROGRESS)
                {
                    var data = loreEvent.GetData<LoreBranchPushFragmentProgressEventDataFFI>();
                    progress(new("push", data.Complete, data.Count, data.BytesTransferred));
                }
                if (loreEvent.Tag == LoreEventTag.BRANCH_PUSH && loreEvent.GetData<LoreBranchPushEventDataFFI>().FlagAlreadyPushed) alreadyPushed = true;
            }).WaitAsync();
        }
        catch (LoreError error) when (!RequiresAuthentication(error))
        {
            var saved = committed ? "The commit is saved in this working copy but wasn't sent to the server: " : "Your saved commits weren't sent to the server: ";
            if (error.Message.Contains("Locked by another user", StringComparison.OrdinalIgnoreCase))
            {
                throw new CompanionException(saved + await WithNames(request, LockRefusal(error.Message))
                    + " When the lock is released, run Lore Commit again to send it.", error);
            }
            if (BehindTheServer(error.Message))
            {
                throw new CompanionException(saved + "someone changed the same files on the server meanwhile. Run Lore Sync: it merges both and sends the result (if you both changed the same file, Lore > Resolve conflicts lets you choose).", error);
            }
            throw new CompanionException("Your commits are saved in this working copy. The server did not confirm the push. Check the server history before retrying. " + error.Message, error);
        }
        if (!alreadyPushed) report("Pushed to the server");
        return !alreadyPushed;
    }

    // The server can't take the commit as it is: someone sent a change to the same files meanwhile
    // (Lore says "diverged", or "Fast-forward merge has conflicts" when it tried to merge them).
    private static bool BehindTheServer(string message) =>
        message.Contains("diverged", StringComparison.OrdinalIgnoreCase)
        || message.Contains("Fast-forward merge has conflicts", StringComparison.OrdinalIgnoreCase);

    private static async Task Stage(OperationRequest request, Action<string> report)
    {
        using var globals = Globals(request);
        // Scan: a folder otherwise stages only files Lore already knows are changed (after a
        // Status), so committing a folder right after editing or deleting files would find nothing.
        using var args = new LoreFileStageArgs { Paths = request.Paths, Scan = true };
        await Lore.FileStage(globals, args).Callback(LogCallback(report)).WaitAsync();
        report("Selected paths staged");
    }

    /// <summary>Stages what the commit records and returns it. A refusal puts the stage back as it
    /// was, so the user finds their own staged files untouched. <paramref name="stagedBefore"/>, when
    /// given, receives what was staged before, for a caller that refuses later.</summary>
    internal static async Task<List<string>> PrepareCommit(OperationRequest request, Action<string> report, List<string>? stagedBefore = null)
    {
        var before = await ReadStatus(request);
        var unresolved = before.Where(file => file.Conflicted).Select(file => file.Path).ToList();
        if (unresolved.Count > 0) throw MergeConflict(unresolved);
        if (request.DisplayedPaths is { } displayed && before.Any(file => file.Staged && !displayed.Contains(file.Path)))
            throw new CompanionException("Another file was staged. Reopen Commit to review the selection.");
        var original = before.Where(file => file.Staged).Select(file => file.Path).ToList();
        stagedBefore?.AddRange(original);
        try
        {
            return await StageSelection(request, before, report);
        }
        catch (CompanionException)
        {
            try { await RestoreStage(request, original, report); } catch (LoreError) { /* the refusal matters more */ }
            throw;
        }
    }

    /// <summary>Puts the stage back to exactly <paramref name="staged"/>: what the commit staged is
    /// unstaged, what it unstaged is staged again. File contents are never touched.</summary>
    internal static async Task RestoreStage(OperationRequest request, IReadOnlyCollection<string> staged, Action<string> report)
    {
        var wanted = new HashSet<string>(staged, PathText.Comparer);
        var now = (await ReadStatus(request)).Where(file => file.Staged).Select(file => file.Path).ToList();
        var extra = now.Where(path => !wanted.Contains(path)).Select(path => FullPath(request, path)).ToArray();
        if (extra.Length > 0) await UnstagePaths(request, extra, report);
        var missing = wanted.Except(now, PathText.Comparer).Select(path => FullPath(request, path)).ToArray();
        if (missing.Length > 0)
        {
            using var globals = Globals(request);
            using var args = new LoreFileStageArgs { Paths = missing, Scan = true };
            await Lore.FileStage(globals, args).Callback(LogCallback(report)).WaitAsync();
        }
        if (extra.Length > 0 || missing.Length > 0) report("The staged files are as they were before");
    }

    private static async Task<List<string>> StageSelection(OperationRequest request, List<FileState> before, Action<string> report)
    {
        if (request.All || request.Files.Length == 0)
        {
            // Everything under the selected paths goes in: staging the paths themselves spares
            // listing hundreds of thousands of files one by one.
            if (request.DisplayedPaths?.Count != 0) await Stage(request, report);
        }
        else
        {
            await StageExactly(request, before, request.Files, report);
        }
        var staged = (await ReadStatus(request)).Where(file => file.Staged).Select(file => file.Path).ToList();
        if (request.All)
        {
            var outside = BulkCommitExclusions(request, staged);
            if (outside.Length > 0)
            {
                await UnstagePaths(request, outside.Select(file => FullPath(request, file)).ToArray(), report);
                report($"{Count(outside.Length, "file")} left out");
                staged = (await ReadStatus(request)).Where(file => file.Staged).Select(file => file.Path).ToList();
                if (BulkCommitExclusions(request, staged).Length > 0)
                    throw new CompanionException("The staged selection changed. Reopen Commit to review it.");
            }
        }
        return staged;
    }

    private static string[] BulkCommitExclusions(OperationRequest request, IEnumerable<string> staged)
    {
        var excluded = request.Excluded.ToHashSet(PathText.Comparer);
        return staged.Where(path => excluded.Contains(path) || request.DisplayedPaths?.Contains(path) == false).ToArray();
    }

    // The files the user ticked in the Commit window become exactly what is staged: they are
    // staged, and anything else staged is unstaged (its changes stay in the files), since a commit
    // records every staged file.
    private static async Task StageExactly(OperationRequest request, List<FileState> before, string[] files, Action<string> report)
    {
        var wanted = new HashSet<string>(files, PathText.Comparer);
        var extra = before.Where(file => file.Staged && !wanted.Contains(file.Path)).Select(file => FullPath(request, file.Path)).ToArray();
        if (extra.Length > 0) await UnstagePaths(request, extra, report);
        using var globals = Globals(request);
        using var args = new LoreFileStageArgs { Paths = files.Select(file => FullPath(request, file)).ToArray(), Scan = true };
        await Lore.FileStage(globals, args).Callback(LogCallback(report)).WaitAsync();
        report($"Staged {Count(files.Length, "file")}");
    }

    private static async Task<OperationOutcome> Unstage(OperationRequest request, Action<string> report)
    {
        await UnstagePaths(request, request.Paths, report);
        return new OperationOutcome("The selected files are no longer staged; their changes are kept.");
    }

    private static async Task UnstagePaths(OperationRequest request, string[] paths, Action<string> report)
    {
        using var globals = Globals(request);
        using var args = new LoreFileUnstageArgs { Paths = paths };
        await Lore.FileUnstage(globals, args).Callback(LogCallback(report)).WaitAsync();
        report("Unstaged");
    }

    // Revert puts the selected changed files back to the version of the last sync. Lore can't
    // undo it, so the window asks for confirmation first and says so. A new file has no synced
    // version: Lore leaves it in place (only unstaged), and so does the Companion; the result
    // names it, so nobody thinks it was deleted.
    private static async Task<OperationOutcome> Revert(OperationRequest request, Action<string> report)
    {
        var isSelected = StatusPathSelection(request);
        var selected = (await ReadStatus(request, scan: true)).Where(file => !file.Conflicted && isSelected(file.Path)).ToList();
        if (selected.Count == 0) return new OperationOutcome("There is nothing to revert: the selected files have no changes.");
        var added = selected.Where(file => file.Added).ToList();
        var changed = selected.Where(file => !file.Added).Select(file => file.Path).ToList();
        if (changed.Count > 0) await Discard(request, changed, report);
        var stagedNew = added.Where(file => file.Staged).Select(file => FullPath(request, file.Path)).ToArray();
        if (stagedNew.Length > 0) await UnstagePaths(request, stagedNew, report);
        report("Reverted");
        var newFiles = added.Select(file => file.Path).ToList();
        var summary = changed.Count > 0 ? $"Reverted {Count(changed.Count, "file")} to the last synced version." : "Nothing was reverted.";
        if (newFiles.Count > 0)
        {
            summary += $" New {(newFiles.Count == 1 ? "file" : "files")} {Names(newFiles)} {(newFiles.Count == 1 ? "isn't" : "aren't")} on the server, so Revert left {(newFiles.Count == 1 ? "it" : "them")} in place; delete {(newFiles.Count == 1 ? "it" : "them")} if you don't need {(newFiles.Count == 1 ? "it" : "them")}.";
        }
        return new OperationOutcome(summary, newFiles.Count > 0);
    }

    // Discards the local changes to <paramref name="files"/>: they get the last synced version back.
    private static async Task Discard(OperationRequest request, List<string> files, Action<string> report)
    {
        var states = (await ReadStatus(request)).ToDictionary(file => file.Path, PathText.Comparer);
        var staged = files.Where(path => states.TryGetValue(path, out var state) && state.Staged).Select(path => FullPath(request, path)).ToArray();
        // Lore can't reset a staged file; unstaged, it resets like any other.
        if (staged.Length > 0) await UnstagePaths(request, staged, report);
        using var globals = Globals(request);
        using var args = new LoreFileResetArgs { Paths = files.Select(path => FullPath(request, path)).ToArray() };
        await Lore.FileReset(globals, args).Callback(LogCallback(report)).WaitAsync();
    }

    // Resolve settles conflicts a Sync merge left, for the selected files, and sends the merge
    // when no conflict is left.
    private static async Task<OperationOutcome> Resolve(OperationRequest request, Action<string> report)
    {
        var isSelected = StatusPathSelection(request);
        var conflicts = (await ReadStatus(request)).Where(file => file.Conflicted && isSelected(file.Path)).Select(file => file.Path).ToList();
        if (conflicts.Count == 0) return new OperationOutcome("There are no conflicts in the selected files.");
        var result = await KeepVersion(request, conflicts, request.Message != "theirs", report);
        var remaining = (await ReadStatus(request)).Where(file => file.Conflicted).Select(file => file.Path).ToList();
        if (remaining.Count > 0) return new OperationOutcome($"{result} Still in conflict: {Names(remaining)}; resolve them to finish the merge.", true);
        await Push(request, report, committed: true);
        return new OperationOutcome(result + " The merge was sent to the server.");
    }

    // Puts the chosen version (mine, or the server's) in each conflicted file and marks it
    // resolved; Lore's own "mine"/"theirs" choices aren't used because in a sync merge they mean
    // the opposite sides. When no conflict is left, the merge is committed (not yet sent).
    private static async Task<string> KeepVersion(OperationRequest request, List<string> conflicts, bool keepMine, Action<string> report)
    {
        foreach (var path in conflicts)
        {
            var file = FullPath(request, path);
            var chosen = file + (keepMine ? MineSuffix : TheirsSuffix);
            if (File.Exists(chosen)) File.Copy(chosen, file, true);
            else if (File.Exists(file)) File.Delete(file); // that side deleted the file
            report($"{path}: keeping {(keepMine ? "your" : "the server's")} version");
        }
        using (var globals = Globals(request))
        using (var args = new LoreBranchMergeResolveArgs { Paths = conflicts.Select(path => FullPath(request, path)).ToArray() })
        {
            await Lore.BranchMergeResolve(globals, args).Callback(LogCallback(report)).WaitAsync();
        }
        if (!(await ReadStatus(request)).Any(file => file.Conflicted))
        {
            using var globals = Globals(request);
            using var args = new LoreRevisionCommitArgs { Message = "Merge with the server" };
            await Lore.RevisionCommit(globals, args).Callback(LogCallback(report)).WaitAsync();
            report("Merge committed");
        }
        return $"Kept {(keepMine ? "your" : "the server's")} version of {Names(conflicts)}.";
    }

    private const string MineSuffix = "~mine";
    private const string TheirsSuffix = "~theirs";

    // Lore writes conflict markers into a conflicted file, which breaks a binary one (an image or
    // a model no longer opens). Such a file gets the user's own version back until they resolve.
    private static void KeepUsableBinaries(OperationRequest request, IEnumerable<string> conflicts, Action<string> report)
    {
        foreach (var path in conflicts)
        {
            var file = FullPath(request, path);
            var mine = file + MineSuffix;
            try
            {
                if (File.Exists(mine) && (IsBinary(mine) || IsBinary(file + TheirsSuffix)))
                {
                    File.Copy(mine, file, true);
                    report($"{path}: binary file, keeping your version in place until you resolve");
                }
            }
            catch (IOException)
            {
                // The markers stay; Resolve still writes the chosen version.
            }
        }
    }

    public static bool IsBinary(string file)
    {
        if (!File.Exists(file)) return false;
        using var stream = File.OpenRead(file);
        var buffer = new byte[8192];
        var read = stream.Read(buffer, 0, buffer.Length);
        return Array.IndexOf(buffer, (byte)0, 0, read) >= 0;
    }

    /// <summary>The working copy's changed, staged and conflicted files (paths relative to the
    /// root). Offline: it reads only local state.</summary>
    public static async Task<List<FileState>> ReadStatus(OperationRequest request, bool scan = false, bool offline = true)
    {
        var root = request.RepositoryRoot();
        var files = new List<FileState>();
        using var globals = new LoreGlobalArgs { RepositoryPath = root, Offline = offline, Identity = SelectedIdentity(request) };
        using var args = new LoreRepositoryStatusArgs { Staged = true, Scan = scan, CheckDirty = !scan };
        await Lore.RepositoryStatus(globals, args).Callback((loreEvent, _) =>
        {
            if (loreEvent.Tag != LoreEventTag.REPOSITORY_STATUS_FILE) return;
            var status = loreEvent.GetData<LoreRepositoryStatusFileEventDataFFI>();
            if (status.Type == LoreNodeType.DIRECTORY) return;
            var path = Path.IsPathRooted(status.Path) ? Path.GetRelativePath(root, status.Path) : PathText.Native(status.Path);
            var conflicted = status.FlagConflictUnresolved || status.FlagConflict && !status.FlagStaged;
            var action = status.Action.ToString();
            var added = action.StartsWith("ADD", StringComparison.OrdinalIgnoreCase);
            var deleted = action.StartsWith("DELETE", StringComparison.OrdinalIgnoreCase);
            files.Add(new FileState(path, status.FlagStaged, conflicted, status.FlagMerged, added, deleted));
        }).WaitAsync();
        return files;
    }

    /// <summary>Locks other users hold on <paramref name="paths"/> (relative to the root) on the
    /// current branch, with the holder's display name where the server knows it. A lock on another
    /// branch doesn't stop a commit here: the server checks only the branch pushed to. Every lock on
    /// the branch arrives in one answer and is intersected here: asking about hundreds of thousands
    /// of paths a hundred at a time kept the Commit window busy for minutes on a large working
    /// copy.</summary>
    public static async Task<List<(string Path, string Owner, DateTime? Since)>> LockedByOthers(OperationRequest request, IReadOnlyCollection<string> paths)
    {
        if (paths.Count == 0) return [];
        var me = await CurrentUserId(request);
        var wanted = new HashSet<string>(paths.Select(path => Normalize(RelativePath(request, path))), PathText.Comparer);
        var held = new List<(string Path, string Owner, ulong LockedAt)>();
        var branch = await CurrentBranchName(request);
        using (var globals = Globals(request))
        using (var args = new LoreLockFileQueryArgs { Branch = branch })
        {
            await Lore.LockFileQuery(globals, args).Callback((loreEvent, _) =>
            {
                if (loreEvent.Tag != LoreEventTag.LOCK_FILE_QUERY) return;
                var status = loreEvent.GetData<LoreLockFileQueryEventDataFFI>();
                if (string.IsNullOrEmpty(status.Owner) || status.Owner == me) return;
                var path = Normalize(RelativePath(request, status.Path));
                if (!wanted.Contains(path)) return;
                held.Add((path, status.Owner, status.LockedAt));
            }).WaitAsync();
        }
        var names = await UserNames(request, held.Select(lock_ => lock_.Owner));
        return held.Select(lock_ => (lock_.Path, names.GetValueOrDefault(lock_.Owner, lock_.Owner),
            lock_.LockedAt > 0 ? (DateTime?)DateTimeOffset.FromUnixTimeMilliseconds((long)lock_.LockedAt).LocalDateTime : null)).ToList();
    }

    private static readonly TimeSpan LockCheckWait = TimeSpan.FromSeconds(20);

    private static string Normalize(string path) => PathText.Native(path).Trim(PathText.Separator);

    /// <summary>The working copy's branch, read locally. Empty when unknown, which asks for the locks
    /// of every branch: the cautious answer.</summary>
    private static async Task<string> CurrentBranchName(OperationRequest request)
    {
        var name = "";
        using var globals = new LoreGlobalArgs { RepositoryPath = request.RepositoryRoot(), Offline = true, Identity = SelectedIdentity(request) };
        using var args = new LoreRepositoryStatusArgs { RevisionOnly = true };
        try
        {
            await Lore.RepositoryStatus(globals, args).Callback((loreEvent, _) =>
            {
                if (loreEvent.Tag == LoreEventTag.REPOSITORY_STATUS_REVISION) name = loreEvent.GetData<LoreRepositoryStatusRevisionEventDataFFI>().BranchName;
            }).WaitAsync();
        }
        catch (LoreError)
        {
        }
        return name;
    }

    private static async Task<string> CurrentUserId(OperationRequest request)
    {
        var id = "";
        using var globals = Globals(request);
        using var args = new LoreAuthUserInfoArgs { UserIds = [] };
        try
        {
            await Lore.AuthUserInfo(globals, args).Callback((loreEvent, _) =>
            {
                if (loreEvent.Tag == LoreEventTag.AUTH_USER_INFO) id = loreEvent.GetData<LoreAuthUserInfoEventDataFFI>().Id;
            }).WaitAsync();
        }
        catch (LoreError)
        {
            // Unknown: every lock then counts as someone else's, the cautious answer.
        }
        return id;
    }

    private static async Task<Dictionary<string, string>> UserNames(OperationRequest request, IEnumerable<string> ids)
    {
        var names = new Dictionary<string, string>();
        var wanted = ids.Where(id => !string.IsNullOrEmpty(id)).Distinct().ToArray();
        if (wanted.Length == 0) return names;
        using var globals = Globals(request);
        using var args = new LoreAuthUserInfoArgs { UserIds = wanted };
        try
        {
            await Lore.AuthUserInfo(globals, args).Callback((loreEvent, _) =>
            {
                if (loreEvent.Tag != LoreEventTag.AUTH_USER_INFO) return;
                var user = loreEvent.GetData<LoreAuthUserInfoEventDataFFI>();
                if (!string.IsNullOrWhiteSpace(user.Name)) names[user.Id] = user.Name;
            }).WaitAsync();
        }
        catch (LoreError)
        {
            // The IDs are shown instead.
        }
        return names;
    }

    private static readonly Regex LockedByLine = new(@"([^,:]+?) \(locked by ([^)]+)\)");

    // The lock guard's refusal, from "Locked by another user: " to the end of its sentence.
    private static string LockRefusal(string message)
    {
        var start = message.IndexOf("Locked by another user", StringComparison.OrdinalIgnoreCase);
        var text = start >= 0 ? message[start..] : message;
        var end = text.IndexOf(". ", StringComparison.Ordinal);
        return end > 0 ? text[..(end + 1)] : text;
    }

    private static async Task<string> WithNames(OperationRequest request, string refusal)
    {
        var ids = LockedByLine.Matches(refusal).Select(match => match.Groups[2].Value).ToList();
        var names = await UserNames(request, ids);
        return LockedByLine.Replace(refusal, match => $"{match.Groups[1].Value} (locked by {names.GetValueOrDefault(match.Groups[2].Value, match.Groups[2].Value)})");
    }

    private static string LockList(List<(string Path, string Owner, DateTime? Since)> locks) =>
        string.Join("; ", locks.Take(10).Select(lock_ => $"{lock_.Path} is locked by {lock_.Owner}" + (lock_.Since is { } since ? $" since {since:g}" : "")))
        + (locks.Count > 10 ? $" and {locks.Count - 10} more" : "");

    private static string Names(IReadOnlyCollection<string> paths) =>
        string.Join(", ", paths.Take(10)) + (paths.Count > 10 ? $" and {paths.Count - 10} more" : "");

    private static string Count(int count, string noun) => $"{count} {noun}{(count == 1 ? "" : "s")}";

    private static string FullPath(OperationRequest request, string relative) =>
        Path.IsPathRooted(relative) ? relative : Path.GetFullPath(Path.Combine(request.RepositoryRoot(), relative));

    // ReadStatus supplies paths from this repository. Resolve the Explorer selection once per scan.
    private static Func<string, bool> StatusPathSelection(OperationRequest request)
    {
        var root = request.RepositoryRoot();
        var selected = request.Paths.Select(path => Path.TrimEndingDirectorySeparator(Path.GetFullPath(path)))
            .ToHashSet(PathText.Comparer);
        if (selected.Contains(Path.TrimEndingDirectorySeparator(root))) return _ => true;

        return relative =>
        {
            for (string? path = Path.GetFullPath(relative, root); path != null; path = Path.GetDirectoryName(path))
            {
                if (selected.Contains(path)) return true;
            }
            return false;
        };
    }

    // The working copy's .lore folder, hidden as Lore's own data rather than the user's files.
    private static void HideMetadata(string root)
    {
        var metadata = Path.Combine(root, ".lore");
        if (Directory.Exists(metadata))
        {
            File.SetAttributes(metadata, File.GetAttributes(metadata) | FileAttributes.Hidden);
        }
    }

    private static async Task Status(OperationRequest request, Action<string> report, bool scan)
    {
        var root = request.RepositoryRoot();
        HideMetadata(root);
        StatusCache.Current.Clear(root);
        var folderStates = new Dictionary<string, OverlayState>(PathText.Comparer);
        var fileStates = new HashSet<string>(PathText.Comparer);
        void TrackParents(string path, OverlayState state)
        {
            for (var directory = Path.GetDirectoryName(path); !string.IsNullOrEmpty(directory); directory = Path.GetDirectoryName(directory))
            {
                folderStates[directory] = folderStates.TryGetValue(directory, out var current)
                    ? CombineOverlayStates(current, state)
                    : state;
            }
            folderStates[""] = folderStates.TryGetValue("", out var rootState)
                ? CombineOverlayStates(rootState, state)
                : state;
        }
        using var globals = new LoreGlobalArgs { RepositoryPath = root, Offline = true };
        using var args = new LoreRepositoryStatusArgs { Staged = true, Scan = scan, CheckDirty = !scan };
        await Lore.RepositoryStatus(globals, args).Callback((loreEvent, _) =>
        {
            if (loreEvent.Tag == LoreEventTag.REPOSITORY_STATUS_FILE)
            {
                var status = loreEvent.GetData<LoreRepositoryStatusFileEventDataFFI>();
                // As `lore status` groups them. A staged file keeps its dirty flag while it differs
                // from the base revision, so the flag doesn't mean "edited again after staging"
                // and isn't used for staged files.
                var state = status.FlagConflict ? OverlayState.Conflicted
                    : status.FlagStaged ? OverlayState.Staged
                    : OverlayState.Modified;
                var path = Path.IsPathRooted(status.Path) ? Path.GetRelativePath(root, status.Path) : status.Path;
                StatusCache.Current.Set(root, path, state);
                fileStates.Add(Path.GetFullPath(Path.Combine(root, path)));
                TrackParents(path, state);
                report($"{status.Path}: {state}");
            }
            else if (loreEvent.Tag == LoreEventTag.PATH_IGNORE)
            {
                var ignored = loreEvent.GetData<LorePathIgnoreEventDataFFI>();
                StatusCache.Current.Set(root, ignored.Path, OverlayState.Ignored);
            }
        }).WaitAsync();
        // The status above is local and knows nothing about locks, and clearing the cache dropped
        // the Locked entries set when the locks were taken. Ask the server for the repository's
        // locks so the padlock survives every refresh; when the server can't be reached, show the
        // locks it reported last time (LockCache). A file that is also modified or staged keeps
        // that state (it matters more).
        List<string> locked;
        try
        {
            locked = [];
            using var lockGlobals = Globals(request);
            using var lockArgs = new LoreLockFileQueryArgs();
            await Lore.LockFileQuery(lockGlobals, lockArgs).Callback((loreEvent, _) =>
            {
                if (loreEvent.Tag == LoreEventTag.LOCK_FILE_QUERY)
                {
                    var lockedPath = loreEvent.GetData<LoreLockFileQueryEventDataFFI>().Path;
                    locked.Add(Path.IsPathRooted(lockedPath) ? Path.GetRelativePath(root, lockedPath) : lockedPath);
                }
            }).WaitAsync();
            LockCache.Save(root, locked);
        }
        catch (Exception exception)
        {
            locked = LockCache.Load(root);
            report(locked.Count > 0
                ? $"The server can't be reached ({exception.Message}); showing the {locked.Count} lock(s) it reported last time."
                : $"Locks couldn't be read from the server: {exception.Message}");
        }
        foreach (var path in locked)
        {
            if (fileStates.Contains(Path.GetFullPath(Path.Combine(root, path)))) continue;
            StatusCache.Current.Set(root, path, OverlayState.Locked);
            TrackParents(path, OverlayState.Locked);
        }
        foreach (var (path, state) in folderStates)
        {
            StatusCache.Current.Set(root, path, state);
        }
        StatusCache.Current.Changed(root, request.Paths);
    }

    public static OverlayState CombineOverlayStates(OverlayState left, OverlayState right)
    {
        static int Priority(OverlayState state) => state switch
        {
            OverlayState.Conflicted => 5,
            OverlayState.StagedModified => 4,
            OverlayState.Modified => 3,
            OverlayState.Staged => 2,
            OverlayState.Locked => 1,
            _ => 0,
        };
        return Priority(left) >= Priority(right) ? left : right;
    }

    private static async Task History(OperationRequest request, Action<string> report)
    {
        using var globals = Globals(request);
        using var args = new LoreRevisionHistoryArgs();
        await Lore.RevisionHistory(globals, args).Callback((loreEvent, _) =>
        {
            if (loreEvent.Tag == LoreEventTag.REVISION_HISTORY_ENTRY)
            {
                var entry = loreEvent.GetData<LoreRevisionHistoryEntryEventDataFFI>();
                report($"Revision {entry.RevisionNumber}: {Convert.ToHexStringLower(entry.Revision.Data)}");
            }
        }).WaitAsync();
    }

    private static async Task ChangeLock(OperationRequest request, Action<string> report, bool acquire)
    {
        using var globals = Globals(request);
        if (acquire)
        {
            using var args = new LoreLockFileAcquireArgs { Paths = request.Paths };
            try
            {
                await Lore.LockFileAcquire(globals, args).Callback(LogCallback(report)).WaitAsync();
            }
            catch (LoreError error) when (error.Message.Contains("already locked", StringComparison.OrdinalIgnoreCase))
            {
                var held = await LockedByOthers(request, request.Paths.Select(path => Path.GetRelativePath(request.RepositoryRoot(), path)).ToList());
                throw new CompanionException(held.Count > 0
                    ? $"No locks were taken: {LockList(held)}."
                    : "No locks were taken: another user holds a lock on one of the selected files.", error);
            }
        }
        else
        {
            using var args = new LoreLockFileReleaseArgs { Paths = request.Paths };
            await Lore.LockFileRelease(globals, args).Callback(LogCallback(report)).WaitAsync();
        }
        var root = request.RepositoryRoot();
        var known = LockCache.Load(root).ToHashSet(PathText.Comparer);
        foreach (var path in request.Paths)
        {
            var relative = Path.GetRelativePath(root, path);
            StatusCache.Current.Set(root, relative, acquire ? OverlayState.Locked : OverlayState.Normal);
            if (acquire) known.Add(relative); else known.Remove(relative);
        }
        LockCache.Save(root, known);
        report(acquire ? "Locks acquired" : "Locks released");
    }

    private static string SelectedIdentity(OperationRequest request) => ClientSettings.Load().SelectedAccount(request.ServerKey());

    private static LoreGlobalArgs Globals(OperationRequest request, uint eventIntervalMs = 0) => new()
    {
        RepositoryPath = request.RepositoryRoot(),
        Identity = SelectedIdentity(request),
        EventIntervalMs = eventIntervalMs,
    };

    private static LoreCallback LogCallback(Action<string> report) => (loreEvent, _) =>
    {
        if (loreEvent.Tag == LoreEventTag.LOG)
        {
            report(loreEvent.GetData<LoreLogEventDataFFI>().Message);
        }
    };
}

// The locks the server reported last for each working copy, so the padlock overlays survive a
// refresh while the server is unreachable. Kept per user in %LOCALAPPDATA%\Lore\locks, one file
// per working copy (named by a hash of its path); only paths, nothing secret.
internal static class LockCache
{
    private static string FileFor(string root)
    {
        var directory = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData), "Lore", "locks");
        return Path.Combine(directory, $"{PathText.Hash(root):x16}.json");
    }

    public static List<string> Load(string root)
    {
        try
        {
            var file = FileFor(root);
            return File.Exists(file) ? System.Text.Json.JsonSerializer.Deserialize<List<string>>(File.ReadAllText(file)) ?? [] : [];
        }
        catch (Exception)
        {
            return [];
        }
    }

    public static void Save(string root, IEnumerable<string> paths)
    {
        try
        {
            var file = FileFor(root);
            Directory.CreateDirectory(Path.GetDirectoryName(file)!);
            File.WriteAllText(file, System.Text.Json.JsonSerializer.Serialize(paths.Distinct(PathText.Comparer).Order(PathText.Comparer).ToList()));
        }
        catch (Exception)
        {
            // Only a fallback for offline refreshes; the server stays the source of truth.
        }
    }
}
