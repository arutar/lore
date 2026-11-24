namespace LoreCompanion.Tests;

/// <summary>The Commit window's model: folder tree, pages of files and the ticked selection. These
/// tests keep the window independent of a server and a working copy.</summary>
[TestClass]
public sealed class CommitListTests
{
    private static CommitCandidate File(string path, string change = "added", string lockedBy = "", bool staged = false) =>
        new(path, change, staged, lockedBy);

    private static CommitList List(params CommitCandidate[] files) => new(files);

    [TestMethod]
    public void CountsEveryFolderAndFoldsItIntoItsParents()
    {
        var list = List(
            File("bulk\\d01\\s02\\a.txt"),
            File("bulk\\d01\\s02\\b.txt", "modified"),
            File("bulk\\d01\\c.txt", "deleted"),
            File("root.txt"));

        var folders = list.Folders();

        // "bulk" holds nothing but "d01", so the two read as one row; "s02" keeps its own.
        Assert.AreEqual(2, folders.Count, string.Join(", ", folders.Select(folder => $"{folder.Path}={folder.Files}")));
        var bulk = folders.Single(folder => folder.Name == "bulk\\d01");
        Assert.AreEqual(3, bulk.Files);
        Assert.AreEqual(1, bulk.Added);
        Assert.AreEqual(1, bulk.Modified);
        Assert.AreEqual(1, bulk.Deleted);
        Assert.AreEqual(4, list.Total);
        var nested = folders.Single(folder => folder.Name == "s02");
        Assert.AreEqual(2, nested.Files);
        // The rows carry the path the page asks their files with.
        Assert.AreEqual(1, list.Page(bulk.Path, "", 0, 100).Total);
        Assert.AreEqual(2, list.Page(nested.Path, "", 0, 100).Total);
    }

    /// <summary>Lore reports paths with '/' on Linux: they fold into the same tree, keyed with '\'
    /// as the page asks for it, and a file's box answers to the path the page sends back.</summary>
    [TestMethod]
    public void FoldsPathsWithForwardSlashes()
    {
        var list = List(File("Art/hero.bin"), File("Docs/notes.txt"), File("Docs/readme.txt"));

        var folders = list.Folders();

        CollectionAssert.AreEqual(new[] { "Art", "Docs" }, folders.Select(folder => folder.Path).ToArray());
        Assert.AreEqual(2, list.Page("Docs", "", 0, 100).Total);
        list.SelectFile("Docs/notes.txt", false);
        CollectionAssert.AreEqual(new[] { "Docs/notes.txt" }, list.ExcludedFiles());
        Assert.AreEqual(1, list.Folders().Single(folder => folder.Path == "Docs").Selected);
    }

    [TestMethod]
    public void PageReturnsTheFilesOfOneFolderOnly()
    {
        var list = List(File("a\\one.txt"), File("a\\two.txt"), File("b\\three.txt"));

        var page = list.Page("a", "", 0, 100);

        Assert.AreEqual(2, page.Total);
        CollectionAssert.AreEquivalent(new[] { "a\\one.txt", "a\\two.txt" }, page.Items.Select(item => item.Path).ToArray());
        Assert.AreEqual("a", page.Folder);
    }

    [TestMethod]
    public void PagePagesThroughAFolderAndFiltersIt()
    {
        var files = Enumerable.Range(0, 450).Select(index => File($"bulk\\file{index:000}.bin")).ToArray();
        var list = List(files);

        var first = list.Page("bulk", "", 0, 200);
        var second = list.Page("bulk", "", 200, 200);
        var third = list.Page("bulk", "", 400, 200);
        var filtered = list.Page("bulk", "file04", 0, 200);

        Assert.AreEqual(450, first.Total);
        Assert.AreEqual(200, first.Items.Count);
        Assert.AreEqual(200, second.Items.Count);
        Assert.AreEqual(50, third.Items.Count);
        Assert.IsTrue(filtered.Total is > 0 and < 450, $"filtered {filtered.Total}");
        Assert.IsTrue(filtered.Items.All(item => item.Path.Contains("file04")));
    }

    [TestMethod]
    public void SearchSpansEveryFolder()
    {
        var list = List(File("a\\needle.txt"), File("b\\haystack.txt"), File("c\\needle.bin"));

        var found = list.Search("needle", 0, 200);

        Assert.AreEqual(2, found.Total);
        Assert.IsTrue(found.Items.All(item => item.Path.Contains("needle")));
    }

    [TestMethod]
    public void LeavingOutAFolderTakesItsFilesOutOfTheCommit()
    {
        var list = List(File("bulk\\a.txt"), File("bulk\\sub\\b.txt"), File("keep\\c.txt"));

        list.SelectFolder("bulk", false);

        Assert.AreEqual(1, list.Selected);
        CollectionAssert.AreEqual(new[] { "keep\\c.txt" }, list.ResultFiles());
        Assert.IsFalse(list.Folders().Single(folder => folder.Path == "bulk").Selected > 0);
    }

