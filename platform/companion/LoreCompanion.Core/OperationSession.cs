using System.Text.Json;

namespace LoreCompanion;

/// <summary>What an operation window does for its <see cref="OperationSession"/>: the window, the web
/// view inside it, and the dialogs the system provides.</summary>
public interface IOperationHost
{
    /// <summary>Hands a JSON message to the page. Called from any thread.</summary>
    void Post(string json);

    /// <summary>Whether the window is still on screen.</summary>
    bool IsOpen { get; }

    void Close();

    /// <summary>Asks the user for a folder, starting at <paramref name="initial"/>; null when they cancel.</summary>
    Task<string?> ChooseFolder(string initial);

    /// <summary>Opens the Accounts window for <paramref name="request"/> over this one, or brings it
    /// to the front when it is open already.</summary>
    void OpenAccounts(OperationRequest request);

    /// <summary>The page's content is <paramref name="height"/> pixels tall.</summary>
    void FitHeight(double height);

    /// <summary>The user picked <paramref name="theme"/>; the window's frame follows it.</summary>
    void ThemeChanged(string theme);

    /// <summary>Shows an identity provider's sign-in page.</summary>
    Task<IDisposable> ShowSignInPage(Uri url, string code, Action closed);
}

/// <summary>Runs one operation window's conversation with the page in
/// <c>platform/windows/LoreCompanion.Web</c>: it answers the page's messages, runs the operation and
/// reports its progress, the same way on every system.</summary>
public sealed class OperationSession
{
    private readonly OperationRequest request;
    private readonly IOperationHost host;
    private readonly ClientSettings settings = ClientSettings.Load();
    private bool running;
    private CancellationTokenSource? signInCancellation;
    private CommitList? changes;
    // The files a ChoiceRequired question is about; the page shows the first few and answers by
    // choice alone.
    private string[] pendingChoice = [];
    private const int FileListShown = 200;

    public OperationSession(OperationRequest request, IOperationHost host)
    {
        this.request = request;
        this.host = host;
        if (request.Operation == CompanionOperation.Clone && string.IsNullOrWhiteSpace(request.TargetPath) && request.Paths.Length > 0)
        {
            var directory = Directory.Exists(request.Paths[0]) ? request.Paths[0] : Path.GetDirectoryName(request.Paths[0]);
            if (!string.IsNullOrEmpty(directory)) request.TargetPath = directory;
        }
    }

    public OperationRequest Request => request;

    public string Theme => settings.Theme;

    public string Title => $"Lore {request.Operation}";

    /// <summary>Wide where paths, URLs or revision IDs are shown; narrower for short confirmations.</summary>
    public double PreferredWidth => request.Operation is CompanionOperation.Clone or CompanionOperation.Commit or CompanionOperation.History ? 620 : 560;

    /// <summary>The window may close: no operation is running, or only a sign-in, which closing cancels.</summary>
    public bool TryClose()
    {
        signInCancellation?.Cancel();
        return !running || signInCancellation is not null;
    }

