#include <initguid.h>
#include "LoreShell.h"
#include "Resource.h"
#include <filesystem>
#include <iostream>

using GetClass = HRESULT(__stdcall*)(REFCLSID, REFIID, void**);

bool HasIcon(HMENU menu, UINT position)
{
    MENUITEMINFOW info{sizeof(info)};
    info.fMask = MIIM_BITMAP;
    return GetMenuItemInfoW(menu, position, TRUE, &info) && info.hbmpItem;
}

bool TestPath(GetClass getClass, const wchar_t* path, int expectedCount)
{
    IClassFactory* factory = nullptr;
    if (FAILED(getClass(CLSID_ContextMenu, IID_PPV_ARGS(&factory)))) return false;
    IContextMenu* context = nullptr;
    HRESULT result = factory->CreateInstance(nullptr, IID_PPV_ARGS(&context));
    factory->Release();
    if (FAILED(result)) return false;

    IShellExtInit* init = nullptr;
    result = context->QueryInterface(IID_PPV_ARGS(&init));
    if (FAILED(result)) return false;
    PIDLIST_ABSOLUTE pidl = nullptr;
    result = SHParseDisplayName(path, nullptr, &pidl, 0, nullptr);
    if (FAILED(result)) return false;
    result = init->Initialize(pidl, nullptr, nullptr);
    CoTaskMemFree(pidl);
    init->Release();
    if (FAILED(result)) return false;

    HMENU menu = CreateMenu();
    result = context->QueryContextMenu(menu, 0, 1000, 2000, 0);
    const int count = GetMenuItemCount(menu);
    bool icons = HasIcon(menu, 0);
    if (expectedCount == 3)
    {
        HMENU submenu = GetSubMenu(menu, 2);
        icons = icons && HasIcon(menu, 1) && HasIcon(menu, 2) && submenu;
        if (submenu)
        {
            for (UINT position : {0u, 1u, 2u, 4u, 5u}) icons = icons && HasIcon(submenu, position);
        }
    }
    context->Release();
    DestroyMenu(menu);
    return SUCCEEDED(result) && count == expectedCount && icons;
}

bool TestOverlay(GetClass getClass)
{
    IClassFactory* factory = nullptr;
    if (FAILED(getClass(CLSID_OverlayModified, IID_PPV_ARGS(&factory)))) return false;
    IShellIconOverlayIdentifier* overlay = nullptr;
    const auto result = factory->CreateInstance(nullptr, IID_PPV_ARGS(&overlay));
    factory->Release();
    if (FAILED(result)) return false;
    wchar_t path[MAX_PATH]{};
    int index = 0;
    DWORD flags = 0;
    const auto info = overlay->GetOverlayInfo(path, MAX_PATH, &index, &flags);
    overlay->Release();
    return SUCCEEDED(info) && index == -IDI_OVERLAY_MODIFIED
        && flags == (ISIOI_ICONFILE | ISIOI_ICONINDEX)
        && std::filesystem::path(path).filename() == L"LoreShell.dll";
}

int main()
{
    HMODULE module = LoadLibraryW(L"LoreShell.dll");
    if (!module) return 1;
    auto getClass = reinterpret_cast<GetClass>(GetProcAddress(module, "DllGetClassObject"));
    if (!getClass || FAILED(CoInitializeEx(nullptr, COINIT_APARTMENTTHREADED))) return 2;
    const auto temporary = std::filesystem::temp_directory_path();
    const auto outside = temporary / "lore-menu-outside";
    const auto repository = temporary / "lore-shell-repo";
    std::filesystem::create_directories(outside);
    std::filesystem::create_directories(repository / ".lore");
    const bool passed = TestPath(getClass, outside.wstring().c_str(), 1)
        && TestPath(getClass, repository.wstring().c_str(), 3)
        && TestOverlay(getClass);
    std::filesystem::remove_all(outside);
    std::filesystem::remove_all(repository);
    CoUninitialize();
    FreeLibrary(module);
    if (!passed) return 3;
    std::cout << "Lore context menu and overlay smoke tests passed\n";
    return 0;
}
