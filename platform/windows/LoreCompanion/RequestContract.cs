using System.Text.Json;

namespace LoreCompanion;

public enum CompanionOperation
{
    Clone,
    Sync,
    Commit,
    Stage,
    Status,
    History,
    Lock,
    Unlock,
    Accounts,
    Unstage,
    Revert,
    Resolve,
}

public sealed class OperationRequest
{
    public CompanionOperation Operation { get; set; }
    public string[] Paths { get; set; } = [];
    public string RepositoryUrl { get; set; } = "";
    public string TargetPath { get; set; } = "";
    public string Message { get; set; } = "";
    /// <summary>How to go on after the operation stopped to ask (ChoiceRequired): "commit" or
    /// "unstage" for staged files, "mine" or "theirs" for files changed here and on the server.
    /// Empty on the first run.</summary>
    public string Choice { get; set; } = "";
    /// <summary>The files the user saw when choosing (relative to the repository root). A choice
    /// applies only to these: a file that turned up since is asked about again.</summary>
    public string[] Files { get; set; } = [];
    /// <summary>Everything under <see cref="Paths"/> goes in, so <see cref="Files"/> stays empty:
    /// the Commit window sets it when every change is ticked, which keeps a working copy with
    /// hundreds of thousands of changes out of the request.</summary>
    public bool All { get; set; }
    /// <summary>Files under <see cref="Paths"/> the user left out; with <see cref="All"/> set they
    /// are unstaged after the paths are staged. Naming the few unticked files beats naming every
    /// ticked one when a working copy holds hundreds of thousands of changes.</summary>
    public string[] Excluded { get; set; } = [];

    public static OperationRequest Parse(string json)
    {
        if (json.Length > 1_048_576)
        {
            throw new InvalidDataException("Request is too large");
        }
        var request = JsonSerializer.Deserialize<OperationRequest>(json) ?? throw new InvalidDataException("Request is empty");
        request.Validate(false);
        return request;
    }

    public void Validate(bool forExecution = true)
    {
        if (!Enum.IsDefined(Operation))
        {
            throw new InvalidDataException("Operation isn't supported");
        }
        if (Paths.Length > 256 || Paths.Any(path => string.IsNullOrWhiteSpace(path) || path.Length > 32_768 || !Path.IsPathFullyQualified(path)))
        {
            throw new InvalidDataException("Request contains an invalid path");
        }
        if (Operation != CompanionOperation.Clone && Operation != CompanionOperation.Accounts && Paths.Length == 0)
        {
            throw new InvalidDataException("Select a repository path");
        }
        if (Choice is not ("" or "commit" or "unstage" or "mine" or "theirs"))
        {
            throw new InvalidDataException("Choice isn't supported");
        }
        // A request written by the Explorer integration is capped tightly; a selection the Commit
        // window made can hold every file of a large working copy.
        var maximumFiles = forExecution ? 5_000_000 : 100_000;
        if (Files.Length > maximumFiles || Excluded.Length > maximumFiles || Files.Concat(Excluded).Any(file => string.IsNullOrWhiteSpace(file) || file.Length > 32_768 || Path.IsPathRooted(file) || file.Split('\\', '/').Contains("..")))
        {
            throw new InvalidDataException("Request contains an invalid file");
        }
        if (Operation == CompanionOperation.Resolve && forExecution && Message is not ("mine" or "theirs"))
        {
            throw new InvalidDataException("Choose which version to keep");
        }
        if (Operation == CompanionOperation.Clone && (forExecution && string.IsNullOrWhiteSpace(TargetPath) || !string.IsNullOrWhiteSpace(TargetPath) && !Path.IsPathFullyQualified(TargetPath)))
        {
            throw new InvalidDataException("Clone target must be an absolute path");
        }
        if (Operation == CompanionOperation.Clone && forExecution && Directory.Exists(TargetPath))
        {
            if (Directory.Exists(Path.Combine(TargetPath, ".lore")))
            {
                throw new InvalidDataException("This folder already contains a Lore working copy. Use Lore Sync to update it.");
            }
            if (Directory.EnumerateFileSystemEntries(TargetPath).Any())
            {
                throw new InvalidDataException("The target folder isn't empty. Choose an empty folder for the clone.");
            }
        }
    }

    public string ServerUrl()
    {
        return !string.IsNullOrWhiteSpace(RepositoryUrl) ? RepositoryUrl.Trim() : RepositorySetting("remote_url");
    }

    public string CurrentIdentity() => RepositorySetting("identity");

    private string RepositorySetting(string name)
    {
        if (Paths.Length == 0) return "";
        try
        {
            var config = Path.Combine(RepositoryRoot(), ".lore", "config.toml");
            if (!File.Exists(config)) return "";
            foreach (var line in File.ReadLines(config))
            {
                var value = line.Trim();
                if (value.StartsWith(name, StringComparison.Ordinal))
                {
                    return value.Split('=', 2)[1].Trim().Trim('"');
                }
            }
        }
        catch (Exception error) when (error is IOException or InvalidDataException or UnauthorizedAccessException)
        {
            return "";
        }
        return "";
    }

    public string ServerKey()
    {
        var server = ServerUrl();
        return Uri.TryCreate(server, UriKind.Absolute, out var uri) ? uri.Host.ToLowerInvariant() : server;
    }

    public string RepositoryRoot()
    {
        if (Operation == CompanionOperation.Clone)
        {
            return TargetPath;
        }
        var path = Paths[0];
        var directory = Directory.Exists(path) ? new DirectoryInfo(path) : new FileInfo(path).Directory;
        while (directory is not null)
        {
            if (Directory.Exists(Path.Combine(directory.FullName, ".lore")))
            {
                return directory.FullName;
            }
            directory = directory.Parent;
        }
        throw new InvalidDataException("The selected path isn't inside a Lore repository instance");
    }
}
