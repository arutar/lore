using System.Runtime.InteropServices;
using static LoreCompanion.Native;

namespace LoreCompanion;

/// <summary>An operation window on Linux: a GTK window holding a WebKitGTK view of the same page the
/// Windows companion shows (<c>platform/windows/LoreCompanion.Web</c>), served from the companion's
/// own folder under <c>lore://client/</c>.</summary>
internal sealed unsafe class LinuxOperationWindow : IOperationHost
{
    private const string Origin = "lore://client/";
    private static readonly string WebRoot = Path.Combine(AppContext.BaseDirectory, "Web");
    private static bool schemeRegistered;
    private static int open;
    private static readonly bool Debug = Environment.GetEnvironmentVariable("LORE_COMPANION_DEBUG") == "1";

    // The page calls window.chrome.webview, as WebView2 offers it on Windows; this gives it the same
    // object over WebKit's message handler. The host answers through window.__loreReceive.
    private const string Bridge = """
        (() => {
          const target = new EventTarget();
          window.chrome = window.chrome || {};
          window.chrome.webview = {
            postMessage: message => window.webkit.messageHandlers.lore.postMessage(JSON.stringify(message)),
            addEventListener: (type, listener) => target.addEventListener(type, listener),
            removeEventListener: (type, listener) => target.removeEventListener(type, listener),
          };
          Object.defineProperty(window, "__loreReceive", {
            value: data => target.dispatchEvent(new MessageEvent("message", { data })),
          });
          // JavaScriptCore in WebKitGTK 2.54 on Rocky Linux 9 aborts the page's process the first
          // time Intl formats a number. The page only groups thousands and limits the fraction, so
          // that is done here without Intl, as the English interface reads.
          Object.defineProperty(Number.prototype, "toLocaleString", {
            value: function (locales, options) {
              const digits = options && Number.isInteger(options.maximumFractionDigits) ? options.maximumFractionDigits : 3;
              const factor = 10 ** digits;
              const rounded = Math.round(Math.abs(this) * factor) / factor;
              const [whole, fraction] = String(rounded).split(".");
              const grouped = whole.replace(/\B(?=(\d{3})+(?!\d))/g, ",");
              return (this < 0 && rounded !== 0 ? "-" : "") + grouped + (fraction ? "." + fraction : "");
            },
            configurable: true, writable: true,
          });
        })();
        """;

    private readonly OperationSession session;
    private readonly IntPtr window;
    private readonly IntPtr view;
    private readonly GCHandle self;
    private readonly int width;
    private bool fitted;
    private bool userSized;
    private int fittedHeight;
    private bool closed;
    private LinuxOperationWindow? accountsWindow;

    public LinuxOperationWindow(OperationRequest request, LinuxOperationWindow? owner = null)
    {
        RegisterScheme();
        session = new OperationSession(request, this);
        self = GCHandle.Alloc(this);
        var data = GCHandle.ToIntPtr(self);
        width = (int)session.PreferredWidth;

        window = gtk_window_new(0);
        gtk_window_set_title(window, session.Title);
        gtk_window_set_default_size(window, width, 360); // until the page reports its height
        gtk_widget_set_size_request(window, 480, 220);
        gtk_window_set_position(window, owner is null ? 1 : 4); // centre, or centre on the owner
        if (owner is not null) gtk_window_set_transient_for(window, owner.window);
        var icon = Path.Combine(AppContext.BaseDirectory, "lore.svg");
        if (File.Exists(icon)) gtk_window_set_icon_from_file(window, icon, IntPtr.Zero);
        ThemeChanged(session.Theme);
        g_signal_connect_data(window, "delete-event", (IntPtr)(delegate* unmanaged<IntPtr, IntPtr, IntPtr, int>)&OnDelete, data, IntPtr.Zero, 0);
        g_signal_connect_data(window, "destroy", (IntPtr)(delegate* unmanaged<IntPtr, IntPtr, void>)&OnDestroy, data, IntPtr.Zero, 0);

        view = webkit_web_view_new();
        var settings = webkit_web_view_get_settings(view);
        webkit_settings_set_enable_developer_extras(settings, 0);
        // LORE_COMPANION_DEBUG=1 prints the page's console to standard output.
        webkit_settings_set_enable_write_console_messages_to_stdout(settings, Debug ? 1 : 0);
        var content = webkit_web_view_get_user_content_manager(view);
        var script = webkit_user_script_new(Bridge, 1, 0, IntPtr.Zero, IntPtr.Zero); // top frame, at document start
        webkit_user_content_manager_add_script(content, script);
        webkit_user_script_unref(script);
        webkit_user_content_manager_register_script_message_handler(content, "lore");
        g_signal_connect_data(content, "script-message-received::lore", (IntPtr)(delegate* unmanaged<IntPtr, IntPtr, IntPtr, void>)&OnMessage, data, IntPtr.Zero, 0);
        g_signal_connect_data(view, "decide-policy", (IntPtr)(delegate* unmanaged<IntPtr, IntPtr, int, IntPtr, int>)&OnPolicy, data, IntPtr.Zero, 0);
        g_signal_connect_data(view, "context-menu", (IntPtr)(delegate* unmanaged<IntPtr, IntPtr, IntPtr, IntPtr, IntPtr, int>)&OnContextMenu, data, IntPtr.Zero, 0);
        gtk_container_add(window, view);
        open++;
        gtk_widget_show_all(window);
        if (!File.Exists(Path.Combine(WebRoot, "index.html"))) throw new FileNotFoundException("Lore Companion Web UI is missing.");
        webkit_web_view_load_uri(view, Origin + "index.html");
    }

