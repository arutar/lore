#include "LoreShell.h"
#include "Resource.h"
#include <shlwapi.h>
#include <shellapi.h>
#include <gdiplus.h>
#include <sstream>

namespace
{
constexpr UINT CommandClone = 0;
constexpr UINT CommandSync = 1;
constexpr UINT CommandCommit = 2;
constexpr UINT CommandStage = 3;
constexpr UINT CommandStatus = 4;
constexpr UINT CommandHistory = 5;
constexpr UINT CommandLock = 6;
constexpr UINT CommandUnlock = 7;
constexpr UINT CommandAccounts = 8;

std::wstring RepositoryRoot(std::wstring path)
{
    if (!PathIsDirectoryW(path.c_str())) PathRemoveFileSpecW(path.data());
    while (!path.empty())
    {
        if (PathFileExistsW((path + L"\\.lore").c_str())) return path;
        const auto length = path.size();
        if (!PathRemoveFileSpecW(path.data()) || path.size() == length) break;
        path.resize(std::wcslen(path.c_str()));
    }
    return {};
}

std::string JsonEscape(const std::wstring& value)
{
    std::ostringstream output;
    output << '"';
    for (const auto character : value)
    {
        if (character == L'"' || character == L'\\') output << '\\' << static_cast<char>(character);
        else if (character < 128) output << static_cast<char>(character);
        else
        {
            char buffer[8];
            snprintf(buffer, sizeof(buffer), "\\u%04x", static_cast<unsigned>(character));
            output << buffer;
        }
    }
    output << '"';
    return output.str();
}

struct Invocation
{
    UINT command;
    std::vector<std::wstring> paths;
};

HBITMAP LoreMenuBitmap(int operation = -1)
{
    constexpr int operationCount = 9;
    if (operation < -1 || operation >= operationCount) return nullptr;
    static HBITMAP bitmaps[operationCount + 1]{};
    constexpr int resources[operationCount + 1]{
        IDI_LORE, IDI_LORE_CLONE, IDI_LORE_SYNC, IDI_LORE_COMMIT,
        IDI_LORE_STAGE, IDI_LORE_STATUS, IDI_LORE_HISTORY, IDI_LORE_LOCK, IDI_LORE_UNLOCK, IDI_LORE_STATUS,
    };
    auto& bitmap = bitmaps[operation + 1];
    if (bitmap) return bitmap;
    const auto resource = resources[operation + 1];
    bitmap = [resource]
    {
        static ULONG_PTR gdiplus = []
        {
            Gdiplus::GdiplusStartupInput input;
            ULONG_PTR token = 0;
            return Gdiplus::GdiplusStartup(&token, &input, nullptr) == Gdiplus::Ok ? token : 0;
        }();
        if (!gdiplus) return static_cast<HBITMAP>(nullptr);
        HICON icon = static_cast<HICON>(LoadImageW(g_instance, MAKEINTRESOURCE(resource), IMAGE_ICON, 16, 16, LR_DEFAULTCOLOR));
        if (!icon) return static_cast<HBITMAP>(nullptr);
        Gdiplus::Bitmap source(icon);
        Gdiplus::Bitmap canvas(16, 16, PixelFormat32bppARGB);
        Gdiplus::Graphics graphics(&canvas);
        graphics.Clear(Gdiplus::Color(0, 0, 0, 0));
        graphics.SetCompositingMode(Gdiplus::CompositingModeSourceCopy);
        graphics.SetInterpolationMode(Gdiplus::InterpolationModeHighQualityBicubic);
        graphics.DrawImage(&source, Gdiplus::Rect(0, 0, 16, 16));
        HBITMAP created = nullptr;
        const auto status = canvas.GetHBITMAP(Gdiplus::Color(0, 0, 0, 0), &created);
        DestroyIcon(icon);
        return status == Gdiplus::Ok ? created : nullptr;
    }();
    return bitmap;
}

void SetLoreMenuIcon(HMENU menu, UINT command, int operation)
{
    MENUITEMINFOW info{sizeof(info)};
    info.fMask = MIIM_BITMAP;
    info.hbmpItem = LoreMenuBitmap(operation);
    SetMenuItemInfoW(menu, command, FALSE, &info);
}

DWORD Fail(DWORD error)
{
    wchar_t message[512];
    const auto result = FormatMessageW(FORMAT_MESSAGE_FROM_SYSTEM, nullptr, error, 0, message, 512, nullptr);
    MessageBoxW(nullptr, result ? message : L"Lore command failed", L"Lore", MB_OK | MB_ICONERROR);
    return error;
}

DWORD StartCompanion(const std::string& body)
{
    wchar_t directory[MAX_PATH]{};
    wchar_t requestPath[MAX_PATH]{};
    if (!GetTempPathW(MAX_PATH, directory) || !GetTempFileNameW(directory, L"Lore", 0, requestPath)) return GetLastError();
    HANDLE request = CreateFileW(requestPath, GENERIC_WRITE, 0, nullptr, OPEN_EXISTING, 0, nullptr);
    if (request == INVALID_HANDLE_VALUE) return GetLastError();
    DWORD written = 0;
    const bool saved = WriteFile(request, body.data(), static_cast<DWORD>(body.size()), &written, nullptr) && written == body.size();
    CloseHandle(request);
    if (!saved)
    {
        DeleteFileW(requestPath);
        return ERROR_WRITE_FAULT;
    }

    wchar_t modulePath[MAX_PATH]{};
    GetModuleFileNameW(g_instance, modulePath, MAX_PATH);
    PathRemoveFileSpecW(modulePath);
    const std::wstring executable = std::wstring(modulePath) + L"\\LoreCompanion.exe";
    const std::wstring arguments = L"--request \"" + std::wstring(requestPath) + L"\"";
    const auto result = ShellExecuteW(nullptr, L"open", executable.c_str(), arguments.c_str(), nullptr, SW_SHOWNORMAL);
    if (reinterpret_cast<INT_PTR>(result) <= 32)
    {
        DeleteFileW(requestPath);
        return static_cast<DWORD>(reinterpret_cast<INT_PTR>(result));
    }
    return ERROR_SUCCESS;
}

DWORD WINAPI InvokeWorker(LPVOID parameter)
{
    std::unique_ptr<Invocation> invocation(static_cast<Invocation*>(parameter));

    std::ostringstream json;
    json << "{\"Operation\":" << invocation->command << ",\"Paths\":[";
    for (size_t index = 0; index < invocation->paths.size(); ++index)
    {
        if (index) json << ',';
        json << JsonEscape(invocation->paths[index]);
    }
    json << "]}";
    const auto body = json.str();
    if (body.size() > 1024 * 1024) return Fail(ERROR_INVALID_DATA);
    const auto result = StartCompanion(body);
    return result == ERROR_SUCCESS ? ERROR_SUCCESS : Fail(result);
}
}

