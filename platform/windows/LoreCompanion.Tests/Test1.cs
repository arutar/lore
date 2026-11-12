using System.Text.Json;

namespace LoreCompanion.Tests;

[TestClass]
public sealed class RequestContractTests
{
    [TestMethod]
    public void ParseAcceptsBoundedAbsolutePaths()
    {
        var request = OperationRequest.Parse("""{"Operation":2,"Paths":["C:\\work\\file.bin"],"Message":"change"}""");
        Assert.AreEqual(CompanionOperation.Commit, request.Operation);
        Assert.AreEqual("C:\\work\\file.bin", request.Paths[0]);
    }

    [TestMethod]
    public void ParseRejectsRelativePaths()
    {
        Assert.ThrowsExactly<InvalidDataException>(() =>
            OperationRequest.Parse("""{"Operation":1,"Paths":["work"]}"""));
    }

    [TestMethod]
    public void ParseRejectsUnknownOperation()
    {
        Assert.ThrowsExactly<JsonException>(() => OperationRequest.Parse("""{"Operation":"unknown"}"""));
    }

    [TestMethod]
    public void CloneTargetIsCollectedByDialog()
    {
        var request = OperationRequest.Parse("""{"Operation":0,"Paths":["C:\\work"]}""");
        Assert.ThrowsExactly<InvalidDataException>(() => request.Validate());
        request.TargetPath = @"C:\work\new-repo";
        request.Validate();
    }

    [TestMethod]
    public void CloneRejectsAnExistingWorkingCopy()
    {
        var root = Path.Combine(Path.GetTempPath(), "LoreCloneTest-" + Guid.NewGuid().ToString("N"));
        try
        {
            Directory.CreateDirectory(Path.Combine(root, ".lore"));
            var request = new OperationRequest { Operation = CompanionOperation.Clone, TargetPath = root };
            var error = Assert.ThrowsExactly<InvalidDataException>(() => request.Validate());
            StringAssert.Contains(error.Message, "Use Lore Sync");
        }
        finally
        {
            Directory.Delete(root, true);
        }
    }

    [TestMethod]
    public void WorkingCopyProvidesCurrentIdentityAndServer()
    {
        var root = Path.Combine(Path.GetTempPath(), "LoreIdentityTest-" + Guid.NewGuid().ToString("N"));
        try
        {
            Directory.CreateDirectory(Path.Combine(root, ".lore"));
            File.WriteAllText(Path.Combine(root, ".lore", "config.toml"), "remote_url = \"lore://localhost:41337\"\nidentity = \"user-id\"\n");
            var request = new OperationRequest { Operation = CompanionOperation.Accounts, Paths = [root] };
            Assert.AreEqual("user-id", request.CurrentIdentity());
            Assert.AreEqual("localhost", request.ServerKey());
        }
        finally
        {
            Directory.Delete(root, true);
        }
    }

    [TestMethod]
    public void ShellOperationNumbersMatchTheRequestContract()
    {
        Assert.AreEqual(CompanionOperation.Stage, OperationRequest.Parse("""{"Operation":3,"Paths":["C:\\work"]}""").Operation);
        Assert.AreEqual(CompanionOperation.Status, OperationRequest.Parse("""{"Operation":4,"Paths":["C:\\work"]}""").Operation);
        Assert.AreEqual(CompanionOperation.Accounts, OperationRequest.Parse("""{"Operation":8,"Paths":[]}""").Operation);
    }

