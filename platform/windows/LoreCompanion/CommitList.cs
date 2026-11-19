namespace LoreCompanion;

/// <summary>A folder row the Commit window draws: how many files with a change sit below it,
/// split by kind, and how many of them are ticked.</summary>
public sealed record CommitFolder(string Path, string Name, int Depth, int Files, int Added, int Modified, int Deleted, int Locked, int Selected);

/// <summary>The changed files the Commit window's tree holds for one folder, and what the page
/// already has: <paramref name="Offset"/> is where the returned slice starts.</summary>
public sealed record CommitPage(string Folder, string Filter, int Offset, int Total, int Selected, IReadOnlyList<CommitCandidate> Items);

/// <summary>Everything the Commit operation would record, folded into folders, with the boxes the
/// user ticked. The window asks for one page of files at a time, so a working copy with hundreds of
/// thousands of changes never travels to the page in one message: the folder tree is all it gets
/// up front, and each folder's files on demand.</summary>
public sealed class CommitList
{
    /// <summary>Files up to this count are handed out whole with the tree: the window then shows
    /// the list the way it always did, without waiting for a page.</summary>
    public const int WholeListFrom = 500;

    /// <summary>Files per page when a folder is opened.</summary>
    public const int PageSize = 200;

    private readonly List<CommitCandidate> files;
    private readonly Node root = new("", "", -1);
    // Paths left out of the commit; the empty set means "everything", which is what the window
    // starts with.
    private readonly HashSet<string> excluded = new(StringComparer.OrdinalIgnoreCase);

    private readonly HashSet<string> lockedPaths = new(StringComparer.OrdinalIgnoreCase);

    private sealed class Node(string path, string name, int depth)
    {
        public string Path { get; set; } = path;
        public string Name { get; set; } = name;
        public int Depth { get; } = depth;
        public List<Node> Folders { get; } = [];
        public List<int> Files { get; } = [];
        public int Count { get; set; }
        public int Added { get; set; }
        public int Modified { get; set; }
        public int Deleted { get; set; }
        public int Locked { get; set; }
        public int Selected { get; set; }
    }

    public CommitList(IEnumerable<CommitCandidate> candidates)
    {
        files = candidates.ToList();
        var nodes = new Dictionary<string, Node>(StringComparer.OrdinalIgnoreCase) { [""] = root };
        for (var index = 0; index < files.Count; index++)
        {
            if (files[index].LockedBy.Length > 0) excluded.Add(files[index].Path);
            var parts = files[index].Path.Split('\\', '/', StringSplitOptions.RemoveEmptyEntries);
            var node = root;
            for (var depth = 0; depth < parts.Length - 1; depth++)
            {
                var path = string.Join('\\', parts[..(depth + 1)]);
                if (!nodes.TryGetValue(path, out var child))
                {
                    child = new Node(path, parts[depth], depth);
                    nodes[path] = child;
                    node.Folders.Add(child);
                }
                node = child;
            }
            node.Files.Add(index);
        }
        lockedPaths.UnionWith(excluded);
        Fold(root);
        Compact(root);
    }

    // Counts every folder once, bottom-up: the tree pass is linear in files and folders.
    private void Fold(Node node)
    {
        foreach (var child in node.Folders) Fold(child);
        node.Count = node.Files.Count + node.Folders.Sum(child => child.Count);
        node.Added = node.Files.Count(index => files[index].Change == "added") + node.Folders.Sum(child => child.Added);
        node.Modified = node.Files.Count(index => files[index].Change == "modified") + node.Folders.Sum(child => child.Modified);
        node.Deleted = node.Files.Count(index => files[index].Change == "deleted") + node.Folders.Sum(child => child.Deleted);
        node.Locked = node.Files.Count(index => files[index].LockedBy.Length > 0) + node.Folders.Sum(child => child.Locked);
        node.Selected = node.Files.Count(index => !excluded.Contains(files[index].Path)) + node.Folders.Sum(child => child.Selected);
    }

    // "art" holding only "textures" reads as one folder, "art\textures", the way it always did.
    private static void Compact(Node node)
    {
        for (var index = 0; index < node.Folders.Count; index++)
        {
            var current = node.Folders[index];
            while (current.Files.Count == 0 && current.Folders.Count == 1)
            {
                var only = current.Folders[0];
                current.Name = $"{current.Name}\\{only.Name}";
                current.Files.AddRange(only.Files);
                current.Folders.Clear();
                current.Folders.AddRange(only.Folders);
            }
            // A merged row keeps the path the page uses to ask for its files: rebuild it, and the
            // paths below it, from names as they read now.
            Rebase(current, node.Path.Length == 0 ? current.Name : $"{node.Path}\\{current.Name}");
            Compact(current);
        }
    }

    private static void Rebase(Node node, string path)
    {
        node.Path = path;
        foreach (var child in node.Folders) Rebase(child, $"{path}\\{child.Name}");
    }

    public int Total => files.Count;

    /// <summary>Files that may be committed: everything but what someone else has locked.</summary>
    public int Eligible => files.Count - root.Locked;

    public int Selected => files.Count - excluded.Count;

    public bool All => excluded.Count == 0;

    /// <summary>Whether the box next to <paramref name="path"/> is ticked.</summary>
    public bool IsSelected(string path) => !excluded.Contains(path);

    /// <summary>True when fewer boxes are off than on: the commit then stages the selected paths and
    /// takes the few out, instead of naming hundreds of thousands of files one by one.</summary>
    public bool MostlySelected => excluded.Count < Selected;

