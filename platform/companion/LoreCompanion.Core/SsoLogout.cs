using System.Net.Http;
using System.Net.Http.Headers;

namespace LoreCompanion;

public static class SsoLogout
{
    private static readonly HttpClient Http = new() { Timeout = TimeSpan.FromSeconds(45) };

    public static async Task Logout(string authUrl, string remoteUrl, string accessToken)
    {
        if (!authUrl.StartsWith("oidc+", StringComparison.OrdinalIgnoreCase)
            || !Uri.TryCreate(authUrl["oidc+".Length..], UriKind.Absolute, out var issuer)
            || issuer.Scheme is not ("http" or "https"))
        {
            throw new InvalidDataException("The account's OIDC issuer is invalid.");
        }
        if (string.IsNullOrWhiteSpace(accessToken))
        {
            throw new InvalidDataException("The saved account has no credential for SSO logout.");
        }
        var endpoint = new Uri(PostLogoutUrl(issuer, remoteUrl), "web/v1/native-logout");
        using var request = new HttpRequestMessage(HttpMethod.Post, endpoint);
        request.Headers.Authorization = new AuthenticationHeaderValue("Bearer", accessToken);
        using var response = await Http.SendAsync(request);
        if (!response.IsSuccessStatusCode)
        {
            throw new InvalidDataException("The server could not clear the Keycloak SSO session. Check the connection and try Remove again.");
        }
    }

    public static Uri PostLogoutUrl(Uri issuer, string remoteUrl)
    {
        var host = issuer.Host;
        var port = issuer.IsDefaultPort ? -1 : issuer.Port;
        if (Uri.TryCreate(remoteUrl, UriKind.Absolute, out var remote) && remote.Scheme == "lore" && !string.IsNullOrEmpty(remote.Host))
        {
            host = remote.Host;
            if (!host.Equals(issuer.Host, StringComparison.OrdinalIgnoreCase)) port = -1;
        }
        return new UriBuilder(issuer.Scheme, host, port, "/").Uri;
    }
}
