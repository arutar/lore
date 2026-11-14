using System.Diagnostics;
using System.Net;
using System.Net.Http.Json;
using System.Text.Json;
using System.Text.RegularExpressions;

namespace LoreCompanion.Tests;

/// <summary>
/// A live Lore Platform installation for the scenario tests: creates repositories and a second
/// user through the control plane's API, signs that user in to Keycloak with the device flow the
/// Lore client itself uses, and runs lore.exe as them. The Companion side runs as the account
/// signed in on this computer (it must be an administrator of the installation).
///
/// Settings (environment):
///   LORE_LIVE_WEB             WebUI address, e.g. http://mega-claude:8080
///   LORE_LIVE_SERVER          Lore server, e.g. lore://mega-claude
///   LORE_LIVE_ADMIN_PASSWORD  password of the local "admin" account (LORE_LIVE_ADMIN_USER to change the name)
///   LORE_LIVE_ROOT            folder for working copies (emptied per test)
///   LORE_CLI                  lore.exe (default: C:\Program Files\Lore\lore.exe)
///   LORE_LIVE_RESTART_COMMAND optional command that restarts the Lore server (enables the restart test)
/// </summary>
public sealed class LiveServer : IDisposable
{
    public string Web { get; }
    public string Server { get; }
    public string Root { get; }
    public string Cli { get; }
    public string? RestartCommand { get; }
    private readonly HttpClient admin;
    private readonly CookieContainer adminCookies = new();
    private string csrf = "";

    private LiveServer(string web, string server, string root)
    {
        Web = web.TrimEnd('/');
        Server = server.TrimEnd('/');
        Root = root;
        Cli = Environment.GetEnvironmentVariable("LORE_CLI") is { Length: > 0 } cli ? cli : @"C:\Program Files\Lore\lore.exe";
        RestartCommand = Environment.GetEnvironmentVariable("LORE_LIVE_RESTART_COMMAND") is { Length: > 0 } restart ? restart : null;
        admin = new HttpClient(new HttpClientHandler { CookieContainer = adminCookies, AllowAutoRedirect = true }) { Timeout = TimeSpan.FromSeconds(60) };
    }

    /// <summary>The configured installation, or null when the environment doesn't name one.</summary>
    public static LiveServer? FromEnvironment()
    {
        string? Get(string name) => Environment.GetEnvironmentVariable(name) is { Length: > 0 } value ? value : null;
        var web = Get("LORE_LIVE_WEB");
        var server = Get("LORE_LIVE_SERVER");
        var root = Get("LORE_LIVE_ROOT");
        if (web is null || server is null || root is null || Get("LORE_LIVE_ADMIN_PASSWORD") is null) return null;
        return new LiveServer(web, server, root);
    }

    public async Task SignInAdmin()
    {
        var user = Environment.GetEnvironmentVariable("LORE_LIVE_ADMIN_USER") is { Length: > 0 } name ? name : "admin";
        await SignInWeb(admin, user, Environment.GetEnvironmentVariable("LORE_LIVE_ADMIN_PASSWORD")!, null);
        var session = await admin.GetFromJsonAsync<JsonElement>($"{Web}/web/v1/session");
        if (!session.TryGetProperty("authenticated", out var authenticated) || !authenticated.GetBoolean())
            throw new InvalidOperationException($"{user} isn't signed in to the WebUI");
        csrf = session.GetProperty("csrfToken").GetString() ?? "";
    }

    // The WebUI's sign-in: /web/v1/login redirects to Keycloak's form; posting it lands back on
    // the WebUI with a session cookie. A temporary password is replaced on Keycloak's next form.
    private async Task SignInWeb(HttpClient client, string user, string password, string? newPassword)
    {
        var page = await client.GetStringAsync($"{Web}/web/v1/login");
        await SubmitKeycloakLogin(client, page, user, password, newPassword);
    }

