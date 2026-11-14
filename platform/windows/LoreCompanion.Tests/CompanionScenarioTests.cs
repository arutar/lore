using System.Text;

namespace LoreCompanion.Tests;

/// <summary>
/// Every Companion operation against a live server, with a second person working through
/// lore.exe at the same time. Each test makes its own repository, so tests don't see each other's
/// files. After every step a test checks what must not be lost: the files on disk, what the
/// working copy reports, and what the server holds (read through the second person's copy).
///
/// These are the checks to run after merging upstream Lore, after changing the Companion or the
/// server's lock handling, and before releasing a client: they pin the behaviour people rely on
/// (a commit reaches the server; a lock holds; nothing is lost on Sync, Revert or a conflict).
/// See platform/windows/tests/README.md for how to run them.
/// </summary>
[TestClass]
[TestCategory("Live")]
[TestCategory("Scenario")]
[DoNotParallelize]
public sealed class CompanionScenarioTests
{
    private static LiveServer? live;
    private static TestUser? other;
    private static string run = "";

    public TestContext TestContext { get; set; } = null!;

    [ClassInitialize]
    public static async Task Start(TestContext _)
    {
        live = LiveServer.FromEnvironment();
        if (live is null) return;
        run = DateTime.UtcNow.ToString("MMdd-HHmmss");
        await live.SignInAdmin();
        other = await live.CreateUser($"companion-{run}");
    }

    [ClassCleanup]
    public static async Task Stop()
    {
        if (live is not null && other is not null) await live.DeleteUser(other);
        live?.Dispose();
    }

    // ---- the test's world: a repository, the Companion's working copy and the other person's -----

    private sealed class World(LiveServer server, TestUser other, string url, string mine, string theirs, List<string> log)
    {
        public LiveServer Server => server;
        public TestUser Other => other;
        public string Url => url;
        /// <summary>The Companion's working copy.</summary>
        public string Mine => mine;
        /// <summary>The other person's working copy.</summary>
        public string Theirs => theirs;
        public List<string> Log => log;

        public string P(string relative) => Path.Combine(mine, relative);

        public Task<OperationOutcome> Run(CompanionOperation operation, string message = "", params string[] relative) =>
            Execute(operation, message, "", [], relative);

        /// <summary>Runs the operation again with the user's answer to a question, as the
        /// window does when a choice button is pressed.</summary>
        public Task<OperationOutcome> Answer(CompanionOperation operation, string choice, params string[] files) =>
            Execute(operation, "", choice, files, []);

        private async Task<OperationOutcome> Execute(CompanionOperation operation, string message, string choice, string[] files, string[] relative)
        {
            var request = new OperationRequest
            {
                Operation = operation,
                RepositoryUrl = operation == CompanionOperation.Clone ? url : "",
                TargetPath = operation == CompanionOperation.Clone ? mine : "",
                Paths = operation == CompanionOperation.Clone ? [] : relative.Length == 0 ? [mine] : relative.Select(P).ToArray(),
                Message = message,
                Choice = choice,
                Files = files,
            };
            request.Validate();
            log.Add($"> Companion {operation} {string.Join(' ', relative)}{(choice.Length > 0 ? $" choice={choice} files={string.Join(',', files)}" : "")}");
            return await LoreOperations.ExecuteAsync(request, log.Add, CancellationToken.None);
        }

        /// <summary>Runs an operation that must stop and ask; returns the question.</summary>
        public async Task<ChoiceRequired> Asked(Func<Task<OperationOutcome>> operation, string kind)
        {
            try
            {
                await operation();
            }
            catch (ChoiceRequired question)
            {
                log.Add($"  asked ({question.Kind}{(question.Merged ? ", merged" : "")}): {question.Message} [{string.Join(", ", question.Files)}]");
                Assert.AreEqual(kind, question.Kind, "the kind of question");
                return question;
            }
            Assert.Fail("The Companion should have stopped to ask");
            throw new InvalidOperationException();
        }

        public async Task<CompanionException> Refused(CompanionOperation operation, string message = "", params string[] relative)
        {
            try
            {
                await Run(operation, message, relative);
            }
            catch (CompanionException refusal)
            {
                log.Add($"  refused: {refusal.Message}");
                return refusal;
            }
            Assert.Fail($"Companion {operation} should have been refused");
            throw new InvalidOperationException();
        }