ContextMenu::ContextMenu() { AddServerLock(); }

STDMETHODIMP ContextMenu::QueryInterface(REFIID iid, void** object)
{
    if (!object) return E_POINTER;
    *object = nullptr;
    if (iid == IID_IUnknown || iid == IID_IShellExtInit) *object = static_cast<IShellExtInit*>(this);
    else if (iid == IID_IContextMenu) *object = static_cast<IContextMenu*>(this);
    else return E_NOINTERFACE;
    AddRef();
    return S_OK;
}

STDMETHODIMP_(ULONG) ContextMenu::AddRef() { return InterlockedIncrement(&references_); }

STDMETHODIMP_(ULONG) ContextMenu::Release()
{
    const auto result = InterlockedDecrement(&references_);
    if (result == 0)
    {
        delete this;
        ReleaseServerLock();
    }
    return result;
}

STDMETHODIMP ContextMenu::Initialize(PCIDLIST_ABSOLUTE folder, IDataObject* data, HKEY)
{
    paths_.clear();
    wchar_t path[32768]{};
    if (folder && SHGetPathFromIDListW(folder, path)) folder_ = path;
    if (data)
    {
        FORMATETC format{CF_HDROP, nullptr, DVASPECT_CONTENT, -1, TYMED_HGLOBAL};
        STGMEDIUM medium{};
        if (SUCCEEDED(data->GetData(&format, &medium)))
        {
            const auto drop = static_cast<HDROP>(medium.hGlobal);
            const auto count = DragQueryFileW(drop, 0xffffffff, nullptr, 0);
            for (UINT index = 0; index < count && index < 256; ++index)
            {
                if (DragQueryFileW(drop, index, path, MAX_PATH)) paths_.emplace_back(path);
            }
            ReleaseStgMedium(&medium);
        }
    }
    if (paths_.empty() && !folder_.empty()) paths_.push_back(folder_);
    insideRepository_ = !paths_.empty() && !RepositoryRoot(paths_.front()).empty();
    return S_OK;
}

