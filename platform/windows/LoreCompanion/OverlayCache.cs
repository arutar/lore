using System.IO;
using System.IO.MemoryMappedFiles;

namespace LoreCompanion;

public sealed class OverlayCache : IDisposable
{
    public const int Capacity = 4096;
    private const int EntrySize = 24;
    private const string MapName = @"Local\Lore.OverlayCache";
    private readonly MemoryMappedFile file = MemoryMappedFile.CreateOrOpen(MapName, 16 + Capacity * EntrySize);
    private readonly Mutex mutex = new(false, @"Local\Lore.OverlayCacheMutex");

    public void ClearRepository(string root)
    {
        var rootHash = RootHash(root);
        if (!mutex.WaitOne(TimeSpan.FromSeconds(2)))
        {
            throw new TimeoutException("Overlay cache is busy");
        }
        try
        {
            using var view = file.CreateViewAccessor(0, 16 + Capacity * EntrySize, MemoryMappedFileAccess.ReadWrite);
            var count = Math.Clamp(view.ReadInt32(0), 0, Capacity);
            for (var index = 0; index < count; index++)
            {
                var offset = 16 + index * EntrySize;
                if (view.ReadUInt32(offset + 20) == rootHash)
                {
                    ClearEntry(view, offset);
                }
            }
        }
        finally
        {
            mutex.ReleaseMutex();
        }
    }

    public void Set(string root, string relativePath, OverlayState state)
    {
        var fullPath = string.IsNullOrEmpty(relativePath) ? root : Path.Combine(root, relativePath);
        var hash = HashPath(fullPath);
        var rootHash = RootHash(root);
        if (!mutex.WaitOne(TimeSpan.FromSeconds(2)))
        {
            throw new TimeoutException("Overlay cache is busy");
        }
        try
        {
            using var view = file.CreateViewAccessor(0, 16 + Capacity * EntrySize, MemoryMappedFileAccess.ReadWrite);
            var count = Math.Clamp(view.ReadInt32(0), 0, Capacity);
            var freeOffset = -1;
            for (var index = 0; index < count; index++)
            {
                var offset = 16 + index * EntrySize;
                var existingHash = view.ReadUInt64(offset);
                if (existingHash == hash)
                {
                    if (state == OverlayState.Normal)
                    {
                        ClearEntry(view, offset);
                    }
                    else
                    {
                        WriteEntry(view, offset, hash, state, rootHash);
                    }
                    return;
                }
                if (existingHash == 0 && freeOffset < 0)
                {
                    freeOffset = offset;
                }
            }
            if (state == OverlayState.Normal || count >= Capacity && freeOffset < 0)
            {
                return;
            }
            var next = freeOffset >= 0 ? freeOffset : 16 + count * EntrySize;
            WriteEntry(view, next, hash, state, rootHash);
            if (freeOffset < 0)
            {
                view.Write(0, count + 1);
            }
        }
        finally
        {
            mutex.ReleaseMutex();
        }
    }

    private static uint RootHash(string root) => unchecked((uint)HashPath(root));

    private static void WriteEntry(UnmanagedMemoryAccessor view, int offset, ulong hash, OverlayState state, uint rootHash)
    {
        view.Write(offset, hash);
        view.Write(offset + 8, (int)state);
        view.Write(offset + 12, DateTime.UtcNow.Ticks);
        view.Write(offset + 20, rootHash);
    }

    private static void ClearEntry(UnmanagedMemoryAccessor view, int offset)
    {
        view.Write(offset, 0UL);
        view.Write(offset + 8, 0);
        view.Write(offset + 12, 0L);
        view.Write(offset + 20, 0U);
    }

    public static ulong HashPath(string path) => PathText.Hash(path);

    public void Dispose()
    {
        file.Dispose();
        mutex.Dispose();
    }
}

/// <summary>Explorer's side of the status cache: the overlay states go to the shared memory the
/// shell extension reads, and Explorer is told to redraw.</summary>
internal sealed class ExplorerStatusCache : IStatusCache
{
    private readonly Lazy<OverlayCache> cache = new();

    public void Set(string root, string relativePath, OverlayState state) => cache.Value.Set(root, relativePath, state);

    public void Clear(string root) => cache.Value.ClearRepository(root);

    public void Changed(string root, IEnumerable<string> paths) => ShellNotify.RepositoryChanged(root, paths);
}

// Tells Explorer that overlays under a working copy changed, so open windows redraw them at once
// instead of after the next manual refresh. Every folder is announced (a file whose state was cleared
// isn't in the new status, and its folder redraw picks that up), plus the paths the operation touched.
internal static class ShellNotify
{
    private const int UpdateItem = 0x00002000; // SHCNE_UPDATEITEM
    private const int UpdateDirectory = 0x00001000; // SHCNE_UPDATEDIR
    private const uint PathAndNoWait = 0x0005 | 0x3000; // SHCNF_PATHW | SHCNF_FLUSHNOWAIT
    private const int MaximumFolders = 5000;

    [System.Runtime.InteropServices.DllImport("shell32.dll", CharSet = System.Runtime.InteropServices.CharSet.Unicode)]
    private static extern void SHChangeNotify(int eventId, uint flags, string item1, IntPtr item2);

    public static void RepositoryChanged(string root, IEnumerable<string> paths)
    {
        try
        {
            var folders = new List<string> { root };
            var options = new EnumerationOptions { RecurseSubdirectories = true, IgnoreInaccessible = true, AttributesToSkip = FileAttributes.ReparsePoint };
            foreach (var folder in Directory.EnumerateDirectories(root, "*", options))
            {
                if (folder.Contains($"{Path.DirectorySeparatorChar}.lore", StringComparison.OrdinalIgnoreCase)) continue;
                folders.Add(folder);
                if (folders.Count >= MaximumFolders) break;
            }
            foreach (var path in paths) SHChangeNotify(UpdateItem, PathAndNoWait, path, IntPtr.Zero);
            foreach (var folder in folders) SHChangeNotify(UpdateDirectory, PathAndNoWait, folder, IntPtr.Zero);
        }
        catch (Exception)
        {
            // Only a redraw hint: the overlays are already in the cache and appear on the next refresh.
        }
    }
}