    // Keeps a trace of what the page reported: a script error would otherwise just leave a blank
    // dialog with nothing to go on.
    public static void Log(string text)
    {
        try
        {
            var folder = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData), "Lore");
            Directory.CreateDirectory(folder);
            File.AppendAllText(Path.Combine(folder, "companion.log"), $"{DateTime.Now:HH:mm:ss} {text}\n");
        }
        catch (Exception error) when (error is IOException or UnauthorizedAccessException)
        {
            // Logging must never break the dialog.
        }
    }

    public async Task Receive(string json)
    {
        try
        {
            using var message = JsonDocument.Parse(json);
            var root = message.RootElement;
            switch (root.GetProperty("type").GetString())
            {
                case "ready":
                    Send(new
                    {
                        type = "init",
                        operation = (int)request.Operation,
                        repositoryUrl = request.Operation == CompanionOperation.Accounts ? request.ServerUrl() : request.RepositoryUrl,
                        repositoryPath = request.Operation is CompanionOperation.Clone or CompanionOperation.Accounts ? "" : request.RepositoryRoot(),
                        targetPath = request.TargetPath,
                        message = request.Message,
                        paths = request.Paths,
                        theme = settings.Theme,
                    });
                    if (request.Operation == CompanionOperation.Accounts) await SendAccounts();
                    if (request.Operation == CompanionOperation.Commit) await SendCommitCandidates();
                    break;
                case "browse":
                    if (request.Operation != CompanionOperation.Clone) break;
                    if (await host.ChooseFolder(request.TargetPath) is { } folder)
                    {
                        request.TargetPath = folder;
                        Send(new { type = "directory", path = folder });
                    }
                    break;
                case "accounts-open":
                    if (running) break;
                    host.OpenAccounts(new OperationRequest
                    {
                        Operation = CompanionOperation.Accounts,
                        RepositoryUrl = request.ServerUrl(),
                        TargetPath = request.TargetPath,
                        Paths = request.Paths,
                    });
                    break;
                case "accounts-add":
                    if (running) break;
                    running = true;
                    var signIn = await LoreAccounts.Add(root.GetProperty("remoteUrl").GetString() ?? "",
                        text => Send(new { type = "progress", text }), SignIn);
                    running = false;
                    await SendAccounts();
                    Send(new { type = "complete", text = LoreAccounts.SignInMessage(signIn) });
                    break;
                case "accounts-select":
                    if (running) break;
                    if (string.IsNullOrEmpty(request.ServerKey())) throw new InvalidDataException("The Lore server is unknown");
                    running = true;
                    settings.SelectAccount(request.ServerKey(), root.GetProperty("userId").GetString() ?? "");
                    running = false;
                    await SendAccounts();
                    Send(new { type = "complete", text = "The account will be used for this server." });
                    break;
                case "accounts-remove":
                    if (running) break;
                    running = true;
                    var ssoCleared = await LoreAccounts.Remove(root.GetProperty("authUrl").GetString() ?? "", root.GetProperty("userId").GetString() ?? "", request.ServerUrl());
                    running = false;
                    await SendAccounts();
                    Send(new
                    {
                        type = "complete",
                        text = ssoCleared
                            ? "Signed out. The saved sign-in was deleted from this computer and the server session ended."
                            : "The saved sign-in was deleted from this computer. The server couldn't be reached to end its session; it expires on its own.",
                    });
                    break;
                case "size":
                    host.FitHeight(root.GetProperty("height").GetDouble());
                    break;
                case "log":
                    Log(root.TryGetProperty("text", out var logged) ? logged.GetString() ?? "" : "");
                    break;
                case "theme-set":
                    settings.SetTheme(root.GetProperty("theme").GetString() ?? "");
                    host.ThemeChanged(settings.Theme);
                    Send(new { type = "theme", theme = settings.Theme });
                    break;
                case "cancel":
                    host.Close();
                    break;
                case "changes-page":
                    if (changes is null) break;
                    var pageFolder = root.TryGetProperty("folder", out var pageFolderValue) ? pageFolderValue.GetString() ?? "" : "";
                    var pageFilter = root.TryGetProperty("filter", out var pageFilterValue) ? pageFilterValue.GetString() ?? "" : "";
                    var pageOffset = root.TryGetProperty("offset", out var pageOffsetValue) && pageOffsetValue.TryGetInt32(out var from) ? from : 0;
                    var pageLimit = root.TryGetProperty("limit", out var pageLimitValue) && pageLimitValue.TryGetInt32(out var size) ? size : CommitList.PageSize;
                    // A filter searches the whole list; without one the page is a folder's own files.
                    var searching = root.TryGetProperty("search", out var search) && search.ValueKind == JsonValueKind.True;
                    SendPage(searching ? changes.Search(pageFilter, pageOffset, pageLimit) : changes.Page(pageFolder, pageFilter, pageOffset, pageLimit));
                    break;
                case "changes-select":
                    if (changes is null) break;
                    var on = root.TryGetProperty("on", out var wanted) && wanted.ValueKind == JsonValueKind.True;
                    var path = root.TryGetProperty("path", out var target) ? target.GetString() ?? "" : "";
                    switch (root.TryGetProperty("scope", out var scope) ? scope.GetString() : "")
                    {
                        case "all": changes.SelectAll(on); break;
                        case "folder": changes.SelectFolder(path, on); break;
                        default: changes.SelectFile(path, on); break;
                    }
                    SendSelection();
                    break;
                case "run":
                    if (!running)
                    {
                        var choice = root.TryGetProperty("choice", out var chosen) ? chosen.GetString() ?? "" : "";
                        var files = root.TryGetProperty("files", out var listed) && listed.ValueKind == JsonValueKind.Array
                            ? listed.EnumerateArray().Select(file => file.GetString() ?? "").ToArray() : [];
                        // The Commit window keeps the selection on this side: a large one never fits a
                        // page message, so the operation takes it from the model.
                        var all = false;
                        string[]? excluded = null;
                        if (choice.Length == 0 && request.Operation == CompanionOperation.Commit && changes is not null)
                        {
                            // Naming the ticked files takes a message per hundred thousand; when only a
                            // few are left out, staging the paths and taking those out is cheaper.
                            if (changes.Total > 0 && changes.Selected == 0)
                                throw new InvalidOperationException("Select at least one file to commit.");
                            if (changes.MostlySelected)
                            {
                                files = [];
                                all = true;
                                excluded = changes.ExcludedFiles();
                            }
                            else
                            {
                                files = changes.ResultFiles() ?? [];
                                all = changes.All;
                            }
                        }
                        await Run(root.GetProperty("repositoryUrl").GetString(), root.GetProperty("targetPath").GetString(), root.GetProperty("message").GetString(), choice, files, all, excluded);
                    }
                    break;
            }
        }
        catch (OperationCanceledException)
        {
            running = false;
            if (host.IsOpen) Send(new { type = "error", text = "Sign-in was canceled." });
        }
        catch (Exception exception)
        {
            running = false;
            Log($"message failed: {exception}");
            Send(new { type = "error", text = OperationError.UserMessage(exception, request.Operation), accessDenied = OperationError.IsAccessDenied(exception, request.Operation) });
        }
    }

    // The files the commit would record, folded into folders. The page gets the folder tree and the
    // files themselves for a small list; a large one gets pages as folders open, so the window
    // never waits for hundreds of thousands of rows to travel to it. A failure leaves the window
    // able to commit the selection as it is, and says why the list is missing.
    private async Task SendCommitCandidates()
    {
        try
        {
            var candidates = await LoreOperations.CommitCandidates(request);
            var list = new CommitList(candidates);
            changes = list;
            var whole = list.Total <= CommitList.WholeListFrom;
            Send(new
            {
                type = "changes",
                total = list.Total,
                selected = list.Selected,
                eligible = list.Eligible,
                whole,
                folders = list.Folders(),
                items = whole ? list.AllFiles().Select(Item) : null,
                locksPending = list.Total > 0,
            });
            if (list.Total > 0) _ = SendLocks(list);
        }
        catch (Exception exception)
        {
            changes = null;
            Send(new { type = "changes", items = Array.Empty<object>(), error = OperationError.UserMessage(exception, request.Operation) });
        }
    }

    // Locks come from the server, which may be slow or unreachable: the list is already on screen,
    // and they mark it when they arrive. Nothing waits on them; a commit of a locked file is refused
    // by the server either way.
    private async Task SendLocks(CommitList list)
    {
        string error = "";
        var owners = new Dictionary<string, string>(PathText.Comparer);
        try
        {
            foreach (var held in await LoreOperations.LockedByOthers(request, list.AllFiles().Select(file => file.Path).ToList()))
            {
                owners[held.Path] = held.Owner;
            }
        }
        catch (Exception exception) when (exception is LoreVcs.LoreError or CompanionException)
        {
            error = OperationError.UserMessage(exception, request.Operation);
        }
        // The user may have started the commit or closed the window meanwhile.
        if (!ReferenceEquals(changes, list) || running) return;
        list.ApplyLocks(owners);
        Send(new
        {
            type = "locks",
            error,
            total = list.Total,
            selected = list.Selected,
            eligible = list.Eligible,
            folders = list.Folders(),
            items = list.Total <= CommitList.WholeListFrom ? list.AllFiles().Select(Item) : null,
        });
    }

    private object Item(CommitCandidate file) =>
        new { path = file.Path, change = file.Change, staged = file.Staged, lockedBy = file.LockedBy, selected = changes?.IsSelected(file.Path) ?? true };

    private void SendPage(CommitPage page) => Send(new
    {
        type = "changes-page",
        folder = page.Folder,
        filter = page.Filter,
        offset = page.Offset,
        total = page.Total,
        selected = page.Selected,
        items = page.Items.Select(Item),
    });

    private void SendSelection() => Send(new
    {
        type = "selection",
        total = changes!.Total,
        selected = changes.Selected,
        eligible = changes.Eligible,
        folders = changes.Folders(),
    });

    private async Task SendAccounts()
    {
        var server = request.ServerKey();
        var selected = ClientSettings.Load().SelectedAccount(server);
        if (string.IsNullOrEmpty(selected)) selected = request.CurrentIdentity();
        var accounts = await LoreAccounts.List();
        if (string.IsNullOrEmpty(selected))
        {
            var matches = accounts.Where(account => account.Domains.Split([',', ' '], StringSplitOptions.RemoveEmptyEntries).Contains(server, StringComparer.OrdinalIgnoreCase)).Take(2).ToArray();
            if (matches.Length == 1) selected = matches[0].UserId;
        }
        Send(new
        {
            type = "accounts",
            items = accounts.Select(account => new
            {
                authUrl = account.AuthUrl,
                userId = account.UserId,
                username = account.Username,
                domains = account.Domains,
                selected = account.UserId == selected,
            }),
        });
    }

    /// <summary>Lore reports progress for every file it touches. Pushing one message per file
    /// starves the page's renderer, and dropping the extra lines would lose the revision list the
    /// History window is built from: the lines are batched instead, and the page reads each one.</summary>
    private sealed class Throttled(Action<object> send)
    {
        private readonly System.Text.StringBuilder pending = new();
        private long last;

        public void Report(string text)
        {
            lock (pending)
            {
                pending.Append(text).Append('\n');
                var now = Environment.TickCount64;
                if (now - last < 150) return;
                last = now;
                Flush();
            }
        }

        public void Flush()
        {
            string text;
            lock (pending)
            {
                if (pending.Length == 0) return;
                text = pending.ToString().TrimEnd('\n');
                pending.Clear();
            }
            send(new { type = "progress", text });
        }
    }

    private async Task Run(string? repositoryUrl, string? targetPath, string? message, string choice, string[] files, bool all = false, string[]? excluded = null)
    {
        request.RepositoryUrl = repositoryUrl?.Trim() ?? "";
        request.TargetPath = targetPath?.Trim() ?? "";
        request.Message = message?.Trim() ?? "";
        request.Choice = choice;
        // A question's answer names the files it was asked about; the page does not send them back,
        // because a large working copy would not fit one message.
        if (files.Length == 0 && choice.Length > 0 && pendingChoice.Length > 0) files = pendingChoice;
        pendingChoice = [];
        request.Files = files;
        request.DisplayedPaths = all && changes is not null
            ? changes.AllFiles().Select(file => file.Path).ToHashSet(PathText.Comparer)
            : null;
        request.All = all;
        request.Excluded = excluded ?? [];
        var progress = new Throttled(Send);
        try
        {
            request.Validate();
            running = true;
            var outcome = await LoreOperations.ExecuteAsync(request, progress.Report, CancellationToken.None,
                update => Send(new { type = "commit-progress", update }));
            Send(new { type = "complete", text = outcome.Summary ?? CompletionText(), warning = outcome.Warning });
        }
        catch (ChoiceRequired question)
        {
            // Nothing the question is about was changed; the page shows the files and the ways forward.
            // The list can be a whole working copy, so the page gets the first of them and the count.
            pendingChoice = question.Files.ToArray();
            Send(new
            {
                type = "choice",
                kind = question.Kind,
                files = pendingChoice.Take(FileListShown).ToArray(),
                total = pendingChoice.Length,
                text = question.Message,
                merged = question.Merged,
            });
        }
        catch (Exception exception)
        {
            Send(new { type = "error", text = OperationError.UserMessage(exception, request.Operation), accessDenied = OperationError.IsAccessDenied(exception, request.Operation) });
        }
        finally
        {
            progress.Flush();
            running = false;
        }
    }

    private string CompletionText() => request.Operation switch
    {
        CompanionOperation.Clone => "Repository cloned successfully.",
        CompanionOperation.Sync => "Repository synchronized successfully.",
        CompanionOperation.Commit => "Revision created successfully.",
        CompanionOperation.Stage => "Selected paths staged for the next commit.",
        CompanionOperation.Status => "Working copy scan completed.",
        CompanionOperation.History => "Revision history updated.",
        CompanionOperation.Lock => "Locks acquired for the selected paths.",
        CompanionOperation.Unlock => "Locks released for the selected paths.",
        CompanionOperation.Unstage => "The selected files are no longer staged; their changes are kept.",
        CompanionOperation.Revert => "The selected files were reverted.",
        CompanionOperation.Resolve => "Conflicts resolved.",
        _ => "Operation completed.",
    };

    // camelCase: the page reads the folder rows as { path, name, files, ... }.
    private void Send(object message) => host.Post(JsonSerializer.Serialize(message, SerializerOptions));

    private static readonly JsonSerializerOptions SerializerOptions = new() { PropertyNamingPolicy = JsonNamingPolicy.CamelCase };

    private async Task<string> SignIn(string remote)
    {
        using var cancellation = new CancellationTokenSource();
        signInCancellation = cancellation;
        try { return await DeviceSignIn.Run(remote, host.ShowSignInPage, cancellation.Token); }
        finally { signInCancellation = null; }
    }
}
