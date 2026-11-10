namespace LoreCompanion;

public static class OperationError
{
    public static string UserMessage(Exception exception, CompanionOperation operation)
    {
        if (exception is OperationCanceledException) return "The operation was canceled.";
        if (exception is InvalidDataException or ArgumentException) return exception.Message;
        if (exception is UnauthorizedAccessException) return "Windows could not access the selected path. Check the folder permissions.";

        var message = exception.Message;
        if (Contains(message, "protocol", "was not recognized"))
            return "This repository URL uses an unsupported protocol. Check the server URL and the Lore client configuration.";
        if (Contains(message, "certificate") || Contains(message, "tls") || Contains(message, "ssl"))
            return "The server certificate could not be validated. Check the HTTPS certificate or your trusted CA configuration.";
        if (Contains(message, "401") || Contains(message, "unauthorized") || Contains(message, "authentication") || Contains(message, "credentials"))
            return "Authentication failed. Sign in again and check your credentials.";
        if (Contains(message, "403") || Contains(message, "forbidden") || Contains(message, "permission denied"))
            return "You do not have permission to perform this operation. Ask a repository administrator for access.";
        if (Contains(message, "conflict"))
            return "The operation conflicts with local or incoming changes. Review the working copy and resolve the conflicts before retrying.";
        if (Contains(message, "already locked") || Contains(message, "lock is held"))
            return "One or more files are locked by another user. Release the locks or choose different files.";
        if (Contains(message, "no space") || Contains(message, "disk full"))
            return "There is not enough free disk space. Free some space and try again.";
        if (Contains(message, "failed to resolve repository") || Contains(message, "failed to connect")
            || Contains(message, "connection refused") || Contains(message, "timed out") || Contains(message, "could not resolve host"))
            return "Could not connect to the Lore server. Check the repository URL, network connection, and server availability.";
        if (Contains(message, "not inside a lore repository"))
            return "The selected path is not inside a Lore repository.";
        if (Contains(message, "not found"))
            return "The repository or selected path was not found. Check the URL and path.";

        return operation switch
        {
            CompanionOperation.Clone => "The repository could not be cloned. Check the URL, target folder, and server access.",
            CompanionOperation.Sync => "The repository could not be synchronized. Review the working copy and server connection.",
            CompanionOperation.Commit => "The revision could not be created. Review the selected changes and try again.",
            CompanionOperation.Stage => "The selected paths could not be staged. Check that they belong to this repository.",
            CompanionOperation.Status => "The working copy status could not be read. Check the repository metadata.",
            CompanionOperation.History => "The revision history could not be loaded. Check the repository and server connection.",
            CompanionOperation.Lock => "The locks could not be acquired. Check the selected paths and existing locks.",
            CompanionOperation.Unlock => "The locks could not be released. Check the selected paths and lock ownership.",
            _ => "The operation could not be completed. Check the repository and try again.",
        };
    }

    private static bool Contains(string message, params string[] parts) =>
        parts.All(part => message.Contains(part, StringComparison.OrdinalIgnoreCase));
}
