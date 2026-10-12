// Native CEF C-API host. Go owns audio, plugins, storage and native dialogs.
// No Node.js, Electron, libcef_dll_wrapper or C++ ABI dependency.
#define _WIN32_WINNT 0x0A00
#define NTDDI_VERSION 0x0A000003
#ifndef UNICODE
#define UNICODE
#endif
#define _UNICODE
#define COBJMACROS
#include <windows.h>
#include <windowsx.h>
#include <shellapi.h>
#include <commctrl.h>
#include <dwmapi.h>
#include <stdio.h>
#include <stdint.h>
#include <wchar.h>
#include <tlhelp32.h>
#include "taskbar.h"
#include "media.h"
#include "include/cef_api_hash.h"
#include "include/capi/cef_app_capi.h"
#include "include/capi/cef_browser_capi.h"
#include "include/capi/cef_client_capi.h"
#include "include/capi/cef_drag_handler_capi.h"
#include "include/capi/cef_display_handler_capi.h"
#include "include/capi/cef_request_handler_capi.h"
#include "include/capi/cef_render_handler_capi.h"

static __typeof__(&cef_execute_process) executeProcess;
static __typeof__(&cef_initialize) initializeCef;
static __typeof__(&cef_run_message_loop) runMessageLoop;
static __typeof__(&cef_quit_message_loop) quitMessageLoop;
static __typeof__(&cef_shutdown) shutdownCef;
static __typeof__(&cef_browser_host_create_browser) createBrowser;
static __typeof__(&cef_api_hash) apiHash;
static __typeof__(&cef_string_userfree_utf16_free) freeString;
static __typeof__(&cef_string_list_alloc) listAlloc;
static __typeof__(&cef_string_list_size) listSize;
static __typeof__(&cef_string_list_value) listValue;
static __typeof__(&cef_string_list_free) listFree;
static HWND mainWindow, browserWindow;
static HWND sleepWindow;
static cef_browser_t *sleepBrowser;
static int sleepClosing;
static HWND sleepPreviousForeground;

static cef_browser_t *mainBrowser;
static HRGN dragRegion;
static int shuttingDown;
static wchar_t *startUrl;
static cef_app_t app;
static cef_browser_process_handler_t processHandler;
static cef_client_t client;
static cef_client_t sleepClient;
static cef_life_span_handler_t lifeHandler;
static cef_life_span_handler_t sleepLifeHandler;
static cef_drag_handler_t dragHandler;
static cef_request_handler_t requestHandler;
static cef_context_menu_handler_t menuHandler;
static cef_display_handler_t displayHandler;
static int fullscreen;
static WINDOWPLACEMENT normalPlacement;
static LONG_PTR normalStyle;
static void layoutPanels(void);
static void closeAllViews(void);
static void maybeQuit(void);
static void hostCommand(char *line);
static int panelDragAt(LPARAM point);
static void stepSlides(void);
static void reportFullscreen(int enabled);
static int openDialogPopup(const cef_string_t *name, const cef_popup_features_t *features, cef_window_info_t *info, cef_client_t **newClient, cef_browser_settings_t *settings);
static int mainClosed;

