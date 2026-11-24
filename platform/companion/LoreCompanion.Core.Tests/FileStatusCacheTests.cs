using System.Buffers.Binary;

namespace LoreCompanion.Tests;

/// <summary>The status cache file the Linux file manager extension reads: what an operation's states
/// look like on disk, and that writers and damaged files don't lose or invent states.</summary>
[TestClass]
public sealed class FileStatusCacheTests
{
    private string folder = "";

    [TestInitialize]
    public void Create() => folder = Directory.CreateTempSubdirectory("lore-status-").FullName;

    [TestCleanup]
    public void Remove() => Directory.Delete(folder, true);

    private Dictionary<ulong, (OverlayState State, uint Root)> Entries()
    {
        var data = FileStatusCache.Read(Path.Combine(folder, "status-cache"));
        var entries = new Dictionary<ulong, (OverlayState, uint)>();
        var count = BinaryPrimitives.ReadInt32LittleEndian(data);
        for (var index = 0; index < count; index++)
        {
            var entry = data.AsSpan(FileStatusCache.HeaderSize + index * FileStatusCache.EntrySize, FileStatusCache.EntrySize);
            var hash = BinaryPrimitives.ReadUInt64LittleEndian(entry);
            if (hash != 0) entries[hash] = ((OverlayState)BinaryPrimitives.ReadInt32LittleEndian(entry[8..]), BinaryPrimitives.ReadUInt32LittleEndian(entry[20..]));
        }
        return entries;
    }

    private static string Root(string name) => Path.Combine(Path.GetTempPath(), name);

    [TestMethod]
    public void StatesReachTheFileWhenTheOperationIsDone()
    {
        var cache = new FileStatusCache(folder);
        var root = Root("game");
        cache.Set(root, Path.Combine("Docs", "readme.txt"), OverlayState.Modified);
        cache.Set(root, "", OverlayState.Modified);

        Assert.IsFalse(File.Exists(cache.FilePath), "nothing is written before Changed");
        cache.Changed(root, []);

        var entries = Entries();
        Assert.AreEqual(2, entries.Count);
        Assert.AreEqual(OverlayState.Modified, entries[PathText.Hash(Path.Combine(root, "Docs", "readme.txt"))].State);
        Assert.AreEqual(unchecked((uint)PathText.Hash(root)), entries[PathText.Hash(root)].Root);
    }

    [TestMethod]
    public void ClearForgetsOneWorkingCopyAndNormalRemovesAnEntry()
    {
        var cache = new FileStatusCache(folder);
        cache.Set(Root("game"), "a.txt", OverlayState.Staged);
        cache.Set(Root("game"), "b.txt", OverlayState.Locked);
        cache.Set(Root("other"), "c.txt", OverlayState.Conflicted);
        cache.Changed(Root("game"), []);

        cache.Clear(Root("game"));
        cache.Set(Root("game"), "b.txt", OverlayState.Locked);
        cache.Set(Root("other"), "c.txt", OverlayState.Normal);
        cache.Changed(Root("game"), []);

        var entries = Entries();
        Assert.AreEqual(1, entries.Count);
        Assert.AreEqual(OverlayState.Locked, entries[PathText.Hash(Path.Combine(Root("game"), "b.txt"))].State);
    }

    [TestMethod]
    public void TwoWritersKeepEachOthersStates()
    {
        var first = new FileStatusCache(folder);
        var second = new FileStatusCache(folder);
        first.Set(Root("game"), "a.txt", OverlayState.Modified);
        second.Set(Root("other"), "b.txt", OverlayState.Staged);

        first.Changed(Root("game"), []);
        second.Changed(Root("other"), []);

        Assert.AreEqual(2, Entries().Count);
    }

    [TestMethod]
    public void AFileFromAnotherVersionIsReadAsEmpty()
    {
        var path = Path.Combine(folder, "status-cache");
        var foreign = new byte[FileStatusCache.HeaderSize + FileStatusCache.Capacity * FileStatusCache.EntrySize];
        BinaryPrimitives.WriteInt32LittleEndian(foreign, 1);
        BinaryPrimitives.WriteUInt64LittleEndian(foreign.AsSpan(FileStatusCache.HeaderSize), 42);
        File.WriteAllBytes(path, foreign);

        Assert.AreEqual(0, Entries().Count);
        File.WriteAllBytes(path, [1, 2, 3]);
        Assert.AreEqual(0, Entries().Count);
    }

    [TestMethod]
    public void AFullCacheKeepsTheStatesItHas()
    {
        var cache = new FileStatusCache(folder);
        for (var index = 0; index < FileStatusCache.Capacity + 10; index++) cache.Set(Root("game"), $"f{index}", OverlayState.Modified);
        cache.Changed(Root("game"), []);

        var entries = Entries();
        Assert.AreEqual(FileStatusCache.Capacity, entries.Count);
        Assert.IsTrue(entries.ContainsKey(PathText.Hash(Path.Combine(Root("game"), "f0"))));
    }

    /// <summary>The extension computes the same hash: lore_menu_test.py checks this value too.</summary>
    [TestMethod]
    [OSCondition(OperatingSystems.Linux)]
    public void HashMatchesTheExtension() =>
        Assert.AreEqual(0x95ce8f54d9fe47a7UL, PathText.Hash("/home/user/wc/Docs/Ёлка.txt"));
}
