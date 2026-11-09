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
    public void HashPathIsCaseAndTrailingSlashInsensitive()
    {
        Assert.AreEqual(OverlayCache.HashPath(@"C:\Work\Repo\"), OverlayCache.HashPath(@"c:\work\repo"));
    }

    [TestMethod]
    public void HashPathDistinguishesFiles()
    {
        Assert.AreNotEqual(OverlayCache.HashPath(@"C:\Work\a.bin"), OverlayCache.HashPath(@"C:\Work\b.bin"));
    }
}
