// System media controls for the main window: the taskbar preview's previous / play-pause / next buttons
// and the System Media Transport Controls (media keys, the volume flyout and the lock screen).
// The Go core sends the playback state with WM_COPYDATA; a pressed control is reported back to it
// as a "MEDIA:<action>" line, the same way the window handle is.
//
// The WinRT objects are called through their vtable slots, numbered as in windows.media.idl, so
// nothing here depends on which WinRT headers the toolchain ships.
#include <initguid.h>
#include <shobjidl.h>

DEFINE_GUID(simIidTransportInterop, 0xddb0472d, 0xc911, 0x4a1f, 0x86, 0xd9, 0xdc, 0x3d, 0x71, 0xa9, 0x5f, 0x5a);
DEFINE_GUID(simIidTransportControls, 0x99fa3ff4, 0x1742, 0x42a6, 0x90, 0x2e, 0x08, 0x7d, 0x41, 0xf9, 0x65, 0xec);
DEFINE_GUID(simIidStreamReferenceStatics, 0x857309dc, 0x3fbf, 0x4e7d, 0x98, 0x6f, 0xef, 0x3b, 0x1a, 0x07, 0xa9, 0x64);
DEFINE_GUID(simIidUriFactory, 0x44a9796f, 0x723e, 0x4fdf, 0xa2, 0x18, 0x03, 0x3e, 0x75, 0xb0, 0xc0, 0x84);
DEFINE_GUID(simIidButtonHandler, 0x0557e996, 0x7b23, 0x5bae, 0xaa, 0x81, 0xea, 0x0d, 0x67, 0x11, 0x43, 0xa4);
DEFINE_GUID(simIidAgileObject, 0x94ea2b94, 0xe9cc, 0x49e0, 0xc0, 0xff, 0xee, 0x64, 0xca, 0x8f, 0x5b, 0x90);

enum { mediaNone, mediaPaused, mediaPlaying };
enum { thumbPrevious = 0x5301, thumbToggle, thumbNext };

static UINT taskbarCreatedMessage;
static ITaskbarList3 *taskbarList;
static HICON thumbIcons[4]; // previous, play, pause, next
static int thumbAdded, mediaState;
static void *transportControls;
static HRESULT (WINAPI *createString)(const wchar_t *, UINT32, void **);
static HRESULT (WINAPI *deleteString)(void *);
static HRESULT (WINAPI *activationFactory)(void *, const GUID *, void **);

static void mediaAction(const char *action) { printf("MEDIA:%s\n", action); fflush(stdout); }

// ---------- taskbar preview buttons ----------

