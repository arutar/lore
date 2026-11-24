namespace LoreCompanion;

public static class OperationError
{
    public static string UserMessage(Exception exception, CompanionOperation operation)
    {
        if (exception is CompanionException) return exception.Message;
        if (exception is TaskCanceledException) return "The server did not answer in time. Check the connection and try again.";
        if (exception is OperationCanceledException) return "The operation was canceled.";
        if (exception is InvalidDataException or ArgumentException) return exception.Message;
        if (exception is UnauthorizedAccessException) return "Windows could not access the selected path. Check the folder permissions.";

        var message = exception.Message;
        if (Contains(message, "protocol", "was not recognized"))
            return "This repository URL uses an unsupported protocol. Check the server URL and the Lore client configuration.";
        if (Contains(message, "code_challenge") || Contains(message, "pkce"))
            return "The server requires an unsupported sign-in method. Disable PKCE enforcement for the Lore native client or update the Lore client.";
        if (Contains(message, "device authorization"))
            return "The server rejected the Lore sign-in request. Check the native OIDC client configuration.";
        if (Contains(message, "offline tokens not allowed"))
            return "The server could not create a persistent Lore session. Assign the realm's default role, including offline_access, to this account.";
        if (Contains(message, "certificate") || Contains(message, "tls") || Contains(message, "ssl"))
            return "The server certificate could not be validated. Check the HTTPS certificate or your trusted CA configuration.";
        if (Contains(message, "401") || Contains(message, "unauthorized") || Contains(message, "authentication") || Contains(message, "credentials") || Contains(message, "missingtoken") || Contains(message, "missing token") || Contains(message, "not authenticated"))
            return "Authentication failed. Sign in again and check your credentials.";
        if (operation == CompanionOperation.Clone && Contains(message, "repository", "not found"))
            return "This account does not have access to the repository, or the repository does not exist. Sign in with another account or ask a repository administrator for access.";
        if (IsAccessDenied(exception, operation))
            return "This account does not have access to the repository or operation. Sign in with another account or ask a repository administrator for access.";
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
            CompanionOperation.Unstage => "The selected files could not be unstaged. Check that they belong to this repository.",
            CompanionOperation.Revert => "The selected files could not be reverted. Your changes are still in place.",
            CompanionOperation.Resolve => "The conflicts could not be resolved. Check the selected files and try again.",
            _ => "The operation could not be completed. Check the repository and try again.",
        };
    }

    public static bool IsAccessDenied(Exception exception, CompanionOperation operation)
    {
        if (exception is LoreVcs.LoreError { ReturnCode: 16 or 17 }) return true;
        if (operation == CompanionOperation.Clone && Contains(exception.Message, "repository", "not found")) return true;
        var message = exception.Message;
        return Contains(message, "not authenticated") || Contains(message, "missing token") || Contains(message, "missingtoken")
            || HttpDenial.IsMatch(message) || Contains(message, "forbidden") || Contains(message, "permission denied")
            || Contains(message, "not authorized") || Contains(message, "access_denied") || Contains(message, "access denied");
    }

    // An HTTP 401 or 403 as a status, not the digits anywhere: a fragment address or a file name in
    // the message can hold them too.
    private static readonly System.Text.RegularExpressions.Regex HttpDenial = new(
        @"\b(?:status|http|code)\W{0,3}40[13]\b|\b40[13]\s+(?:unauthorized|forbidden)\b",
        System.Text.RegularExpressions.RegexOptions.IgnoreCase);

    private static bool Contains(string message, params string[] parts) =>
        parts.All(part => message.Contains(part, StringComparison.OrdinalIgnoreCase));
}
