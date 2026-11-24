using System.Buffers.Binary;

namespace LoreCompanion;

/// <summary>The status cache the file manager extension reads on Linux: a file in the session's
/// runtime directory, <c>$XDG_RUNTIME_DIR/lore/status-cache</c>, laid out as Explorer's shared memory
/// on Windows. A 16-byte header holds the entry count, <see cref="Magic"/> and a generation that grows
/// with every write; then up to <see cref="Capacity"/> entries of 24 bytes: the path's
/// <see cref="PathText.Hash"/>, the <see cref="OverlayState"/>, the time it was written and the hash of
/// the working copy's root.
///
/// An operation's states collect here and reach the file together in <see cref="Changed"/>, which
/// every operation calls when it is done: the file is replaced whole, by a rename, so a reader never
/// sees half of a write, and the replacement is what tells the extension to redraw. Writers take an
/// advisory lock on <c>status-cache.lock</c> and apply their changes to the file as it is then, so two
/// companions don't undo each other's states.</summary>
public sealed class FileStatusCache : IStatusCache
{
    public const int Capacity = 4096;
    public const int EntrySize = 24;
    public const int HeaderSize = 16;
    /// <summary>"LSC1": a file without it, from another version or damaged, is treated as empty.</summary>
    public const uint Magic = 0x3143534C;

    private readonly string folder;
    private readonly List<(string Root, string Path, OverlayState State, bool Clear)> pending = [];

    public FileStatusCache(string folder) => this.folder = folder;

    /// <summary>The cache of this session, or null without a runtime directory.</summary>
    public static FileStatusCache? ForSession()
    {
        var runtime = Environment.GetEnvironmentVariable("XDG_RUNTIME_DIR");
        return string.IsNullOrEmpty(runtime) ? null : new FileStatusCache(Path.Combine(runtime, "lore"));
    }

    public string FilePath => Path.Combine(folder, "status-cache");

    public void Set(string root, string relativePath, OverlayState state)
    {
        lock (pending) pending.Add((root, relativePath, state, false));
    }

    public void Clear(string root)
    {
        lock (pending) pending.Add((root, "", OverlayState.Normal, true));
    }

    public void Changed(string root, IEnumerable<string> paths)
    {
        try { Flush(); }
        catch (Exception error) when (error is IOException or UnauthorizedAccessException or TimeoutException)
        {
            // Only the file manager's icons: they catch up with the next operation.
            OperationSession.Log($"status cache not written: {error.Message}");
        }
    }

    /// <summary>Writes the collected states to the file.</summary>
    public void Flush()
    {
        List<(string Root, string Path, OverlayState State, bool Clear)> changes;
        lock (pending)
        {
            if (pending.Count == 0) return;
            changes = [.. pending];
            pending.Clear();
        }
        Directory.CreateDirectory(folder);
        if (!OperatingSystem.IsWindows()) File.SetUnixFileMode(folder, UnixFileMode.UserRead | UnixFileMode.UserWrite | UnixFileMode.UserExecute);
        using var guard = Lock();
        var data = Read(FilePath);
        foreach (var change in changes)
        {
            if (change.Clear) ClearRoot(data, RootHash(change.Root));
            else SetEntry(data, change.Root, change.Path, change.State);
        }
        BinaryPrimitives.WriteUInt32LittleEndian(data.AsSpan(4), Magic);
        BinaryPrimitives.WriteUInt64LittleEndian(data.AsSpan(8), BinaryPrimitives.ReadUInt64LittleEndian(data.AsSpan(8)) + 1);
        var temporary = Path.Combine(folder, $".status-cache.{Environment.ProcessId}");
        using (var stream = new FileStream(temporary, Private(new FileStreamOptions { Mode = FileMode.Create, Access = FileAccess.Write })))
        {
            stream.Write(data);
        }
        File.Move(temporary, FilePath, true);
    }

    // FileShare.None takes an advisory lock on Linux; another companion writing waits for it.
    private FileStream Lock()
    {
        var deadline = Environment.TickCount64 + 2000;
        while (true)
        {
            try
            {
                return new FileStream(Path.Combine(folder, "status-cache.lock"),
                    Private(new FileStreamOptions { Mode = FileMode.OpenOrCreate, Access = FileAccess.ReadWrite, Share = FileShare.None }));
            }
            catch (IOException) when (Environment.TickCount64 < deadline)
            {
                Thread.Sleep(20);
            }
        }
    }

    // Files only this user may read, where the system has such modes.
    private static FileStreamOptions Private(FileStreamOptions options)
    {
        if (!OperatingSystem.IsWindows()) options.UnixCreateMode = UnixFileMode.UserRead | UnixFileMode.UserWrite;
        return options;
    }

    /// <summary>The cache as it is on disk, or an empty one when the file is missing or not one of ours.</summary>
    public static byte[] Read(string path)
    {
        var data = new byte[HeaderSize + Capacity * EntrySize];
        try
        {
            var existing = File.ReadAllBytes(path);
            if (existing.Length == data.Length && BinaryPrimitives.ReadUInt32LittleEndian(existing.AsSpan(4)) == Magic)
                existing.CopyTo(data, 0);
        }
        catch (FileNotFoundException) { }
        catch (DirectoryNotFoundException) { }
        return data;
    }

    private static uint RootHash(string root) => unchecked((uint)PathText.Hash(root));

    private static int Count(byte[] data) => Math.Clamp(BinaryPrimitives.ReadInt32LittleEndian(data), 0, Capacity);

    private static void ClearRoot(byte[] data, uint rootHash)
    {
        for (var index = 0; index < Count(data); index++)
        {
            var entry = data.AsSpan(HeaderSize + index * EntrySize, EntrySize);
            if (BinaryPrimitives.ReadUInt32LittleEndian(entry[20..]) == rootHash) entry.Clear();
        }
    }

    // As OverlayCache.Set on Windows: the path's entry is replaced, Normal removes it, and a full
    // cache drops new states rather than old ones.
    private static void SetEntry(byte[] data, string root, string relativePath, OverlayState state)
    {
        var hash = PathText.Hash(string.IsNullOrEmpty(relativePath) ? root : Path.Combine(root, relativePath));
        var count = Count(data);
        var free = -1;
        for (var index = 0; index < count; index++)
        {
            var entry = data.AsSpan(HeaderSize + index * EntrySize, EntrySize);
            var existing = BinaryPrimitives.ReadUInt64LittleEndian(entry);
            if (existing == hash)
            {
                if (state == OverlayState.Normal) entry.Clear();
                else Write(entry, hash, state, RootHash(root));
                return;
            }
            if (existing == 0 && free < 0) free = index;
        }
        if (state == OverlayState.Normal || count >= Capacity && free < 0) return;
        var slot = free >= 0 ? free : count;
        Write(data.AsSpan(HeaderSize + slot * EntrySize, EntrySize), hash, state, RootHash(root));
        if (free < 0) BinaryPrimitives.WriteInt32LittleEndian(data, count + 1);
    }

    private static void Write(Span<byte> entry, ulong hash, OverlayState state, uint rootHash)
    {
        BinaryPrimitives.WriteUInt64LittleEndian(entry, hash);
        BinaryPrimitives.WriteInt32LittleEndian(entry[8..], (int)state);
        BinaryPrimitives.WriteInt64LittleEndian(entry[12..], DateTime.UtcNow.Ticks);
        BinaryPrimitives.WriteUInt32LittleEndian(entry[20..], rootHash);
    }
}