        public void Write(string relative, string content)
        {
            Directory.CreateDirectory(Path.GetDirectoryName(P(relative))!);
            File.WriteAllText(P(relative), content);
        }

        public string Read(string relative) => File.ReadAllText(P(relative));

        public async Task<List<FileState>> Status() =>
            await LoreOperations.ReadStatus(new OperationRequest { Operation = CompanionOperation.Status, Paths = [mine] }, scan: true);

        /// <summary>What the server holds: the other person's copy after a sync, every file and its text.</summary>
        public async Task<Dictionary<string, string>> OnServer()
        {
            await other.Must(theirs, "sync");
            return Directory.EnumerateFiles(theirs, "*", SearchOption.AllDirectories)
                .Where(file => !file.Contains(@"\.lore\") && !file.Contains('~'))
                .ToDictionary(file => Path.GetRelativePath(theirs, file), File.ReadAllText, StringComparer.OrdinalIgnoreCase);
        }

        public async Task<string> Locks() => await other.Must(theirs, "lock", "query");
    }

    private async Task<World> NewWorld(params (string Path, string Content)[] initial)
    {
        if (live is null || other is null) Assert.Inconclusive("Set LORE_LIVE_WEB, LORE_LIVE_SERVER, LORE_LIVE_ROOT and LORE_LIVE_ADMIN_PASSWORD to run the scenario tests");
        var name = $"companion-{run}/{TestContext.TestName!.ToLowerInvariant()}";
        var (id, url) = await live.CreateRepository(name);
        await live.Grant(id, other, "write");
        var folder = Path.Combine(live.Root, run, TestContext.TestName!);
        if (Directory.Exists(folder)) Directory.Delete(folder, true);
        Directory.CreateDirectory(folder);
        var world = new World(live, other, url, Path.Combine(folder, "mine"), Path.Combine(folder, "theirs"), []);
        TestContext.WriteLine($"Repository {url}, working copies in {folder}");
        // The other person starts the repository, so it isn't empty (an empty one can't be synced).
        await other.Must(folder, "clone", url, world.Theirs);
        await other.Push(world.Theirs, "Initial files", initial.Length > 0 ? initial : [("readme.txt", "start\n")]);
        await world.Run(CompanionOperation.Clone);
        return world;
    }

    private void Show(World world) => TestContext.WriteLine(string.Join(Environment.NewLine, world.Log));

    private static void Same(Dictionary<string, string> expected, Dictionary<string, string> actual, string what)
    {
        var want = string.Join("\n", expected.OrderBy(pair => pair.Key, StringComparer.OrdinalIgnoreCase).Select(pair => $"{pair.Key}={pair.Value}"));
        var have = string.Join("\n", actual.OrderBy(pair => pair.Key, StringComparer.OrdinalIgnoreCase).Select(pair => $"{pair.Key}={pair.Value}"));
        Assert.AreEqual(want, have, what);
    }

    private static Dictionary<string, string> Files(params (string Path, string Content)[] files) =>
        files.ToDictionary(file => file.Path, file => file.Content, StringComparer.OrdinalIgnoreCase);

    // ---- Clone, Status, Commit, Sync, History ------------------------------------------------------

    [TestMethod]
    public async Task CloneCommitSyncAndHistory()
    {
        var world = await NewWorld(("readme.txt", "start\n"));
        try
        {
            Assert.AreEqual("start\n", world.Read("readme.txt"), "Clone brings the server's files");

            world.Write("a.txt", "alpha\n");
            world.Write(@"art\tex.png", "texture\0one");
            await world.Run(CompanionOperation.Status);
            var changed = (await world.Status()).Select(file => file.Path).Order().ToList();
            CollectionAssert.AreEqual(new[] { "a.txt", @"art\tex.png" }, changed, "Status sees new files, in folders too");

            await world.Run(CompanionOperation.Commit, "first");
            Assert.AreEqual(0, (await world.Status()).Count, "nothing is left changed after a commit");
            Same(Files(("readme.txt", "start\n"), ("a.txt", "alpha\n"), (@"art\tex.png", "texture\0one")), await world.OnServer(),
                "Commit sends the revision to the server, not only to this working copy");

            await world.Other.Push(world.Theirs, "from the other person", ("b.txt", "bravo\n"));
            await world.Run(CompanionOperation.Sync);
            Assert.AreEqual("bravo\n", world.Read("b.txt"), "Sync brings what others pushed");

            await world.Run(CompanionOperation.History);
            Assert.IsTrue(world.Log.Count(line => line.StartsWith("Revision ")) >= 3, "History lists the revisions");
        }
        finally { Show(world); }
    }

    [TestMethod]
    public async Task CommitOfAFolderFindsEditsAndDeletesWithoutAStatusFirst()
    {
        var world = await NewWorld(("a.txt", "a1\n"), ("b.txt", "b1\n"), (@"art\c.txt", "c1\n"));
        try
        {
            world.Write("a.txt", "a2\n");
            File.Delete(world.P("b.txt"));
            world.Write(@"art\new.txt", "new\n");
            await world.Run(CompanionOperation.Commit, "folder commit");
            Same(Files(("a.txt", "a2\n"), (@"art\c.txt", "c1\n"), (@"art\new.txt", "new\n")), await world.OnServer(),
                "a folder commit takes edits, deletions and new files");
        }
        finally { Show(world); }
    }

    [TestMethod]
    public async Task CommitOfOneFileLeavesTheOtherChangesAlone()
    {
        var world = await NewWorld(("a.txt", "a1\n"), ("b.txt", "b1\n"));
        try
        {
            world.Write("a.txt", "a2\n");
            world.Write("b.txt", "b2 not ready\n");
            await world.Run(CompanionOperation.Commit, "only a", "a.txt");
            Same(Files(("a.txt", "a2\n"), ("b.txt", "b1\n")), await world.OnServer(), "only the selected file is committed");
            Assert.AreEqual("b2 not ready\n", world.Read("b.txt"), "the other change stays on disk");
            CollectionAssert.AreEqual(new[] { "b.txt" }, (await world.Status()).Select(file => file.Path).ToList(), "and is still reported as changed");
        }
        finally { Show(world); }
    }

    [TestMethod]
    public async Task CommitWithNothingChangedSaysSo()
    {
        var world = await NewWorld();
        try
        {
            var refusal = await world.Refused(CompanionOperation.Commit, "", "readme.txt");
            StringAssert.Contains(refusal.Message, "nothing to commit");
        }
        finally { Show(world); }
    }

    [TestMethod]
    public async Task CommitAfterSomeoneChangedOtherFilesIsMergedByTheServer()
    {
        var world = await NewWorld(("a.txt", "a1\n"), ("b.txt", "b1\n"));
        try
        {
            await world.Other.Push(world.Theirs, "other edits b", ("b.txt", "b2\n"));
            world.Write("a.txt", "a2\n");
            await world.Run(CompanionOperation.Commit, "edits a", "a.txt");
            Same(Files(("a.txt", "a2\n"), ("b.txt", "b2\n")), await world.OnServer(), "both changes are on the server");
            await world.Run(CompanionOperation.Sync);
            Assert.AreEqual("b2\n", world.Read("b.txt"));
            Assert.AreEqual("a2\n", world.Read("a.txt"));
        }
        finally { Show(world); }
    }

    // ---- Stage, Unstage, Sync with staged or locally changed files ---------------------------------

    [TestMethod]
    public async Task StageThenUnstageKeepsTheChange()
    {
        var world = await NewWorld(("a.txt", "a1\n"));
        try
        {
            world.Write("a.txt", "a2\n");
            await world.Run(CompanionOperation.Stage, "", "a.txt");
            Assert.IsTrue((await world.Status()).Single().Staged, "Stage marks the file");
            await world.Run(CompanionOperation.Unstage, "", "a.txt");
            var state = (await world.Status()).Single();
            Assert.IsFalse(state.Staged, "Unstage removes the mark");
            Assert.AreEqual("a2\n", world.Read("a.txt"), "and keeps the change");
        }
        finally { Show(world); }
    }

    // Staged a.txt, someone else changed b.txt: Sync asks before doing anything.
    private async Task<World> StagedWhileOthersPushed()
    {
        var world = await NewWorld(("a.txt", "a1\n"), ("b.txt", "b1\n"));
        world.Write("a.txt", "a2 mine\n");
        await world.Run(CompanionOperation.Stage, "", "a.txt");
        await world.Other.Push(world.Theirs, "other edits b", ("b.txt", "b2\n"));
        var question = await world.Asked(() => world.Run(CompanionOperation.Sync), "staged");
        CollectionAssert.AreEqual(new[] { "a.txt" }, question.Files.ToList(), "the question lists the staged files");
        // Cancel = the window closes; nothing may have changed.
        Assert.AreEqual("a2 mine\n", world.Read("a.txt"));
        Assert.IsTrue((await world.Status()).Single(file => file.Path == "a.txt").Staged, "still staged");
        Assert.AreEqual("b1\n", world.Read("b.txt"), "nothing was downloaded before the answer");
        return world;
    }

    [TestMethod]
    public async Task SyncWithStagedFilesAsksAndSyncWithoutCommittingKeepsTheChanges()
    {
        var world = await StagedWhileOthersPushed();
        try
        {
            await world.Answer(CompanionOperation.Sync, "unstage", "a.txt");
            Assert.AreEqual("b2\n", world.Read("b.txt"), "the other person's change arrived");
            Assert.AreEqual("a2 mine\n", world.Read("a.txt"), "the change is still in the file");
            Assert.IsFalse((await world.Status()).Single(file => file.Path == "a.txt").Staged, "only the mark is gone");
            Same(Files(("a.txt", "a1\n"), ("b.txt", "b2\n")), await world.OnServer(), "nothing of mine was sent");
        }
        finally { Show(world); }
    }

    [TestMethod]
    public async Task SyncWithStagedFilesAsksAndCommitAndSyncSendsThem()
    {
        var world = await StagedWhileOthersPushed();
        try
        {
            await world.Answer(CompanionOperation.Sync, "commit", "a.txt");
            Assert.AreEqual("b2\n", world.Read("b.txt"));
            Assert.AreEqual(0, (await world.Status()).Count, "nothing left staged or changed");
            Same(Files(("a.txt", "a2 mine\n"), ("b.txt", "b2\n")), await world.OnServer(), "my commit reached the server");
        }
        finally { Show(world); }
    }

    // I changed a.txt, someone else changed a.txt and b.txt: Lore refuses the whole sync, the
    // Companion asks.
    private async Task<World> ChangedHereAndOnTheServer(string start = "a1\n", string mine = "a2 mine\n", string theirs = "a2 theirs\n")
    {
        var world = await NewWorld(("a.txt", start), ("b.txt", "b1\n"));
        world.Write("a.txt", mine);
        await world.Other.Push(world.Theirs, "other edits a and b", ("a.txt", theirs), ("b.txt", "b2\n"));
        var question = await world.Asked(() => world.Run(CompanionOperation.Sync), "both");
        CollectionAssert.AreEqual(new[] { "a.txt" }, question.Files.ToList(), "the question names the file");
        Assert.IsFalse(question.Merged);
        Assert.AreEqual(mine, world.Read("a.txt"), "my change isn't touched");
        Assert.AreEqual("b1\n", world.Read("b.txt"), "nothing was downloaded");
        return world;
    }

    [TestMethod]
    public async Task ChangedHereAndOnTheServerTakeTheServersVersion()
    {
        var world = await ChangedHereAndOnTheServer();
        try
        {
            await world.Answer(CompanionOperation.Sync, "theirs", "a.txt");
            Assert.AreEqual("a2 theirs\n", world.Read("a.txt"));
            Assert.AreEqual("b2\n", world.Read("b.txt"));
            Assert.AreEqual(0, (await world.Status()).Count);
        }
        finally { Show(world); }
    }

    [TestMethod]
    public async Task ChangedHereAndOnTheServerKeepMyVersion()
    {
        var world = await ChangedHereAndOnTheServer();
        try
        {
            await world.Answer(CompanionOperation.Sync, "mine", "a.txt");
            Assert.AreEqual("a2 mine\n", world.Read("a.txt"));
            Assert.AreEqual("b2\n", world.Read("b.txt"), "the other person's other change is kept too");
            Assert.AreEqual(0, (await world.Status()).Count);
            Same(Files(("a.txt", "a2 mine\n"), ("b.txt", "b2\n")), await world.OnServer(), "my version is on the server");
        }
        finally { Show(world); }
    }

    [TestMethod]
    public async Task ChangedHereAndOnTheServerKeepMyVersionMergesChangesToDifferentLines()
    {
        var world = await ChangedHereAndOnTheServer("1\n2\n3\n4\n5\n", "1 mine\n2\n3\n4\n5\n", "1\n2\n3\n4\n5 theirs\n");
        try
        {
            await world.Answer(CompanionOperation.Sync, "mine", "a.txt");
            Assert.AreEqual("1 mine\n2\n3\n4\n5 theirs\n", world.Read("a.txt"), "both changes are kept when Lore can merge them");
            Same(Files(("a.txt", "1 mine\n2\n3\n4\n5 theirs\n"), ("b.txt", "b2\n")), await world.OnServer(), "and sent");
        }
        finally { Show(world); }
    }

    [TestMethod]
    public async Task AChoiceAppliesOnlyToTheFilesTheUserSaw()
    {
        var world = await ChangedHereAndOnTheServer();
        try
        {
            // An answer given for other files (the list changed since the window asked) asks again.
            var again = await world.Asked(() => world.Answer(CompanionOperation.Sync, "theirs", "b.txt"), "both");
            CollectionAssert.AreEqual(new[] { "a.txt" }, again.Files.ToList());
            Assert.AreEqual("a2 mine\n", world.Read("a.txt"), "my change is still there");
            Assert.AreEqual("b1\n", world.Read("b.txt"));
        }
        finally { Show(world); }
    }

    // ---- Revert --------------------------------------------------------------------------------------

    [TestMethod]
    public async Task RevertRestoresEditedStagedAndDeletedFiles()
    {
        var world = await NewWorld(("a.txt", "a1\n"), ("b.txt", "b1\n"), ("c.txt", "c1\n"), ("keep.txt", "k1\n"));
        try
        {
            world.Write("a.txt", "a2\n");
            world.Write("b.txt", "b2\n");
            await world.Run(CompanionOperation.Stage, "", "b.txt");
            File.Delete(world.P("c.txt"));
            world.Write("keep.txt", "k2 keep\n");

            var outcome = await world.Run(CompanionOperation.Revert, "", "a.txt", "b.txt", "c.txt");
            StringAssert.Contains(outcome.Summary, "3 files");
            Assert.AreEqual("a1\n", world.Read("a.txt"));
            Assert.AreEqual("b1\n", world.Read("b.txt"), "a staged file is reverted too");
            Assert.AreEqual("c1\n", world.Read("c.txt"), "a deleted file comes back");
            Assert.AreEqual("k2 keep\n", world.Read("keep.txt"), "files not selected are untouched");
            CollectionAssert.AreEqual(new[] { "keep.txt" }, (await world.Status()).Select(file => file.Path).ToList());
        }
        finally { Show(world); }
    }

    [TestMethod]
    public async Task RevertOfANewFile()
    {
        var world = await NewWorld(("a.txt", "a1\n"));
        try
        {
            world.Write("new.txt", "brand new\n");
            var outcome = await world.Run(CompanionOperation.Revert, "", "new.txt");
            // A file the server never had has no synced version to go back to: Lore leaves it in
            // place, and the result says so, so nobody thinks it was deleted (or that it's gone).
            Assert.AreEqual("brand new\n", world.Read("new.txt"), "a new file stays");
            StringAssert.Contains(outcome.Summary, "new.txt");
            StringAssert.Contains(outcome.Summary, "left");
            Assert.IsTrue(outcome.Warning);
            Assert.IsTrue((await world.Status()).Single().Added, "still reported as new");
        }
        finally { Show(world); }
    }

    // ---- Locks ---------------------------------------------------------------------------------------

    [TestMethod]
    public async Task LockBlocksTheOtherPersonUntilReleased()
    {
        var world = await NewWorld(("a.txt", "a1\n"));
        try
        {
            await world.Run(CompanionOperation.Lock, "", "a.txt");
            StringAssert.Contains(await world.Locks(), "a.txt", "the server holds the lock");
            var (exit, _) = await world.Other.Lore(world.Theirs, "lock", "acquire", "a.txt");
            Assert.AreNotEqual(0, exit, "the other person can't take a locked file");

            await world.Run(CompanionOperation.Unlock, "", "a.txt");
            Assert.IsFalse((await world.Locks()).Contains("a.txt"), "released");
            await world.Other.Must(world.Theirs, "lock", "acquire", "a.txt");

            var refusal = await world.Refused(CompanionOperation.Lock, "", "a.txt");
            StringAssert.Contains(refusal.Message, "a.txt");
            StringAssert.Contains(refusal.Message, "locked by");
            await world.Other.Must(world.Theirs, "lock", "release", "a.txt");
        }
        finally { Show(world); }
    }

    [TestMethod]
    public async Task CommitIsRefusedBeforeAnythingIsRecordedWhenSomeoneElseHoldsTheLock()
    {
        var world = await NewWorld(("a.txt", "a1\n"), ("b.txt", "b1\n"));
        try
        {
            await world.Other.Must(world.Theirs, "lock", "acquire", "a.txt");
            world.Write("a.txt", "a2 mine\n");
            var refusal = await world.Refused(CompanionOperation.Commit, "", "a.txt");
            StringAssert.Contains(refusal.Message, "a.txt");
            StringAssert.Contains(refusal.Message, "locked by");
            Same(Files(("a.txt", "a1\n"), ("b.txt", "b1\n")), await world.OnServer(), "nothing reached the server");
            var state = (await world.Status()).Single();
            Assert.AreEqual("a2 mine\n", world.Read("a.txt"), "my change stays");
            Assert.IsFalse(state.Staged, "and isn't left staged");

            // Unlocked files can still be committed meanwhile.
            world.Write("b.txt", "b2\n");
            await world.Run(CompanionOperation.Commit, "", "b.txt");
            Assert.AreEqual("b2\n", (await world.OnServer())["b.txt"]);
            await world.Other.Must(world.Theirs, "lock", "release", "a.txt");
        }
        finally { Show(world); }
    }

    [TestMethod]
    public async Task TheServerRefusesAPushThatChangesSomeoneElsesLockedFile()
    {
        var world = await NewWorld(("a.txt", "a1\n"), ("b.txt", "b1\n"));
        try
        {
            await world.Run(CompanionOperation.Lock, "", "a.txt");
            // The other person ignores the lock (lore.exe doesn't check locks before committing).
            await File.WriteAllTextAsync(Path.Combine(world.Theirs, "a.txt"), "a2 theirs\n");
            await world.Other.Must(world.Theirs, "stage", "a.txt");
            await world.Other.Must(world.Theirs, "commit", "ignores the lock");
            var (exit, output) = await world.Other.Lore(world.Theirs, "push");
            Assert.AreNotEqual(0, exit, "the server refuses the push");
            StringAssert.Contains(output, "Locked by another user");
            StringAssert.Contains(output, "a.txt");

            // A push that doesn't touch the locked file still goes through.
            var fresh = world.Theirs + "-2";
            await world.Other.Must(Path.GetDirectoryName(fresh)!, "clone", world.Url, fresh);
            await world.Other.Push(fresh, "edits b", ("b.txt", "b2\n"));
            await world.Run(CompanionOperation.Sync);
            Assert.AreEqual("a1\n", world.Read("a.txt"), "the locked file is unchanged on the server");
            Assert.AreEqual("b2\n", world.Read("b.txt"));
            await world.Run(CompanionOperation.Unlock, "", "a.txt");
        }
        finally { Show(world); }
    }

    [TestMethod]
    public async Task LocksSurviveAServerRestart()
    {
        var world = await NewWorld(("a.txt", "a1\n"));
        try
        {
            if (world.Server.RestartCommand is null) Assert.Inconclusive("Set LORE_LIVE_RESTART_COMMAND to check locks across a restart");
            await world.Run(CompanionOperation.Lock, "", "a.txt");
            world.Server.Restart();
            for (var attempt = 0; attempt < 30; attempt++)
            {
                var (exit, output) = await world.Other.Lore(world.Theirs, "lock", "query");
                if (exit == 0)
                {
                    StringAssert.Contains(output, "a.txt", "the lock outlived the restart");
                    break;
                }
                await Task.Delay(2000);
            }
            var (refused, _) = await world.Other.Lore(world.Theirs, "lock", "acquire", "a.txt");
            Assert.AreNotEqual(0, refused, "and is still enforced");
            await world.Run(CompanionOperation.Unlock, "", "a.txt");
        }
        finally { Show(world); }
    }

    // ---- Conflicts -----------------------------------------------------------------------------------

    // Both change the same file without a lock: the server can't take my commit as it is, so
    // Commit merges with the server's and, where Lore can't merge, asks which version to keep (the
    // revision stays saved here meanwhile).
    private async Task<World> Conflict(string file, string start, string mine, string theirs)
    {
        var world = await NewWorld((file, start), ("other.txt", "o1\n"));
        await world.Other.Push(world.Theirs, "theirs", (file, theirs));
        world.Write(file, mine);
        var question = await world.Asked(() => world.Run(CompanionOperation.Commit, "mine", file), "both");
        Assert.IsTrue(question.Merged, "Lore merged and left a conflict");
        CollectionAssert.AreEqual(new[] { file }, question.Files.ToList());
        Assert.IsTrue((await world.Status()).Single(state => state.Path == file).Conflicted);
        return world;
    }

    [TestMethod]
    public async Task ConflictOnCommitKeepMyVersion()
    {
        var world = await Conflict("a.txt", "base\n", "mine\n", "theirs\n");
        try
        {
            await world.Answer(CompanionOperation.Commit, "mine", "a.txt");
            Assert.AreEqual("mine\n", world.Read("a.txt"));
            Assert.AreEqual(0, (await world.Status()).Count, "the merge is committed");
            Same(Files(("a.txt", "mine\n"), ("other.txt", "o1\n")), await world.OnServer(), "my version is on the server");
        }
        finally { Show(world); }
    }

    [TestMethod]
    public async Task BinaryConflictKeepsAUsableFileAndTakesTheServersVersion()
    {
        var world = await Conflict("tex.png", "png\0base", "png\0mine", "png\0theirs");
        try
        {
            Assert.AreEqual("png\0mine", world.Read("tex.png"), "no conflict markers inside a binary file: it holds my version until resolved");
            await world.Answer(CompanionOperation.Commit, "theirs", "tex.png");
            Assert.AreEqual("png\0theirs", world.Read("tex.png"));
            Same(Files(("tex.png", "png\0theirs"), ("other.txt", "o1\n")), await world.OnServer(), "the server keeps its version");
            Assert.AreEqual(0, (await world.Status()).Count);
        }
        finally { Show(world); }
    }

    [TestMethod]
    public async Task ConflictLeftForLaterIsAskedAgainAndResolvedFromTheMenu()
    {
        var world = await Conflict("a.txt", "base\n", "mine\n", "theirs\n");
        try
        {
            // Cancel: the conflict stays; Sync and Commit ask again instead of going on.
            var again = await world.Asked(() => world.Run(CompanionOperation.Sync), "both");
            Assert.IsTrue(again.Merged);
            await world.Asked(() => world.Run(CompanionOperation.Commit, "", "a.txt"), "both");
            await world.Run(CompanionOperation.Resolve, "mine", "a.txt");
            Assert.AreEqual("mine\n", world.Read("a.txt"));
            Same(Files(("a.txt", "mine\n"), ("other.txt", "o1\n")), await world.OnServer(), "the merge was sent");
        }
        finally { Show(world); }
    }

    [TestMethod]
    public async Task ResolveWithoutConflictsSaysSo()
    {
        var world = await NewWorld();
        try
        {
            var outcome = await world.Run(CompanionOperation.Resolve, "mine", "readme.txt");
            StringAssert.Contains(outcome.Summary, "no conflicts");
        }
        finally { Show(world); }
    }
}
