using System.Diagnostics;
using System.Text.Json;
using System.Windows;
using Microsoft.Web.WebView2.Wpf;

namespace LoreCompanion;

internal static class DeviceSignIn
{
    // The device flow runs in its own process so closing either window stops polling immediately.
    internal static async Task<string> Run(string remote, Window owner, CancellationToken cancellation)
    {
        using var canceled = CancellationTokenSource.CreateLinkedTokenSource(cancellation);
        var token = canceled.Token;
        var start = new ProcessStartInfo(Path.Combine(AppContext.BaseDirectory, "lore.exe"))
        {
            UseShellExecute = false,
            CreateNoWindow = true,
            RedirectStandardOutput = true,
            ArgumentList = { "--json", "auth", "login", "--no-browser", remote },
        };
        // The window owns the poll even when Explorer uses the shared Lore service.
        start.Environment["LORE_USE_SERVICE"] = "0";
        using var process = Process.Start(start) ?? throw new IOException("Could not start Lore sign-in.");
        Window? dialog = null;
        WebView2? browser = null;
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
                        var code = data.GetProperty("userCode").GetString();
                        browser = new WebView2();
                        dialog = new Window { Owner = owner, Title = $"Lore sign-in  {code}", Content = browser, Width = 680, Height = 720 };
                        dialog.Closed += (_, _) => canceled.Cancel();
                        dialog.Show();
                        await browser.EnsureCoreWebView2Async(((WebView2)owner.Content).CoreWebView2.Environment);
                        token.ThrowIfCancellationRequested();
                        browser.CoreWebView2.Settings.IsWebMessageEnabled = false;
                        browser.CoreWebView2.Settings.AreHostObjectsAllowed = false;
                        browser.CoreWebView2.Navigate(url.AbsoluteUri);
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
            if (dialog?.IsVisible == true) dialog.Close();
            browser?.Dispose();
        }
    }
}
