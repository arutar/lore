using System.IO;
using System.IO.MemoryMappedFiles;

namespace LoreCompanion;

public enum OverlayState
{
    Normal = 0,
    Modified = 1,
    Staged = 2,
    Conflicted = 3,
    Locked = 4,
    Ignored = 5,
    StagedModified = 6,
}

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

    public static ulong HashPath(string path)
    {
        var normalized = Path.GetFullPath(path).TrimEnd(Path.DirectorySeparatorChar).ToUpperInvariant();
        var hash = 14695981039346656037UL;
        foreach (var character in normalized)
        {
            hash ^= character;
            hash *= 1099511628211UL;
        }
        return hash;
    }

    public void Dispose()
    {
        file.Dispose();
        mutex.Dispose();
    }
}
