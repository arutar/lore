using System.Reflection;
using System.Runtime.InteropServices;

namespace LoreCompanion;

/// <summary>The GTK 3 and WebKitGTK calls the Linux companion makes. WebKitGTK 4.1 (libsoup 3) is
/// used where the system has it and 4.0 otherwise, as on Rocky Linux 9: both offer the same API.</summary>
internal static unsafe partial class Native
{
    private const string Gtk = "gtk";
    private const string Gdk = "gdk";
    private const string GObject = "gobject";
    private const string GLib = "glib";
    private const string Gio = "gio";
    private const string WebKit = "webkit";
    private const string Jsc = "jsc";

    private static readonly Dictionary<string, string[]> Libraries = new()
    {
        [Gtk] = ["libgtk-3.so.0"],
        [Gdk] = ["libgdk-3.so.0"],
        [GObject] = ["libgobject-2.0.so.0"],
        [GLib] = ["libglib-2.0.so.0"],
        [Gio] = ["libgio-2.0.so.0"],
        [WebKit] = ["libwebkit2gtk-4.1.so.0", "libwebkit2gtk-4.0.so.37"],
        [Jsc] = ["libjavascriptcoregtk-4.1.so.0", "libjavascriptcoregtk-4.0.so.18"],
    };

    // WebKit and JavaScriptCore have to come from the same API version.
    private static int webKitChoice = -1;

    public static void Register() => NativeLibrary.SetDllImportResolver(typeof(Native).Assembly, Resolve);

    private static IntPtr Resolve(string name, Assembly assembly, DllImportSearchPath? path)
    {
        if (!Libraries.TryGetValue(name, out var candidates)) return IntPtr.Zero;
        if (name is WebKit or Jsc)
        {
            if (webKitChoice < 0)
            {
                for (var index = 0; index < Libraries[WebKit].Length; index++)
                {
                    if (NativeLibrary.TryLoad(Libraries[WebKit][index], out _)) { webKitChoice = index; break; }
                }
                if (webKitChoice < 0) throw new DllNotFoundException("WebKitGTK is required: install webkit2gtk4.1 or webkit2gtk3 (libwebkit2gtk-4.1-0 on Debian and Ubuntu).");
            }
            return NativeLibrary.Load(candidates[webKitChoice]);
        }
        foreach (var candidate in candidates)
        {
            if (NativeLibrary.TryLoad(candidate, out var handle)) return handle;
        }
        throw new DllNotFoundException($"{candidates[0]} is required.");
    }

    // GLib and GObject
    [LibraryImport(GLib)] public static partial uint g_idle_add(delegate* unmanaged<IntPtr, int> function, IntPtr data);
    [LibraryImport(GLib)] public static partial IntPtr g_malloc(nuint size);
    [LibraryImport(GLib)] public static partial void g_free(IntPtr memory);
    [LibraryImport(GLib, StringMarshalling = StringMarshalling.Utf8)] public static partial uint g_quark_from_string(string text);
    [LibraryImport(GLib, StringMarshalling = StringMarshalling.Utf8)] public static partial IntPtr g_error_new_literal(uint domain, int code, string message);
    [LibraryImport(GLib)] public static partial void g_error_free(IntPtr error);
    [LibraryImport(GObject, StringMarshalling = StringMarshalling.Utf8)]
    public static partial ulong g_signal_connect_data(IntPtr instance, string signal, IntPtr handler, IntPtr data, IntPtr destroy, int flags);
    [LibraryImport(GObject)] public static partial void g_object_unref(IntPtr instance);
    [LibraryImport(GObject, StringMarshalling = StringMarshalling.Utf8)]
    public static partial void g_object_set(IntPtr instance, string property, int value, IntPtr end);
    [LibraryImport(Gio)] public static partial IntPtr g_memory_input_stream_new_from_data(IntPtr data, nint length, IntPtr destroy);

