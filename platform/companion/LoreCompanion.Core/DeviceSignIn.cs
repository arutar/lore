using System.Diagnostics;
using System.Text.Json;

namespace LoreCompanion;

/// <summary>Shows the identity provider's sign-in page for a device code. The host opens a window
/// on <c>url</c> titled with <c>code</c>, calls <c>closed</c> when the user closes it, and returns
/// what closes it again.</summary>
public delegate Task<IDisposable> SignInPage(Uri url, string code, Action closed);

public static class DeviceSignIn
{
    /// <summary>The Lore CLI installed beside the companion.</summary>
    public static string LoreExecutable => Path.Combine(AppContext.BaseDirectory, OperatingSystem.IsWindows() ? "lore.exe" : "lore");

    // The device flow runs in its own process so closing either window stops polling immediately.
    public static async Task<string> Run(string remote, SignInPage showPage, CancellationToken cancellation)
    {
        using var canceled = CancellationTokenSource.CreateLinkedTokenSource(cancellation);
        var token = canceled.Token;
        var start = new ProcessStartInfo(LoreExecutable)
        {
            UseShellExecute = false,
            CreateNoWindow = true,
            RedirectStandardOutput = true,
            ArgumentList = { "--json", "auth", "login", "--no-browser", remote },
        };
        // The window owns the poll even when the file manager uses the shared Lore service.
        start.Environment["LORE_USE_SERVICE"] = "0";
        using var process = Process.Start(start) ?? throw new IOException("Could not start Lore sign-in.");
        IDisposable? page = null;
        string? userId = null;
        try
        {
            while (await process.StandardOutput.ReadLineAsync(token) is { } line)
            {
                using var json = JsonDocument.Parse(line);
                var root = json.RootElement;
                switch (root.GetProperty("tagName").GetString())
                {
                    case "authUrl":
                        var data = root.GetProperty("data");
                        var url = new Uri(data.GetProperty("url").GetString()!);
                        if (url.Scheme != "http" && url.Scheme != "https") throw new InvalidDataException("Unsupported sign-in URL.");
                        page = await showPage(url, data.GetProperty("userCode").GetString() ?? "", canceled.Cancel);
                        token.ThrowIfCancellationRequested();
                        break;
                    case "authUserInfo":
                        userId = root.GetProperty("data").GetProperty("id").GetString();
                        break;
                }
            }
            await process.WaitForExitAsync(token);
            if (process.ExitCode != 0 || string.IsNullOrWhiteSpace(userId))
                throw new InvalidDataException("Sign-in did not complete. You can retry from Accounts.");
            return userId;
        }
        finally
        {
            if (!process.HasExited) process.Kill();
            await process.WaitForExitAsync();
            page?.Dispose();
        }
    }
}
