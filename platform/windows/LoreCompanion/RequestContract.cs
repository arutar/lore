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
        if (Operation != CompanionOperation.Clone && Paths.Length == 0)
        {
            throw new InvalidDataException("Select a repository path");
        }
        if (Operation == CompanionOperation.Clone && (forExecution && string.IsNullOrWhiteSpace(TargetPath) || !string.IsNullOrWhiteSpace(TargetPath) && !Path.IsPathFullyQualified(TargetPath)))
        {
            throw new InvalidDataException("Clone target must be an absolute path");
        }
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
