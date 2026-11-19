using LoreVcs;
using LoreVcs.Types.Args;

namespace LoreCompanion.Tests;

[TestClass]
public sealed class CommitSnapshotTests
{
    [TestMethod]
    public async Task BulkCommitRecordsOnlyDisplayedAndCheckedPaths()
    {
        var root = Directory.CreateTempSubdirectory("LoreCommitSnapshot-");
        try
        {
            using var globals = new LoreGlobalArgs { RepositoryPath = root.FullName, Offline = true };
            using var create = new LoreRepositoryCreateArgs { RepositoryUrl = "snapshot" };
            await Lore.RepositoryCreate(globals, create).WaitAsync();
            File.WriteAllText(Path.Combine(root.FullName, "included.txt"), "included");
            File.WriteAllText(Path.Combine(root.FullName, "unchecked.txt"), "unchecked");
            var request = new OperationRequest { Operation = CompanionOperation.Commit, Paths = [root.FullName], All = true, Excluded = ["unchecked.txt"] };
            request.DisplayedPaths = (await LoreOperations.ReadStatus(request, scan: true)).Select(file => file.Path).ToHashSet(StringComparer.OrdinalIgnoreCase);
            File.WriteAllText(Path.Combine(root.FullName, "late.txt"), "late");

            var staged = await LoreOperations.PrepareCommit(request, _ => { });
            CollectionAssert.AreEqual(new[] { "included.txt" }, staged);
            using var commit = new LoreRevisionCommitArgs { Message = "Snapshot check" };
            await Lore.RevisionCommit(globals, commit).WaitAsync();

            var remaining = await LoreOperations.ReadStatus(request, scan: true);
            CollectionAssert.AreEquivalent(new[] { "unchecked.txt", "late.txt" }, remaining.Select(file => file.Path).ToArray());
            Assert.IsFalse(remaining.Any(file => file.Staged));
            Assert.AreEqual("late", File.ReadAllText(Path.Combine(root.FullName, "late.txt")));
        }
        finally
        {
            using var releaseGlobals = new LoreGlobalArgs { RepositoryPath = root.FullName, Offline = true };
            using var release = new LoreRepositoryReleaseArgs();
            await Lore.RepositoryRelease(releaseGlobals, release).WaitAsync();
            // The Windows native store can close its last file handle shortly after Release returns.
            for (var attempt = 0; ; attempt++)
            {
                try
                {
                    root.Delete(true);
                    break;
                }
                catch (IOException) when (attempt < 20)
                {
                    await Task.Delay(50);
                }
            }
        }
    }
}