// Static callback objects have process lifetime and retain the owner reference.
static void CEF_CALLBACK addRef(cef_base_ref_counted_t *self) { (void)self; }
static int CEF_CALLBACK releaseRef(cef_base_ref_counted_t *self) { (void)self; return 0; }
static int CEF_CALLBACK hasRef(cef_base_ref_counted_t *self) { (void)self; return 1; }
static int CEF_CALLBACK hasOneRef(cef_base_ref_counted_t *self) { (void)self; return 0; }
static void initBase(cef_base_ref_counted_t *base, size_t size) {
    base->size = size;
    base->add_ref = addRef;
    base->release = releaseRef;
    base->has_one_ref = hasOneRef;
    base->has_at_least_one_ref = hasRef;
}
static cef_string_t cefString(const wchar_t *text) {
    cef_string_t value = {0};
    value.str = (cef_char_t *)text;
    value.length = wcslen(text);
    return value;
}
static void releaseCef(void *object) {
    if (object) { cef_base_ref_counted_t *base = object; base->release(base); }
}
static int isDraggable(LPARAM point) {
    POINT position = { GET_X_LPARAM(point), GET_Y_LPARAM(point) };
    ScreenToClient(mainWindow, &position);
    return dragRegion && PtInRegion(dragRegion, position.x, position.y);
}
static LRESULT CALLBACK childProc(HWND window, UINT message, WPARAM wParam, LPARAM lParam, UINT_PTR id, DWORD_PTR data) {
    if (message == WM_NCHITTEST && SendMessageW(mainWindow, message, wParam, lParam) != HTCLIENT) return HTTRANSPARENT;
    if (message == WM_NCDESTROY) RemoveWindowSubclass(window, childProc, id);
    return DefSubclassProc(window, message, wParam, lParam);
}
static BOOL CALLBACK subclassChild(HWND window, LPARAM data) {
    SetWindowSubclass(window, childProc, 1, 0);
    return TRUE;
}
static void requestClose(void) {
    if (!mainBrowser) return;
    cef_frame_t *frame = mainBrowser->get_main_frame(mainBrowser);
    cef_string_t code = cefString(L"closeButton.click()");
    cef_string_t url = cefString(startUrl);
    frame->execute_java_script(frame, &code, &url, 0);
    releaseCef(frame);
}
static void applyFrame(HWND window) {
    int borderless = IsZoomed(window) || fullscreen;
    DWM_WINDOW_CORNER_PREFERENCE corners = borderless ? DWMWCP_DONOTROUND : DWMWCP_ROUND;
    COLORREF border = DWMWA_COLOR_NONE;
    DwmSetWindowAttribute(window, DWMWA_WINDOW_CORNER_PREFERENCE, &corners, sizeof(corners));
    HRESULT borderResult = DwmSetWindowAttribute(window, DWMWA_BORDER_COLOR, &border, sizeof(border));
    SetPropW(window, L"SimMusicBorderResult", (HANDLE)(uintptr_t)((uint32_t)borderResult + 1));
    MARGINS margins = {0, 0, 0, borderless ? 0 : 1};
    DwmExtendFrameIntoClientArea(window, &margins);
}
static void resizeBrowser(void) {
    if (!browserWindow || IsIconic(mainWindow)) return;
    RECT client, child;
    GetClientRect(mainWindow, &client);
    GetWindowRect(browserWindow, &child);
    POINT origin = {child.left, child.top};
    ScreenToClient(mainWindow, &origin);
    int width = client.right-client.left, height = client.bottom-client.top;
    if (width <= 0 || height <= 0) return;
    if (origin.x == 0 && origin.y == 0 && child.right-child.left == width && child.bottom-child.top == height) return;
    SetWindowPos(browserWindow, NULL, 0, 0, width, height, SWP_NOZORDER | SWP_NOACTIVATE | SWP_NOCOPYBITS);
}
static void cancelSleepWindow(void) {
    if (!sleepBrowser) return;
    cef_frame_t *frame = sleepBrowser->get_main_frame(sleepBrowser);
    cef_string_t code = cefString(L"cancelSleepOperation()");
    cef_string_t url = cefString(startUrl);
    frame->execute_java_script(frame, &code, &url, 0);
    releaseCef(frame);
}
#include "sleep_render.h"
static void CEF_CALLBACK sleepAfterCreated(cef_life_span_handler_t *self, cef_browser_t *browser) {
    browser->base.add_ref(&browser->base);
    sleepBrowser = browser;
    if (sleepClosing) finishSleepClose();
}
static void CEF_CALLBACK sleepBeforeClose(cef_life_span_handler_t *self, cef_browser_t *browser) {
    releaseCef(sleepBrowser); sleepBrowser = NULL;
    if (sleepWindow) KillTimer(sleepWindow, 1);
    releaseSleepSurface();
    if (IsWindow(sleepWindow)) DestroyWindow(sleepWindow);
    sleepWindow = NULL;
    RemovePropW(mainWindow, L"SimMusicSleepHandle");
    if (!shuttingDown) {
        EnableWindow(mainWindow, TRUE);
        if (IsWindow(sleepPreviousForeground)) SetForegroundWindow(sleepPreviousForeground);
    }
}
static cef_life_span_handler_t *CEF_CALLBACK getSleepLife(cef_client_t *self) { return &sleepLifeHandler; }
static void showSleepWindow(void) {
    if (sleepWindow || shuttingDown) return;
    sleepClosing = 0;
    sleepOpacity = 0;
    WNDCLASSEXW cls = { .cbSize = sizeof(cls), .lpfnWndProc = sleepProc, .hInstance = GetModuleHandleW(NULL), .lpszClassName = L"SimMusicSleepWindow" };
    cls.hIcon = LoadIconW(cls.hInstance, MAKEINTRESOURCEW(1));
    RegisterClassExW(&cls);
    MONITORINFO monitor = { .cbSize = sizeof(monitor) };
    GetMonitorInfoW(MonitorFromWindow(mainWindow, MONITOR_DEFAULTTONEAREST), &monitor);
    sleepRect = monitor.rcMonitor;
    RECT rect = sleepRect;
    sleepWindow = CreateWindowExW(WS_EX_TOPMOST | WS_EX_TOOLWINDOW | WS_EX_LAYERED, cls.lpszClassName, L"SimMusic 睡眠定时", WS_POPUP, rect.left, rect.top, rect.right-rect.left, rect.bottom-rect.top, mainWindow, NULL, cls.hInstance, NULL);
    if (!sleepWindow) return;
    sleepDpi = GetDpiForWindow(sleepWindow);
    SetPropW(mainWindow, L"SimMusicSleepHandle", sleepWindow);
    cef_window_info_t info = { .parent_window = sleepWindow, .windowless_rendering_enabled = 1 };
    cef_browser_settings_t settings = { .size = sizeof(settings), .background_color = 0, .windowless_frame_rate = 30 };
    wchar_t address[2048]; swprintf(address, 2048, L"%lssleep.html", startUrl);
    cef_string_t url = cefString(address);
    if (!createBrowser(&info, &sleepClient, &url, &settings, NULL, NULL)) { DestroyWindow(sleepWindow); sleepWindow = NULL; RemovePropW(mainWindow, L"SimMusicSleepHandle"); }
}
static LRESULT CALLBACK windowProc(HWND window, UINT message, WPARAM wParam, LPARAM lParam) {
    if (mediaMessage(window, message, wParam, lParam)) return message == WM_COPYDATA;
    switch (message) {
    case WM_NCCALCSIZE: {
        // Windows retains an invisible resize frame around maximized windows.
        // Keep the client inside the work area so controls are not clipped.
        if (IsZoomed(window) && !fullscreen) {
            RECT *rect = wParam ? &((NCCALCSIZE_PARAMS *)lParam)->rgrc[0] : (RECT *)lParam;
            MONITORINFO monitor = { .cbSize = sizeof(monitor) };
            if (GetMonitorInfoW(MonitorFromRect(rect, MONITOR_DEFAULTTONEAREST), &monitor)) *rect = monitor.rcWork;
        }
        return wParam ? WVR_REDRAW : 0;
    }
    case WM_NCACTIVATE:
        return DefWindowProcW(window, message, wParam, -1);
    case WM_ACTIVATE:
    case WM_DWMCOMPOSITIONCHANGED:
        applyFrame(window);
        break;
    case WM_ERASEBKGND:
        return 1;
    case WM_PAINT: {
        PAINTSTRUCT paint;
        HDC dc = BeginPaint(window, &paint);
        FillRect(dc, &paint.rcPaint, GetStockObject(BLACK_BRUSH));
        EndPaint(window, &paint);
        return 0;
    }
    case WM_NCHITTEST: {
        if (fullscreen) return HTCLIENT;
        RECT rect; GetWindowRect(window, &rect);
        int x = GET_X_LPARAM(lParam), y = GET_Y_LPARAM(lParam);
        int border = MulDiv(6, GetDpiForWindow(window), 96);
        if (!IsZoomed(window)) {
            int left = x < rect.left + border, right = x >= rect.right - border;
            int top = y < rect.top + border, bottom = y >= rect.bottom - border;
            if (top) return left ? HTTOPLEFT : right ? HTTOPRIGHT : HTTOP;
            if (bottom) return left ? HTBOTTOMLEFT : right ? HTBOTTOMRIGHT : HTBOTTOM;
            if (left) return HTLEFT;
            if (right) return HTRIGHT;
        }
        if (isDraggable(lParam) || panelDragAt(lParam)) return HTCAPTION;
        return HTCLIENT;
    }
    case WM_GETMINMAXINFO: {
        if (fullscreen) break;
        MINMAXINFO *info = (MINMAXINFO *)lParam;
        UINT dpi = GetDpiForWindow(window);
        info->ptMinTrackSize.x = MulDiv(880, dpi, 96);
        info->ptMinTrackSize.y = MulDiv(620, dpi, 96);
        MONITORINFO monitor = { .cbSize = sizeof(MONITORINFO) };
        if (GetMonitorInfoW(MonitorFromWindow(window, MONITOR_DEFAULTTONEAREST), &monitor)) {
            info->ptMaxPosition.x = monitor.rcWork.left - monitor.rcMonitor.left;
            info->ptMaxPosition.y = monitor.rcWork.top - monitor.rcMonitor.top;
            info->ptMaxSize.x = monitor.rcWork.right - monitor.rcWork.left;
            info->ptMaxSize.y = monitor.rcWork.bottom - monitor.rcWork.top;
        }
        return 0;
    }
    case WM_SIZE:
        if (wParam != SIZE_MINIMIZED) { applyFrame(window); resizeBrowser(); layoutPanels(); }
        return 0;
    case WM_DPICHANGED: {
        RECT *rect = (RECT *)lParam;
        SetWindowPos(window, NULL, rect->left, rect->top, rect->right - rect->left, rect->bottom - rect->top, SWP_NOZORDER | SWP_NOACTIVATE | SWP_FRAMECHANGED);
        resizeBrowser();
        return 0;
    }
    case WM_CLOSE:
        if (shuttingDown) return DefWindowProcW(window, message, wParam, lParam);
        requestClose();
        return 0;
    case WM_APP + 1:
        shuttingDown = 1;
        closeSleepWindow();
        closeAllViews();
        if (mainBrowser) {
            cef_browser_host_t *host = mainBrowser->get_host(mainBrowser);
            host->close_browser(host, 1);
            releaseCef(host);
        } else { DestroyWindow(window); mainClosed = 1; maybeQuit(); }
        return 0;
    case WM_APP + 10:
        hostCommand((char *)lParam);
        free((char *)lParam);
        return 0;
    case WM_APP + 11:
        // A cookie operation finished on another thread.
        printf("WV:%s\n", (char *)lParam); fflush(stdout);
        free((char *)lParam);
        return 0;
    case WM_APP + 12: stepSlides(); return 0;
    case WM_APP + 2: showSleepWindow(); return 0;
    case WM_APP + 3: closeSleepWindow(); return 0;
    case WM_APP + 4:
        if (!GetPropW(mainWindow, L"SimMusicContentReady")) {
            SetPropW(mainWindow, L"SimMusicContentReady", (HANDLE)1);
            ShowWindow(mainWindow, SW_SHOW);
            SetWindowPos(mainWindow, NULL, 0, 0, 0, 0, SWP_NOMOVE | SWP_NOSIZE | SWP_NOZORDER | SWP_SHOWWINDOW);
        }
        return 0;
    case WM_APP + 6: RemovePropW(mainWindow, L"SimMusicContentReady"); ShowWindow(mainWindow, SW_HIDE); return 0;
    case WM_APP + 5:
        if (sleepWindow && !sleepClosing && !GetPropW(sleepWindow, L"SimMusicContentReady")) {
            SetPropW(sleepWindow, L"SimMusicContentReady", (HANDLE)1);
            revealSleepWindow();
        }
        return 0;
    }
    return DefWindowProcW(window, message, wParam, lParam);
}
static void CEF_CALLBACK afterCreated(cef_life_span_handler_t *self, cef_browser_t *browser) {
    (void)self;
    browser->base.add_ref(&browser->base);
    mainBrowser = browser;
    cef_browser_host_t *host = browser->get_host(browser);
    browserWindow = host->get_window_handle(host);
    releaseCef(host);
    subclassChild(browserWindow, 0);
    EnumChildWindows(browserWindow, subclassChild, 0);
    resizeBrowser();
    printf("HWND:%llu\n", (unsigned long long)(uintptr_t)mainWindow); fflush(stdout);
}
static void CEF_CALLBACK beforeClose(cef_life_span_handler_t *self, cef_browser_t *browser) {
    (void)self; (void)browser;
    releaseCef(mainBrowser); mainBrowser = NULL;
    if (IsWindow(mainWindow)) DestroyWindow(mainWindow);
    // The message loop ends once the extension webviews have closed as well.
    mainClosed = 1;
    maybeQuit();
}
static int CEF_CALLBACK beforePopup(cef_life_span_handler_t *self, cef_browser_t *browser, cef_frame_t *frame,
    const cef_string_t *targetUrl, const cef_string_t *targetName,
    cef_window_open_disposition_t disposition, int gesture, const cef_popup_features_t *features,
    cef_window_info_t *info, cef_client_t **newClient, cef_browser_settings_t *settings,
    cef_dictionary_value_t **extra, int *noJs) {
    // The only window a page of the host may open is one of its own dialogs.
    // Compared by identifier: is_same would take a reference of the main browser away with every call.
    return !mainBrowser || browser->get_identifier(browser) != mainBrowser->get_identifier(mainBrowser) || !openDialogPopup(targetName, features, info, newClient, settings);
}
static int CEF_CALLBACK beforeBrowse(cef_request_handler_t *self, cef_browser_t *browser, cef_frame_t *frame,
    cef_request_t *request, int gesture, int redirect) {
    cef_string_userfree_t url = request->get_url(request);
    int block = !url || url->length < wcslen(startUrl) || wcsncmp((wchar_t *)url->str, startUrl, wcslen(startUrl));
    if (url) freeString(url);
    return block;
}
static void CEF_CALLBACK dragChanged(cef_drag_handler_t *self, cef_browser_t *browser, cef_frame_t *frame,
    size_t count, const cef_draggable_region_t *regions) {
    if (dragRegion) DeleteObject(dragRegion);
    dragRegion = CreateRectRgn(0, 0, 0, 0);
    UINT dpi = GetDpiForWindow(mainWindow);
    for (size_t i = 0; i < count; i++) {
        cef_rect_t r = regions[i].bounds;
        HRGN region = CreateRectRgn(MulDiv(r.x, dpi, 96), MulDiv(r.y, dpi, 96), MulDiv(r.x + r.width, dpi, 96), MulDiv(r.y + r.height, dpi, 96));
        CombineRgn(dragRegion, dragRegion, region, regions[i].draggable ? RGN_OR : RGN_DIFF);
        DeleteObject(region);
    }
    if (browserWindow) EnumChildWindows(browserWindow, subclassChild, 0);
}
// A page never learns the paths of dropped files. They are reported to the Go core when the drag
// enters, one "DRAG:" line of tab-separated UTF-8 paths; the page then only signals the drop.
static int CEF_CALLBACK dragEnter(cef_drag_handler_t *self, cef_browser_t *browser, cef_drag_data_t *data,
    cef_drag_operations_mask_t mask) {
    cef_string_list_t names = listAlloc();
    printf("DRAG:");
    if (data->get_file_names(data, names)) {
        size_t count = listSize(names);
        for (size_t i = 0; i < count; i++) {
            cef_string_t name = {0};
            if (!listValue(names, i, &name)) continue;
            char path[4096];
            int length = WideCharToMultiByte(CP_UTF8, 0, (wchar_t *)name.str, (int)name.length, path, sizeof(path), NULL, NULL);
            if (length > 0) { if (i) putchar('\t'); fwrite(path, 1, length, stdout); }
            if (name.dtor) name.dtor(name.str);
        }
    }
    printf("\n"); fflush(stdout);
    listFree(names);
    data->base.release(&data->base);
    browser->base.release(&browser->base);
    return 0;
}
static void CEF_CALLBACK beforeMenu(cef_context_menu_handler_t *self, cef_browser_t *browser, cef_frame_t *frame,
    cef_context_menu_params_t *params, cef_menu_model_t *model) { model->clear(model); }
