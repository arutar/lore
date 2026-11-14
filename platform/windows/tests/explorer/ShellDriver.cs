using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Linq;
using System.Runtime.InteropServices;
using System.Text;
using System.Threading;

// Drives Lore's Explorer integration the way a person does: the context menu Explorer builds for
// the selected items (all registered handlers, LoreShell among them), the Companion window it
// opens, and the overlay handlers Explorer asks which icon to draw.
public static class ShellDriver
{
    [ComImport, Guid("000214E6-0000-0000-C000-000000000046"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
    interface IShellFolder
    {
        void ParseDisplayName(IntPtr hwnd, IntPtr bindContext, [MarshalAs(UnmanagedType.LPWStr)] string name, out uint eaten, out IntPtr pidl, ref uint attributes);
        void EnumObjects(IntPtr hwnd, int flags, out IntPtr enumIdList);
        void BindToObject(IntPtr pidl, IntPtr bindContext, ref Guid riid, out IntPtr result);
        void BindToStorage(IntPtr pidl, IntPtr bindContext, ref Guid riid, out IntPtr result);
        [PreserveSig] int CompareIDs(IntPtr lParam, IntPtr pidl1, IntPtr pidl2);
        void CreateViewObject(IntPtr hwnd, ref Guid riid, out IntPtr result);
        void GetAttributesOf(uint count, [MarshalAs(UnmanagedType.LPArray)] IntPtr[] pidls, ref uint attributes);
        void GetUIObjectOf(IntPtr hwnd, uint count, [MarshalAs(UnmanagedType.LPArray)] IntPtr[] pidls, ref Guid riid, IntPtr reserved, [MarshalAs(UnmanagedType.Interface)] out object result);
    }

    [ComImport, Guid("000214E4-0000-0000-C000-000000000046"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
    interface IContextMenu
    {
        [PreserveSig] int QueryContextMenu(IntPtr menu, uint index, uint first, uint last, uint flags);
        [PreserveSig] int InvokeCommand(ref CMINVOKECOMMANDINFO info);
        [PreserveSig] int GetCommandString(UIntPtr command, uint flags, IntPtr reserved, IntPtr name, uint size);
    }

    [StructLayout(LayoutKind.Sequential)]
    struct CMINVOKECOMMANDINFO
    {
        public int cbSize; public int fMask; public IntPtr hwnd; public IntPtr lpVerb;
        public IntPtr lpParameters; public IntPtr lpDirectory; public int nShow; public int dwHotKey; public IntPtr hIcon;
    }

    [ComImport, Guid("0C6C4200-C589-11D0-999A-00C04FD655E1"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
    interface IShellIconOverlayIdentifier
    {
        [PreserveSig] int IsMemberOf([MarshalAs(UnmanagedType.LPWStr)] string path, uint attributes);
        [PreserveSig] int GetOverlayInfo(IntPtr path, int size, out int index, out uint flags);
        [PreserveSig] int GetPriority(out int priority);
    }

    [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
    struct MENUITEMINFO
    {
        public int cbSize; public uint fMask; public uint fType; public uint fState; public uint wID; public IntPtr hSubMenu;
        public IntPtr hbmpChecked; public IntPtr hbmpUnchecked; public IntPtr dwItemData; public IntPtr dwTypeData; public uint cch; public IntPtr hbmpItem;
    }

    [DllImport("shell32.dll", CharSet = CharSet.Unicode)] static extern int SHParseDisplayName(string name, IntPtr bindContext, out IntPtr pidl, uint sfgaoIn, out uint sfgaoOut);
    [DllImport("shell32.dll")] static extern int SHBindToObject(IntPtr parent, IntPtr pidl, IntPtr bindContext, ref Guid riid, [MarshalAs(UnmanagedType.Interface)] out object result);
    [DllImport("ole32.dll")] static extern void CoTaskMemFree(IntPtr pointer);
    [DllImport("user32.dll")] static extern IntPtr CreatePopupMenu();
    [DllImport("user32.dll")] static extern bool DestroyMenu(IntPtr menu);
    [DllImport("user32.dll")] static extern int GetMenuItemCount(IntPtr menu);
    [DllImport("user32.dll", CharSet = CharSet.Unicode)] static extern bool GetMenuItemInfoW(IntPtr menu, uint item, bool byPosition, ref MENUITEMINFO info);

    static readonly Guid ShellFolderId = new("000214E6-0000-0000-C000-000000000046");
    static readonly Guid ContextMenuId = new("000214E4-0000-0000-C000-000000000046");

    // Runs on an STA thread, as Explorer's own menu code does.
    static T Sta<T>(Func<T> work)
    {
        T result = default!;
        Exception? failure = null;
        var thread = new Thread(() => { try { result = work(); } catch (Exception e) { failure = e; } });
        thread.SetApartmentState(ApartmentState.STA);
        thread.Start();
        thread.Join();
        if (failure != null) throw failure;
        return result;
    }

    /// <summary>Right-clicks the items (all in one folder) and chooses the menu item with the given
    /// text, looking through submenus. Returns every item text seen, for diagnostics.</summary>
    public static string Invoke(string[] paths, string itemText)
    {
        return Sta(() =>
        {
            var parent = Path.GetDirectoryName(paths[0].TrimEnd('\\'))!;
            Check(SHParseDisplayName(parent, IntPtr.Zero, out var parentPidl, 0, out _), "parse " + parent);
            var folderId = ShellFolderId;
            Check(SHBindToObject(IntPtr.Zero, parentPidl, IntPtr.Zero, ref folderId, out var folderObject), "bind " + parent);
            var folder = (IShellFolder)folderObject;
            var children = paths.Select(path =>
            {
                uint attributes = 0;
                folder.ParseDisplayName(IntPtr.Zero, IntPtr.Zero, Path.GetFileName(path.TrimEnd('\\')), out _, out var child, ref attributes);
                return child;
            }).ToArray();
            var menuId = ContextMenuId;
            folder.GetUIObjectOf(IntPtr.Zero, (uint)children.Length, children, ref menuId, IntPtr.Zero, out var menuObject);
            var contextMenu = (IContextMenu)menuObject;
            var menu = CreatePopupMenu();
            try
            {
                Check(contextMenu.QueryContextMenu(menu, 0, 1, 0x7FFF, 0), "QueryContextMenu");
                var seen = new List<string>();
                var id = Find(menu, itemText, seen);
                if (id == 0) throw new InvalidOperationException($"No \"{itemText}\" in the menu: {string.Join(" | ", seen)}");
                var info = new CMINVOKECOMMANDINFO { cbSize = Marshal.SizeOf<CMINVOKECOMMANDINFO>(), lpVerb = (IntPtr)(id - 1), nShow = 1 };
                Check(contextMenu.InvokeCommand(ref info), "InvokeCommand " + itemText);
                // LoreShell starts the Companion from a worker thread of this process.
                Thread.Sleep(1500);
                return string.Join(" | ", seen);
            }
            finally
            {
                DestroyMenu(menu);
                foreach (var child in children) CoTaskMemFree(child);
                CoTaskMemFree(parentPidl);
            }
        });
    }

    static uint Find(IntPtr menu, string text, List<string> seen)
    {
        var count = GetMenuItemCount(menu);
        for (uint position = 0; position < count; position++)
        {
            var buffer = Marshal.AllocCoTaskMem(1024);
            try
            {
                var info = new MENUITEMINFO { cbSize = Marshal.SizeOf<MENUITEMINFO>(), fMask = 0x2 | 0x4 | 0x40, dwTypeData = buffer, cch = 500 };
                if (!GetMenuItemInfoW(menu, position, true, ref info)) continue;
                var label = Marshal.PtrToStringUni(buffer) ?? "";
                if (label.Length > 0) seen.Add(label.Replace("&", ""));
                if (info.hSubMenu != IntPtr.Zero)
                {
                    var inner = Find(info.hSubMenu, text, seen);
                    if (inner != 0) return inner;
                }
                else if (label.Replace("&", "") == text) return info.wID;
            }
            finally { Marshal.FreeCoTaskMem(buffer); }
        }
        return 0;
    }

    static void Check(int hresult, string what)
    {
        if (hresult < 0) throw new InvalidOperationException($"{what} failed: 0x{hresult:X8}");
    }

    static readonly (string Name, Guid Id)[] Overlays =
    {
        ("Modified", new Guid("b056f7b6-3445-46d0-b3e3-01573d936aba")),
        ("Staged", new Guid("95ecca15-ad76-4802-8f4e-37e967bf7139")),
        ("Conflicted", new Guid("9dcc2578-1112-455a-9af7-d28f18cea2b1")),
        ("Locked", new Guid("224f8e7b-61ba-4da3-97d3-6092d9c47a0b")),
        ("Ignored", new Guid("902208de-1b5a-462f-9aa7-a409d88b8042")),
        ("StagedModified", new Guid("80444c1d-93fc-4241-9af5-7eff9280985e")),
        ("Clean", new Guid("e3d52a22-7739-4a37-a35f-47a1d00f8d6f")),
    };

    /// <summary>The overlay(s) Explorer would draw on the path: every Lore handler that claims it.
    /// Empty when none does (a plain icon).</summary>
    public static string Overlay(string path)
    {
        return Sta(() =>
        {
            var claimed = new List<string>();
            foreach (var (name, id) in Overlays)
            {
                var handler = (IShellIconOverlayIdentifier)Activator.CreateInstance(Type.GetTypeFromCLSID(id)!)!;
                if (handler.IsMemberOf(path, 0) == 0) claimed.Add(name);
                Marshal.ReleaseComObject(handler);
            }
            return string.Join("+", claimed);
        });
    }
}
