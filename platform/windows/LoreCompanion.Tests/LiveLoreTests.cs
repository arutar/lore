using LoreVcs;
using LoreVcs.Types.Args;

namespace LoreCompanion.Tests;

[TestClass]
[TestCategory("Live")]
public sealed class LiveLoreTests
{
    [TestMethod]
    public async Task CloneCommitAndSync()
    {
        var token = Environment.GetEnvironmentVariable("LORE_LIVE_TOKEN");
        var repositoryUrl = Environment.GetEnvironmentVariable("LORE_LIVE_REPOSITORY_URL");
        var root = Environment.GetEnvironmentVariable("LORE_LIVE_ROOT");
        if (string.IsNullOrEmpty(token) || string.IsNullOrEmpty(repositoryUrl) || string.IsNullOrEmpty(root))
        {
            Assert.Inconclusive("Live Lore endpoint isn't configured");
        }
        Directory.CreateDirectory(root);
        using (var globals = new LoreGlobalArgs { RepositoryPath = root, IdentityToken = token, AccessToken = token })
        using (var args = new LoreRepositoryCloneArgs { RepositoryUrl = repositoryUrl })
        {
            await Lore.RepositoryClone(globals, args).WaitAsync();
        }

        var file = Path.Combine(root, "companion-e2e.txt");
        await File.WriteAllTextAsync(file, "Lore companion end-to-end\n");
        using (var globals = new LoreGlobalArgs { RepositoryPath = root, IdentityToken = token, AccessToken = token })
        using (var args = new LoreFileStageArgs { Paths = [file] })
        {
            await Lore.FileStage(globals, args).WaitAsync();
        }
        using (var globals = new LoreGlobalArgs { RepositoryPath = root, IdentityToken = token, AccessToken = token })
        using (var args = new LoreRevisionCommitArgs { Message = "Companion E2E" })
        {
            await Lore.RevisionCommit(globals, args).WaitAsync();
        }
        using (var globals = new LoreGlobalArgs { RepositoryPath = root, IdentityToken = token, AccessToken = token })
        using (var args = new LoreRevisionSyncArgs())
        {
            await Lore.RevisionSync(globals, args).WaitAsync();
        }
        Assert.AreEqual("Lore companion end-to-end\n", await File.ReadAllTextAsync(file));
    }
}
