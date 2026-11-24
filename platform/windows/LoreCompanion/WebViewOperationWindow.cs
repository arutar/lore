using System.Runtime.InteropServices;
using System.Text.Json;
using System.Windows;
using System.Windows.Interop;
using Microsoft.Web.WebView2.Core;
using Microsoft.Web.WebView2.Wpf;

namespace LoreCompanion;

internal sealed class WebViewOperationWindow : Window, IOperationHost
{
    private readonly OperationSession session;
    private readonly WebView2 webView = new();
    // The page reports its natural height ("size") and the window follows it, so each dialog is
    // as tall as its content instead of one fixed size with empty space. Once the user resizes the
    // window by hand, it keeps that size.
    private bool fitted;
    private bool userSized;
    private double fittedHeight;
    private WebViewOperationWindow? accountsWindow;

    public WebViewOperationWindow(OperationRequest request)
    {
        session = new OperationSession(request, this);
        Title = session.Title;
        Width = session.PreferredWidth;
        Height = 360; // until the page reports its height
        MinWidth = 480;
        MinHeight = 220;
        // A long list must never push the buttons off the screen: the window stops at the work area
        // and the list itself scrolls.
        MaxWidth = Math.Max(MinWidth, SystemParameters.WorkArea.Width - 40);
        MaxHeight = Math.Max(MinHeight, SystemParameters.WorkArea.Height - 40);
        SizeChanged += (_, _) =>
        {
            if (fitted && Math.Abs(ActualHeight - fittedHeight) > 2) userSized = true;
        };
        WindowStartupLocation = WindowStartupLocation.CenterScreen;
        Icon = new System.Windows.Media.Imaging.BitmapImage(new Uri("pack://application:,,,/app.ico"));
        SourceInitialized += (_, _) => ThemeChanged(session.Theme);
        Content = webView;
        Loaded += async (_, _) => await Initialize();
        webView.CoreWebView2InitializationCompleted += (_, args) =>
        {
            if (!args.IsSuccess) ShowInitializationError(args.InitializationException);
        };
    }

    [DllImport("dwmapi.dll")]
    private static extern int DwmSetWindowAttribute(IntPtr window, int attribute, ref int value, int size);

    public void ThemeChanged(string theme)
    {
        // Every theme but Light is dark; the title bar follows.
        var dark = theme == "light" ? 0 : 1;
        DwmSetWindowAttribute(new WindowInteropHelper(this).Handle, 20, ref dark, sizeof(int));
    }

    private static void Log(string text) => OperationSession.Log(text);

    public static bool IsRuntimeAvailable()
    {
        try { return !string.IsNullOrWhiteSpace(CoreWebView2Environment.GetAvailableBrowserVersionString()); }
        catch { return false; }
    }

    private async Task Initialize()
    {
        try
        {
            Log($"initializing, base={AppContext.BaseDirectory}");
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
                Log($"navigate: {args.Uri}");
                args.Cancel = !Uri.TryCreate(args.Uri, UriKind.Absolute, out var uri)
                    || uri.Scheme != Uri.UriSchemeHttps || !uri.Host.Equals("lore.client", StringComparison.OrdinalIgnoreCase);
                if (args.Cancel) Log($"navigation cancelled: {args.Uri}");
            };
            core.NavigationCompleted += (_, args) => Log($"navigation completed: {args.IsSuccess} {args.WebErrorStatus}");
            core.NewWindowRequested += (_, args) => args.Handled = true;
            // The page's own errors would otherwise leave a blank window with no trace anywhere.
            core.ProcessFailed += (_, args) => Log($"web view failed: {args.ProcessFailedKind}");
            core.WebMessageReceived += async (_, args) => await session.Receive(args.WebMessageAsJson);
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

    public void FitHeight(double content)
    {
        if (userSized || content <= 0) return;
        // Title bar and borders: the window's height minus the page's.
        var chrome = ActualHeight - webView.ActualHeight;
        if (chrome <= 0 || webView.ActualHeight <= 0) chrome = SystemParameters.WindowCaptionHeight + 16;
        var area = SystemParameters.WorkArea;
        var target = Math.Ceiling(Math.Clamp(content + chrome, MinHeight, area.Height * 0.9));
        if (fitted && Math.Abs(target - ActualHeight) < 1) return;
        var first = !fitted;
        fitted = true;
        fittedHeight = target;
        Height = target;
        // The window was centred for its provisional height; centre it again for the real one.
        if (first) Top = area.Top + Math.Max(0, (area.Height - target) / 2);
        // Content that arrives later (the Commit window's file list) makes it taller: keep its
        // bottom, and the buttons there, above the taskbar.
        else if (Top + target > area.Bottom) Top = Math.Max(area.Top, area.Bottom - target);
        // Whatever the content asked for, the window stays on the screen, buttons and all.
        if (!double.IsNaN(Left)) Left = Math.Clamp(Left, area.Left, Math.Max(area.Left, area.Right - Width));
        if (!double.IsNaN(Top)) Top = Math.Clamp(Top, area.Top, Math.Max(area.Top, area.Bottom - Height));
    }

    public bool IsOpen => IsVisible;

    public void Post(string json)
    {
        if (!Dispatcher.CheckAccess())
        {
            Dispatcher.BeginInvoke(() => Post(json));
            return;
        }
        webView.CoreWebView2?.PostWebMessageAsJson(json);
    }

    public Task<string?> ChooseFolder(string initial)
    {
        var dialog = new Microsoft.Win32.OpenFolderDialog { InitialDirectory = initial };
        return Task.FromResult(dialog.ShowDialog(this) == true ? dialog.FolderName : null);
    }

    public void OpenAccounts(OperationRequest request)
    {
        if (accountsWindow is not null)
        {
            accountsWindow.Activate();
            return;
        }
        accountsWindow = new WebViewOperationWindow(request) { Owner = this };
        accountsWindow.Closed += (_, _) => accountsWindow = null;
        accountsWindow.Show();
    }

    public async Task<IDisposable> ShowSignInPage(Uri url, string code, Action closed)
    {
        var browser = new WebView2();
        var dialog = new Window { Owner = this, Title = $"Lore sign-in  {code}", Content = browser, Width = 680, Height = 720 };
        dialog.Closed += (_, _) => closed();
        dialog.Show();
        var page = new SignInWindow(dialog, browser);
        try
        {
            await browser.EnsureCoreWebView2Async(webView.CoreWebView2.Environment);
            browser.CoreWebView2.Settings.IsWebMessageEnabled = false;
            browser.CoreWebView2.Settings.AreHostObjectsAllowed = false;
            browser.CoreWebView2.Navigate(url.AbsoluteUri);
            return page;
        }
        catch
        {
            page.Dispose();
            throw;
        }
    }

    private sealed class SignInWindow(Window dialog, WebView2 browser) : IDisposable
    {
        public void Dispose()
        {
            if (dialog.IsVisible) dialog.Close();
            browser.Dispose();
        }
    }

    protected override void OnClosing(System.ComponentModel.CancelEventArgs e)
    {
        e.Cancel = !session.TryClose();
        base.OnClosing(e);
    }
}