    /// <summary>The files left out, for the operation to unstage after staging the paths.</summary>
    public string[] ExcludedFiles() => excluded.ToArray();

    /// <summary>The folder tree, flattened in display order, so the page can draw every row
    /// without walking it itself. Files at the top of the repository belong to the root row, which
    /// is not a folder and has no entry here.</summary>
    public List<CommitFolder> Folders()
    {
        var rows = new List<CommitFolder>();
        void Walk(Node node)
        {
            foreach (var child in node.Folders.OrderBy(child => child.Name, StringComparer.OrdinalIgnoreCase))
            {
                rows.Add(new CommitFolder(child.Path, child.Name, child.Depth, child.Count, child.Added,
                    child.Modified, child.Deleted, child.Locked, child.Selected));
                Walk(child);
            }
        }
        Walk(root);
        return rows;
    }

    /// <summary>Every changed file, for a list small enough to show whole.</summary>
    public List<CommitCandidate> AllFiles() => files;

    /// <summary>One page of the changed files directly in <paramref name="folder"/> (the empty
    /// path means the top of the repository) whose path contains <paramref name="filter"/>.</summary>
    public CommitPage Page(string folder, string filter, int offset, int limit)
    {
        var node = Find(folder ?? "");
        if (node is null) return new CommitPage(folder ?? "", filter ?? "", 0, 0, 0, []);
        var needle = (filter ?? "").Trim();
        IEnumerable<int> indexes = node.Files;
        if (needle.Length > 0) indexes = indexes.Where(index => files[index].Path.Contains(needle, StringComparison.OrdinalIgnoreCase));
        var matched = indexes.ToList();
        var start = Math.Clamp(offset, 0, matched.Count);
        var selected = matched.Count(index => !excluded.Contains(files[index].Path));
        return new CommitPage(node.Path, needle, start, matched.Count, selected,
            matched.Skip(start).Take(Count(limit)).Select(index => files[index]).ToList());
    }

    /// <summary>Every changed file whose path contains <paramref name="filter"/>, for the window's
    /// filter field; <paramref name="offset"/> and <paramref name="limit"/> work as in
    /// <see cref="Page"/>.</summary>
    public CommitPage Search(string filter, int offset, int limit)
    {
        var needle = (filter ?? "").Trim();
        IEnumerable<CommitCandidate> matched = files;
        if (needle.Length > 0) matched = files.Where(file => file.Path.Contains(needle, StringComparison.OrdinalIgnoreCase));
        var list = matched.ToList();
        var start = Math.Clamp(offset, 0, list.Count);
        var selected = list.Count(file => !excluded.Contains(file.Path));
        return new CommitPage("", needle, start, list.Count, selected, list.Skip(start).Take(Count(limit)).ToList());
    }

    private static int Count(int limit) => Math.Clamp(limit <= 0 ? PageSize : limit, 1, PageSize * 10);

    /// <summary>Takes in or leaves out every file below <paramref name="folder"/> that may be
    /// committed. Files someone else has locked stay out: Commit would refuse them.</summary>
    public void SelectFolder(string folder, bool on)
    {
        var node = Find(folder ?? "");
        if (node is null) return;
        foreach (var index in FilesBelow(node))
        {
            var file = files[index];
            if (file.LockedBy.Length > 0) continue;
            if (on) excluded.Remove(file.Path); else excluded.Add(file.Path);
        }
        Recount(root);
    }

    public void SelectFile(string path, bool on)
    {
        var wanted = (path ?? "").Replace('/', '\\').Trim('\\');
        var index = files.FindIndex(file => file.Path.Equals(wanted, StringComparison.OrdinalIgnoreCase));
        if (index < 0 || files[index].LockedBy.Length > 0) return;
        if (on) excluded.Remove(files[index].Path); else excluded.Add(files[index].Path);
        Recount(root);
    }

    public void SelectAll(bool on)
    {
        if (on) excluded.RemoveWhere(path => !lockedPaths.Contains(path));
        else foreach (var file in files) if (file.LockedBy.Length == 0) excluded.Add(file.Path);
        Recount(root);
    }

    /// <summary>The paths to commit, or null when everything is ticked: the operation then stages
    /// the selected paths as they are instead of listing each file.</summary>
    public string[]? ResultFiles()
    {
        if (excluded.Count == 0) return null;
        return files.Where(file => !excluded.Contains(file.Path) && file.LockedBy.Length == 0).Select(file => file.Path).ToArray();
    }

    private void Recount(Node node)
    {
        foreach (var child in node.Folders) Recount(child);
        node.Selected = node.Files.Count(index => !excluded.Contains(files[index].Path)) + node.Folders.Sum(child => child.Selected);
    }

    private IEnumerable<int> FilesBelow(Node node)
    {
        foreach (var index in node.Files) yield return index;
        foreach (var child in node.Folders)
            foreach (var index in FilesBelow(child)) yield return index;
    }

    private Node? Find(string folder)
    {
        var normalized = folder.Replace('/', '\\').Trim('\\');
        if (normalized.Length == 0) return root;
        return Find(root, normalized);
    }

    private static Node? Find(Node node, string path)
    {
        foreach (var child in node.Folders)
        {
            if (child.Path.Equals(path, StringComparison.OrdinalIgnoreCase)) return child;
            var found = Find(child, path);
            if (found is not null) return found;
        }
        return null;
    }
}