    // GTK and GDK
    [LibraryImport(Gtk)] public static partial int gtk_init_check(IntPtr argc, IntPtr argv);
    [LibraryImport(Gtk)] public static partial void gtk_main();
    [LibraryImport(Gtk)] public static partial void gtk_main_quit();
    [LibraryImport(Gtk)] public static partial IntPtr gtk_window_new(int type);
    [LibraryImport(Gtk, StringMarshalling = StringMarshalling.Utf8)] public static partial void gtk_window_set_title(IntPtr window, string title);
    [LibraryImport(Gtk)] public static partial void gtk_window_set_default_size(IntPtr window, int width, int height);
    [LibraryImport(Gtk)] public static partial void gtk_window_resize(IntPtr window, int width, int height);
    [LibraryImport(Gtk)] public static partial void gtk_window_get_size(IntPtr window, out int width, out int height);
    [LibraryImport(Gtk)] public static partial void gtk_window_set_position(IntPtr window, int position);
    [LibraryImport(Gtk)] public static partial void gtk_window_set_transient_for(IntPtr window, IntPtr parent);
    [LibraryImport(Gtk)] public static partial void gtk_window_present(IntPtr window);
    [LibraryImport(Gtk, StringMarshalling = StringMarshalling.Utf8)] public static partial int gtk_window_set_icon_from_file(IntPtr window, string path, IntPtr error);
    [LibraryImport(Gtk)] public static partial void gtk_widget_set_size_request(IntPtr widget, int width, int height);
    [LibraryImport(Gtk)] public static partial void gtk_container_add(IntPtr container, IntPtr widget);
    [LibraryImport(Gtk)] public static partial void gtk_widget_show_all(IntPtr widget);
    [LibraryImport(Gtk)] public static partial void gtk_widget_destroy(IntPtr widget);
    [LibraryImport(Gtk)] public static partial IntPtr gtk_settings_get_default();
    [LibraryImport(Gtk, StringMarshalling = StringMarshalling.Utf8)]
    public static partial IntPtr gtk_file_chooser_native_new(string title, IntPtr parent, int action, string accept, string cancel);
    [LibraryImport(Gtk, StringMarshalling = StringMarshalling.Utf8)] public static partial int gtk_file_chooser_set_current_folder(IntPtr chooser, string folder);
    [LibraryImport(Gtk)] public static partial IntPtr gtk_file_chooser_get_filename(IntPtr chooser);
    [LibraryImport(Gtk)] public static partial int gtk_native_dialog_run(IntPtr dialog);
    [LibraryImport(Gtk, StringMarshalling = StringMarshalling.Utf8)]
    public static partial IntPtr gtk_message_dialog_new(IntPtr parent, int flags, int type, int buttons, string format, string text);
    [LibraryImport(Gtk)] public static partial int gtk_dialog_run(IntPtr dialog);
    [LibraryImport(Gdk)] public static partial IntPtr gdk_display_get_default();
    [LibraryImport(Gdk)] public static partial IntPtr gdk_display_get_primary_monitor(IntPtr display);
    [LibraryImport(Gdk)] public static partial IntPtr gdk_display_get_monitor(IntPtr display, int index);
    [LibraryImport(Gdk)] public static partial void gdk_monitor_get_workarea(IntPtr monitor, out Rectangle area);

    [StructLayout(LayoutKind.Sequential)]
    public struct Rectangle { public int X, Y, Width, Height; }

