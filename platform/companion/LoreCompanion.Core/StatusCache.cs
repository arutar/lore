namespace LoreCompanion;

/// <summary>The state a file manager shows on a file or folder, in the values both hosts' caches
/// store. Folders take the strongest state of what they hold.</summary>
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

/// <summary>Where an operation records the states it learned, for the file manager to show. Each
/// host supplies its own: Explorer's shared memory on Windows, a file in the session's runtime
/// directory on Linux.</summary>
public interface IStatusCache
{
    /// <summary>Records <paramref name="state"/> for <paramref name="relativePath"/> under the working
    /// copy at <paramref name="root"/>; <see cref="OverlayState.Normal"/> removes it.</summary>
    void Set(string root, string relativePath, OverlayState state);

    /// <summary>Forgets every state recorded under the working copy at <paramref name="root"/>.</summary>
    void Clear(string root);

    /// <summary>Tells the file manager the states under <paramref name="root"/> changed, so open
    /// windows redraw <paramref name="paths"/> and the folders now rather than on the next refresh.</summary>
    void Changed(string root, IEnumerable<string> paths);
}

public static class StatusCache
{
    /// <summary>The host's cache. Until a host sets one, states are not recorded anywhere.</summary>
    public static IStatusCache Current { get; set; } = new None();

    private sealed class None : IStatusCache
    {
        public void Set(string root, string relativePath, OverlayState state) { }
        public void Clear(string root) { }
        public void Changed(string root, IEnumerable<string> paths) { }
    }
}

/// <summary>How paths compare on this system: without regard to case on Windows, exactly on Linux.</summary>
public static class PathText
{
    public static readonly StringComparer Comparer = OperatingSystem.IsWindows() ? StringComparer.OrdinalIgnoreCase : StringComparer.Ordinal;

    public static readonly StringComparison Comparison = OperatingSystem.IsWindows() ? StringComparison.OrdinalIgnoreCase : StringComparison.Ordinal;

    public static readonly char Separator = Path.DirectorySeparatorChar;

    /// <summary>A path Lore reports, which separates with '/', in this system's separators.</summary>
    public static string Native(string path) => Separator == '/' ? path : path.Replace('/', Separator);

    /// <summary>The 64-bit FNV-1a hash of the full path, which names a working copy in the caches. On
    /// Windows it ignores case, as the file system does.</summary>
    public static ulong Hash(string path)
    {
        var normalized = Path.GetFullPath(path).TrimEnd(Separator);
        if (OperatingSystem.IsWindows()) normalized = normalized.ToUpperInvariant();
        var hash = 14695981039346656037UL;
        foreach (var character in normalized)
        {
            hash ^= character;
            hash *= 1099511628211UL;
        }
        return hash;
    }
}