// The glyphs are drawn here, 4×4 supersampled, in the colour that suits the taskbar theme.
static int inTriangle(double x, double y, double ax, double ay, double bx, double by, double cx, double cy) {
    double d1 = (x - bx) * (ay - by) - (ax - bx) * (y - by);
    double d2 = (x - cx) * (by - cy) - (bx - cx) * (y - cy);
    double d3 = (x - ax) * (cy - ay) - (cx - ax) * (y - ay);
    return !((d1 < 0 || d2 < 0 || d3 < 0) && (d1 > 0 || d2 > 0 || d3 > 0));
}
static int inGlyph(int glyph, double x, double y) {
    if (glyph == 0) x = 1 - x; // previous mirrors next
    switch (glyph) {
    case 1: return inTriangle(x, y, .30, .20, .30, .80, .80, .50);
    case 2: return y >= .22 && y <= .78 && (x >= .27 && x <= .42 || x >= .58 && x <= .73);
    default: return inTriangle(x, y, .22, .22, .22, .78, .66, .50) || y >= .22 && y <= .78 && x >= .70 && x <= .79;
    }
}
static HICON glyphIcon(int glyph, int size, DWORD rgb) {
    BITMAPINFO info = {0};
    info.bmiHeader.biSize = sizeof(info.bmiHeader);
    info.bmiHeader.biWidth = size; info.bmiHeader.biHeight = -size;
    info.bmiHeader.biPlanes = 1; info.bmiHeader.biBitCount = 32;
    DWORD *pixels = NULL;
    HDC screen = GetDC(NULL);
    HBITMAP color = CreateDIBSection(screen, &info, DIB_RGB_COLORS, (void **)&pixels, NULL, 0);
    ReleaseDC(NULL, screen);
    if (!color) return NULL;
    for (int y = 0; y < size; y++) for (int x = 0; x < size; x++) {
        int covered = 0;
        for (int sy = 0; sy < 4; sy++) for (int sx = 0; sx < 4; sx++)
            covered += inGlyph(glyph, (x + (sx + .5) / 4) / size, (y + (sy + .5) / 4) / size);
        pixels[y * size + x] = ((DWORD)(covered * 255 / 16) << 24) | rgb;
    }
    HBITMAP mask = CreateBitmap(size, size, 1, 1, NULL);
    ICONINFO icon = { .fIcon = TRUE, .hbmMask = mask, .hbmColor = color };
    HICON result = CreateIconIndirect(&icon);
    DeleteObject(mask); DeleteObject(color);
    return result;
}
static void makeThumbIcons(HWND window) {
    DWORD light = 0, bytes = sizeof(light);
    RegGetValueW(HKEY_CURRENT_USER, L"Software\\Microsoft\\Windows\\CurrentVersion\\Themes\\Personalize", L"SystemUsesLightTheme", RRF_RT_REG_DWORD, NULL, &light, &bytes);
    int size = MulDiv(GetSystemMetrics(SM_CXSMICON), GetDpiForWindow(window), 96);
    for (int i = 0; i < 4; i++) {
        if (thumbIcons[i]) DestroyIcon(thumbIcons[i]);
        thumbIcons[i] = glyphIcon(i, size, light ? 0x202020 : 0xFFFFFF);
    }
}
static void fillThumbButtons(THUMBBUTTON *buttons) {
    static const wchar_t *tips[] = { L"上一首", L"播放", L"暂停", L"下一首" };
    int icons[] = { 0, mediaState == mediaPlaying ? 2 : 1, 3 };
    for (int i = 0; i < 3; i++) {
        buttons[i].dwMask = THB_ICON | THB_TOOLTIP | THB_FLAGS;
        buttons[i].iId = thumbPrevious + i;
        buttons[i].hIcon = thumbIcons[icons[i]];
        wcscpy(buttons[i].szTip, tips[icons[i]]);
        buttons[i].dwFlags = mediaState == mediaNone ? THBF_DISABLED : THBF_ENABLED;
    }
}
// Buttons can be added once per taskbar button; Explorer announces a new one with TaskbarButtonCreated.
static void addThumbButtons(HWND window) {
    if (!taskbarList && FAILED(CoCreateInstance(&CLSID_TaskbarList, NULL, CLSCTX_INPROC_SERVER, &IID_ITaskbarList3, (void **)&taskbarList))) return;
    ITaskbarList3_HrInit(taskbarList);
    makeThumbIcons(window);
    THUMBBUTTON buttons[3] = {0};
    fillThumbButtons(buttons);
    thumbAdded = SUCCEEDED(ITaskbarList3_ThumbBarAddButtons(taskbarList, window, 3, buttons));
}
static void updateThumbButtons(HWND window) {
    if (!thumbAdded) return;
    THUMBBUTTON buttons[3] = {0};
    fillThumbButtons(buttons);
    ITaskbarList3_ThumbBarUpdateButtons(taskbarList, window, 3, buttons);
}

// ---------- System Media Transport Controls ----------

#define SLOT(object, index, ...) ((HRESULT (STDMETHODCALLTYPE *)(void *, ##__VA_ARGS__))(*(void ***)(object))[index])
static void releaseObject(void *object) { if (object) ((ULONG (STDMETHODCALLTYPE *)(void *))(*(void ***)object)[2])(object); }
static void *hstring(const wchar_t *text) {
    void *value = NULL;
    createString(text, (UINT32)wcslen(text), &value);
    return value;
}

