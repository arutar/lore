using LoreVcs;
using LoreVcs.Types.Args;

namespace LoreCompanion.Tests;

[TestClass]
[TestCategory("Live")]
public sealed class LiveLoreTests
{
    [TestMethod]
    public async Task AccountsListUsesFriendlyNames()
    {
        if (string.IsNullOrEmpty(Environment.GetEnvironmentVariable("LORE_LIVE_REPOSITORY_URL")))
        {
            Assert.Inconclusive("Live Lore endpoint isn't configured");
        }
        var accounts = await LoreAccounts.List();
        Console.WriteLine(string.Join("; ", accounts.Select(account => $"{account.UserId} {account.Username} {account.Domains}")));
        Assert.IsTrue(accounts.Any(account => !string.IsNullOrWhiteSpace(account.Username) && account.Domains.Contains("localhost")));
    }

    [TestMethod]
    public async Task RemoveOpensSsoLogout()
    {
        var authUrl = Environment.GetEnvironmentVariable("LORE_LIVE_LOGOUT_AUTH_URL");
        var userId = Environment.GetEnvironmentVariable("LORE_LIVE_LOGOUT_USER_ID");
        if (string.IsNullOrEmpty(authUrl) || string.IsNullOrEmpty(userId))
        {
            Assert.Inconclusive("Live logout account isn't configured");
        }
        await LoreAccounts.Remove(authUrl, userId, "lore://localhost");
        Assert.IsFalse((await LoreAccounts.List()).Any(account => account.AuthUrl == authUrl && account.UserId == userId));
    }

    [TestMethod]
    public async Task CloneUsesStoredIdentity()
    {
        var repositoryUrl = Environment.GetEnvironmentVariable("LORE_LIVE_REPOSITORY_URL");
        var root = Environment.GetEnvironmentVariable("LORE_LIVE_ROOT");
        if (string.IsNullOrEmpty(repositoryUrl) || string.IsNullOrEmpty(root))
        {
            Assert.Inconclusive("Live Lore endpoint isn't configured");
        }
        var request = new OperationRequest { Operation = CompanionOperation.Clone, RepositoryUrl = repositoryUrl, TargetPath = root };
        var identity = Environment.GetEnvironmentVariable("LORE_LIVE_IDENTITY");
        if (!string.IsNullOrEmpty(identity)) LoreAccounts.Select(repositoryUrl, identity);
        await LoreOperations.ExecuteAsync(request, _ => { }, CancellationToken.None);
        Assert.IsTrue(Directory.Exists(Path.Combine(root, ".lore")));
    }

    [TestMethod]
    public async Task CloneCommitAndSync()
    {
        var repositoryUrl = Environment.GetEnvironmentVariable("LORE_LIVE_REPOSITORY_URL");
        var root = Environment.GetEnvironmentVariable("LORE_LIVE_ROOT");
        if (string.IsNullOrEmpty(repositoryUrl) || string.IsNullOrEmpty(root))
        {
            Assert.Inconclusive("Live Lore endpoint isn't configured");
        }
        var logs = new List<string>();
        Task Execute(CompanionOperation operation, string path = "", string message = "") =>
            LoreOperations.ExecuteAsync(new OperationRequest
            {
                Operation = operation,
                RepositoryUrl = repositoryUrl,
                TargetPath = root,
                Paths = string.IsNullOrEmpty(path) ? [] : [path],
                Message = message,
            }, logs.Add, CancellationToken.None);

        await Execute(CompanionOperation.Clone);
        var file = Path.Combine(root, "companion-e2e.txt");
        await File.WriteAllTextAsync(file, "Lore companion end-to-end\n");
        await Execute(CompanionOperation.Stage, file);
        await Execute(CompanionOperation.Status, file);
        await Execute(CompanionOperation.Commit, file, "Companion E2E");
        await Execute(CompanionOperation.History, file);
        await Execute(CompanionOperation.Sync, file);
        await Execute(CompanionOperation.Lock, file);
        await Execute(CompanionOperation.Unlock, file);
        Assert.AreEqual("Lore companion end-to-end\n", await File.ReadAllTextAsync(file));
        Assert.IsTrue(logs.Any(line => line.StartsWith("Revision ")));
    }
}