    // WebKitGTK
    [LibraryImport(WebKit)] public static partial IntPtr webkit_web_context_get_default();
    [LibraryImport(WebKit, StringMarshalling = StringMarshalling.Utf8)]
    public static partial void webkit_web_context_register_uri_scheme(IntPtr context, string scheme, delegate* unmanaged<IntPtr, IntPtr, void> callback, IntPtr data, IntPtr destroy);
    [LibraryImport(WebKit)] public static partial IntPtr webkit_web_context_get_security_manager(IntPtr context);
    [LibraryImport(WebKit, StringMarshalling = StringMarshalling.Utf8)] public static partial void webkit_security_manager_register_uri_scheme_as_secure(IntPtr manager, string scheme);
    [LibraryImport(WebKit, StringMarshalling = StringMarshalling.Utf8)] public static partial void webkit_security_manager_register_uri_scheme_as_cors_enabled(IntPtr manager, string scheme);
    [LibraryImport(WebKit)] public static partial IntPtr webkit_uri_scheme_request_get_path(IntPtr request);
    [LibraryImport(WebKit, StringMarshalling = StringMarshalling.Utf8)]
    public static partial void webkit_uri_scheme_request_finish(IntPtr request, IntPtr stream, long length, string mime);
    [LibraryImport(WebKit)] public static partial void webkit_uri_scheme_request_finish_error(IntPtr request, IntPtr error);
    [LibraryImport(WebKit)] public static partial IntPtr webkit_web_view_new();
    [LibraryImport(WebKit)] public static partial IntPtr webkit_web_view_get_user_content_manager(IntPtr view);
    [LibraryImport(WebKit)] public static partial IntPtr webkit_web_view_get_settings(IntPtr view);
    [LibraryImport(WebKit, StringMarshalling = StringMarshalling.Utf8)] public static partial void webkit_web_view_load_uri(IntPtr view, string uri);
    [LibraryImport(WebKit, StringMarshalling = StringMarshalling.Utf8)]
    public static partial void webkit_web_view_run_javascript(IntPtr view, string script, IntPtr cancellable, IntPtr callback, IntPtr data);
    [LibraryImport(WebKit)] public static partial void webkit_settings_set_enable_developer_extras(IntPtr settings, int enabled);
    [LibraryImport(WebKit)] public static partial void webkit_settings_set_enable_write_console_messages_to_stdout(IntPtr settings, int enabled);
    [LibraryImport(WebKit, StringMarshalling = StringMarshalling.Utf8)]
    public static partial int webkit_user_content_manager_register_script_message_handler(IntPtr manager, string name);
    [LibraryImport(WebKit, StringMarshalling = StringMarshalling.Utf8)]
    public static partial IntPtr webkit_user_script_new(string source, int frames, int time, IntPtr allow, IntPtr block);
    [LibraryImport(WebKit)] public static partial void webkit_user_content_manager_add_script(IntPtr manager, IntPtr script);
    [LibraryImport(WebKit)] public static partial void webkit_user_script_unref(IntPtr script);

    [LibraryImport(WebKit)] public static partial IntPtr webkit_javascript_result_get_js_value(IntPtr result);
    [LibraryImport(WebKit)] public static partial IntPtr webkit_navigation_policy_decision_get_navigation_action(IntPtr decision);
    [LibraryImport(WebKit)] public static partial IntPtr webkit_navigation_action_get_request(IntPtr action);
    [LibraryImport(WebKit)] public static partial IntPtr webkit_uri_request_get_uri(IntPtr request);
    [LibraryImport(WebKit)] public static partial void webkit_policy_decision_ignore(IntPtr decision);
    [LibraryImport(Jsc)] public static partial IntPtr jsc_value_to_string(IntPtr value);

    /// <summary>g_free itself, for GLib to call when it is done with memory it was handed.</summary>
    public static IntPtr GFree => gFree ??= NativeLibrary.GetExport(NativeLibrary.Load(Libraries[GLib][0]), "g_free");
    private static IntPtr? gFree;

    public static string? Utf8(IntPtr text) => text == IntPtr.Zero ? null : Marshal.PtrToStringUTF8(text);

    /// <summary>A string GLib allocated, read and freed.</summary>
    public static string? TakeUtf8(IntPtr text)
    {
        var value = Utf8(text);
        if (text != IntPtr.Zero) g_free(text);
        return value;
    }
}
