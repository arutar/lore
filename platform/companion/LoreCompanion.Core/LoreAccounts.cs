using LoreVcs;
using LoreVcs.Types.Args;
using LoreVcs.Types.Enums;
using LoreVcs.Types.Events;

namespace LoreCompanion;

public sealed record StoredAccount(string AuthUrl, string UserId, string Username, string Domains);

/// <summary>The outcome of signing in: the new account and the sign-ins it replaced.</summary>
public sealed record SignInResult(string UserId, IReadOnlyList<string> Replaced, bool SsoCleared);

public static class LoreAccounts
{
    public static async Task<IReadOnlyList<StoredAccount>> List()
    {
        var identities = new List<StoredAccount>();
        using (var globals = new LoreGlobalArgs())
        using (var args = new LoreAuthListArgs())
        {
            await Lore.AuthList(globals, args).Callback((loreEvent, _) =>
            {
                if (loreEvent.Tag == LoreEventTag.AUTH_IDENTITY)
                {
                    var identity = loreEvent.GetData<LoreAuthIdentityEventDataFFI>();
                    identities.Add(new StoredAccount(identity.AuthUrl, identity.UserId, "", identity.AuthorizedDomains));
                }
            }).WaitAsync();
        }

        var accounts = new List<StoredAccount>();
        foreach (var group in identities.GroupBy(identity => (identity.AuthUrl, identity.UserId)))
        {
            var username = "";
            using var globals = new LoreGlobalArgs();
            using var args = new LoreAuthLocalUserInfoArgs { AuthEndpoint = group.Key.AuthUrl, UserIds = [group.Key.UserId] };
            await Lore.AuthLocalUserInfo(globals, args).Callback((loreEvent, _) =>
            {
                if (loreEvent.Tag == LoreEventTag.AUTH_USER_TOKEN)
                {
                    var info = loreEvent.GetData<LoreAuthUserTokenEventDataFFI>();
                    username = string.IsNullOrWhiteSpace(info.PreferredUsername) ? info.Name : info.PreferredUsername;
                }
            }).WaitAsync();
            var domains = string.Join(", ", group.SelectMany(identity => identity.Domains.Split([',', ' '], StringSplitOptions.RemoveEmptyEntries)).Distinct(StringComparer.OrdinalIgnoreCase).Order());
            accounts.Add(new StoredAccount(group.Key.AuthUrl, group.Key.UserId, username, domains));
        }
        return accounts.OrderBy(account => account.Username).ThenBy(account => account.AuthUrl).ToArray();
    }

    /// <summary>
    /// Signs in to a server and keeps a single account for it: every other saved sign-in of
    /// the same identity provider is signed out and deleted, so switching accounts never
    /// leaves the previous credentials on the machine.
    /// </summary>
    public static async Task<SignInResult> Add(string remoteUrl, Action<string> report, Func<string, Task<string>> signIn)
    {
        if (!Uri.TryCreate(remoteUrl, UriKind.Absolute, out var uri) || uri.Scheme != "lore" || string.IsNullOrEmpty(uri.Host))
        {
            throw new InvalidDataException("Enter a Lore server URL, for example lore://localhost");
        }
        var userId = await signIn(remoteUrl.Trim());
        Select(remoteUrl, userId);

        var accounts = await List();
        var signedIn = accounts.FirstOrDefault(account => account.UserId == userId);
        var replaced = new List<string>();
        var ssoCleared = true;
        if (signedIn is not null)
        {
            foreach (var previous in Superseded(accounts, signedIn))
            {
                report($"Signing out the previous account {DisplayName(previous)}…");
                ssoCleared &= await Remove(previous.AuthUrl, previous.UserId, remoteUrl);
                replaced.Add(DisplayName(previous));
            }
        }
        return new SignInResult(userId, replaced, ssoCleared);
    }

    /// <summary>Saved sign-ins that the new account replaces: same identity provider, another user.</summary>
    public static IReadOnlyList<StoredAccount> Superseded(IEnumerable<StoredAccount> accounts, StoredAccount signedIn) =>
        accounts.Where(account => string.Equals(account.AuthUrl, signedIn.AuthUrl, StringComparison.OrdinalIgnoreCase)
                && !string.Equals(account.UserId, signedIn.UserId, StringComparison.Ordinal))
            .ToArray();

    public static string SignInMessage(SignInResult result)
    {
        if (result.Replaced.Count == 0) return "Signed in.";
        var previous = string.Join(", ", result.Replaced);
        return result.SsoCleared
            ? $"Signed in. The previous account ({previous}) was signed out and deleted from this computer."
            : $"Signed in. The previous account ({previous}) was deleted from this computer; the server couldn't be reached to end its session.";
    }

    private static string DisplayName(StoredAccount account) =>
        string.IsNullOrWhiteSpace(account.Username) ? account.UserId : account.Username;

    public static void Select(string remoteUrl, string userId)
    {
        if (!Uri.TryCreate(remoteUrl, UriKind.Absolute, out var uri) || uri.Scheme != "lore" || string.IsNullOrEmpty(uri.Host) || string.IsNullOrWhiteSpace(userId))
        {
            throw new InvalidDataException("The Lore server and account must be specified.");
        }
        ClientSettings.Load().SelectAccount(uri.Host.ToLowerInvariant(), userId);
    }

    /// <summary>
    /// Deletes a saved sign-in. Ending the identity provider's SSO session is best effort:
    /// the local credentials are always deleted, even when the server can't be reached.
    /// Returns whether the SSO session was ended too.
    /// </summary>
    public static async Task<bool> Remove(string authUrl, string userId, string remoteUrl)
    {
        var ssoCleared = true;
        try
        {
            var accessToken = await IdentityToken(authUrl, userId);
            if (!string.IsNullOrWhiteSpace(accessToken))
            {
                await SsoLogout.Logout(authUrl, remoteUrl, accessToken);
            }
        }
        catch (Exception exception) when (exception is not OperationCanceledException)
        {
            ssoCleared = false;
        }
        using var globals = new LoreGlobalArgs();
        using var args = new LoreAuthLogoutArgs { AuthUrl = authUrl, UserId = userId };
        await Lore.AuthLogout(globals, args).WaitAsync();
        ClientSettings.Load().RemoveAccount(userId);
        return ssoCleared;
    }

    private static async Task<string> IdentityToken(string authUrl, string userId)
    {
        var token = "";
        using var globals = new LoreGlobalArgs();
        using var args = new LoreAuthLocalUserInfoArgs
        {
            AuthEndpoint = authUrl,
            UserIds = [userId],
            WithIdentityToken = true,
        };
        await Lore.AuthLocalUserInfo(globals, args).Callback((loreEvent, _) =>
        {
            if (loreEvent.Tag == LoreEventTag.AUTH_USER_TOKEN)
            {
                token = loreEvent.GetData<LoreAuthUserTokenEventDataFFI>().Token;
            }
        }).WaitAsync();
        return token;
    }
}