    private static LinuxOperationWindow From(IntPtr data) => (LinuxOperationWindow)GCHandle.FromIntPtr(data).Target!;

    public bool IsOpen => !closed;

    public void Post(string json)
    {
        if (SynchronizationContext.Current is not GLibContext)
        {
            Program.MainLoop.Post(_ => Post(json), null);
            return;
        }
        if (closed) return;
        if (Debug) Console.WriteLine($"{DateTime.Now:HH:mm:ss.fff} t{Environment.CurrentManagedThreadId} host: {(json.Length > 300 ? json[..300] + "…" : json)}");
        // The serializer escapes everything outside ASCII, so the JSON is also a valid script literal.
        webkit_web_view_run_javascript(view, $"window.__loreReceive({json});", IntPtr.Zero, IntPtr.Zero, IntPtr.Zero);
    }

    public void Close()
    {
        if (!closed && session.TryClose()) gtk_widget_destroy(window);
    }

    public Task<string?> ChooseFolder(string initial)
    {
        var chooser = gtk_file_chooser_native_new("Choose a folder", window, 2, "_Select", "_Cancel"); // SELECT_FOLDER
        try
        {
            if (Directory.Exists(initial)) gtk_file_chooser_set_current_folder(chooser, initial);
            var accepted = gtk_native_dialog_run(chooser) == -3; // GTK_RESPONSE_ACCEPT
            return Task.FromResult(accepted ? TakeUtf8(gtk_file_chooser_get_filename(chooser)) : null);
        }
        finally
        {
            g_object_unref(chooser);
        }
    }

    public void OpenAccounts(OperationRequest request)
    {
        if (accountsWindow is { IsOpen: true })
        {
            gtk_window_present(accountsWindow.window);
            return;
        }
        accountsWindow = new LinuxOperationWindow(request, this);
    }

    public void FitHeight(double height)
    {
        if (height <= 0 || closed) return;
        gtk_window_get_size(window, out var shownWidth, out var shown);
        // Once the user resizes the window by hand, it keeps that size.
        if (fitted && Math.Abs(shown - fittedHeight) > 2) userSized = true;
        if (userSized) return;
        var target = (int)Math.Ceiling(Math.Clamp(height, 220, WorkArea() * 0.9));
        if (fitted && target == shown) return;
        fitted = true;
        fittedHeight = target;
        gtk_window_resize(window, shownWidth, target);
    }

    public void ThemeChanged(string theme) =>
        // Every theme but Light is dark; the window frame follows.
        g_object_set(gtk_settings_get_default(), "gtk-application-prefer-dark-theme", theme == "light" ? 0 : 1, IntPtr.Zero);

    public Task<IDisposable> ShowSignInPage(Uri url, string code, Action closedByUser)
    {
        var page = new SignInWindow(window, url, code, closedByUser);
        return Task.FromResult<IDisposable>(page);
    }

    private static double WorkArea()
    {
        var display = gdk_display_get_default();
        var monitor = gdk_display_get_primary_monitor(display);
        if (monitor == IntPtr.Zero) monitor = gdk_display_get_monitor(display, 0);
        if (monitor == IntPtr.Zero) return 1000;
        gdk_monitor_get_workarea(monitor, out var area);
        return area.Height > 0 ? area.Height : 1000;
    }

    [UnmanagedCallersOnly]
    private static int OnDelete(IntPtr widget, IntPtr gdkEvent, IntPtr data) => From(data).session.TryClose() ? 0 : 1;

    [UnmanagedCallersOnly]
    private static void OnDestroy(IntPtr widget, IntPtr data)
    {
        var host = From(data);
        host.closed = true;
        host.self.Free();
        if (--open == 0) gtk_main_quit();
    }

    [UnmanagedCallersOnly]
    private static void OnMessage(IntPtr manager, IntPtr result, IntPtr data)
    {
        var json = TakeUtf8(jsc_value_to_string(webkit_javascript_result_get_js_value(result)));
        if (json is null) return;
        var host = From(data);
        if (Debug) Console.WriteLine($"{DateTime.Now:HH:mm:ss.fff} t{Environment.CurrentManagedThreadId} page: {json}");
        _ = host.session.Receive(json);
    }

