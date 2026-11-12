using System.Runtime.InteropServices;
using System.Text.Json;
using System.Windows;
using System.Windows.Interop;
using Microsoft.Web.WebView2.Core;
using Microsoft.Web.WebView2.Wpf;

namespace LoreCompanion;

internal sealed class WebViewOperationWindow : Window
{
    private readonly OperationRequest request;
    private readonly ClientSettings settings = ClientSettings.Load();
    private readonly WebView2 webView = new();
    private readonly int baseHeight;
    private bool running;
    private WebViewOperationWindow? accountsWindow;

    public WebViewOperationWindow(OperationRequest request)
    {
        this.request = request;
        baseHeight = request.Operation switch
        {
            CompanionOperation.Clone => 340,
            CompanionOperation.Sync => 340,
            CompanionOperation.Commit => 500,
            CompanionOperation.Status => 450,
            CompanionOperation.History => 420,
            CompanionOperation.Accounts => 500,
            _ => 430,
        };
        Title = $"Lore {request.Operation}";
        Width = 620;
        Height = baseHeight;
        MinWidth = 480;
        MinHeight = 340;
        WindowStartupLocation = WindowStartupLocation.CenterScreen;
        Icon = new System.Windows.Media.Imaging.BitmapImage(new Uri("pack://application:,,,/lore.ico"));
        SourceInitialized += (_, _) => ApplyWindowTheme();
        Content = webView;
        if (request.Operation == CompanionOperation.Clone && string.IsNullOrWhiteSpace(request.TargetPath) && request.Paths.Length > 0)
        {
            var directory = Directory.Exists(request.Paths[0]) ? request.Paths[0] : Path.GetDirectoryName(request.Paths[0]);
            if (!string.IsNullOrEmpty(directory)) request.TargetPath = directory;
        }
        Loaded += async (_, _) => await Initialize();
        webView.CoreWebView2InitializationCompleted += (_, args) =>
        {
            if (!args.IsSuccess) ShowInitializationError(args.InitializationException);
        };
    }

    [DllImport("dwmapi.dll")]
    private static extern int DwmSetWindowAttribute(IntPtr window, int attribute, ref int value, int size);

    private void ApplyWindowTheme()
    {
        var dark = settings.Theme == "midnight" ? 1 : 0;
        DwmSetWindowAttribute(new WindowInteropHelper(this).Handle, 20, ref dark, sizeof(int));
    }

    public static bool IsRuntimeAvailable()
    {
        try { return !string.IsNullOrWhiteSpace(CoreWebView2Environment.GetAvailableBrowserVersionString()); }
        catch { return false; }
    }