static cef_life_span_handler_t *CEF_CALLBACK getLife(cef_client_t *self) { return &lifeHandler; }
static cef_drag_handler_t *CEF_CALLBACK getDrag(cef_client_t *self) { return &dragHandler; }
static cef_request_handler_t *CEF_CALLBACK getRequest(cef_client_t *self) { return &requestHandler; }
static cef_context_menu_handler_t *CEF_CALLBACK getMenu(cef_client_t *self) { return &menuHandler; }
static cef_display_handler_t *CEF_CALLBACK getDisplay(cef_client_t *self) { return &displayHandler; }
static void setMainFullscreen(int enabled) {
    if (fullscreen == enabled) return;
    fullscreen = enabled;
    if (enabled) {
        normalPlacement.length = sizeof(normalPlacement);
        GetWindowPlacement(mainWindow, &normalPlacement);
        normalStyle = GetWindowLongPtrW(mainWindow, GWL_STYLE);
        MONITORINFO monitor = { .cbSize = sizeof(monitor) };
        GetMonitorInfoW(MonitorFromWindow(mainWindow, MONITOR_DEFAULTTONEAREST), &monitor);
        SetWindowLongPtrW(mainWindow, GWL_STYLE, (normalStyle & ~WS_OVERLAPPEDWINDOW) | WS_POPUP);
        RECT r = monitor.rcMonitor;
        SetWindowPos(mainWindow, NULL, r.left, r.top, r.right-r.left, r.bottom-r.top, SWP_NOZORDER | SWP_FRAMECHANGED);
    } else {
        SetWindowLongPtrW(mainWindow, GWL_STYLE, normalStyle);
        SetWindowPlacement(mainWindow, &normalPlacement);
        SetWindowPos(mainWindow, NULL, 0, 0, 0, 0, SWP_NOMOVE | SWP_NOSIZE | SWP_NOZORDER | SWP_FRAMECHANGED);
    }
    reportFullscreen(enabled);
}
static void CEF_CALLBACK fullscreenChanged(cef_display_handler_t *self, cef_browser_t *browser, int enabled) { setMainFullscreen(enabled); }
static cef_browser_process_handler_t *CEF_CALLBACK getProcess(cef_app_t *self) { return &processHandler; }
static void CEF_CALLBACK contextInitialized(cef_browser_process_handler_t *self) {
    WNDCLASSEXW windowClass = { .cbSize = sizeof(WNDCLASSEXW), .style = CS_DBLCLKS, .lpfnWndProc = windowProc,
        .hInstance = GetModuleHandleW(NULL), .hCursor = LoadCursorW(NULL, IDC_ARROW), .hbrBackground = GetStockObject(BLACK_BRUSH), .lpszClassName = L"SimMusicCefWindow" };
    windowClass.hIcon = LoadIconW(windowClass.hInstance, MAKEINTRESOURCEW(1));
    windowClass.hIconSm = LoadImageW(windowClass.hInstance, MAKEINTRESOURCEW(1), IMAGE_ICON, GetSystemMetrics(SM_CXSMICON), GetSystemMetrics(SM_CYSMICON), 0);
    RegisterClassExW(&windowClass);
    UINT dpi = GetDpiForSystem();
    int width = MulDiv(1120, dpi, 96), height = MulDiv(780, dpi, 96);
    mainWindow = CreateWindowExW(WS_EX_APPWINDOW, windowClass.lpszClassName, L"SimMusic Next", WS_OVERLAPPEDWINDOW | WS_CLIPCHILDREN,
        (GetSystemMetrics(SM_CXSCREEN) - width) / 2, (GetSystemMetrics(SM_CYSCREEN) - height) / 2, width, height, NULL, NULL, windowClass.hInstance, NULL);
    SendMessageW(mainWindow, WM_SETICON, ICON_BIG, (LPARAM)windowClass.hIcon);
    SendMessageW(mainWindow, WM_SETICON, ICON_SMALL, (LPARAM)windowClass.hIconSm);
    SetWindowPos(mainWindow, NULL, 0, 0, 0, 0, SWP_NOMOVE | SWP_NOSIZE | SWP_NOZORDER | SWP_NOACTIVATE | SWP_FRAMECHANGED);
    applyFrame(mainWindow);
    setTaskbarIdentity(mainWindow);
    initMedia(mainWindow);
    cef_window_info_t info = { .style = WS_CHILD | WS_VISIBLE | WS_CLIPCHILDREN | WS_CLIPSIBLINGS,
        .parent_window = mainWindow };
    RECT rect; GetClientRect(mainWindow, &rect);
    info.bounds.width = rect.right; info.bounds.height = rect.bottom;
    cef_browser_settings_t settings = { .size = sizeof(settings), .background_color = 0xfff9f9fb };
    cef_string_t url = cefString(startUrl);
    if (!createBrowser(&info, &client, &url, &settings, NULL, NULL)) { DestroyWindow(mainWindow); quitMessageLoop(); }
}

