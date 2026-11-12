#include "LoreShell.h"
#include <shlwapi.h>

namespace
{
struct Registration
{
    const CLSID* clsid;
    const wchar_t* name;
    const wchar_t* overlayName;
};

const Registration registrations[] = {
    {&CLSID_ContextMenu, L"Lore Explorer context menu", nullptr},
    {&CLSID_OverlayModified, L"Lore modified overlay", L" Lore Modified"},
    {&CLSID_OverlayStaged, L"Lore staged overlay", L" Lore Staged"},
    {&CLSID_OverlayConflicted, L"Lore conflicted overlay", L" Lore Conflicted"},
    {&CLSID_OverlayLocked, L"Lore locked overlay", L" Lore Locked"},
    {&CLSID_OverlayIgnored, L"Lore ignored overlay", L" Lore Ignored"},
    {&CLSID_OverlayStagedModified, L"Lore staged and modified overlay", L" Lore Staged Modified"},
    {&CLSID_OverlayClean, L"Lore clean repository overlay", L" Lore Clean"},
};

HRESULT SetString(HKEY root, const std::wstring& path, const wchar_t* value, const std::wstring& data)
{
    HKEY key;
    const auto result = RegCreateKeyExW(root, path.c_str(), 0, nullptr, 0, KEY_SET_VALUE, nullptr, &key, nullptr);
    if (result != ERROR_SUCCESS) return HRESULT_FROM_WIN32(result);
    const auto status = RegSetValueExW(key, value, 0, REG_SZ, reinterpret_cast<const BYTE*>(data.c_str()), static_cast<DWORD>((data.size() + 1) * sizeof(wchar_t)));
    RegCloseKey(key);
    return status == ERROR_SUCCESS ? S_OK : HRESULT_FROM_WIN32(status);
}

std::wstring ClsidString(const CLSID& clsid)
{
    wchar_t value[64]{};
    StringFromGUID2(clsid, value, 64);
    return value;
}
}

HRESULT RegisterComponent(HKEY root)
{
    wchar_t module[MAX_PATH]{};
    if (!GetModuleFileNameW(g_instance, module, MAX_PATH)) return HRESULT_FROM_WIN32(GetLastError());
    for (const auto& registration : registrations)
    {
        const auto clsid = ClsidString(*registration.clsid);
        const auto base = L"Software\\Classes\\CLSID\\" + clsid;
        HRESULT result = SetString(root, base, nullptr, registration.name);
        if (FAILED(result)) return result;
        result = SetString(root, base + L"\\InprocServer32", nullptr, module);
        if (FAILED(result)) return result;
        result = SetString(root, base + L"\\InprocServer32", L"ThreadingModel", L"Apartment");
        if (FAILED(result)) return result;
        if (registration.overlayName)
        {
            result = SetString(root,
                std::wstring(L"Software\\Microsoft\\Windows\\CurrentVersion\\Explorer\\ShellIconOverlayIdentifiers\\") + registration.overlayName,
                nullptr, clsid);
            if (FAILED(result)) return result;
        }
    }

    const auto clsid = ClsidString(CLSID_ContextMenu);
    const wchar_t* handlers[] = {L"*", L"Directory", L"Directory\\Background", L"Folder"};
    for (const auto* handler : handlers)
    {
        const auto result = SetString(root,
            std::wstring(L"Software\\Classes\\") + handler + L"\\shellex\\ContextMenuHandlers\\Lore", nullptr, clsid);
        if (FAILED(result)) return result;
    }
    return SetString(root,
        L"Software\\Microsoft\\Windows\\CurrentVersion\\Shell Extensions\\Approved",
        clsid.c_str(), L"Lore Explorer context menu");
}

HRESULT UnregisterComponent(HKEY root)
{
    for (const auto& registration : registrations)
    {
        const auto clsid = ClsidString(*registration.clsid);
        RegDeleteTreeW(root, (L"Software\\Classes\\CLSID\\" + clsid).c_str());
        if (registration.overlayName)
        {
            RegDeleteTreeW(root,
                (std::wstring(L"Software\\Microsoft\\Windows\\CurrentVersion\\Explorer\\ShellIconOverlayIdentifiers\\") + registration.overlayName).c_str());
        }
    }
    const wchar_t* handlers[] = {L"*", L"Directory", L"Directory\\Background", L"Folder"};
    for (const auto* handler : handlers)
    {
        RegDeleteTreeW(root,
            (std::wstring(L"Software\\Classes\\") + handler + L"\\shellex\\ContextMenuHandlers\\Lore").c_str());
    }
    HKEY approved;
    if (RegOpenKeyExW(root, L"Software\\Microsoft\\Windows\\CurrentVersion\\Shell Extensions\\Approved", 0, KEY_SET_VALUE, &approved) == ERROR_SUCCESS)
    {
        const auto clsid = ClsidString(CLSID_ContextMenu);
        RegDeleteValueW(approved, clsid.c_str());
        RegCloseKey(approved);
    }
    return S_OK;
}