    private static async Task<string> SubmitKeycloakLogin(HttpClient client, string page, string user, string password, string? newPassword)
    {
        var response = await client.PostAsync(FormAction(page), new FormUrlEncodedContent(new Dictionary<string, string>
        {
            ["username"] = user, ["password"] = password, ["credentialId"] = "",
        }));
        page = await response.Content.ReadAsStringAsync();
        if (page.Contains("name=\"password-new\""))
        {
            if (newPassword is null) throw new InvalidOperationException($"Keycloak asks {user} for a new password");
            response = await client.PostAsync(FormAction(page), new FormUrlEncodedContent(new Dictionary<string, string>
            {
                ["password-new"] = newPassword, ["password-confirm"] = newPassword,
            }));
            page = await response.Content.ReadAsStringAsync();
        }
        return page;
    }

    private static string FormAction(string page)
    {
        var match = Regex.Match(page, "<form[^>]*action=\"([^\"]+)\"");
        if (!match.Success) throw new InvalidOperationException("Keycloak didn't show a form");
        return WebUtility.HtmlDecode(match.Groups[1].Value);
    }

    private async Task<JsonElement> Api(HttpMethod method, string path, object? body = null)
    {
        using var request = new HttpRequestMessage(method, $"{Web}/web/v1{path}");
        if (method != HttpMethod.Get) request.Headers.Add("X-CSRF-Token", csrf);
        if (body is not null) request.Content = JsonContent.Create(body);
        using var response = await admin.SendAsync(request);
        var text = await response.Content.ReadAsStringAsync();
        if (!response.IsSuccessStatusCode) throw new InvalidOperationException($"{method} {path} -> {(int)response.StatusCode}: {text[..Math.Min(300, text.Length)]}");
        return text.Length == 0 ? default : JsonDocument.Parse(text).RootElement.Clone();
    }

    /// <summary>Creates a private repository and returns its lore:// URL.</summary>
    public async Task<(string Id, string Url)> CreateRepository(string name)
    {
        var created = await Api(HttpMethod.Post, "/admin/repositories", new { name, description = "Companion scenario test", @public = false });
        return (created.GetProperty("id").GetString()!, $"{Server}/{name}");
    }

    public async Task<TestUser> CreateUser(string tag)
    {
        var email = $"{tag}@example.invalid";
        var temporary = $"Temp-{Guid.NewGuid():N}";
        var permanent = $"Kept-{Guid.NewGuid():N}";
        var created = await Api(HttpMethod.Post, "/admin/users", new { email, displayName = $"Companion test {tag}", password = temporary, role = "user" });
        var user = new TestUser(this, created.GetProperty("id").GetString()!, email, permanent);
        // The first sign-in replaces the temporary password, as a person would.
        using var client = new HttpClient(new HttpClientHandler { CookieContainer = new CookieContainer() });
        await SignInWeb(client, email, temporary, permanent);
        return user;
    }

    public Task DeleteUser(TestUser user) => Api(HttpMethod.Delete, $"/admin/users/{user.Id}");

    public Task Grant(string repositoryId, TestUser user, string permission) =>
        Api(HttpMethod.Put, $"/repositories/{repositoryId}/grants/", new { subjectType = "user", subject = user.Id, permission });

    /// <summary>Signs <paramref name="user"/> in to Keycloak with the device flow, as lore.exe's
    /// own sign-in does, and returns the token pair.</summary>
    internal async Task<(string Access, string Refresh)> DeviceSignIn(string email, string password)
    {
        var realm = $"{Web}/auth/realms/lore/protocol/openid-connect";
        using var client = new HttpClient(new HttpClientHandler { CookieContainer = new CookieContainer() });
        var start = await (await client.PostAsync($"{realm}/auth/device", new FormUrlEncodedContent(new Dictionary<string, string>
        {
            ["client_id"] = "lore-native", ["scope"] = "openid",
        }))).Content.ReadFromJsonAsync<JsonElement>();
        var page = await client.GetStringAsync(start.GetProperty("verification_uri_complete").GetString());
        page = await SubmitKeycloakLogin(client, page, email, password, null);
        // Keycloak asks to grant the device access unless the client skips consent.
        if (page.Contains("name=\"accept\""))
        {
            await client.PostAsync(new Uri(new Uri(Web), FormAction(page)), new FormUrlEncodedContent(new Dictionary<string, string> { ["accept"] = "Yes" }));
        }
        return await Token(client, realm, new Dictionary<string, string>
        {
            ["grant_type"] = "urn:ietf:params:oauth:grant-type:device_code",
            ["device_code"] = start.GetProperty("device_code").GetString()!,
            ["client_id"] = "lore-native",
        });
    }

