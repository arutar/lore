#include <initguid.h>
#include "LoreShell.h"

std::atomic<long> g_serverLocks{0};
HINSTANCE g_instance;

BOOL APIENTRY DllMain(HMODULE instance, DWORD reason, LPVOID)
{
    if (reason == DLL_PROCESS_ATTACH)
    {
        g_instance = instance;
        DisableThreadLibraryCalls(instance);
    }
    return TRUE;
}

static HRESULT GetClass(REFCLSID, REFIID iid, void** object, int state = 0)
{
    if (!object) return E_POINTER;
    *object = nullptr;
    IClassFactory* factory = state == 0
        ? static_cast<IClassFactory*>(new (std::nothrow) ClassFactory<ContextMenu>())
        : static_cast<IClassFactory*>(new (std::nothrow) ClassFactory<OverlayHandler>(state));
    if (!factory) return E_OUTOFMEMORY;
    const auto result = factory->QueryInterface(iid, object);
    factory->Release();
    return result;
}

STDAPI DllGetClassObject(REFCLSID clsid, REFIID iid, void** object)
{
    if (clsid == CLSID_ContextMenu) return GetClass(clsid, iid, object);
    if (clsid == CLSID_OverlayModified) return GetClass(clsid, iid, object, 1);
    if (clsid == CLSID_OverlayStaged) return GetClass(clsid, iid, object, 2);
    if (clsid == CLSID_OverlayConflicted) return GetClass(clsid, iid, object, 3);
    if (clsid == CLSID_OverlayLocked) return GetClass(clsid, iid, object, 4);
    if (clsid == CLSID_OverlayIgnored) return GetClass(clsid, iid, object, 5);
    if (clsid == CLSID_OverlayStagedModified) return GetClass(clsid, iid, object, 6);
    if (clsid == CLSID_OverlayClean) return GetClass(clsid, iid, object, 7);
    return CLASS_E_CLASSNOTAVAILABLE;
}

STDAPI DllCanUnloadNow()
{
    return g_serverLocks.load() == 0 ? S_OK : S_FALSE;
}

STDAPI DllRegisterServer()
{
    return RegisterComponent(IsUserAnAdmin() ? HKEY_LOCAL_MACHINE : HKEY_CURRENT_USER);
}

STDAPI DllUnregisterServer()
{
    return UnregisterComponent(IsUserAnAdmin() ? HKEY_LOCAL_MACHINE : HKEY_CURRENT_USER);
}
