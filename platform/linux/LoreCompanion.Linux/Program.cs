using System.Text;
using static LoreCompanion.Native;

[assembly: System.Runtime.Versioning.SupportedOSPlatform("linux")]

namespace LoreCompanion;

internal static class Program
{
    public const int MaximumRequestBytes = 1_048_576;

    /// <summary>The GTK main loop's context, for work handed over from other threads.</summary>
    public static readonly GLibContext MainLoop = new();

    private static int Main(string[] args)
    {
        Register();
        if (gtk_init_check(IntPtr.Zero, IntPtr.Zero) == 0)
        {
            Console.Error.WriteLine("Lore Companion needs a graphical session.");
            return 1;
        }
        SynchronizationContext.SetSynchronizationContext(MainLoop);
        try
        {
            if (args.Length != 2 || args[0] != "--request")
                throw new InvalidOperationException("Use the Lore menu in your file manager to start an operation.");
            var request = OperationRequest.Parse(ReadRequest(args[1]));
            if (FileStatusCache.ForSession() is { } cache) StatusCache.Current = cache;
            _ = new LinuxOperationWindow(request);
            gtk_main();
            return 0;
        }
        catch (Exception exception)
        {
            OperationSession.Log($"start failed: {exception}");
            ShowError(exception.Message);
            return 1;
        }
    }

    /// <summary>Reads and deletes the request a file manager plugin wrote. Only a private file in this
    /// user's runtime directory is accepted, so no other user can make the companion act.</summary>
    internal static string ReadRequest(string path)
    {
        var runtime = Environment.GetEnvironmentVariable("XDG_RUNTIME_DIR");
        if (string.IsNullOrEmpty(runtime)) throw new InvalidOperationException("XDG_RUNTIME_DIR is not set.");
        var folder = Path.GetFullPath(Path.Combine(runtime, "lore"));
        var full = Path.GetFullPath(path);
        if (Path.GetDirectoryName(full) != folder) throw new InvalidDataException("The request is not in the Lore runtime folder.");
        var info = new FileInfo(full);
        if (!info.Exists || info.LinkTarget is not null) throw new InvalidDataException("The request file is missing.");
        const UnixFileMode others = UnixFileMode.GroupRead | UnixFileMode.GroupWrite | UnixFileMode.GroupExecute
            | UnixFileMode.OtherRead | UnixFileMode.OtherWrite | UnixFileMode.OtherExecute;
        if ((File.GetUnixFileMode(folder) & others) != 0 || (info.UnixFileMode & others) != 0)
            throw new InvalidDataException("The request file can be read by other users.");
        if (info.Length <= 0 || info.Length > MaximumRequestBytes) throw new InvalidDataException("Invalid request size");
        try { return File.ReadAllText(full, Encoding.UTF8); }
        finally { File.Delete(full); }
    }

    private static void ShowError(string message)
    {
        var dialog = gtk_message_dialog_new(IntPtr.Zero, 1, 3, 1, "%s", message); // modal, error, OK
        gtk_dialog_run(dialog);
        gtk_widget_destroy(dialog);
    }
}