    internal async Task<(string Access, string Refresh)> Refresh(string refreshToken)
    {
        using var client = new HttpClient();
        return await Token(client, $"{Web}/auth/realms/lore/protocol/openid-connect", new Dictionary<string, string>
        {
            ["grant_type"] = "refresh_token", ["refresh_token"] = refreshToken, ["client_id"] = "lore-native",
        });
    }

    private static async Task<(string, string)> Token(HttpClient client, string realm, Dictionary<string, string> form)
    {
        var response = await client.PostAsync($"{realm}/token", new FormUrlEncodedContent(form));
        var body = await response.Content.ReadFromJsonAsync<JsonElement>();
        if (!response.IsSuccessStatusCode) throw new InvalidOperationException($"Keycloak refused the token request: {body}");
        return (body.GetProperty("access_token").GetString()!, body.TryGetProperty("refresh_token", out var refresh) ? refresh.GetString() ?? "" : "");
    }

    public void Restart()
    {
        if (RestartCommand is null) throw new InvalidOperationException("LORE_LIVE_RESTART_COMMAND isn't set");
        using var process = Process.Start(new ProcessStartInfo("cmd.exe", $"/c {RestartCommand}") { UseShellExecute = false, RedirectStandardOutput = true, RedirectStandardError = true })!;
        process.WaitForExit(120_000);
        if (process.ExitCode != 0) throw new InvalidOperationException($"Restarting the server failed: {process.StandardError.ReadToEnd()}");
    }

    public void Dispose() => admin.Dispose();
}

/// <summary>A second person: a local account that works through lore.exe.</summary>
public sealed class TestUser(LiveServer server, string id, string email, string password)
{
    public string Id { get; } = id;
    public string Email { get; } = email;
    private (string Access, string Refresh)? tokens;
    private DateTime issued;

    private async Task<string> AccessToken()
    {
        if (tokens is null) tokens = await server.DeviceSignIn(Email, password);
        else if (DateTime.UtcNow - issued > TimeSpan.FromMinutes(3)) tokens = await server.Refresh(tokens.Value.Refresh);
        else return tokens.Value.Access;
        issued = DateTime.UtcNow;
        return tokens.Value.Access;
    }

    /// <summary>Runs lore.exe as this user in <paramref name="folder"/>.</summary>
    public async Task<(int Exit, string Output)> Lore(string folder, params string[] arguments)
    {
        var start = new ProcessStartInfo(server.Cli) { WorkingDirectory = folder, UseShellExecute = false, RedirectStandardOutput = true, RedirectStandardError = true };
        foreach (var argument in arguments) start.ArgumentList.Add(argument);
        start.Environment["LORE_IDENTITY_TOKEN"] = await AccessToken();
        using var process = Process.Start(start)!;
        var output = process.StandardOutput.ReadToEndAsync();
        var error = process.StandardError.ReadToEndAsync();
        await process.WaitForExitAsync();
        return (process.ExitCode, await output + await error);
    }

    /// <summary>Runs lore.exe and fails the test if it fails.</summary>
    public async Task<string> Must(string folder, params string[] arguments)
    {
        var (exit, output) = await Lore(folder, arguments);
        if (exit != 0) Assert.Fail($"lore {string.Join(' ', arguments)} as {Email} failed ({exit}): {output}");
        return output;
    }

    /// <summary>Writes, stages, commits and pushes files from this user's working copy.</summary>
    public async Task Push(string folder, string message, params (string Path, string Content)[] files)
    {
        foreach (var (path, content) in files)
        {
            var full = Path.Combine(folder, path);
            Directory.CreateDirectory(Path.GetDirectoryName(full)!);
            await File.WriteAllTextAsync(full, content);
        }
        await Must(folder, ["stage", .. files.Select(file => file.Path)]);
        await Must(folder, "commit", message);
        await Must(folder, "push");
    }
}
