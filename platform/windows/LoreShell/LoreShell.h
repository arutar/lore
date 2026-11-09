#pragma once

#include <windows.h>
#include <shlobj.h>
#include "Guids.h"
#include <atomic>
#include <string>
#include <vector>

extern std::atomic<long> g_serverLocks;
extern HINSTANCE g_instance;

inline void AddServerLock() { g_serverLocks.fetch_add(1); }
inline void ReleaseServerLock() { g_serverLocks.fetch_sub(1); }

class ContextMenu final : public IShellExtInit, public IContextMenu
{
public:
    ContextMenu();
    STDMETHODIMP QueryInterface(REFIID iid, void** object) override;
    STDMETHODIMP_(ULONG) AddRef() override;
    STDMETHODIMP_(ULONG) Release() override;
    STDMETHODIMP Initialize(PCIDLIST_ABSOLUTE folder, IDataObject* data, HKEY program) override;
    STDMETHODIMP QueryContextMenu(HMENU menu, UINT index, UINT first, UINT last, UINT flags) override;
    STDMETHODIMP InvokeCommand(CMINVOKECOMMANDINFO* info) override;
    STDMETHODIMP GetCommandString(UINT_PTR command, UINT flags, UINT* reserved, CHAR* name, UINT size) override;

private:
    ~ContextMenu() = default;
    ULONG references_ = 1;
    std::wstring folder_;
    std::vector<std::wstring> paths_;
    bool insideRepository_ = false;
};

class OverlayHandler final : public IShellIconOverlayIdentifier
{
public:
    explicit OverlayHandler(int state);
    STDMETHODIMP QueryInterface(REFIID iid, void** object) override;
    STDMETHODIMP_(ULONG) AddRef() override;
    STDMETHODIMP_(ULONG) Release() override;
    STDMETHODIMP IsMemberOf(PCWSTR path, DWORD attributes) override;
    STDMETHODIMP GetOverlayInfo(PWSTR path, int pathSize, int* index, DWORD* flags) override;
    STDMETHODIMP GetPriority(int* priority) override;

private:
    ~OverlayHandler() = default;
    ULONG references_ = 1;
    int state_;
};

template<typename T>
class ClassFactory final : public IClassFactory
{
public:
    explicit ClassFactory(int state = 0) : state_(state) { AddServerLock(); }
    STDMETHODIMP QueryInterface(REFIID iid, void** object) override
    {
        if (!object) return E_POINTER;
        *object = nullptr;
        if (iid != IID_IUnknown && iid != IID_IClassFactory) return E_NOINTERFACE;
        *object = static_cast<IClassFactory*>(this);
        AddRef();
        return S_OK;
    }
    STDMETHODIMP_(ULONG) AddRef() override { return InterlockedIncrement(&references_); }
    STDMETHODIMP_(ULONG) Release() override
    {
        const auto result = InterlockedDecrement(&references_);
        if (result == 0) delete this;
        return result;
    }
    STDMETHODIMP CreateInstance(IUnknown* outer, REFIID iid, void** object) override
    {
        if (!object) return E_POINTER;
        *object = nullptr;
        if (outer) return CLASS_E_NOAGGREGATION;
        T* instance;
        if constexpr (std::is_same_v<T, OverlayHandler>) instance = new (std::nothrow) T(state_);
        else instance = new (std::nothrow) T();
        if (!instance) return E_OUTOFMEMORY;
        const auto result = instance->QueryInterface(iid, object);
        instance->Release();
        return result;
    }
    STDMETHODIMP LockServer(BOOL lock) override
    {
        if (lock) AddServerLock(); else ReleaseServerLock();
        return S_OK;
    }
private:
    ~ClassFactory() { ReleaseServerLock(); }
    ULONG references_ = 1;
    int state_;
};

HRESULT RegisterComponent(HKEY root);
HRESULT UnregisterComponent(HKEY root);