#include "webview.h"
// A fault of the host is recorded beside the CEF log: the address and the modules it may lie in.
static wchar_t crashPath[MAX_PATH];
static LONG WINAPI recordCrash(EXCEPTION_POINTERS *info) {
    HANDLE file = CreateFileW(crashPath, FILE_APPEND_DATA, FILE_SHARE_READ, NULL, OPEN_ALWAYS, FILE_ATTRIBUTE_NORMAL, NULL);
    if (file != INVALID_HANDLE_VALUE) {
        char text[512];
        void *frames[24];
        USHORT count = CaptureStackBackTrace(0, 24, frames, NULL);
        int length = snprintf(text, sizeof(text), "code %08lx at %p (host %p, libcef %p) thread %lu, access %p\r\n", info->ExceptionRecord->ExceptionCode,
            info->ExceptionRecord->ExceptionAddress, (void *)GetModuleHandleW(NULL), (void *)GetModuleHandleW(L"libcef.dll"), GetCurrentThreadId(),
            (void *)info->ExceptionRecord->ExceptionInformation[1]);
        DWORD written;
        WriteFile(file, text, length, &written, NULL);
        for (USHORT i = 0; i < count; i++) { length = snprintf(text, sizeof(text), "  %p\r\n", frames[i]); WriteFile(file, text, length, &written, NULL); }
        CloseHandle(file);
    }
    return EXCEPTION_CONTINUE_SEARCH;
}
static int launchedByMain(void) {
    HANDLE snapshot = CreateToolhelp32Snapshot(TH32CS_SNAPPROCESS, 0);
    if (snapshot == INVALID_HANDLE_VALUE) return 0;
    PROCESSENTRY32W entry = { .dwSize = sizeof(entry) };
    DWORD parent = 0;
    if (Process32FirstW(snapshot, &entry)) do { if (entry.th32ProcessID == GetCurrentProcessId()) { parent = entry.th32ParentProcessID; break; } } while (Process32NextW(snapshot, &entry));
    CloseHandle(snapshot);
    HANDLE process = OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, FALSE, parent);
    if (!process) return 0;
    wchar_t actual[MAX_PATH], expected[MAX_PATH]; DWORD length = MAX_PATH;
    int valid = QueryFullProcessImageNameW(process, 0, actual, &length);
    CloseHandle(process);
    mainExecutable(expected);
    return valid && !wcsicmp(actual, expected);
}
int WINAPI wWinMain(HINSTANCE instance, HINSTANCE previous, LPWSTR commandLine, int show) {
    int argc; wchar_t **argv = CommandLineToArgvW(GetCommandLineW(), &argc);
    wchar_t *cache = NULL, *logPath = NULL;
    int subprocess = 0;
    for (int i = 1; i < argc; i++) {
        if (!wcsncmp(argv[i], L"--type=", 7)) subprocess = 1;
        if (!wcsncmp(argv[i], L"--simmusic-url=", 15)) startUrl = argv[i] + 15;
        if (!wcsncmp(argv[i], L"--simmusic-cache=", 17)) cache = argv[i] + 17;
        if (!wcsncmp(argv[i], L"--simmusic-log=", 15)) logPath = argv[i] + 15;
    }
    if (!subprocess && (!startUrl || !cache || !launchedByMain())) { LocalFree(argv); return 0; }
    SetProcessDpiAwarenessContext(DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2);
    if (!subprocess) SetCurrentProcessExplicitAppUserModelID(L"SimMusic.Next");
    HMODULE cef = LoadLibraryW(L"libcef.dll");
    if (!cef) { fprintf(stderr, "libcef.dll load failed: %lu\n", GetLastError()); return 1; }
#define LOAD(pointer, name) pointer = (__typeof__(pointer))GetProcAddress(cef, name); if (!pointer) return 2
    LOAD(executeProcess, "cef_execute_process"); LOAD(initializeCef, "cef_initialize");
    LOAD(runMessageLoop, "cef_run_message_loop"); LOAD(quitMessageLoop, "cef_quit_message_loop");
    LOAD(shutdownCef, "cef_shutdown"); LOAD(createBrowser, "cef_browser_host_create_browser");
    LOAD(apiHash, "cef_api_hash"); LOAD(freeString, "cef_string_userfree_utf16_free");
    LOAD(listAlloc, "cef_string_list_alloc"); LOAD(listSize, "cef_string_list_size");
    LOAD(listValue, "cef_string_list_value"); LOAD(listFree, "cef_string_list_free");
    LOAD(createBrowserSync, "cef_browser_host_create_browser_sync"); LOAD(createContext, "cef_request_context_create_context");
    LOAD(createDictionary, "cef_dictionary_value_create"); LOAD(createProcessMessage, "cef_process_message_create");
    LOAD(createV8Function, "cef_v8value_create_function"); LOAD(currentV8Context, "cef_v8context_get_current_context");
    if (strcmp(apiHash(0), CEF_API_HASH_PLATFORM)) { fprintf(stderr, "CEF API hash mismatch\n"); return 3; }
    initBase(&app.base, sizeof(app)); initBase(&processHandler.base, sizeof(processHandler));
    initBase(&client.base, sizeof(client)); initBase(&lifeHandler.base, sizeof(lifeHandler));
    initBase(&dragHandler.base, sizeof(dragHandler)); initBase(&requestHandler.base, sizeof(requestHandler));
    initBase(&menuHandler.base, sizeof(menuHandler));
    initBase(&displayHandler.base, sizeof(displayHandler));
    app.get_browser_process_handler = getProcess; processHandler.on_context_initialized = contextInitialized;
    client.get_life_span_handler = getLife; client.get_drag_handler = getDrag;
    client.get_request_handler = getRequest; client.get_context_menu_handler = getMenu;
    client.get_display_handler = getDisplay;
    displayHandler.on_fullscreen_mode_change = fullscreenChanged;
    lifeHandler.on_after_created = afterCreated; lifeHandler.on_before_close = beforeClose;
    lifeHandler.on_before_popup = beforePopup;
    dragHandler.on_draggable_regions_changed = dragChanged; dragHandler.on_drag_enter = dragEnter;
    requestHandler.on_before_browse = beforeBrowse;
    menuHandler.on_before_context_menu = beforeMenu;
    initBase(&sleepLifeHandler.base, sizeof(sleepLifeHandler));
    sleepLifeHandler.on_after_created = sleepAfterCreated;
    sleepLifeHandler.on_before_close = sleepBeforeClose;
    sleepLifeHandler.on_before_popup = beforePopup;
    sleepClient = client;
    sleepClient.get_life_span_handler = getSleepLife;
    sleepClient.get_drag_handler = NULL;
    sleepClient.get_display_handler = NULL;
    sleepClient.get_render_handler = getSleepRender;
    initBase(&sleepRenderHandler.base, sizeof(sleepRenderHandler));
    sleepRenderHandler.get_view_rect = sleepViewRect;
    sleepRenderHandler.get_screen_info = sleepScreenInfo;
    sleepRenderHandler.get_screen_point = sleepScreenPoint;
    sleepRenderHandler.on_paint = sleepPaint;
    initWebviews();
    cef_main_args_t args = { .instance = GetModuleHandleW(NULL) };
    int result = executeProcess(&args, &app, NULL);
    if (result >= 0) return result;
    cef_settings_t settings = { .size = sizeof(settings), .no_sandbox = 1, .windowless_rendering_enabled = 1, .background_color = 0 };
    settings.root_cache_path = cefString(cache);
    settings.cache_path = cefString(cache);
    if (logPath) settings.log_file = cefString(logPath);
    settings.log_severity = LOGSEVERITY_WARNING;
    cef_string_t locale = cefString(L"zh-CN"); settings.locale = locale;
    if (!initializeCef(&args, &settings, &app, NULL)) return 5;
    if (logPath) { swprintf(crashPath, MAX_PATH, L"%ls.crash", logPath); SetUnhandledExceptionFilter(recordCrash); }
    CloseHandle(CreateThread(NULL, 0, readCommands, NULL, 0, NULL));
    runMessageLoop();
    releasePartitions();
    shutdownCef();
    if (dragRegion) DeleteObject(dragRegion);
    LocalFree(argv);
    return 0;
}
