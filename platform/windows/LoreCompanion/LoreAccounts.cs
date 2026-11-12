using LoreVcs;
using LoreVcs.Types.Args;
using LoreVcs.Types.Enums;
using LoreVcs.Types.Events;

namespace LoreCompanion;

public sealed record StoredAccount(string AuthUrl, string UserId, string Username, string Domains);

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

    public static async Task Add(string remoteUrl, Action<string> report)
    {
        if (!Uri.TryCreate(remoteUrl, UriKind.Absolute, out var uri) || uri.Scheme != "lore" || string.IsNullOrEmpty(uri.Host))
        {
            throw new InvalidDataException("Enter a Lore server URL, for example lore://localhost");
        }
        var userId = "";
        using var globals = new LoreGlobalArgs();
        using var args = new LoreAuthLoginInteractiveArgs { RemoteUrl = remoteUrl.Trim() };
        await Lore.AuthLoginInteractive(globals, args).Callback((loreEvent, _) =>
        {
            if (loreEvent.Tag == LoreEventTag.LOG)
            {
                report(loreEvent.GetData<LoreLogEventDataFFI>().Message);
            }
            else if (loreEvent.Tag == LoreEventTag.AUTH_USER_INFO)
            {
                userId = loreEvent.GetData<LoreAuthUserInfoEventDataFFI>().Id;
            }
        }).WaitAsync();
        if (string.IsNullOrWhiteSpace(userId)) throw new InvalidDataException("The server returned no account identity after sign-in.");
        Select(remoteUrl, userId);
    }

    public static void Select(string remoteUrl, string userId)
    {
        if (!Uri.TryCreate(remoteUrl, UriKind.Absolute, out var uri) || uri.Scheme != "lore" || string.IsNullOrEmpty(uri.Host) || string.IsNullOrWhiteSpace(userId))
        {
            throw new InvalidDataException("The Lore server and account must be specified.");
        }
        ClientSettings.Load().SelectAccount(uri.Host.ToLowerInvariant(), userId);
    }

    public static async Task Remove(string authUrl, string userId, string remoteUrl)
    {
        var accessToken = await IdentityToken(authUrl, userId);
        if (!string.IsNullOrWhiteSpace(accessToken))
        {
            await SsoLogout.Logout(authUrl, remoteUrl, accessToken);
        }
        using var globals = new LoreGlobalArgs();
        using var args = new LoreAuthLogoutArgs { AuthUrl = authUrl, UserId = userId };
        await Lore.AuthLogout(globals, args).WaitAsync();
        ClientSettings.Load().RemoveAccount(userId);
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