// The button handler is a static object: it lives as long as the process and is called on a system thread.
typedef struct { void **vtable; } ButtonHandler;
static HRESULT STDMETHODCALLTYPE handlerQuery(ButtonHandler *self, REFIID iid, void **out) {
    if (IsEqualGUID(iid, &IID_IUnknown) || IsEqualGUID(iid, &simIidButtonHandler) || IsEqualGUID(iid, &simIidAgileObject)) { *out = self; return S_OK; }
    *out = NULL;
    return E_NOINTERFACE;
}
static ULONG STDMETHODCALLTYPE handlerRef(ButtonHandler *self) { (void)self; return 1; }
static HRESULT STDMETHODCALLTYPE handlerInvoke(ButtonHandler *self, void *sender, void *args) {
    (void)self; (void)sender;
    int button = -1;
    if (FAILED(SLOT(args, 6, int *)(args, &button))) return S_OK;
    static const char *actions[] = { "play", "pause", "stop", NULL, NULL, NULL, "next", "previous" };
    if (button >= 0 && button < 8 && actions[button]) mediaAction(actions[button]);
    return S_OK;
}
static void *handlerVtable[] = { handlerQuery, handlerRef, handlerRef, handlerInvoke };
static ButtonHandler buttonHandler = { handlerVtable };

static void initTransportControls(HWND window) {
    HMODULE combase = LoadLibraryW(L"combase.dll");
    if (!combase) return;
    createString = (void *)GetProcAddress(combase, "WindowsCreateString");
    deleteString = (void *)GetProcAddress(combase, "WindowsDeleteString");
    activationFactory = (void *)GetProcAddress(combase, "RoGetActivationFactory");
    if (!createString || !deleteString || !activationFactory) return;
    void *name = hstring(L"Windows.Media.SystemMediaTransportControls"), *interop = NULL;
    if (SUCCEEDED(activationFactory(name, &simIidTransportInterop, &interop)) && interop) {
        SLOT(interop, 6, HWND, const GUID *, void **)(interop, window, &simIidTransportControls, &transportControls);
        releaseObject(interop);
    }
    deleteString(name);
    if (!transportControls) return;
    INT64 token = 0;
    SLOT(transportControls, 32, void *, INT64 *)(transportControls, &buttonHandler, &token);
    // Play 13, Stop 15, Pause 17, Previous 25, Next 27.
    for (int slot = 13; slot <= 27; slot += 2) if (slot <= 17 || slot >= 25) SLOT(transportControls, slot, boolean)(transportControls, 1);
    SLOT(transportControls, 11, boolean)(transportControls, 0);
}
// fields: title, artist, cover address — each may be empty.
static void updateTransportControls(wchar_t **fields) {
    if (!transportControls) return;
    SLOT(transportControls, 11, boolean)(transportControls, mediaState != mediaNone);
    SLOT(transportControls, 7, int)(transportControls, mediaState == mediaPlaying ? 3 : mediaState == mediaPaused ? 4 : 0);
    void *updater = NULL, *music = NULL;
    if (FAILED(SLOT(transportControls, 8, void **)(transportControls, &updater)) || !updater) return;
    if (mediaState == mediaNone) SLOT(updater, 16)(updater);
    else {
        SLOT(updater, 7, int)(updater, 1); // MediaPlaybackType_Music
        if (SUCCEEDED(SLOT(updater, 12, void **)(updater, &music)) && music) {
            void *title = hstring(fields[0]), *artist = hstring(fields[1]);
            SLOT(music, 7, void *)(music, title);
            SLOT(music, 11, void *)(music, artist);
            deleteString(title); deleteString(artist);
            releaseObject(music);
        }
        void *thumbnail = NULL;
        if (fields[2][0]) {
            void *uriClass = hstring(L"Windows.Foundation.Uri"), *streamClass = hstring(L"Windows.Storage.Streams.RandomAccessStreamReference");
            void *uriFactory = NULL, *streamStatics = NULL, *uri = NULL, *address = hstring(fields[2]);
            if (SUCCEEDED(activationFactory(uriClass, &simIidUriFactory, &uriFactory)) && uriFactory) SLOT(uriFactory, 6, void *, void **)(uriFactory, address, &uri);
            if (uri && SUCCEEDED(activationFactory(streamClass, &simIidStreamReferenceStatics, &streamStatics)) && streamStatics) SLOT(streamStatics, 7, void *, void **)(streamStatics, uri, &thumbnail);
            releaseObject(uri); releaseObject(uriFactory); releaseObject(streamStatics);
            deleteString(address); deleteString(uriClass); deleteString(streamClass);
        }
        SLOT(updater, 11, void *)(updater, thumbnail);
        releaseObject(thumbnail);
    }
    SLOT(updater, 17)(updater);
    releaseObject(updater);
}

// ---------- entry points for the window procedure ----------