    private async Task Initialize()
    {
        try
        {
            var profile = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData), "Lore", "WebView2");
            Directory.CreateDirectory(profile);
            var environment = await CoreWebView2Environment.CreateAsync(null, profile);
            await webView.EnsureCoreWebView2Async(environment);
            var core = webView.CoreWebView2;
            core.Settings.AreDefaultContextMenusEnabled = false;
            core.Settings.AreDevToolsEnabled = false;
            core.Settings.IsStatusBarEnabled = false;
            core.Settings.IsZoomControlEnabled = false;
            core.NavigationStarting += (_, args) =>
            {
                args.Cancel = !Uri.TryCreate(args.Uri, UriKind.Absolute, out var uri)
                    || uri.Scheme != Uri.UriSchemeHttps || !uri.Host.Equals("lore.client", StringComparison.OrdinalIgnoreCase);
            };
            core.NewWindowRequested += (_, args) => args.Handled = true;
            core.WebMessageReceived += async (_, args) => await Receive(args.WebMessageAsJson);
            var root = Path.Combine(AppContext.BaseDirectory, "Web");
            if (!File.Exists(Path.Combine(root, "index.html"))) throw new FileNotFoundException("Lore Companion Web UI is missing.");
            core.SetVirtualHostNameToFolderMapping("lore.client", root, CoreWebView2HostResourceAccessKind.DenyCors);
            core.Navigate("https://lore.client/index.html");
        }
        catch (Exception exception)
        {
            ShowInitializationError(exception);
        }
    }

    private void ShowInitializationError(Exception exception)
    {
        MessageBox.Show(this, $"The Lore WebView2 interface could not be started.\n\n{exception.Message}", "Lore Companion", MessageBoxButton.OK, MessageBoxImage.Error);
        Close();
    }

    private async Task Receive(string json)
    {
        try
        {
            using var message = JsonDocument.Parse(json);
            var root = message.RootElement;
            switch (root.GetProperty("type").GetString())
            {
                case "ready":
                    Send(new
                    {
                        type = "init",
                        operation = (int)request.Operation,
                        repositoryUrl = request.Operation == CompanionOperation.Accounts ? request.ServerUrl() : request.RepositoryUrl,
                        repositoryPath = request.Operation is CompanionOperation.Clone or CompanionOperation.Accounts ? "" : request.RepositoryRoot(),
                        targetPath = request.TargetPath,
                        message = request.Message,
                        paths = request.Paths,
                        theme = settings.Theme,
                    });
                    if (request.Operation == CompanionOperation.Accounts) await SendAccounts();
                    break;
                case "browse":
                    if (request.Operation != CompanionOperation.Clone) break;
                    var dialog = new Microsoft.Win32.OpenFolderDialog { InitialDirectory = request.TargetPath };
                    if (dialog.ShowDialog(this) == true)
                    {
                        request.TargetPath = dialog.FolderName;
                        Send(new { type = "directory", path = dialog.FolderName });
                    }
                    break;
                case "accounts-open":
                    if (running) break;
                    if (accountsWindow is not null)
                    {
                        accountsWindow.Activate();
                        break;
                    }
                    accountsWindow = new WebViewOperationWindow(new OperationRequest
                    {
                        Operation = CompanionOperation.Accounts,
                        RepositoryUrl = request.ServerUrl(),
                        TargetPath = request.TargetPath,
                        Paths = request.Paths,
                    }) { Owner = this };
                    accountsWindow.Closed += (_, _) => accountsWindow = null;
                    accountsWindow.Show();
                    break;
                case "accounts-add":
                    if (running) break;
                    running = true;
                    await LoreAccounts.Add(root.GetProperty("remoteUrl").GetString() ?? "", text => Send(new { type = "progress", text }));
                    running = false;
                    await SendAccounts();
                    Send(new { type = "complete", text = "Account added successfully." });
                    break;
                case "accounts-select":
                    if (running) break;
                    if (string.IsNullOrEmpty(request.ServerKey())) throw new InvalidDataException("The Lore server is unknown");
                    running = true;
                    settings.SelectAccount(request.ServerKey(), root.GetProperty("userId").GetString() ?? "");
                    running = false;
                    await SendAccounts();
                    Send(new { type = "complete", text = "The account will be used for this server." });
                    break;
                case "accounts-remove":
                    if (running) break;
                    running = true;
                    await LoreAccounts.Remove(root.GetProperty("authUrl").GetString() ?? "", root.GetProperty("userId").GetString() ?? "", request.ServerUrl());
                    running = false;
                    await SendAccounts();
                    Send(new { type = "complete", text = "Account removed and its Keycloak SSO sessions signed out. You can add another account." });
                    break;
                case "cancel":
                    Close();
                    break;
                case "run":
                    if (!running)
                    {
                        await Run(root.GetProperty("repositoryUrl").GetString(), root.GetProperty("targetPath").GetString(), root.GetProperty("message").GetString());
                    }
                    break;
            }
        }
        catch (Exception exception)
        {
            running = false;
            Send(new { type = "error", text = OperationError.UserMessage(exception, request.Operation), accessDenied = OperationError.IsAccessDenied(exception, request.Operation) });
        }
    }

    private async Task SendAccounts()
    {
        var server = request.ServerKey();
        var selected = ClientSettings.Load().SelectedAccount(server);
        if (string.IsNullOrEmpty(selected)) selected = request.CurrentIdentity();
        var accounts = await LoreAccounts.List();
        if (string.IsNullOrEmpty(selected))
        {
            var matches = accounts.Where(account => account.Domains.Split([',', ' '], StringSplitOptions.RemoveEmptyEntries).Contains(server, StringComparer.OrdinalIgnoreCase)).Take(2).ToArray();
            if (matches.Length == 1) selected = matches[0].UserId;
        }
        Send(new
        {
            type = "accounts",
            items = accounts.Select(account => new
            {
                authUrl = account.AuthUrl,
                userId = account.UserId,
                username = account.Username,
                domains = account.Domains,
                selected = account.UserId == selected,
            }),
        });
    }

    private async Task Run(string? repositoryUrl, string? targetPath, string? message)
    {
        request.RepositoryUrl = repositoryUrl?.Trim() ?? "";
        request.TargetPath = targetPath?.Trim() ?? "";
        request.Message = message?.Trim() ?? "";
        try
        {
            request.Validate();
            running = true;
            Height = baseHeight + 140;
            await LoreOperations.ExecuteAsync(request, text => Send(new { type = "progress", text }), CancellationToken.None);
            Send(new { type = "complete", text = CompletionText() });
        }
        catch (Exception exception)
        {
            Send(new { type = "error", text = OperationError.UserMessage(exception, request.Operation), accessDenied = OperationError.IsAccessDenied(exception, request.Operation) });
        }
        finally
        {
            running = false;
        }
    }

    private string CompletionText() => request.Operation switch
    {
        CompanionOperation.Clone => "Repository cloned successfully.",
        CompanionOperation.Sync => "Repository synchronized successfully.",
        CompanionOperation.Commit => "Revision created successfully.",
        CompanionOperation.Stage => "Selected paths staged for the next commit.",
        CompanionOperation.Status => "Working copy scan completed.",
        CompanionOperation.History => "Revision history updated.",
        CompanionOperation.Lock => "Locks acquired for the selected paths.",
        CompanionOperation.Unlock => "Locks released for the selected paths.",
        _ => "Operation completed.",
    };

    private void Send(object message)
    {
        if (!Dispatcher.CheckAccess())
        {
            Dispatcher.BeginInvoke(() => Send(message));
            return;
        }
        webView.CoreWebView2?.PostWebMessageAsJson(JsonSerializer.Serialize(message));
    }

    protected override void OnClosing(System.ComponentModel.CancelEventArgs e)
    {
        e.Cancel = running;
        base.OnClosing(e);
    }
}