STDMETHODIMP ContextMenu::QueryContextMenu(HMENU menu, UINT index, UINT first, UINT, UINT flags)
{
    if (flags & CMF_DEFAULTONLY) return MAKE_HRESULT(SEVERITY_SUCCESS, FACILITY_NULL, 0);
    UINT next = index;
    InsertMenuW(menu, next++, MF_BYPOSITION | MF_SEPARATOR, 0, nullptr);
    if (!insideRepository_)
    {
        InsertMenuW(menu, next++, MF_BYPOSITION | MF_STRING, first + CommandClone, L"Lore Clone...");
        SetLoreMenuIcon(menu, first + CommandClone, CommandClone);
        InsertMenuW(menu, next, MF_BYPOSITION | MF_SEPARATOR, 0, nullptr);
        return MAKE_HRESULT(SEVERITY_SUCCESS, FACILITY_NULL, 3);
    }

    InsertMenuW(menu, next++, MF_BYPOSITION | MF_STRING, first + CommandSync, L"Lore Sync");
    SetLoreMenuIcon(menu, first + CommandSync, CommandSync);
    InsertMenuW(menu, next++, MF_BYPOSITION | MF_STRING, first + CommandCommit, L"Lore Commit...");
    SetLoreMenuIcon(menu, first + CommandCommit, CommandCommit);
    HMENU submenu = CreatePopupMenu();
    AppendMenuW(submenu, MF_STRING, first + CommandStage, L"Stage selected paths");
    SetLoreMenuIcon(submenu, first + CommandStage, CommandStage);
    AppendMenuW(submenu, MF_STRING, first + CommandStatus, L"Status");
    SetLoreMenuIcon(submenu, first + CommandStatus, CommandStatus);
    AppendMenuW(submenu, MF_STRING, first + CommandHistory, L"History");
    SetLoreMenuIcon(submenu, first + CommandHistory, CommandHistory);
    AppendMenuW(submenu, MF_SEPARATOR, 0, nullptr);
    AppendMenuW(submenu, MF_STRING, first + CommandLock, L"Acquire locks");
    SetLoreMenuIcon(submenu, first + CommandLock, CommandLock);
    AppendMenuW(submenu, MF_STRING, first + CommandUnlock, L"Release locks");
    SetLoreMenuIcon(submenu, first + CommandUnlock, CommandUnlock);
    AppendMenuW(submenu, MF_SEPARATOR, 0, nullptr);
    AppendMenuW(submenu, MF_STRING, first + CommandAccounts, L"Accounts");
    SetLoreMenuIcon(submenu, first + CommandAccounts, CommandAccounts);
    InsertMenuW(menu, next++, MF_BYPOSITION | MF_STRING | MF_POPUP, reinterpret_cast<UINT_PTR>(submenu), L"Lore");
    MENUITEMINFOW submenuInfo{sizeof(submenuInfo)};
    submenuInfo.fMask = MIIM_BITMAP;
    submenuInfo.hbmpItem = LoreMenuBitmap();
    SetMenuItemInfoW(menu, index + 3, TRUE, &submenuInfo);
    InsertMenuW(menu, next, MF_BYPOSITION | MF_SEPARATOR, 0, nullptr);
    return MAKE_HRESULT(SEVERITY_SUCCESS, FACILITY_NULL, CommandAccounts + 1);
}

STDMETHODIMP ContextMenu::InvokeCommand(CMINVOKECOMMANDINFO* info)
{
    if (!info || HIWORD(info->lpVerb)) return E_INVALIDARG;
    const auto command = LOWORD(info->lpVerb);
    if (command > CommandAccounts) return E_INVALIDARG;
    if (command == CommandClone && insideRepository_) return E_INVALIDARG;
    if (command != CommandClone && !insideRepository_) return E_INVALIDARG;

    auto invocation = std::make_unique<Invocation>();
    invocation->command = command;
    invocation->paths = paths_;
    HANDLE thread = CreateThread(nullptr, 0, InvokeWorker, invocation.get(), 0, nullptr);
    if (!thread) return HRESULT_FROM_WIN32(GetLastError());
    invocation.release();
    CloseHandle(thread);
    return S_OK;
}

STDMETHODIMP ContextMenu::GetCommandString(UINT_PTR, UINT flags, UINT*, CHAR* name, UINT size)
{
    if ((flags == GCS_HELPTEXTA || flags == GCS_VERBA) && name && size) name[0] = '\0';
    if ((flags == GCS_HELPTEXTW || flags == GCS_VERBW) && name && size) reinterpret_cast<wchar_t*>(name)[0] = L'\0';
    return S_OK;
}
