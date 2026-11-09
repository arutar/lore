#include <initguid.h>
#include "LoreShell.h"
#include <filesystem>
#include <iostream>

using GetClass = HRESULT(__stdcall*)(REFCLSID, REFIID, void**);

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
    context->Release();
    DestroyMenu(menu);
    return SUCCEEDED(result) && count == expectedCount;
}

int main()
{
    HMODULE module = LoadLibraryW(L"LoreShell.dll");
    if (!module) return 1;
    auto getClass = reinterpret_cast<GetClass>(GetProcAddress(module, "DllGetClassObject"));
    if (!getClass || FAILED(CoInitializeEx(nullptr, COINIT_APARTMENTTHREADED))) return 2;
    const auto repository = std::filesystem::current_path() / "shell-repo";
    std::filesystem::create_directories(repository / ".lore");
    const bool passed = TestPath(getClass, L"D:\\projects\\lore", 1)
        && TestPath(getClass, repository.wstring().c_str(), 3);
    std::filesystem::remove_all(repository);
    CoUninitialize();
    FreeLibrary(module);
    if (!passed) return 3;
    std::cout << "Lore context menu smoke test passed\n";
    return 0;
}
