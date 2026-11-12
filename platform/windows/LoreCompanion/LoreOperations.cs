using LoreVcs;
using LoreVcs.Types;
using LoreVcs.Types.Args;
using LoreVcs.Types.Enums;
using LoreVcs.Types.Events;

namespace LoreCompanion;

public static class LoreOperations
{
    public static async Task ExecuteAsync(OperationRequest request, Action<string> report, CancellationToken cancellationToken)
    {
        cancellationToken.ThrowIfCancellationRequested();
        try
        {
            await ExecuteCore(request, report);
        }
        catch (LoreError error) when (NeedsInteractiveLogin(error))
        {
            report("Authentication is required. Complete the sign-in in the browser.");
            var userId = "";
            using var globals = new LoreGlobalArgs
            {
                RepositoryPath = request.RepositoryRoot(),
                Identity = SelectedIdentity(request),
            };
            using var args = new LoreAuthLoginInteractiveArgs
            {
                RemoteUrl = request.Operation == CompanionOperation.Clone ? request.RepositoryUrl : "",
            };
            await Lore.AuthLoginInteractive(globals, args).Callback((loreEvent, _) =>
            {
                LogCallback(report)(loreEvent, _);
                if (loreEvent.Tag == LoreEventTag.AUTH_USER_INFO)
                {
                    userId = loreEvent.GetData<LoreAuthUserInfoEventDataFFI>().Id;
                }
            }).WaitAsync();
            if (string.IsNullOrWhiteSpace(userId)) throw new InvalidDataException("The server returned no account identity after sign-in.");
            ClientSettings.Load().SelectAccount(request.ServerKey(), userId);
            await ExecuteCore(request, report);
        }
    }

    private static bool NeedsInteractiveLogin(LoreError error) =>
        error.Message.Contains("Not authenticated", StringComparison.OrdinalIgnoreCase)
        || error.Message.Contains("MissingToken", StringComparison.OrdinalIgnoreCase)
        || error.Message.Contains("missing token", StringComparison.OrdinalIgnoreCase);

    private static async Task ExecuteCore(OperationRequest request, Action<string> report)
    {
        switch (request.Operation)
        {
            case CompanionOperation.Clone:
                await Clone(request, report);
                break;
            case CompanionOperation.Sync:
                await Sync(request, report);
                break;
            case CompanionOperation.Commit:
                await Commit(request, report);
                break;
            case CompanionOperation.Status:
                await Status(request, report, true);
                break;
            case CompanionOperation.Stage:
                await Stage(request, report);
                break;
            case CompanionOperation.History:
                await History(request, report);
                break;
            case CompanionOperation.Lock:
            case CompanionOperation.Unlock:
                await ChangeLock(request, report, request.Operation == CompanionOperation.Lock);
                break;
            default:
                throw new InvalidDataException("Operation isn't supported");
        }
        if (request.Operation != CompanionOperation.Status && request.Operation != CompanionOperation.History)
        {
            await Status(request, report, false);
        }
    }

    private static async Task Clone(OperationRequest request, Action<string> report)
    {
        using var globals = new LoreGlobalArgs { RepositoryPath = request.TargetPath, Identity = SelectedIdentity(request) };
        using var args = new LoreRepositoryCloneArgs { RepositoryUrl = request.RepositoryUrl };
        await Lore.RepositoryClone(globals, args).Callback(LogCallback(report)).WaitAsync();
        report("Clone completed");
    }

    private static async Task Sync(OperationRequest request, Action<string> report)
    {
        using var globals = Globals(request);
        using var args = new LoreRevisionSyncArgs();
        await Lore.RevisionSync(globals, args).Callback(LogCallback(report)).WaitAsync();
        report("Sync completed");
    }

    private static async Task Commit(OperationRequest request, Action<string> report)
    {
        await Stage(request, report);
        using var globals = Globals(request);
        using var args = new LoreRevisionCommitArgs { Message = request.Message };
        await Lore.RevisionCommit(globals, args).Callback(LogCallback(report)).WaitAsync();
        report("Commit completed");
    }

    private static async Task Stage(OperationRequest request, Action<string> report)
    {
        using var globals = Globals(request);
        using var args = new LoreFileStageArgs { Paths = request.Paths };
        await Lore.FileStage(globals, args).Callback(LogCallback(report)).WaitAsync();
        report("Selected paths staged");
    }

    private static async Task Status(OperationRequest request, Action<string> report, bool scan)
    {
        var root = request.RepositoryRoot();
        var metadata = Path.Combine(root, ".lore");
        if (Directory.Exists(metadata))
        {
            File.SetAttributes(metadata, File.GetAttributes(metadata) | FileAttributes.Hidden);
        }
        OverlayCacheAccessor.Clear(root);
        var folderStates = new Dictionary<string, OverlayState>(StringComparer.OrdinalIgnoreCase);
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
                var state = status.FlagConflict ? OverlayState.Conflicted
                    : status.FlagStaged && status.FlagDirty ? OverlayState.StagedModified
                    : status.FlagStaged ? OverlayState.Staged
                    : OverlayState.Modified;
                var path = Path.IsPathRooted(status.Path) ? Path.GetRelativePath(root, status.Path) : status.Path;
                OverlayCacheAccessor.Set(root, path, state);
                TrackParents(path, state);
                report($"{status.Path}: {state}");
            }
            else if (loreEvent.Tag == LoreEventTag.PATH_IGNORE)
            {
                var ignored = loreEvent.GetData<LorePathIgnoreEventDataFFI>();
                OverlayCacheAccessor.Set(root, ignored.Path, OverlayState.Ignored);
            }
        }).WaitAsync();
        foreach (var (path, state) in folderStates)
        {
            OverlayCacheAccessor.Set(root, path, state);
        }
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
                report($"Revision {entry.RevisionNumber}: {entry.Revision}");
            }
        }).WaitAsync();
    }

    private static async Task ChangeLock(OperationRequest request, Action<string> report, bool acquire)
    {
        using var globals = Globals(request);
        if (acquire)
        {
            using var args = new LoreLockFileAcquireArgs { Paths = request.Paths };
            await Lore.LockFileAcquire(globals, args).Callback(LogCallback(report)).WaitAsync();
        }
        else
        {
            using var args = new LoreLockFileReleaseArgs { Paths = request.Paths };
            await Lore.LockFileRelease(globals, args).Callback(LogCallback(report)).WaitAsync();
        }
        var root = request.RepositoryRoot();
        foreach (var path in request.Paths)
        {
            OverlayCacheAccessor.Set(root, Path.GetRelativePath(root, path), acquire ? OverlayState.Locked : OverlayState.Normal);
        }
        report(acquire ? "Locks acquired" : "Locks released");
    }

    private static string SelectedIdentity(OperationRequest request) => ClientSettings.Load().SelectedAccount(request.ServerKey());

    private static LoreGlobalArgs Globals(OperationRequest request) => new()
    {
        RepositoryPath = request.RepositoryRoot(),
        Identity = SelectedIdentity(request),
    };

    private static LoreCallback LogCallback(Action<string> report) => (loreEvent, _) =>
    {
        if (loreEvent.Tag == LoreEventTag.LOG)
        {
            report(loreEvent.GetData<LoreLogEventDataFFI>().Message);
        }
    };
}

internal static class OverlayCacheAccessor
{
    private static readonly Lazy<OverlayCache> Cache = new();

    public static void Set(string root, string path, OverlayState state) => Cache.Value.Set(root, path, state);

    public static void Clear(string root) => Cache.Value.ClearRepository(root);
}
