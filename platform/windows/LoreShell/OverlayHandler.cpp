#include "LoreShell.h"
#include "Resource.h"

namespace
{
constexpr size_t Capacity = 4096;
constexpr size_t EntrySize = 24;
constexpr wchar_t MapName[] = L"Local\\Lore.OverlayCache";

struct Entry
{
    uint64_t hash;
    int32_t state;
    int64_t ticks;
    uint32_t rootHash;
};

uint64_t HashPath(const std::wstring& input)
{
    wchar_t full[32768];
    const auto length = GetFullPathNameW(input.c_str(), 32768, full, nullptr);
    if (!length || length >= 32768) return 0;
    std::wstring path(full, length);
    while (path.size() > 3 && path.back() == L'\\') path.pop_back();
    uint64_t hash = 14695981039346656037ULL;
    for (auto character : path)
    {
        hash ^= static_cast<uint16_t>(towupper(character));
        hash *= 1099511628211ULL;
    }
    return hash;
}

int Lookup(const std::wstring& path)
{
    const auto hash = HashPath(path);
    if (!hash) return 0;
    HANDLE mapping = OpenFileMappingW(FILE_MAP_READ, FALSE, MapName);
    if (!mapping) return 0;
    const auto* bytes = static_cast<const unsigned char*>(MapViewOfFile(mapping, FILE_MAP_READ, 0, 0, 16 + Capacity * EntrySize));
    if (!bytes)
    {
        CloseHandle(mapping);
        return 0;
    }
    int32_t count = 0;
    memcpy(&count, bytes, sizeof(count));
    count = (std::max)(0, (std::min)(count, static_cast<int32_t>(Capacity)));
    int state = 0;
    for (int32_t index = 0; index < count; ++index)
    {
        const auto* entry = reinterpret_cast<const Entry*>(bytes + 16 + index * EntrySize);
        if (entry->hash == hash && entry->state >= 0 && entry->state <= 6)
        {
            state = entry->state;
            break;
        }
    }
    UnmapViewOfFile(bytes);
    CloseHandle(mapping);
    return state;
}
}

OverlayHandler::OverlayHandler(int state) : state_(state) { AddServerLock(); }

STDMETHODIMP OverlayHandler::QueryInterface(REFIID iid, void** object)
{
    if (!object) return E_POINTER;
    *object = nullptr;
    if (iid != IID_IUnknown && iid != IID_IShellIconOverlayIdentifier) return E_NOINTERFACE;
    *object = static_cast<IShellIconOverlayIdentifier*>(this);
    AddRef();
    return S_OK;
}

STDMETHODIMP_(ULONG) OverlayHandler::AddRef() { return InterlockedIncrement(&references_); }

STDMETHODIMP_(ULONG) OverlayHandler::Release()
{
    const auto result = InterlockedDecrement(&references_);
    if (result == 0)
    {
        delete this;
        ReleaseServerLock();
    }
    return result;
}

STDMETHODIMP OverlayHandler::IsMemberOf(PCWSTR path, DWORD)
{
    return path && Lookup(path) == state_ ? S_OK : S_FALSE;
}

STDMETHODIMP OverlayHandler::GetOverlayInfo(PWSTR path, int pathSize, int* index, DWORD* flags)
{
    if (!path || !index || !flags) return E_POINTER;
    if (state_ < 1 || state_ > 6) return E_UNEXPECTED;
    const auto result = GetModuleFileNameW(g_instance, path, pathSize);
    if (!result || result >= static_cast<UINT>(pathSize)) return E_FAIL;
    *index = -(200 + state_);
    *flags = ISIOI_ICONFILE | ISIOI_ICONINDEX;
    return S_OK;
}

STDMETHODIMP OverlayHandler::GetPriority(int* priority)
{
    if (!priority) return E_POINTER;
    *priority = 10 + state_;
    return S_OK;
}
