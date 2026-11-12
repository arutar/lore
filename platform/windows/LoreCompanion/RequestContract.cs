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
}

public sealed class OperationRequest
{
    public CompanionOperation Operation { get; set; }
    public string[] Paths { get; set; } = [];
    public string RepositoryUrl { get; set; } = "";
    public string TargetPath { get; set; } = "";
    public string Message { get; set; } = "";

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