    [TestMethod]
    public void LockedFilesAreNeverTicked()
    {
        var list = List(File("a\\mine.txt"), File("a\\theirs.txt", lockedBy: "Someone"));

        Assert.AreEqual(1, list.Selected);
        Assert.IsFalse(list.IsSelected(@"a\theirs.txt"));
        Assert.AreEqual(1, list.Folders().Single().Selected);
        CollectionAssert.Contains(list.ExcludedFiles(), @"a\theirs.txt");
        list.SelectFolder("a", false);
        Assert.AreEqual(0, list.Selected);
        list.SelectFolder("a", true);
        Assert.AreEqual(1, list.Selected);
        list.SelectFile("a\\theirs.txt", true);
        Assert.AreEqual(1, list.Selected);
    }

    [TestMethod]
    public void EverythingTickedSendsNoFileList()
    {
        var list = List(File("a\\one.txt"), File("a\\two.txt"));

        Assert.IsTrue(list.All);
        Assert.IsNull(list.ResultFiles());

        list.SelectFile("a\\one.txt", false);
        Assert.IsFalse(list.All);
        CollectionAssert.AreEqual(new[] { "a\\two.txt" }, list.ResultFiles());

        list.SelectAll(true);
        Assert.IsTrue(list.All);
        Assert.IsNull(list.ResultFiles());
    }

    [TestMethod]
    public void ClosingFilesInAFolderSaysSoInTheFolderRow()
    {
        var list = List(File("a\\one.txt"), File("a\\two.txt"), File("a\\sub\\three.txt"));

        list.SelectFolder("a\\sub", false);

        var row = list.Folders().Single(folder => folder.Path == "a");
        Assert.AreEqual(3, row.Files);
        Assert.AreEqual(2, row.Selected);
    }

    [TestMethod]
    public void LeavingOutAFewFilesNamesThemInsteadOfTheRest()
    {
        var files = Enumerable.Range(0, 1000).Select(index => File($"bulk\\file{index:000}.bin")).ToArray();
        var list = List(files);

        list.SelectFile("bulk\\file000.bin", false);

        // One box off, 999 on: staging the path and taking that one out is cheaper than naming 999.
        Assert.IsTrue(list.MostlySelected);
        CollectionAssert.AreEqual(new[] { "bulk\\file000.bin" }, list.ExcludedFiles());
        Assert.AreEqual(999, list.Selected);
    }

    [TestMethod]
    public void LeavingOutAlmostEverythingNamesTheFilesThatStayIn()
    {
        var files = Enumerable.Range(0, 1000).Select(index => File($"bulk\\file{index:000}.bin")).ToArray();
        var list = List(files);

        list.SelectAll(false);
        list.SelectFile("bulk\\file001.bin", true);

        Assert.IsFalse(list.MostlySelected);
        CollectionAssert.AreEqual(new[] { "bulk\\file001.bin" }, list.ResultFiles());
    }

    [TestMethod]
    public void SevenHundredThousandFilesStayQuick()
    {
        var files = new List<CommitCandidate>(700_000);
        for (var folder = 0; folder < 60; folder++)
            for (var sub = 0; sub < 20; sub++)
                for (var file = 0; file < 583; file++)
                    files.Add(File($"bulk\\d{folder:00}\\s{sub:00}\\file{file:000}.bin"));

        var watch = System.Diagnostics.Stopwatch.StartNew();
        var list = new CommitList(files);
        var build = watch.ElapsedMilliseconds;

        watch.Restart();
        var folders = list.Folders();
        var tree = watch.ElapsedMilliseconds;

        watch.Restart();
        var page = list.Page("bulk\\d00\\s00", "", 0, 200);
        var pageTime = watch.ElapsedMilliseconds;

        watch.Restart();
        list.SelectFolder("bulk", false);
        var select = watch.ElapsedMilliseconds;

        watch.Restart();
        var remaining = list.ResultFiles();
        var result = watch.ElapsedMilliseconds;

        Assert.AreEqual(files.Count, list.Total);
        Assert.AreEqual(1261, folders.Count);
        Assert.AreEqual(200, page.Items.Count);
        Assert.AreEqual(0, list.Selected);
        Assert.AreEqual(0, remaining!.Length);
        // Generous ceilings: what matters is that nothing is quadratic or blocking for minutes.
        Assert.IsTrue(build < 20_000, $"build {build} ms");
        Assert.IsTrue(tree < 5_000, $"tree {tree} ms");
        Assert.IsTrue(pageTime < 500, $"page {pageTime} ms");
        Assert.IsTrue(select < 5_000, $"select {select} ms");
        Assert.IsTrue(result < 5_000, $"result {result} ms");
    }
}