    // Only the companion's own pages load in the window; links elsewhere and new windows are refused.
    [UnmanagedCallersOnly]
    private static int OnPolicy(IntPtr view, IntPtr decision, int type, IntPtr data)
    {
        if (type != 0 && type != 1) return 0; // only navigation and new-window decisions
        var action = webkit_navigation_policy_decision_get_navigation_action(decision);
        var uri = Utf8(webkit_uri_request_get_uri(webkit_navigation_action_get_request(action))) ?? "";
        if (type == 0 && uri.StartsWith(Origin, StringComparison.Ordinal)) return 0;
        OperationSession.Log($"navigation refused: {uri}");
        webkit_policy_decision_ignore(decision);
        return 1;
    }

    [UnmanagedCallersOnly]
    private static int OnContextMenu(IntPtr view, IntPtr menu, IntPtr gdkEvent, IntPtr hit, IntPtr data) => 1;

    private static void RegisterScheme()
    {
        if (schemeRegistered) return;
        schemeRegistered = true;
        var context = webkit_web_context_get_default();
        webkit_web_context_register_uri_scheme(context, "lore", &OnScheme, IntPtr.Zero, IntPtr.Zero);
        var security = webkit_web_context_get_security_manager(context);
        webkit_security_manager_register_uri_scheme_as_secure(security, "lore");
        webkit_security_manager_register_uri_scheme_as_cors_enabled(security, "lore");
    }

    // Serves lore://client/<path> from the Web folder beside the companion, and nothing outside it.
    [UnmanagedCallersOnly]
    private static void OnScheme(IntPtr request, IntPtr data)
    {
        var relative = (Utf8(webkit_uri_scheme_request_get_path(request)) ?? "").TrimStart('/');
        var path = Path.GetFullPath(Path.Combine(WebRoot, relative));
        if (!path.StartsWith(WebRoot + Path.DirectorySeparatorChar, StringComparison.Ordinal) || !File.Exists(path))
        {
            var error = g_error_new_literal(g_quark_from_string("lore-companion"), 404, $"Not found: {relative}");
            webkit_uri_scheme_request_finish_error(request, error);
            g_error_free(error);
            return;
        }
        var bytes = File.ReadAllBytes(path);
        var memory = g_malloc((nuint)Math.Max(1, bytes.Length));
        Marshal.Copy(bytes, 0, memory, bytes.Length);
        var stream = g_memory_input_stream_new_from_data(memory, bytes.Length, GFree);
        webkit_uri_scheme_request_finish(request, stream, bytes.Length, Mime(path));
        g_object_unref(stream);
    }

    private static string Mime(string path) => Path.GetExtension(path).ToLowerInvariant() switch
    {
        ".html" => "text/html",
        ".js" => "text/javascript",
        ".css" => "text/css",
        ".svg" => "image/svg+xml",
        ".png" => "image/png",
        ".woff2" => "font/woff2",
        ".json" => "application/json",
        _ => "application/octet-stream",
    };

    /// <summary>The identity provider's sign-in page, in a window of its own with no bridge to the
    /// companion.</summary>
    private sealed class SignInWindow : IDisposable
    {
        private readonly IntPtr window;
        private readonly GCHandle self;
        private readonly Action closedByUser;
        private bool closed;
        private bool disposed;

        public SignInWindow(IntPtr owner, Uri url, string code, Action closedByUser)
        {
            this.closedByUser = closedByUser;
            self = GCHandle.Alloc(this);
            window = gtk_window_new(0);
            gtk_window_set_title(window, $"Lore sign-in  {code}");
            gtk_window_set_default_size(window, 680, 720);
            gtk_window_set_transient_for(window, owner);
            gtk_window_set_position(window, 4);
            g_signal_connect_data(window, "destroy", (IntPtr)(delegate* unmanaged<IntPtr, IntPtr, void>)&OnDestroy, GCHandle.ToIntPtr(self), IntPtr.Zero, 0);
            var view = webkit_web_view_new();
            gtk_container_add(window, view);
            gtk_widget_show_all(window);
            webkit_web_view_load_uri(view, url.AbsoluteUri);
        }

        public void Dispose()
        {
            disposed = true;
            if (!closed) gtk_widget_destroy(window);
        }

        [UnmanagedCallersOnly]
        private static void OnDestroy(IntPtr widget, IntPtr data)
        {
            var page = (SignInWindow)GCHandle.FromIntPtr(data).Target!;
            var byUser = !page.disposed;
            page.closed = true;
            page.self.Free();
            if (byUser) page.closedByUser();
        }
    }
}