static void initMedia(HWND window) {
    taskbarCreatedMessage = RegisterWindowMessageW(L"TaskbarButtonCreated");
    initTransportControls(window);
}
// The core's message: "<state>\t<title>\t<artist>\t<cover address>" in UTF-16, zero-terminated.
static void mediaChanged(HWND window, const COPYDATASTRUCT *data) {
    if (data->dwData != 1 || data->cbData < 2 * sizeof(wchar_t) || data->cbData > 16384) return;
    wchar_t text[8192 + 1] = {0};
    memcpy(text, data->lpData, data->cbData);
    wchar_t *fields[4] = { text, L"", L"", L"" };
    int count = 1;
    for (wchar_t *p = text; *p && count < 4; p++) if (*p == L'\t') { *p = 0; fields[count++] = p + 1; }
    mediaState = fields[0][0] == L'2' ? mediaPlaying : fields[0][0] == L'1' ? mediaPaused : mediaNone;
    // In case the taskbar button was announced before this window listened for it.
    if (!thumbAdded && IsWindowVisible(window)) addThumbButtons(window);
    updateThumbButtons(window);
    updateTransportControls(fields + 1);
}
// ---------- opening folders, files and addresses ----------

// Windows only lets the foreground process bring a new window to the front. The user clicks in this
// process's window, so Explorer and the browser are started from here: started by the core, a
// background process, their windows would open behind this one.
// The core's message is "<kind>\t<target>": "open" a folder, "select" a file in its folder, "url".
#define WM_SHELL_REQUEST (WM_APP + 7)
static wchar_t shellRequest[4096];
static void shellRequested(HWND window, const COPYDATASTRUCT *data) {
    if (data->cbData < 2 * sizeof(wchar_t) || data->cbData >= sizeof(shellRequest)) return;
    memset(shellRequest, 0, sizeof(shellRequest));
    memcpy(shellRequest, data->lpData, data->cbData);
    // Run after this cross-process call has returned: the shell cannot be called out to from inside it.
    PostMessageW(window, WM_SHELL_REQUEST, 0, 0);
}
static void runShellRequest(HWND window) {
    wchar_t *target = wcschr(shellRequest, L'\t');
    if (!target) return;
    *target++ = 0;
    wchar_t parameters[4200];
    if (!wcscmp(shellRequest, L"url")) {
        if (!wcsncmp(target, L"http://", 7) || !wcsncmp(target, L"https://", 8)) ShellExecuteW(window, L"open", target, NULL, NULL, SW_SHOWNORMAL);
    } else if (!wcscmp(shellRequest, L"select")) {
        swprintf(parameters, 4200, L"/select,\"%ls\"", target);
        ShellExecuteW(window, L"open", L"explorer.exe", parameters, NULL, SW_SHOWNORMAL);
    } else if (!wcscmp(shellRequest, L"open")) {
        swprintf(parameters, 4200, L"\"%ls\"", target);
        ShellExecuteW(window, L"open", L"explorer.exe", parameters, NULL, SW_SHOWNORMAL);
    }
    shellRequest[0] = 0;
}

// Returns 1 when the message was one of ours.
static int mediaMessage(HWND window, UINT message, WPARAM wParam, LPARAM lParam) {
    if (taskbarCreatedMessage && message == taskbarCreatedMessage) { addThumbButtons(window); return 1; }
    if (message == WM_COPYDATA) {
        const COPYDATASTRUCT *data = (const COPYDATASTRUCT *)lParam;
        if (data->dwData == 2) shellRequested(window, data); else mediaChanged(window, data);
        return 1;
    }
    if (message == WM_SHELL_REQUEST) { runShellRequest(window); return 1; }
    if (message == WM_COMMAND && HIWORD(wParam) == THBN_CLICKED) {
        int id = LOWORD(wParam);
        if (id >= thumbPrevious && id <= thumbNext) { mediaAction(id == thumbPrevious ? "previous" : id == thumbToggle ? "toggle" : "next"); return 1; }
    }
    if (message == WM_SETTINGCHANGE || message == WM_DPICHANGED) {
        // The taskbar theme or scale may have changed: redraw the glyphs. Not consumed.
        if (thumbAdded) { makeThumbIcons(window); updateThumbButtons(window); }
    }
    return 0;
}