    [TestMethod]
    public void HashPathIsCaseAndTrailingSlashInsensitive()
    {
        Assert.AreEqual(OverlayCache.HashPath(@"C:\Work\Repo\"), OverlayCache.HashPath(@"c:\work\repo"));
    }

    [TestMethod]
    public void HashPathDistinguishesFiles()
    {
        Assert.AreNotEqual(OverlayCache.HashPath(@"C:\Work\a.bin"), OverlayCache.HashPath(@"C:\Work\b.bin"));
    }

    [TestMethod]
    public void ProtocolErrorIsReadableAndDoesNotExposeInternalTrace()
    {
        var error = new Exception("Lore error -1: protocol https was not recognized at lore-transport/src/connection.rs:73:21");
        var message = OperationError.UserMessage(error, CompanionOperation.Clone);
        StringAssert.Contains(message, "unsupported protocol");
        Assert.DoesNotContain("lore-transport", message);
    }

    [TestMethod]
    public void PkceDeviceLoginErrorExplainsTheServerMisconfiguration()
    {
        var error = new Exception("OIDC device authorization refused: invalid_request: Missing parameter: code_challenge_method");
        var message = OperationError.UserMessage(error, CompanionOperation.Clone);
        StringAssert.Contains(message, "Disable PKCE enforcement");
        Assert.DoesNotContain("code_challenge_method", message);
    }

    [TestMethod]
    public void OfflineTokenErrorExplainsTheMissingRealmRole()
    {
        var error = new Exception("OIDC token endpoint refused the device code grant: not_allowed: Offline tokens not allowed for the user or client");
        var message = OperationError.UserMessage(error, CompanionOperation.Clone);
        StringAssert.Contains(message, "default role");
        StringAssert.Contains(message, "offline_access");
    }

    [TestMethod]
    public void PermissionErrorSuggestsHowToResolveIt()
    {
        var message = OperationError.UserMessage(new Exception("HTTP 403 Forbidden"), CompanionOperation.Commit);
        StringAssert.Contains(message, "repository administrator");
    }

    [TestMethod]
    public void AccessDeniedExplainsRepositoryPermission()
    {
        var message = OperationError.UserMessage(new Exception("Not connected to remote: Not authorized to access repository"), CompanionOperation.Clone);
        StringAssert.Contains(message, "does not have access");
        StringAssert.Contains(message, "repository administrator");
    }

    [TestMethod]
    public void HiddenPrivateCloneOffersAnotherAccount()
    {
        var error = new Exception("Repository megatest_repo not found");
        var message = OperationError.UserMessage(error, CompanionOperation.Clone);
        StringAssert.Contains(message, "does not have access");
        StringAssert.Contains(message, "another account");
        Assert.IsTrue(OperationError.IsAccessDenied(error, CompanionOperation.Clone));
        Assert.IsFalse(OperationError.IsAccessDenied(error, CompanionOperation.Sync));
    }

    [TestMethod]
    public void LogoutTimeoutIsReadable()
    {
        var message = OperationError.UserMessage(new TaskCanceledException("The operation was canceled."), CompanionOperation.Accounts);
        StringAssert.Contains(message, "did not answer in time");
    }

    [TestMethod]
    public void ValidationErrorTextIsPreserved()
    {
        var message = OperationError.UserMessage(new InvalidDataException("Enter a commit message"), CompanionOperation.Commit);
        Assert.AreEqual("Enter a commit message", message);
    }

    [TestMethod]
    public void OverlayCacheCanClearPreviouslyWrittenState()
    {
        using var cache = new OverlayCache();
        var root = Path.Combine(Path.GetTempPath(), "LoreOverlayTest-" + Guid.NewGuid().ToString("N"));
        cache.Set(root, "file.bin", OverlayState.Modified);
        cache.ClearRepository(root);
    }

    [TestMethod]
    public void FolderOverlayUsesTheMostImportantChildState()
    {
        Assert.AreEqual(OverlayState.Conflicted, LoreOperations.CombineOverlayStates(OverlayState.Modified, OverlayState.Conflicted));
        Assert.AreEqual(OverlayState.StagedModified, LoreOperations.CombineOverlayStates(OverlayState.StagedModified, OverlayState.Modified));
        Assert.AreEqual(OverlayState.Modified, LoreOperations.CombineOverlayStates(OverlayState.Staged, OverlayState.Modified));
        Assert.AreEqual(OverlayState.Staged, LoreOperations.CombineOverlayStates(OverlayState.Locked, OverlayState.Staged));
    }

    [TestMethod]
    public void SsoLogoutUsesTheRepositoryWebHost()
    {
        Assert.AreEqual(
            "http://localhost:8080/",
            SsoLogout.PostLogoutUrl(new Uri("http://localhost:8080/auth/realms/lore"), "lore://localhost/real-e2e").AbsoluteUri);
        Assert.AreEqual(
            "https://lore.example/",
            SsoLogout.PostLogoutUrl(new Uri("https://auth.example/auth/realms/lore"), "lore://lore.example/real-e2e").AbsoluteUri);
    }

    [TestMethod]
    public async Task SsoLogoutRejectsUnknownAuthenticationSchemes()
    {
        await Assert.ThrowsExactlyAsync<InvalidDataException>(() => SsoLogout.Logout("ucs-auth://auth.example", "", "token"));
        await Assert.ThrowsExactlyAsync<InvalidDataException>(() => SsoLogout.Logout("oidc+file:///tmp/logout", "", "token"));
    }
}
