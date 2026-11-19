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
            await ReleaseRepository(root);
        }
    }

    [TestMethod]
    public async Task CandidatesIncludeSelectedDescendantsAndFilesStagedElsewhere()
    {
        var root = Directory.CreateTempSubdirectory("LoreCommitSelection-");
        try
        {
            using var globals = new LoreGlobalArgs { RepositoryPath = root.FullName, Offline = true };
            using var create = new LoreRepositoryCreateArgs { RepositoryUrl = "selection" };
            await Lore.RepositoryCreate(globals, create).WaitAsync();
            string[] files = [@"art\one.txt", @"art\sub\two.txt", @"artwork\three.txt", "staged.txt"];
            foreach (var file in files)
            {
                var path = Path.Combine(root.FullName, file);
                Directory.CreateDirectory(Path.GetDirectoryName(path)!);
                File.WriteAllText(path, file);
            }
            using var stage = new LoreFileStageArgs { Paths = [Path.Combine(root.FullName, "staged.txt")], Scan = true };
            await Lore.FileStage(globals, stage).WaitAsync();

            async Task Check(string[] selection, params string[] expected)
            {
                var request = new OperationRequest { Operation = CompanionOperation.Commit, Paths = selection };
                var candidates = await LoreOperations.CommitCandidates(request);
                CollectionAssert.AreEquivalent(expected, candidates.Select(file => file.Path).ToArray());
                Assert.IsTrue(candidates.Single(file => file.Path == "staged.txt").Staged);
            }

            await Check([root.FullName], files);
            await Check([Path.Combine(root.FullName, @"ART\")], files[0], files[1], files[3]);
            await Check([Path.Combine(root.FullName, files[0]), Path.Combine(root.FullName, files[2])],
                files[0], files[2], files[3]);
        }
        finally
        {
            await ReleaseRepository(root);
        }
    }

    private static async Task ReleaseRepository(DirectoryInfo root)
    {
        using var globals = new LoreGlobalArgs { RepositoryPath = root.FullName, Offline = true };
        using var release = new LoreRepositoryReleaseArgs();
        await Lore.RepositoryRelease(globals, release).WaitAsync();
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
