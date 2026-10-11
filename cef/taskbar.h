// Window identity and relaunch target belong to the Go application, not CEF.
#define COBJMACROS
#include <initguid.h>
#include <shobjidl.h>
#include <propkey.h>
#include <propvarutil.h>

static void stringProperty(IPropertyStore *store, const PROPERTYKEY *key, const wchar_t *text) {
    PROPVARIANT value = {0};
    value.vt = VT_LPWSTR;
    value.pwszVal = (wchar_t *)text;
    IPropertyStore_SetValue(store, key, &value);
}

static void mainExecutable(wchar_t *path) {
    GetModuleFileNameW(NULL, path, MAX_PATH);
    wchar_t *end = wcsrchr(path, L'\\'); if (end) *end = 0;
    end = wcsrchr(path, L'\\'); if (end) *end = 0;
    wcscat(path, L"\\SimMusicNext.exe");
}

static void repairPinnedFolder(const wchar_t *folder, const wchar_t *hostPath, const wchar_t *exe) {
    wchar_t pattern[MAX_PATH], file[MAX_PATH], target[MAX_PATH], working[MAX_PATH];
    swprintf(pattern, MAX_PATH, L"%ls\\*.lnk", folder);
    wcscpy(working, exe); wchar_t *end = wcsrchr(working, L'\\'); if (end) *end = 0;
    WIN32_FIND_DATAW data;
    HANDLE search = FindFirstFileW(pattern, &data);
    if (search == INVALID_HANDLE_VALUE) return;
    do {
        IShellLinkW *link = NULL;
        IPersistFile *persist = NULL;
        swprintf(file, MAX_PATH, L"%ls\\%ls", folder, data.cFileName);
        if (FAILED(CoCreateInstance(&CLSID_ShellLink, NULL, CLSCTX_INPROC_SERVER, &IID_IShellLinkW, (void **)&link))) continue;
        if (SUCCEEDED(IShellLinkW_QueryInterface(link, &IID_IPersistFile, (void **)&persist)) && SUCCEEDED(IPersistFile_Load(persist, file, STGM_READWRITE)) && SUCCEEDED(IShellLinkW_GetPath(link, target, MAX_PATH, NULL, SLGP_RAWPATH)) && !wcsicmp(target, hostPath)) {
            IShellLinkW_SetPath(link, exe);
            IShellLinkW_SetArguments(link, L"");
            IShellLinkW_SetWorkingDirectory(link, working);
            IShellLinkW_SetIconLocation(link, exe, 0);
            IShellLinkW_SetDescription(link, L"SimMusic Next");
            IPropertyStore *store = NULL;
            if (SUCCEEDED(IShellLinkW_QueryInterface(link, &IID_IPropertyStore, (void **)&store))) { stringProperty(store, &PKEY_AppUserModel_ID, L"SimMusic.Next"); IPropertyStore_Commit(store); IPropertyStore_Release(store); }
            IPersistFile_Save(persist, file, TRUE);
        }
        if (persist) IPersistFile_Release(persist);
        IShellLinkW_Release(link);
    } while (FindNextFileW(search, &data));
    FindClose(search);
}

static void setTaskbarIdentity(HWND window) {
    wchar_t exe[MAX_PATH], hostPath[MAX_PATH], command[MAX_PATH + 4], icon[MAX_PATH + 8], display[MAX_PATH + 8];
    mainExecutable(exe);
    GetModuleFileNameW(NULL, hostPath, MAX_PATH);
    swprintf(command, MAX_PATH + 4, L"\"%ls\"", exe);
    swprintf(icon, MAX_PATH + 8, L"%ls,0", exe);
    swprintf(display, MAX_PATH + 8, L"@%ls,-1", hostPath);
    IPropertyStore *store = NULL;
    if (SUCCEEDED(SHGetPropertyStoreForWindow(window, &IID_IPropertyStore, (void **)&store))) {
        stringProperty(store, &PKEY_AppUserModel_RelaunchCommand, command);
        stringProperty(store, &PKEY_AppUserModel_RelaunchDisplayNameResource, display);
        stringProperty(store, &PKEY_AppUserModel_RelaunchIconResource, icon);
        stringProperty(store, &PKEY_AppUserModel_ID, L"SimMusic.Next");
        IPropertyStore_Commit(store);
        IPropertyStore_Release(store);
    }
    wchar_t folder[MAX_PATH];
    if (GetEnvironmentVariableW(L"APPDATA", folder, MAX_PATH) && wcslen(folder) + 70 < MAX_PATH) {
        wcscat(folder, L"\\Microsoft\\Internet Explorer\\Quick Launch\\User Pinned\\TaskBar");
        repairPinnedFolder(folder, hostPath, exe);
    }
}
