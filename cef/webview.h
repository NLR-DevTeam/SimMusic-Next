// Extension webviews: browsers apart from the host page. A panel is a child of the main window laid
// over its content area, a window is a top-level window, an offscreen view has no window at all.
// The Go core sends commands as lines on standard input and reads "WV:" event lines from standard
// output; fields are tab-separated, with \\ \t \n \r escaped. Included by host.c.
#include "include/capi/cef_load_handler_capi.h"
#include "include/capi/cef_keyboard_handler_capi.h"
#include "include/capi/cef_render_process_handler_capi.h"
#include "include/capi/cef_request_context_capi.h"
#include "include/capi/cef_cookie_capi.h"
#include "include/capi/cef_v8_capi.h"
#include "include/capi/cef_process_message_capi.h"
#include "include/capi/cef_values_capi.h"

enum { viewResizable = 1, viewFrame = 2, viewTop = 4, viewFullscreen = 8, viewShow = 16, viewSkipTaskbar = 32,
    viewMinimizable = 64, viewMaximizable = 128, viewPositioned = 256, viewTitleFixed = 512, viewCover = 1024, viewModal = 2048 };
enum { kindPanel, kindWindow, kindOffscreen };

typedef struct Webview {
    struct Webview *next;
    char id[40];
    int kind;
    HWND window;            // container: the top-level window, or the child of the main window
    cef_browser_t *browser;
    int browserId;
    wchar_t *base;          // origin of the extension's own pages, the target of ext:// links
    int flags;
    int width, height, minWidth, minHeight, maxWidth, maxHeight;
    HRGN dragRegion;
    int fullscreen;
    WINDOWPLACEMENT placement;
    LONG_PTR style;
    int insets[4], shown, holeCount, holes[16][5];
    int offset, slideFrom, slideTo, sliding;   // a covering panel slides in from below and out again
    LARGE_INTEGER slideStart;
    HICON icon;
    int reported;           // the Go core has been told this view is closed
    int modalHeld;          // a dialog that has the main window disabled
    COLORREF background;    // what the container shows where its browser is not
    int settled, stirred, asleep, waking;   // the play page at rest: see viewAwake
    int unrevealed;         // a new panel or window kept out of sight until its page has drawn: see revealView
} Webview;

static __typeof__(&cef_browser_host_create_browser_sync) createBrowserSync;
static __typeof__(&cef_request_context_create_context) createContext;
static __typeof__(&cef_dictionary_value_create) createDictionary;
static __typeof__(&cef_process_message_create) createProcessMessage;
static __typeof__(&cef_v8value_create_function) createV8Function;
static __typeof__(&cef_v8context_get_current_context) currentV8Context;
static Webview *webviews, *creatingView;
static void releaseModal(Webview *view);
static cef_client_t viewClient;
static cef_life_span_handler_t viewLife;
static cef_load_handler_t viewLoad;
static cef_display_handler_t viewDisplay;
static cef_request_handler_t viewRequest;
static cef_drag_handler_t viewDrag;
static cef_context_menu_handler_t viewMenu;
static cef_render_handler_t viewRender;
static cef_keyboard_handler_t viewKeyboard;
static cef_render_process_handler_t rendererHandler;
static cef_v8handler_t nativeHandler;
static struct { wchar_t *path; cef_request_context_t *context; } partitions[64];


static wchar_t *wide(const char *text) {
    int length = MultiByteToWideChar(CP_UTF8, 0, text, -1, NULL, 0);
    wchar_t *result = calloc(length + 1, sizeof(wchar_t));
    MultiByteToWideChar(CP_UTF8, 0, text, -1, result, length);
    return result;
}

// ---------- events ----------

static void emitBegin(const char *event, const char *id) { printf("WV:%s\t%s", event, id); }
static void emitBytes(const char *text, size_t length) {
    putchar('\t');
    size_t start = 0;
    for (size_t i = 0; i < length; i++) {
        char c = text[i];
        const char *escape = c == '\\' ? "\\\\" : c == '\t' ? "\\t" : c == '\n' ? "\\n" : c == '\r' ? "\\r" : NULL;
        if (!escape) continue;
        fwrite(text + start, 1, i - start, stdout);
        fputs(escape, stdout);
        start = i + 1;
    }
    fwrite(text + start, 1, length - start, stdout);
}
static void emitWide(const wchar_t *text, size_t length) {
    if (!text || !length) { putchar('\t'); return; }
    int size = WideCharToMultiByte(CP_UTF8, 0, text, (int)length, NULL, 0, NULL, NULL);
    char *buffer = malloc(size + 1);
    WideCharToMultiByte(CP_UTF8, 0, text, (int)length, buffer, size, NULL, NULL);
    emitBytes(buffer, size);
    free(buffer);
}
static void emitString(const cef_string_t *text) { emitWide(text ? (wchar_t *)text->str : NULL, text ? text->length : 0); }
static void emitUserFree(cef_string_userfree_t text) { emitString(text); if (text) freeString(text); }
static void emitNumber(long long value) { printf("\t%lld", value); }
static void emitEnd(void) { putchar('\n'); fflush(stdout); }

static Webview *findView(const char *id) {
    for (Webview *view = webviews; view; view = view->next) if (!strcmp(view->id, id)) return view;
    return NULL;
}
static Webview *viewOf(cef_browser_t *browser) {
    int id = browser->get_identifier(browser);
    for (Webview *view = webviews; view; view = view->next) if (view->browserId == id) return view;
    return creatingView;
}

// ---------- windows ----------

// Whether a screen point lies in a region a panel's page marked as draggable (-webkit-app-region).
// A panel reaches up to the top of the main window, so its page may offer a place to move it by.
static int panelDragAt(LPARAM point) {
    for (Webview *view = webviews; view; view = view->next) {
        if (view->kind != kindPanel || !view->window || !view->dragRegion || !IsWindowVisible(view->window)) continue;
        POINT position = { GET_X_LPARAM(point), GET_Y_LPARAM(point) };
        ScreenToClient(view->window, &position);
        RECT client;
        GetClientRect(view->window, &client);
        if (!PtInRect(&client, position) || !PtInRegion(view->dragRegion, position.x, position.y)) continue;
        // Where the panel is cut out (the window buttons, a menu) the host page is what is there.
        HRGN shape = CreateRectRgn(0, 0, 0, 0);
        int cut = GetWindowRgn(view->window, shape) != ERROR && !PtInRegion(shape, position.x, position.y);
        DeleteObject(shape);
        if (!cut) return 1;
    }
    return 0;
}
static LRESULT CALLBACK viewChildProc(HWND window, UINT message, WPARAM wParam, LPARAM lParam, UINT_PTR id, DWORD_PTR data) {
    if (message == WM_NCHITTEST) {
        // The frame's resize borders and the drag regions belong to the top-level window. Inside the main
        // window only a panel's own drag regions count: the host page's title bar lies under the panel.
        HWND root = GetAncestor(window, GA_ROOT);
        LRESULT hit = SendMessageW(root, message, wParam, lParam);
        if (hit != HTCLIENT && (root != mainWindow || hit != HTCAPTION || panelDragAt(lParam))) return HTTRANSPARENT;
    }
    if (message == WM_NCDESTROY) RemoveWindowSubclass(window, viewChildProc, id);
    return DefSubclassProc(window, message, wParam, lParam);
}
static BOOL CALLBACK subclassViewChild(HWND window, LPARAM data) {
    SetWindowSubclass(window, viewChildProc, 2, 0);
    return TRUE;
}
// The play page draws only while there is someone to see it: while it is open (or sliding) and the main
// window is on screen. A page hidden behind the host page, or in a window that is minimized, in the tray or
// wholly covered by other windows, would otherwise go on animating at full rate. It is left alone until
// its first page has been drawn once, so that it is laid out and has its pictures before it rests, and
// stirred for a few seconds when the Go core has told it of another song or other lyrics: a page that
// draws nothing does not finish what waits on drawing (a picture being decoded), and would be found
// showing the previous song when it is opened.
static int mainUnseen;
static int viewAwake(Webview *view) {
    if (view->kind != kindPanel || !(view->flags & viewCover) || !view->settled || view->stirred) return 1;
    return (view->shown || view->sliding) && !mainUnseen;
}
// The browser fills its container. One at rest is given no size instead: that is what hides a page as far
// as Chromium is concerned (hiding the window does not). The page stops drawing, its animations and
// requestAnimationFrame stop, document.visibilityState is "hidden"; it keeps the size it was laid out at.
static void resizeView(Webview *view) {
    if (!view->browser || !view->window) return;
    cef_browser_host_t *host = view->browser->get_host(view->browser);
    HWND child = host->get_window_handle(host);
    releaseCef(host);
    RECT client;
    GetClientRect(view->window, &client);
    if (!child || client.right <= 0 || client.bottom <= 0) return;
    view->asleep = !viewAwake(view);
    SetWindowPos(child, NULL, 0, 0, view->asleep ? 0 : client.right, view->asleep ? 0 : client.bottom, SWP_NOZORDER | SWP_NOACTIVATE | SWP_NOCOPYBITS);
}
static void applyViewFrame(Webview *view) {
    int borderless = IsZoomed(view->window) || view->fullscreen;
    DWM_WINDOW_CORNER_PREFERENCE corners = borderless ? DWMWCP_DONOTROUND : DWMWCP_ROUND;
    COLORREF border = DWMWA_COLOR_NONE;
    DwmSetWindowAttribute(view->window, DWMWA_WINDOW_CORNER_PREFERENCE, &corners, sizeof(corners));
    DwmSetWindowAttribute(view->window, DWMWA_BORDER_COLOR, &border, sizeof(border));
    MARGINS margins = {0, 0, 0, borderless ? 0 : 1};
    DwmExtendFrameIntoClientArea(view->window, &margins);
}
static void setViewFullscreen(Webview *view, int enabled) {
    if (view->kind != kindWindow || view->fullscreen == enabled) return;
    view->fullscreen = enabled;
    if (enabled) {
        view->placement.length = sizeof(view->placement);
        GetWindowPlacement(view->window, &view->placement);
        view->style = GetWindowLongPtrW(view->window, GWL_STYLE);
        MONITORINFO monitor = { .cbSize = sizeof(monitor) };
        GetMonitorInfoW(MonitorFromWindow(view->window, MONITOR_DEFAULTTONEAREST), &monitor);
        SetWindowLongPtrW(view->window, GWL_STYLE, (view->style & ~WS_OVERLAPPEDWINDOW) | WS_POPUP);
        RECT r = monitor.rcMonitor;
        SetWindowPos(view->window, NULL, r.left, r.top, r.right - r.left, r.bottom - r.top, SWP_NOZORDER | SWP_FRAMECHANGED);
    } else {
        SetWindowLongPtrW(view->window, GWL_STYLE, view->style | (GetWindowLongPtrW(view->window, GWL_STYLE) & WS_VISIBLE));
        SetWindowPlacement(view->window, &view->placement);
        SetWindowPos(view->window, NULL, 0, 0, 0, 0, SWP_NOMOVE | SWP_NOSIZE | SWP_NOZORDER | SWP_FRAMECHANGED);
    }
}
// A panel covers the main window's client area less its insets, given in CSS pixels: from the top
// edge down to the player bar, right of the sidebar. The window buttons of the host page stay above it. Rectangles of
// host elements shown above it are cut out of its region. A hidden panel leaves the top of the
// z-order, so that the host page stays the main window's first child.
// The timing of a CSS "ease" transition: cubic-bezier(.25, .1, .25, 1).
static double easeCss(double t) {
    double low = 0, high = 1, s = t;
    for (int i = 0; i < 24; i++) {
        s = (low + high) / 2;
        if (3 * (1 - s) * (1 - s) * s * .25 + 3 * (1 - s) * s * s * .25 + s * s * s < t) low = s; else high = s;
    }
    return 3 * (1 - s) * (1 - s) * s * .1 + 3 * (1 - s) * s * s + s * s * s;
}
static void layoutPanel(Webview *view) {
    if (view->kind != kindPanel || !view->window) return;
    RECT client;
    GetClientRect(mainWindow, &client);
    UINT dpi = GetDpiForWindow(mainWindow);
    int left = MulDiv(view->insets[0], dpi, 96), top = MulDiv(view->insets[1], dpi, 96);
    int width = client.right - left - MulDiv(view->insets[2], dpi, 96), height = client.bottom - top - MulDiv(view->insets[3], dpi, 96);
    if (view->flags & viewCover) {
        left = 0; top = 0; width = client.right; height = client.bottom;
        // A covering panel that is not shown is not hidden but parked below the window, clipped away by it:
        // the page stays laid out at its real size, and has only to draw itself before it slides in (wakePanel).
        if (!view->shown && !view->sliding && width > 0 && height > 0) {
            view->offset = height * 6 / 5;
            SetWindowRgn(view->window, NULL, FALSE);
            SetWindowPos(view->window, HWND_BOTTOM, 0, view->offset, width, height, SWP_NOACTIVATE | SWP_SHOWWINDOW);
            return;
        }
    }
    if (!(view->shown || view->sliding) || width <= 0 || height <= 0) {
        SetWindowPos(view->window, HWND_BOTTOM, 0, 0, 0, 0, SWP_NOMOVE | SWP_NOSIZE | SWP_NOACTIVATE | SWP_HIDEWINDOW);
        return;
    }
    HRGN region = NULL;
    if (view->holeCount) {
        region = CreateRectRgn(0, 0, width, height);
        for (int i = 0; i < view->holeCount; i++) {
            int *h = view->holes[i];
            int l = MulDiv(h[0], dpi, 96) - left, t = MulDiv(h[1], dpi, 96) - top - view->offset, r = MulDiv(h[0] + h[2], dpi, 96) - left, b = MulDiv(h[1] + h[3], dpi, 96) - top - view->offset;
            int corner = 2 * MulDiv(h[4], dpi, 96);
            HRGN hole = corner ? CreateRoundRectRgn(l, t, r + 1, b + 1, corner, corner) : CreateRectRgn(l, t, r, b);
            CombineRgn(region, region, hole, RGN_DIFF);
            DeleteObject(hole);
        }
    }
    // Until its page has drawn, the panel is in place at its size but shows nothing (revealView).
    if (view->unrevealed) {
        if (region) DeleteObject(region);
        region = CreateRectRgn(0, 0, 0, 0);
    }
    SetWindowRgn(view->window, region, TRUE);
    SetWindowPos(view->window, HWND_TOP, left, top + view->offset, width, height, SWP_NOACTIVATE | SWP_SHOWWINDOW);
}
// The play page is told whether the main window is fullscreen, however it came to be.
static void reportFullscreen(int enabled) {
    for (Webview *view = webviews; view; view = view->next)
        if (view->kind == kindPanel && (view->flags & viewCover)) { emitBegin("fullscreen", view->id); emitNumber(enabled); emitEnd(); }
}
static volatile LONG slideTicking;
static DWORD WINAPI slideTicks(void *unused) {
    while (slideTicking && !shuttingDown) { DwmFlush(); PostMessageW(mainWindow, WM_APP + 12, 0, 0); }
    return 0;
}
// A covering panel (the play page) moves like the page it replaces: .3s from below the window, and back.
static void slidePanel(Webview *view, int wasShown) {
    RECT client;
    GetClientRect(mainWindow, &client);
    int travel = client.bottom * 6 / 5;
    if (!view->sliding && !wasShown) view->offset = travel;
    SetWindowPos(view->window, HWND_TOP, 0, 0, 0, 0, SWP_NOMOVE | SWP_NOSIZE | SWP_NOACTIVATE);
    view->slideFrom = view->offset;
    view->slideTo = view->shown ? 0 : travel;
    view->sliding = 1;
    QueryPerformanceCounter(&view->slideStart);
    if (!InterlockedExchange(&slideTicking, 1)) CloseHandle(CreateThread(NULL, 0, slideTicks, NULL, 0, NULL));
}
// A browser child shows black until Chromium has drawn its first frame into it. A new panel or window is
// therefore kept out of sight, though laid out at its real size so that the page is drawn, until the page
// reports two frames after its first load (or a second and a half has passed, whatever the page does):
// a panel with an empty window region, a window fully transparent.
static void askDrawn(Webview *view) {
    cef_frame_t *frame = view->browser ? view->browser->get_main_frame(view->browser) : NULL;
    if (!frame) return;
    cef_string_t code = cefString(L"window.__simHostReceive&&window.__simHostReceive(\"p\")"), source = cefString(L"simhost://command");
    frame->execute_java_script(frame, &code, &source, 0);
    releaseCef(frame);
}
// A play page at rest has to draw itself as it is now before it slides in: moved into view while its first
// frames are still being made (a page with heavy effects takes a while over them), the window would drag
// stale pixels up the screen. So it starts drawing where it is parked, and slides once the page reports
// two frames of its own, or after half a second whatever the page does.
static void wakePanel(Webview *view) {
    RECT client;
    GetClientRect(mainWindow, &client);
    if (!view->sliding) view->offset = client.bottom * 6 / 5;
    view->waking = 1;
    resizeView(view);
    askDrawn(view);
    SetTimer(view->window, 1, 500, NULL);
}
static void wokePanel(Webview *view) {
    if (!view->waking) return;
    view->waking = 0;
    KillTimer(view->window, 1);
    if (!view->shown) return;
    slidePanel(view, 0);
    layoutPanel(view);
}
static void revealView(Webview *view) {
    if (!view->unrevealed) return;
    view->unrevealed = 0;
    KillTimer(view->window, 4);
    if (view->kind == kindPanel) layoutPanel(view);
    else {
        SetLayeredWindowAttributes(view->window, 0, 255, LWA_ALPHA);
        SetWindowLongPtrW(view->window, GWL_EXSTYLE, GetWindowLongPtrW(view->window, GWL_EXSTYLE) & ~WS_EX_LAYERED);
        RedrawWindow(view->window, NULL, NULL, RDW_INVALIDATE | RDW_FRAME | RDW_ALLCHILDREN);
    }
}
// One step for every frame the desktop composes, from the thread below.
static void stepSlides(void) {
    LARGE_INTEGER now, frequency;
    QueryPerformanceCounter(&now); QueryPerformanceFrequency(&frequency);
    int sliding = 0;
    for (Webview *view = webviews; view; view = view->next) {
        if (!view->sliding) continue;
        double t = (double)(now.QuadPart - view->slideStart.QuadPart) / frequency.QuadPart / .3;
        if (t >= 1) { view->offset = view->slideTo; view->sliding = 0; }
        else { view->offset = view->slideFrom + (int)((view->slideTo - view->slideFrom) * easeCss(t)); sliding = 1; }
        layoutPanel(view);
        // Slid away, the page rests.
        if (!view->sliding) resizeView(view);
    }
    if (!sliding) InterlockedExchange(&slideTicking, 0);
}
// Whether anything of the main window can be seen: it is shown, not minimized, on this virtual desktop, and
// the windows above it leave some of it uncovered. A window that may be seen through (layered, drawn by
// composition only, or of a shape of its own) is not taken to cover anything.
#ifndef WS_EX_NOREDIRECTIONBITMAP
#define WS_EX_NOREDIRECTIONBITMAP 0x00200000L
#endif
static int mainSeen(void) {
    BOOL cloaked = FALSE;
    if (!IsWindowVisible(mainWindow) || IsIconic(mainWindow)) return 0;
    if (SUCCEEDED(DwmGetWindowAttribute(mainWindow, DWMWA_CLOAKED, &cloaked, sizeof(cloaked))) && cloaked) return 0;
    RECT rect, screen = { GetSystemMetrics(SM_XVIRTUALSCREEN), GetSystemMetrics(SM_YVIRTUALSCREEN), 0, 0 };
    screen.right = screen.left + GetSystemMetrics(SM_CXVIRTUALSCREEN); screen.bottom = screen.top + GetSystemMetrics(SM_CYVIRTUALSCREEN);
    GetWindowRect(mainWindow, &rect);
    if (!IntersectRect(&rect, &rect, &screen)) return 0;
    HRGN left = CreateRectRgnIndirect(&rect), shape = CreateRectRgn(0, 0, 0, 0);
    int seen = 1;
    for (HWND above = GetWindow(mainWindow, GW_HWNDPREV); above && seen; above = GetWindow(above, GW_HWNDPREV)) {
        if (!IsWindowVisible(above) || IsIconic(above)) continue;
        if (GetWindowLongPtrW(above, GWL_EXSTYLE) & (WS_EX_LAYERED | WS_EX_TRANSPARENT | WS_EX_NOREDIRECTIONBITMAP)) continue;
        if (SUCCEEDED(DwmGetWindowAttribute(above, DWMWA_CLOAKED, &cloaked, sizeof(cloaked))) && cloaked) continue;
        if (GetWindowRgn(above, shape) != ERROR) continue;
        RECT cover;
        if (FAILED(DwmGetWindowAttribute(above, DWMWA_EXTENDED_FRAME_BOUNDS, &cover, sizeof(cover))) && !GetWindowRect(above, &cover)) continue;
        HRGN covered = CreateRectRgnIndirect(&cover);
        seen = CombineRgn(left, left, covered, RGN_DIFF) != NULLREGION;
        DeleteObject(covered);
    }
    DeleteObject(left); DeleteObject(shape);
    return seen;
}
// Looked at when the window is shown, hidden, moved or activated, and on a timer besides, since nothing
// tells a window that others moved over it or away: often while unseen, so that the page is back at once.
static void checkSeen(void) {
    int unseen = !mainSeen();
    SetTimer(mainWindow, 1, unseen ? 100 : 1000, NULL);
    if (unseen == mainUnseen) return;
    mainUnseen = unseen;
    for (Webview *view = webviews; view; view = view->next) if (view->kind == kindPanel && (view->flags & viewCover)) resizeView(view);
}
static void layoutPanels(void) {
    for (Webview *view = webviews; view; view = view->next) layoutPanel(view);
}
static LRESULT CALLBACK viewProc(HWND window, UINT message, WPARAM wParam, LPARAM lParam) {
    Webview *view = (Webview *)GetWindowLongPtrW(window, GWLP_USERDATA);
    if (!view) return DefWindowProcW(window, message, wParam, lParam);
    int frameless = view->kind == kindWindow && !(view->flags & viewFrame);
    switch (message) {
    case WM_NCCALCSIZE:
        if (!frameless) break;
        if (IsZoomed(window) && !view->fullscreen) {
            RECT *rect = wParam ? &((NCCALCSIZE_PARAMS *)lParam)->rgrc[0] : (RECT *)lParam;
            MONITORINFO monitor = { .cbSize = sizeof(monitor) };
            if (GetMonitorInfoW(MonitorFromRect(rect, MONITOR_DEFAULTTONEAREST), &monitor)) *rect = monitor.rcWork;
        }
        return wParam ? WVR_REDRAW : 0;
    case WM_NCACTIVATE:
        if (frameless) return DefWindowProcW(window, message, wParam, -1);
        break;
    case WM_ACTIVATE:
    case WM_DWMCOMPOSITIONCHANGED:
        if (frameless) applyViewFrame(view);
        break;
    case WM_ERASEBKGND: {
        // Seen only where the browser is not: all of it while the page rests, until its first frame after.
        RECT client;
        GetClientRect(window, &client);
        HBRUSH brush = CreateSolidBrush(view->background);
        FillRect((HDC)wParam, &client, brush);
        DeleteObject(brush);
        return 1;
    }
    case WM_TIMER:
        if (wParam == 1) { KillTimer(window, 1); wokePanel(view); }
        if (wParam == 2) { KillTimer(window, 2); view->settled = 1; resizeView(view); }
        if (wParam == 3) { KillTimer(window, 3); view->stirred = 0; resizeView(view); }
        if (wParam == 4) revealView(view);
        return 0;
    case WM_NCHITTEST: {
        if (view->kind == kindPanel) {
            LRESULT hit = SendMessageW(mainWindow, message, wParam, lParam);
            return hit != HTCLIENT && (hit != HTCAPTION || panelDragAt(lParam)) ? HTTRANSPARENT : HTCLIENT;
        }
        if (!frameless) break;
        if (view->fullscreen) return HTCLIENT;
        RECT rect; GetWindowRect(window, &rect);
        int x = GET_X_LPARAM(lParam), y = GET_Y_LPARAM(lParam);
        int border = MulDiv(6, GetDpiForWindow(window), 96);
        if (!IsZoomed(window) && (view->flags & viewResizable)) {
            int left = x < rect.left + border, right = x >= rect.right - border;
            int top = y < rect.top + border, bottom = y >= rect.bottom - border;
            if (top) return left ? HTTOPLEFT : right ? HTTOPRIGHT : HTTOP;
            if (bottom) return left ? HTBOTTOMLEFT : right ? HTBOTTOMRIGHT : HTBOTTOM;
            if (left) return HTLEFT;
            if (right) return HTRIGHT;
        }
        POINT position = { x, y };
        ScreenToClient(window, &position);
        return view->dragRegion && PtInRegion(view->dragRegion, position.x, position.y) ? HTCAPTION : HTCLIENT;
    }
    case WM_GETMINMAXINFO: {
        if (view->kind != kindWindow || view->fullscreen) break;
        MINMAXINFO *info = (MINMAXINFO *)lParam;
        UINT dpi = GetDpiForWindow(window);
        // Sizes are those of the page; a native frame adds its own.
        RECT frame = {0};
        if (!frameless) AdjustWindowRectExForDpi(&frame, (DWORD)GetWindowLongPtrW(window, GWL_STYLE), FALSE, (DWORD)GetWindowLongPtrW(window, GWL_EXSTYLE), dpi);
        int extraWidth = frame.right - frame.left, extraHeight = frame.bottom - frame.top;
        if (view->minWidth > 0) info->ptMinTrackSize.x = MulDiv(view->minWidth, dpi, 96) + extraWidth;
        if (view->minHeight > 0) info->ptMinTrackSize.y = MulDiv(view->minHeight, dpi, 96) + extraHeight;
        if (view->maxWidth > 0) info->ptMaxTrackSize.x = MulDiv(view->maxWidth, dpi, 96) + extraWidth;
        if (view->maxHeight > 0) info->ptMaxTrackSize.y = MulDiv(view->maxHeight, dpi, 96) + extraHeight;
        if (!frameless) return 0;
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
        if (wParam != SIZE_MINIMIZED) { if (frameless) applyViewFrame(view); resizeView(view); }
        return 0;
    case WM_DPICHANGED: {
        RECT *rect = (RECT *)lParam;
        SetWindowPos(window, NULL, rect->left, rect->top, rect->right - rect->left, rect->bottom - rect->top, SWP_NOZORDER | SWP_NOACTIVATE | SWP_FRAMECHANGED);
        return 0;
    }
    case WM_SETFOCUS:
        if (view->browser) {
            cef_browser_host_t *host = view->browser->get_host(view->browser);
            host->set_focus(host, 1);
            releaseCef(host);
        }
        return 0;
    case WM_CLOSE:
        // Destroying the container destroys the browser's window, which closes the browser.
        releaseModal(view);
        DestroyWindow(window);
        return 0;
    case WM_NCDESTROY:
        releaseModal(view);
        // With its window gone the view is closed as far as the extension is concerned, whenever
        // the browser itself finishes closing.
        if (!view->reported) { view->reported = 1; emitBegin("closed", view->id); emitEnd(); }
        view->window = NULL;
        SetWindowLongPtrW(window, GWLP_USERDATA, 0);
        break;
    }
    return DefWindowProcW(window, message, wParam, lParam);
}
// A dialog of the host page is a window of its own, modal to the main window: that window is disabled
// while a dialog is shown, and enabled again before the last one goes, so that it is the one to come forward.
static int modalCount;
static void releaseModal(Webview *view) {
    if (!view->modalHeld) return;
    view->modalHeld = 0;
    if (--modalCount > 0 || shuttingDown) return;
    EnableWindow(mainWindow, TRUE);
    SetForegroundWindow(mainWindow);
}
static void centerOnMain(Webview *view) {
    RECT main, own;
    GetWindowRect(mainWindow, &main); GetWindowRect(view->window, &own);
    SetWindowPos(view->window, NULL, main.left + (main.right - main.left - (own.right - own.left)) / 2, main.top + (main.bottom - main.top - (own.bottom - own.top)) / 2, 0, 0, SWP_NOSIZE | SWP_NOZORDER | SWP_NOACTIVATE);
}
static void closeView(Webview *view) {
    releaseModal(view);
    if (view->window) { DestroyWindow(view->window); return; }
    if (!view->browser) return;
    cef_browser_host_t *host = view->browser->get_host(view->browser);
    host->close_browser(host, 1);
    releaseCef(host);
}
static void maybeQuit(void) {
    if (mainClosed && !webviews) quitMessageLoop();
}
// At shutdown a browser that never finishes closing must not keep the process alive.
static void CALLBACK quitAnyway(HWND window, UINT message, UINT_PTR id, DWORD time) {
    KillTimer(NULL, id);
    quitMessageLoop();
}
static void closeAllViews(void) {
    for (Webview *view = webviews, *next; view; view = next) { next = view->next; closeView(view); }
    SetTimer(NULL, 0, 3000, quitAnyway);
}

// ---------- browser callbacks ----------

static Webview *pendingDialog;
static void CEF_CALLBACK viewAfterCreated(cef_life_span_handler_t *self, cef_browser_t *browser) {
    if (creatingView) creatingView->browserId = browser->get_identifier(browser);
    else if (pendingDialog && browser->is_popup(browser)) {
        // The browser of a dialog the host page opened: it fills the window made for it.
        Webview *view = pendingDialog;
        pendingDialog = NULL;
        browser->base.add_ref(&browser->base);
        view->browser = browser;
        view->browserId = browser->get_identifier(browser);
        resizeView(view);
        EnumChildWindows(view->window, subclassViewChild, 0);
    }
    releaseCef(browser);
}
// A page cannot open windows: whatever asks for a new one replaces the current page.
static int CEF_CALLBACK viewBeforePopup(cef_life_span_handler_t *self, cef_browser_t *browser, cef_frame_t *frame,
    const cef_string_t *targetUrl, const cef_string_t *targetName,
    cef_window_open_disposition_t disposition, int gesture, const cef_popup_features_t *features,
    cef_window_info_t *info, cef_client_t **newClient, cef_browser_settings_t *settings,
    cef_dictionary_value_t **extra, int *noJs) {
    if (targetUrl && targetUrl->length) {
        cef_frame_t *top = browser->get_main_frame(browser);
        top->load_url(top, targetUrl);
        releaseCef(top);
    }
    releaseCef(frame); releaseCef(browser);
    return 1;
}
static void CEF_CALLBACK viewBeforeClose(cef_life_span_handler_t *self, cef_browser_t *browser) {
    int id = browser->get_identifier(browser);
    releaseCef(browser);
    for (Webview **link = &webviews; *link; link = &(*link)->next) {
        Webview *view = *link;
        if (view->browserId != id) continue;
        *link = view->next;
        if (!view->reported) { emitBegin("closed", view->id); emitEnd(); }
        releaseCef(view->browser);
        // The container usually went first; one that outlived its browser is closed in its own turn.
        if (view->window) { SetWindowLongPtrW(view->window, GWLP_USERDATA, 0); PostMessageW(view->window, WM_CLOSE, 0, 0); }
        if (view->dragRegion) DeleteObject(view->dragRegion);
        if (view->icon) DestroyIcon(view->icon);
        free(view->base);
        free(view);
        break;
    }
    maybeQuit();
}
static int mainFrameEvent(const char *event, cef_browser_t *browser, cef_frame_t *frame, Webview **out) {
    Webview *view = viewOf(browser);
    int top = frame->is_main(frame);
    if (view && top) { emitBegin(event, view->id); emitUserFree(frame->get_url(frame)); }
    *out = view;
    releaseCef(frame); releaseCef(browser);
    return view && top;
}
static void CEF_CALLBACK viewLoadStart(cef_load_handler_t *self, cef_browser_t *browser, cef_frame_t *frame, cef_transition_type_t transition) {
    Webview *view;
    if (mainFrameEvent("start", browser, frame, &view)) emitEnd();
}
static void CEF_CALLBACK viewLoadEnd(cef_load_handler_t *self, cef_browser_t *browser, cef_frame_t *frame, int status) {
    Webview *view;
    if (!mainFrameEvent("load", browser, frame, &view)) return;
    emitNumber(status); emitEnd();
    if (view->window) EnumChildWindows(view->window, subclassViewChild, 0);
    // The play page may rest once this first page has had time to be drawn.
    if (view->unrevealed) askDrawn(view);
    if (view->window && (view->flags & viewCover) && !view->settled) SetTimer(view->window, 2, 1500, NULL);
}
static void CEF_CALLBACK viewLoadError(cef_load_handler_t *self, cef_browser_t *browser, cef_frame_t *frame, cef_errorcode_t code,
    const cef_string_t *text, const cef_string_t *failedUrl) {
    Webview *view = viewOf(browser);
    // ERR_ABORTED is a navigation replaced by another one, not a failure of the page.
    if (view && frame->is_main(frame) && code != ERR_ABORTED) {
        emitBegin("fail", view->id); emitString(failedUrl); emitNumber(code); emitString(text); emitEnd();
    }
    releaseCef(frame); releaseCef(browser);
}
static void CEF_CALLBACK viewAddressChange(cef_display_handler_t *self, cef_browser_t *browser, cef_frame_t *frame, const cef_string_t *url) {
    Webview *view = viewOf(browser);
    if (view && frame->is_main(frame)) { emitBegin("nav", view->id); emitString(url); emitEnd(); }
    releaseCef(frame); releaseCef(browser);
}
static void CEF_CALLBACK viewTitleChange(cef_display_handler_t *self, cef_browser_t *browser, const cef_string_t *title) {
    Webview *view = viewOf(browser);
    if (view) {
        emitBegin("title", view->id); emitString(title); emitEnd();
        if (view->kind == kindWindow && view->window && !(view->flags & viewTitleFixed) && title && title->str) SetWindowTextW(view->window, (wchar_t *)title->str);
    }
    releaseCef(browser);
}
static void CEF_CALLBACK viewFullscreenChange(cef_display_handler_t *self, cef_browser_t *browser, int enabled) {
    Webview *view = viewOf(browser);
    // A panel that covers the whole main window (the play page) takes that window fullscreen with it.
    if (view && view->kind == kindPanel && (view->flags & viewCover)) setMainFullscreen(enabled);
    else if (view && view->window) setViewFullscreen(view, enabled);
    releaseCef(browser);
}
// ext://page.html is the extension's own file, wherever a link to it appears.
static int CEF_CALLBACK viewBeforeBrowse(cef_request_handler_t *self, cef_browser_t *browser, cef_frame_t *frame,
    cef_request_t *request, int gesture, int redirect) {
    Webview *view = viewOf(browser);
    cef_string_userfree_t url = request->get_url(request);
    int handled = 0;
    if (view && view->base && url && url->length > 6 && !_wcsnicmp((wchar_t *)url->str, L"ext://", 6)) {
        const wchar_t *rest = (wchar_t *)url->str + 6;
        while (*rest == L'/') rest++;
        size_t size = wcslen(view->base) + wcslen(rest) + 1;
        wchar_t *address = calloc(size, sizeof(wchar_t));
        swprintf(address, size, L"%ls%ls", view->base, rest);
        cef_string_t target = cefString(address);
        cef_frame_t *top = browser->get_main_frame(browser);
        top->load_url(top, &target);
        releaseCef(top);
        free(address);
        handled = 1;
    }
    if (url) freeString(url);
    releaseCef(request); releaseCef(frame); releaseCef(browser);
    return handled;
}
static int CEF_CALLBACK viewOpenFromTab(cef_request_handler_t *self, cef_browser_t *browser, cef_frame_t *frame,
    const cef_string_t *targetUrl, cef_window_open_disposition_t disposition, int gesture) {
    cef_frame_t *top = browser->get_main_frame(browser);
    top->load_url(top, targetUrl);
    releaseCef(top); releaseCef(frame); releaseCef(browser);
    return 1;
}
static void CEF_CALLBACK viewDragChanged(cef_drag_handler_t *self, cef_browser_t *browser, cef_frame_t *frame,
    size_t count, const cef_draggable_region_t *regions) {
    Webview *view = viewOf(browser);
    if (view && view->window && (view->kind == kindPanel || (view->kind == kindWindow && !(view->flags & viewFrame)))) {
        if (view->dragRegion) DeleteObject(view->dragRegion);
        view->dragRegion = CreateRectRgn(0, 0, 0, 0);
        UINT dpi = GetDpiForWindow(view->window);
        // The regions come in document order. What a page marks as not draggable stays so wherever it stands
        // in the document, a button placed over a draggable header included: those are taken out last.
        for (int pass = 1; pass >= 0; pass--) for (size_t i = 0; i < count; i++) {
            if (regions[i].draggable != pass) continue;
            cef_rect_t r = regions[i].bounds;
            HRGN region = CreateRectRgn(MulDiv(r.x, dpi, 96), MulDiv(r.y, dpi, 96), MulDiv(r.x + r.width, dpi, 96), MulDiv(r.y + r.height, dpi, 96));
            CombineRgn(view->dragRegion, view->dragRegion, region, pass ? RGN_OR : RGN_DIFF);
            DeleteObject(region);
        }
        EnumChildWindows(view->window, subclassViewChild, 0);
    }
    releaseCef(frame); releaseCef(browser);
}
// Files dropped on the play page would replace it with the file: the drag is refused there.
static int CEF_CALLBACK viewDragEnter(cef_drag_handler_t *self, cef_browser_t *browser, cef_drag_data_t *data, cef_drag_operations_mask_t mask) {
    Webview *view = viewOf(browser);
    int refuse = view && (view->flags & viewCover);
    releaseCef(data); releaseCef(browser);
    return refuse;
}
// Keep the editing commands; a webview has no history buttons, printing or source view.
static void CEF_CALLBACK viewBeforeMenu(cef_context_menu_handler_t *self, cef_browser_t *browser, cef_frame_t *frame,
    cef_context_menu_params_t *params, cef_menu_model_t *model) {
    static const int removed[] = { MENU_ID_BACK, MENU_ID_FORWARD, MENU_ID_RELOAD, MENU_ID_RELOAD_NOCACHE, MENU_ID_STOPLOAD, MENU_ID_PRINT, MENU_ID_VIEW_SOURCE, MENU_ID_FIND };
    for (size_t i = 0; i < sizeof(removed) / sizeof(*removed); i++) model->remove(model, removed[i]);
    while (model->get_count(model) && model->get_type_at(model, 0) == MENUITEMTYPE_SEPARATOR) model->remove_at(model, 0);
    while (model->get_count(model) && model->get_type_at(model, model->get_count(model) - 1) == MENUITEMTYPE_SEPARATOR) model->remove_at(model, model->get_count(model) - 1);
    releaseCef(model); releaseCef(params); releaseCef(frame); releaseCef(browser);
}
// Escape always closes a window of its own, whatever the page does with its keys and whether or
// not the window is fullscreen: no page can keep the user in one.
static int CEF_CALLBACK viewPreKey(cef_keyboard_handler_t *self, cef_browser_t *browser, const cef_key_event_t *event,
    cef_event_handle_t osEvent, int *shortcut) {
    Webview *view = viewOf(browser);
    int close = view && view->kind == kindWindow && view->window && event->windows_key_code == VK_ESCAPE;
    if (close && event->type == KEYEVENT_RAWKEYDOWN) PostMessageW(view->window, WM_CLOSE, 0, 0);
    releaseCef(browser);
    return close;
}
// An offscreen view only needs a size to lay its page out in; what it paints is discarded.
static void CEF_CALLBACK viewRect(cef_render_handler_t *self, cef_browser_t *browser, cef_rect_t *rect) {
    Webview *view = viewOf(browser);
    rect->x = rect->y = 0;
    rect->width = view && view->width > 0 ? view->width : 1280;
    rect->height = view && view->height > 0 ? view->height : 720;
    releaseCef(browser);
}
static void CEF_CALLBACK viewPaint(cef_render_handler_t *self, cef_browser_t *browser, cef_paint_element_type_t type,
    size_t count, const cef_rect_t *rects, const void *buffer, int width, int height) { releaseCef(browser); }
// A message of the page's simHost object, or the result of a script the extension ran in it.
static int CEF_CALLBACK viewProcessMessage(cef_client_t *self, cef_browser_t *browser, cef_frame_t *frame,
    cef_process_id_t source, cef_process_message_t *message) {
    Webview *view = viewOf(browser);
    cef_string_userfree_t name = message->get_name(message);
    int handled = name && !wcscmp((wchar_t *)name->str, L"simHost");
    if (handled && view && frame->is_main(frame)) {
        cef_list_value_t *list = message->get_argument_list(message);
        cef_string_userfree_t kind = list->get_string(list, 0);
        // "p": the page has drawn since it was asked to say so. The host's own; the Go core is not told.
        if (kind && kind->length == 1 && kind->str[0] == L'p') { revealView(view); wokePanel(view); freeString(kind); }
        else {
            emitBegin("msg", view->id);
            emitUserFree(frame->get_url(frame));
            emitUserFree(kind);
            emitUserFree(list->get_string(list, 1));
            emitEnd();
        }
        releaseCef(list);
    }
    if (name) freeString(name);
    releaseCef(message); releaseCef(frame); releaseCef(browser);
    return handled;
}
static cef_life_span_handler_t *CEF_CALLBACK getViewLife(cef_client_t *self) { return &viewLife; }
static cef_load_handler_t *CEF_CALLBACK getViewLoad(cef_client_t *self) { return &viewLoad; }
static cef_display_handler_t *CEF_CALLBACK getViewDisplay(cef_client_t *self) { return &viewDisplay; }
static cef_request_handler_t *CEF_CALLBACK getViewRequest(cef_client_t *self) { return &viewRequest; }
static cef_drag_handler_t *CEF_CALLBACK getViewDrag(cef_client_t *self) { return &viewDrag; }
static cef_context_menu_handler_t *CEF_CALLBACK getViewMenu(cef_client_t *self) { return &viewMenu; }
static cef_render_handler_t *CEF_CALLBACK getViewRender(cef_client_t *self) { return &viewRender; }
static cef_keyboard_handler_t *CEF_CALLBACK getViewKeyboard(cef_client_t *self) { return &viewKeyboard; }

// ---------- cookies ----------

// One callback object for the three cookie operations; each CEF structure is a base and one function.
typedef struct { cef_base_ref_counted_t base; void *function; LONG references; int visitor, done; char request[32]; } CookieCallback;
static void postCookieLine(const char *event, const char *request, int value) {
    char *line = malloc(96);
    snprintf(line, 96, "%s\t%s\t%d", event, request, value);
    if (!mainWindow || !PostMessageW(mainWindow, WM_APP + 11, 0, (LPARAM)line)) free(line);
}
static void CEF_CALLBACK cookieAddRef(cef_base_ref_counted_t *self) { InterlockedIncrement(&((CookieCallback *)self)->references); }
static int CEF_CALLBACK cookieRelease(cef_base_ref_counted_t *self) {
    CookieCallback *callback = (CookieCallback *)self;
    if (InterlockedDecrement(&callback->references)) return 0;
    // The last reference may go on any thread; the line is written by the window's thread.
    if (callback->visitor) postCookieLine("cookieEnd", callback->request, 0);
    else if (!callback->done) postCookieLine("cookieDone", callback->request, 0);
    free(callback);
    return 1;
}
static int CEF_CALLBACK cookieHasOne(cef_base_ref_counted_t *self) { return ((CookieCallback *)self)->references == 1; }
static int CEF_CALLBACK cookieHasAny(cef_base_ref_counted_t *self) { return ((CookieCallback *)self)->references > 0; }
static int CEF_CALLBACK cookieVisit(cef_cookie_visitor_t *self, const cef_cookie_t *cookie, int count, int total, int *deleteCookie) {
    emitBegin("cookie", ((CookieCallback *)self)->request);
    emitString(&cookie->name); emitString(&cookie->value); emitString(&cookie->domain); emitString(&cookie->path);
    emitNumber(cookie->secure); emitNumber(cookie->httponly);
    emitNumber(cookie->has_expires ? cookie->expires.val / 1000000 - 11644473600LL : 0);
    emitEnd();
    return 1;
}
static void CEF_CALLBACK cookieComplete(void *self, int value) {
    CookieCallback *callback = self;
    callback->done = 1;
    postCookieLine("cookieDone", callback->request, value);
}
static CookieCallback *cookieCallback(const char *request, int visitor) {
    CookieCallback *callback = calloc(1, sizeof(CookieCallback));
    callback->base.size = sizeof(cef_cookie_visitor_t);
    callback->base.add_ref = cookieAddRef; callback->base.release = cookieRelease;
    callback->base.has_one_ref = cookieHasOne; callback->base.has_at_least_one_ref = cookieHasAny;
    callback->function = visitor ? (void *)cookieVisit : (void *)cookieComplete;
    callback->references = 1;
    callback->visitor = visitor;
    snprintf(callback->request, sizeof(callback->request), "%s", request);
    return callback;
}
// Each extension has its own browser storage, kept under the given directory.
static cef_request_context_t *partition(const char *path) {
    wchar_t *directory = wide(path);
    int count = sizeof(partitions) / sizeof(*partitions);
    for (int i = 0; i < count; i++) {
        if (partitions[i].path && !_wcsicmp(partitions[i].path, directory)) { free(directory); return partitions[i].context; }
        if (partitions[i].path) continue;
        cef_request_context_settings_t settings = { .size = sizeof(settings) };
        settings.cache_path = cefString(directory);
        cef_request_context_t *context = createContext(&settings, NULL);
        if (!context) break;
        quietContext(context);
        partitions[i].path = directory;
        partitions[i].context = context;
        return context;
    }
    free(directory);
    return NULL;
}
static void releasePartitions(void) {
    for (int i = 0; i < (int)(sizeof(partitions) / sizeof(*partitions)); i++) if (partitions[i].context) { releaseCef(partitions[i].context); partitions[i].context = NULL; }
}
static void cookieCommand(const char *command, char **f, int count) {
    // request, partition, url, then the cookie
    cef_request_context_t *context = count > 1 ? partition(f[1]) : NULL;
    cef_cookie_manager_t *manager = context ? context->get_cookie_manager(context, NULL) : NULL;
    int visitor = !strcmp(command, "cookieGet");
    if (!manager) { postCookieLine(visitor ? "cookieEnd" : "cookieDone", f[0], 0); return; }
    CookieCallback *callback = cookieCallback(f[0], visitor);
    wchar_t *address = wide(count > 2 ? f[2] : "");
    cef_string_t url = cefString(address);
    if (visitor) {
        if (*address) manager->visit_url_cookies(manager, &url, 1, (cef_cookie_visitor_t *)callback);
        else manager->visit_all_cookies(manager, (cef_cookie_visitor_t *)callback);
    } else if (!strcmp(command, "cookieSet") && count >= 10) {
        wchar_t *name = wide(f[3]), *value = wide(f[4]), *domain = wide(f[5]), *path = wide(f[6]);
        cef_cookie_t cookie = { .name = cefString(name), .value = cefString(value), .domain = cefString(domain), .path = cefString(path),
            .secure = f[7][0] == '1', .httponly = f[8][0] == '1' };
        long long expires = atoll(f[9]);
        if (expires > 0) { cookie.has_expires = 1; cookie.expires.val = (expires + 11644473600LL) * 1000000; }
        manager->set_cookie(manager, &url, &cookie, (cef_set_cookie_callback_t *)callback);
        free(name); free(value); free(domain); free(path);
    } else {
        wchar_t *name = wide(count > 3 ? f[3] : "");
        cef_string_t cookieName = cefString(name);
        manager->delete_cookies(manager, *address ? &url : NULL, *name ? &cookieName : NULL, (cef_delete_cookies_callback_t *)callback);
        free(name);
    }
    free(address);
    releaseCef(manager);
}

// ---------- commands ----------

static HICON loadViewIcon(const wchar_t *path, int size) {
    HICON icon = LoadImageW(NULL, path, IMAGE_ICON, size, size, LR_LOADFROMFILE);
    if (icon) return icon;
    // A PNG (or any icon resource image) is turned into an icon by the system.
    HANDLE file = CreateFileW(path, GENERIC_READ, FILE_SHARE_READ, NULL, OPEN_EXISTING, 0, NULL);
    if (file == INVALID_HANDLE_VALUE) return NULL;
    DWORD length = GetFileSize(file, NULL), read = 0;
    BYTE *data = length && length < (16 << 20) ? malloc(length) : NULL;
    if (data && ReadFile(file, data, length, &read, NULL) && read == length) icon = CreateIconFromResourceEx(data, length, TRUE, 0x00030000, size, size, 0);
    free(data);
    CloseHandle(file);
    return icon;
}
static void registerViewClass(void) {
    static int registered;
    if (registered) return;
    HINSTANCE instance = GetModuleHandleW(NULL);
    WNDCLASSEXW cls = { .cbSize = sizeof(cls), .style = CS_DBLCLKS, .lpfnWndProc = viewProc, .hInstance = instance,
        .hCursor = LoadCursorW(NULL, IDC_ARROW), .hbrBackground = GetStockObject(WHITE_BRUSH), .lpszClassName = L"SimMusicWebview" };
    cls.hIcon = LoadIconW(instance, MAKEINTRESOURCEW(1));
    cls.hIconSm = LoadImageW(instance, MAKEINTRESOURCEW(1), IMAGE_ICON, GetSystemMetrics(SM_CXSMICON), GetSystemMetrics(SM_CYSMICON), 0);
    RegisterClassExW(&cls);
    registered = 1;
}
// window.open of the host page with a name beginning "simDialog" makes a dialog: a frameless window owned by
// the main window, with the page's own browser inside. The host page fills it with the dialog it built; being
// opened by that page, it stays in the same renderer, so the dialog's elements and handlers simply move over.
// The window stays hidden until the Go core shows it; it is sized, shown and closed like any webview window.
static int openDialogPopup(const cef_string_t *name, const cef_popup_features_t *features, cef_window_info_t *info, cef_client_t **newClient, cef_browser_settings_t *settings) {
    if (!name || name->length < 10 || name->length > 30 || wcsncmp((wchar_t *)name->str, L"simDialog", 9) || shuttingDown) return 0;
    if (pendingDialog) { Webview *stale = pendingDialog; pendingDialog = NULL; if (stale->window) DestroyWindow(stale->window); }
    Webview *view = calloc(1, sizeof(Webview));
    WideCharToMultiByte(CP_UTF8, 0, (wchar_t *)name->str, (int)name->length, view->id, sizeof(view->id) - 1, NULL, NULL);
    view->kind = kindWindow;
    view->flags = viewModal;
    view->background = RGB(0xf9, 0xf9, 0xfb);
    view->base = wide("");
    registerViewClass();
    UINT dpi = GetDpiForWindow(mainWindow);
    int width = MulDiv(features && features->widthSet && features->width > 0 ? features->width : 500, dpi, 96);
    int height = MulDiv(features && features->heightSet && features->height > 0 ? features->height : 300, dpi, 96);
    view->window = CreateWindowExW(0, L"SimMusicWebview", L"SimMusic", WS_OVERLAPPED | WS_CAPTION | WS_SYSMENU | WS_CLIPCHILDREN, 0, 0, width, height, mainWindow, NULL, GetModuleHandleW(NULL), NULL);
    if (!view->window) { free(view->base); free(view); return 0; }
    SetWindowLongPtrW(view->window, GWLP_USERDATA, (LONG_PTR)view);
    SetWindowPos(view->window, NULL, 0, 0, width, height, SWP_NOMOVE | SWP_NOZORDER | SWP_NOACTIVATE | SWP_FRAMECHANGED);
    applyViewFrame(view);
    centerOnMain(view);
    RECT client;
    GetClientRect(view->window, &client);
    info->style = WS_CHILD | WS_VISIBLE | WS_CLIPCHILDREN | WS_CLIPSIBLINGS;
    info->ex_style = 0;
    info->parent_window = view->window;
    info->bounds.x = info->bounds.y = 0;
    info->bounds.width = client.right; info->bounds.height = client.bottom;
    settings->background_color = 0xfff9f9fb;
    *newClient = &viewClient;
    view->next = webviews;
    webviews = view;
    pendingDialog = view;
    return 1;
}
static void createView(char **f, int count) {
    // id kind url base partition title icon background width height minWidth minHeight maxWidth maxHeight x y flags
    if (count < 17 || findView(f[0]) || shuttingDown) return;
    Webview *view = calloc(1, sizeof(Webview));
    snprintf(view->id, sizeof(view->id), "%s", f[0]);
    view->kind = !strcmp(f[1], "panel") ? kindPanel : !strcmp(f[1], "offscreen") ? kindOffscreen : kindWindow;
    view->base = wide(f[3]);
    view->width = atoi(f[8]); view->height = atoi(f[9]);
    view->minWidth = atoi(f[10]); view->minHeight = atoi(f[11]); view->maxWidth = atoi(f[12]); view->maxHeight = atoi(f[13]);
    view->flags = atoi(f[16]);
    HINSTANCE instance = GetModuleHandleW(NULL);
    registerViewClass();
    UINT dpi = GetDpiForWindow(mainWindow);
    wchar_t *title = wide(f[5][0] ? f[5] : "SimMusic");
    cef_window_info_t info = {0};
    if (view->kind == kindPanel) {
        view->window = CreateWindowExW(0, L"SimMusicWebview", L"", WS_CHILD | WS_CLIPCHILDREN | WS_CLIPSIBLINGS, 0, 0, 0, 0, mainWindow, NULL, instance, NULL);
    } else if (view->kind == kindWindow) {
        DWORD style = WS_OVERLAPPEDWINDOW | WS_CLIPCHILDREN, extended = view->flags & viewSkipTaskbar ? WS_EX_TOOLWINDOW : WS_EX_APPWINDOW;
        if (!(view->flags & viewResizable)) style &= ~(WS_THICKFRAME | WS_MAXIMIZEBOX);
        if (!(view->flags & viewMinimizable)) style &= ~WS_MINIMIZEBOX;
        if (!(view->flags & viewMaximizable)) style &= ~WS_MAXIMIZEBOX;
        RECT rect = { 0, 0, MulDiv(view->width > 0 ? view->width : 800, dpi, 96), MulDiv(view->height > 0 ? view->height : 600, dpi, 96) };
        if (view->flags & viewFrame) AdjustWindowRectExForDpi(&rect, style, FALSE, extended, dpi);
        int width = rect.right - rect.left, height = rect.bottom - rect.top;
        MONITORINFO monitor = { .cbSize = sizeof(monitor) };
        GetMonitorInfoW(MonitorFromWindow(mainWindow, MONITOR_DEFAULTTONEAREST), &monitor);
        int x = monitor.rcWork.left + (monitor.rcWork.right - monitor.rcWork.left - width) / 2, y = monitor.rcWork.top + (monitor.rcWork.bottom - monitor.rcWork.top - height) / 2;
        if (view->flags & viewPositioned) { x = MulDiv(atoi(f[14]), dpi, 96); y = MulDiv(atoi(f[15]), dpi, 96); }
        view->window = CreateWindowExW(extended | WS_EX_LAYERED, L"SimMusicWebview", title, style, x, y, width, height, NULL, NULL, instance, NULL);
        if (view->window) SetLayeredWindowAttributes(view->window, 0, 0, LWA_ALPHA);
    }
    free(title);
    if (view->kind != kindOffscreen && !view->window) { emitBegin("closed", view->id); emitEnd(); free(view->base); free(view); return; }
    if (view->window) {
        SetWindowLongPtrW(view->window, GWLP_USERDATA, (LONG_PTR)view);
        if (view->kind == kindWindow) {
            wchar_t *path = wide(f[6]);
            if (*path && (view->icon = loadViewIcon(path, GetSystemMetrics(SM_CXICON)))) {
                SendMessageW(view->window, WM_SETICON, ICON_BIG, (LPARAM)view->icon);
                SendMessageW(view->window, WM_SETICON, ICON_SMALL, (LPARAM)view->icon);
            }
            free(path);
            if (!(view->flags & viewFrame)) {
                SetWindowPos(view->window, NULL, 0, 0, 0, 0, SWP_NOMOVE | SWP_NOSIZE | SWP_NOZORDER | SWP_NOACTIVATE | SWP_FRAMECHANGED);
                applyViewFrame(view);
            }
        }
        RECT client;
        GetClientRect(view->window, &client);
        info.style = WS_CHILD | WS_VISIBLE | WS_CLIPCHILDREN | WS_CLIPSIBLINGS;
        info.parent_window = view->window;
        info.bounds.width = client.right; info.bounds.height = client.bottom;
    } else {
        info.windowless_rendering_enabled = 1;
        info.parent_window = mainWindow;
    }
    cef_browser_settings_t settings = { .size = sizeof(settings), .background_color = 0xffffffff, .windowless_frame_rate = 1 };
    unsigned int color;
    if (f[7][0] == '#' && strlen(f[7]) == 7 && sscanf(f[7] + 1, "%6x", &color) == 1) settings.background_color = 0xff000000 | color;
    view->background = RGB((settings.background_color >> 16) & 255, (settings.background_color >> 8) & 255, settings.background_color & 255);
    cef_dictionary_value_t *extra = createDictionary();
    cef_string_t key = cefString(L"simWebview");
    extra->set_bool(extra, &key, 1);
    cef_request_context_t *context = partition(f[4]);
    if (context) context->base.add_ref(&context->base);
    wchar_t *address = wide(f[2]);
    cef_string_t url = cefString(address);
    view->next = webviews;
    webviews = view;
    creatingView = view;
    view->browser = createBrowserSync(&info, &viewClient, &url, &settings, extra, context);
    creatingView = NULL;
    free(address);
    if (!view->browser) {
        webviews = view->next;
        emitBegin("closed", view->id); emitEnd();
        if (view->window) { SetWindowLongPtrW(view->window, GWLP_USERDATA, 0); DestroyWindow(view->window); }
        free(view->base); free(view);
        return;
    }
    view->browserId = view->browser->get_identifier(view->browser);
    if (view->window) EnumChildWindows(view->window, subclassViewChild, 0);
    if (view->kind == kindWindow || (view->kind == kindPanel && !(view->flags & viewCover))) {
        view->unrevealed = 1;
        SetTimer(view->window, 4, 1500, NULL);
    }
    if (view->kind == kindPanel && (view->flags & viewCover)) layoutPanel(view);
    if (view->kind == kindWindow) {
        if (view->flags & viewFullscreen) setViewFullscreen(view, 1);
        if (view->flags & viewShow) { ShowWindow(view->window, SW_SHOW); SetForegroundWindow(view->window); }
        // Topmost is applied to the finished window. A browser created inside a window that is already
        // topmost fails to get its GPU surface at first, and that failure costs every page of the host
        // its GPU context: the main window flashes and renders slower from then on.
        if (view->flags & viewTop) SetWindowPos(view->window, HWND_TOPMOST, 0, 0, 0, 0, SWP_NOMOVE | SWP_NOSIZE | SWP_NOACTIVATE);
    }
}
static void windowCommand(Webview *view, const char *action, const char *a, const char *b) {
    HWND window = view->window;
    if (view->kind != kindWindow || !window) return;
    UINT dpi = GetDpiForWindow(window);
    if (!strcmp(action, "show")) {
        if ((view->flags & viewModal) && !view->modalHeld) { view->modalHeld = 1; modalCount++; EnableWindow(mainWindow, FALSE); }
        ShowWindow(window, IsIconic(window) ? SW_RESTORE : SW_SHOW); SetForegroundWindow(window);
    }
    else if (!strcmp(action, "hide")) ShowWindow(window, SW_HIDE);
    else if (!strcmp(action, "focus")) { if (IsIconic(window)) ShowWindow(window, SW_RESTORE); SetForegroundWindow(window); }
    else if (!strcmp(action, "minimize")) ShowWindow(window, SW_MINIMIZE);
    else if (!strcmp(action, "maximize")) ShowWindow(window, SW_MAXIMIZE);
    else if (!strcmp(action, "restore")) ShowWindow(window, SW_RESTORE);
    else if (!strcmp(action, "title")) { wchar_t *title = wide(a); SetWindowTextW(window, title); free(title); view->flags |= viewTitleFixed; }
    else if (!strcmp(action, "top")) SetWindowPos(window, a[0] == '1' ? HWND_TOPMOST : HWND_NOTOPMOST, 0, 0, 0, 0, SWP_NOMOVE | SWP_NOSIZE | SWP_NOACTIVATE);
    else if (!strcmp(action, "fullscreen")) setViewFullscreen(view, a[0] == '1');
    else if (!strcmp(action, "size")) {
        RECT rect = { 0, 0, MulDiv(atoi(a), dpi, 96), MulDiv(atoi(b), dpi, 96) };
        if (view->flags & viewFrame) AdjustWindowRectExForDpi(&rect, (DWORD)GetWindowLongPtrW(window, GWL_STYLE), FALSE, (DWORD)GetWindowLongPtrW(window, GWL_EXSTYLE), dpi);
        SetWindowPos(window, NULL, 0, 0, rect.right - rect.left, rect.bottom - rect.top, SWP_NOMOVE | SWP_NOZORDER | SWP_NOACTIVATE);
        // A dialog that has not been shown yet takes its place over the main window at its final size.
        if ((view->flags & viewModal) && !IsWindowVisible(window)) centerOnMain(view);
    }
    else if (!strcmp(action, "position")) SetWindowPos(window, NULL, MulDiv(atoi(a), dpi, 96), MulDiv(atoi(b), dpi, 96), 0, 0, SWP_NOSIZE | SWP_NOZORDER | SWP_NOACTIVATE);
    else if (!strcmp(action, "center")) {
        RECT rect; GetWindowRect(window, &rect);
        MONITORINFO monitor = { .cbSize = sizeof(monitor) };
        GetMonitorInfoW(MonitorFromWindow(window, MONITOR_DEFAULTTONEAREST), &monitor);
        SetWindowPos(window, NULL, monitor.rcWork.left + (monitor.rcWork.right - monitor.rcWork.left - (rect.right - rect.left)) / 2,
            monitor.rcWork.top + (monitor.rcWork.bottom - monitor.rcWork.top - (rect.bottom - rect.top)) / 2, 0, 0, SWP_NOSIZE | SWP_NOZORDER | SWP_NOACTIVATE);
    }
}
// One command line of the Go core; the line is modified in place.
static void hostCommand(char *line) {
    char *f[96];
    int count = 0;
    for (char *field = line; field && count < 96;) {
        char *end = strchr(field, '\t');
        if (end) *end = 0;
        char *out = field;
        for (char *in = field; *in; in++) {
            if (*in == '\\' && in[1]) { in++; *out++ = *in == 't' ? '\t' : *in == 'n' ? '\n' : *in == 'r' ? '\r' : *in; }
            else *out++ = *in;
        }
        *out = 0;
        f[count++] = field;
        field = end ? end + 1 : NULL;
    }
    if (count < 2 || shuttingDown) return;
    const char *command = f[0];
    if (!strcmp(command, "create")) { createView(f + 1, count - 1); return; }
    if (!strncmp(command, "cookie", 6)) { cookieCommand(command, f + 1, count - 1); return; }
    Webview *view = findView(f[1]);
    if (!view || !view->browser) return;
    if (!strcmp(command, "close")) closeView(view);
    else if (!strcmp(command, "reload")) view->browser->reload(view->browser);
    else if ((!strcmp(command, "load") || !strcmp(command, "eval")) && count >= 3) {
        wchar_t *text = wide(f[2]);
        cef_string_t value = cefString(text), source = cefString(L"simhost://command");
        cef_frame_t *frame = view->browser->get_main_frame(view->browser);
        if (frame) {
            if (command[0] == 'l') frame->load_url(frame, &value); else frame->execute_java_script(frame, &value, &source, 0);
            releaseCef(frame);
        }
        free(text);
    }
    else if (!strcmp(command, "layout") && count >= 7) {
        for (int i = 0; i < 4; i++) view->insets[i] = atoi(f[2 + i]);
        int wasShown = view->shown;
        view->shown = f[6][0] == '1';
        view->holeCount = 0;
        // x y width height and the corner radius of each cut-out
        for (int i = 7; i + 4 < count && view->holeCount < 16; i += 5, view->holeCount++)
            for (int j = 0; j < 5; j++) view->holes[view->holeCount][j] = atoi(f[i + j]);
        if ((view->flags & viewCover) && wasShown != view->shown) {
            if (view->shown && view->asleep) wakePanel(view);
            else { view->waking = 0; KillTimer(view->window, 1); slidePanel(view, wasShown); }
            // The keyboard goes back to the host page with the play page.
            if (!view->shown && mainBrowser && IsChild(view->window, GetFocus())) {
                cef_browser_host_t *host = mainBrowser->get_host(mainBrowser);
                host->set_focus(host, 1);
                releaseCef(host);
            }
        }
        layoutPanel(view);
    }
    else if (!strcmp(command, "stir")) { if (view->window && (view->flags & viewCover)) { view->stirred = 1; SetTimer(view->window, 3, 5000, NULL); resizeView(view); } }
    else if (!strcmp(command, "fullscreen") && count >= 3) { if (view->kind == kindPanel && (view->flags & viewCover)) setMainFullscreen(f[2][0] == '1'); }
    else if (!strcmp(command, "win") && count >= 5) windowCommand(view, f[2], f[3], f[4]);
}
// Commands arrive on standard input and are handed to the window's thread one line at a time.
static DWORD WINAPI readCommands(void *unused) {
    HANDLE input = GetStdHandle(STD_INPUT_HANDLE);
    size_t capacity = 1 << 16, length = 0;
    char *buffer = malloc(capacity);
    for (;;) {
        if (length + 4096 > capacity) buffer = realloc(buffer, capacity *= 2);
        DWORD read = 0;
        if (!ReadFile(input, buffer + length, (DWORD)(capacity - length), &read, NULL) || !read) break;
        length += read;
        size_t start = 0;
        for (size_t i = start; i < length; i++) {
            if (buffer[i] != '\n') continue;
            char *line = malloc(i - start + 1);
            memcpy(line, buffer + start, i - start);
            line[i - start] = 0;
            while (!mainWindow && !shuttingDown) Sleep(10);
            if (!PostMessageW(mainWindow, WM_APP + 10, 0, (LPARAM)line)) free(line);
            start = i + 1;
        }
        memmove(buffer, buffer + start, length - start);
        length -= start;
    }
    free(buffer);
    return 0;
}

// ---------- render process ----------

// Runs before the page's own scripts. The page gets simHost: postMessage, onmessage and the
// message event. The native function stays inside this closure.
static const wchar_t bootstrap[] = L"(function(native){"
    L"if(window.simHost)return;"
    L"var target=new EventTarget(),handler=null,running={};"
    L"var send=function(kind,value){var text;try{text=JSON.stringify(value===undefined?null:value)}catch(error){throw new TypeError('simHost: value is not JSON-serializable')}native(kind,text===undefined?'null':text)};"
    L"var api={postMessage:function(data){send('m',data)},"
    L"addEventListener:function(){return target.addEventListener.apply(target,arguments)},"
    L"removeEventListener:function(){return target.removeEventListener.apply(target,arguments)}};"
    L"Object.defineProperty(api,'onmessage',{enumerable:true,get:function(){return handler},set:function(value){handler=typeof value==='function'?value:null}});"
    L"target.addEventListener('message',function(event){if(handler)handler.call(api,event)});"
    L"Object.defineProperty(window,'simHost',{value:Object.freeze(api)});"
    L"var reply=function(id,value,error){try{send('r',error===undefined?{id:id,value:value}:{id:id,error:error})}catch(failure){native('r',JSON.stringify({id:id,error:'result is not JSON-serializable'}))}};"
    L"Object.defineProperty(window,'__simHostReceive',{value:function(kind,a,b){"
    L"if(kind==='m')target.dispatchEvent(new MessageEvent('message',{data:JSON.parse(a)}));"
    L"else if(kind==='e'){running[a]=1;Promise.resolve().then(b).then(function(value){reply(a,value)},function(error){reply(a,null,String(error&&error.message||error))})}"
    L"else if(kind==='c'){if(!running[a])reply(a,null,'script could not be parsed');delete running[a]}"
    L"else if(kind==='p')requestAnimationFrame(function(){requestAnimationFrame(function(){native('p','')})});"
    L"}});})";
static int rendererViews[256], rendererViewCount;
static void CEF_CALLBACK rendererBrowserCreated(cef_render_process_handler_t *self, cef_browser_t *browser, cef_dictionary_value_t *extra) {
    cef_string_t key = cefString(L"simWebview");
    if (extra && extra->has_key(extra, &key) && rendererViewCount < 256) rendererViews[rendererViewCount++] = browser->get_identifier(browser);
    releaseCef(extra); releaseCef(browser);
}
static void CEF_CALLBACK rendererBrowserDestroyed(cef_render_process_handler_t *self, cef_browser_t *browser) {
    int id = browser->get_identifier(browser);
    for (int i = 0; i < rendererViewCount; i++) if (rendererViews[i] == id) { rendererViews[i] = rendererViews[--rendererViewCount]; break; }
    releaseCef(browser);
}
static int CEF_CALLBACK nativeExecute(cef_v8handler_t *self, const cef_string_t *name, cef_v8value_t *object, size_t count,
    cef_v8value_t *const *arguments, cef_v8value_t **result, cef_string_t *exception) {
    if (count == 2 && arguments[0]->is_string(arguments[0]) && arguments[1]->is_string(arguments[1])) {
        cef_v8context_t *context = currentV8Context();
        cef_frame_t *frame = context ? context->get_frame(context) : NULL;
        if (frame) {
            cef_string_t channel = cefString(L"simHost");
            cef_process_message_t *message = createProcessMessage(&channel);
            cef_list_value_t *list = message->get_argument_list(message);
            for (int i = 0; i < 2; i++) {
                cef_string_userfree_t text = arguments[i]->get_string_value(arguments[i]);
                cef_string_t empty = cefString(L"");
                list->set_string(list, i, text ? text : &empty);
                if (text) freeString(text);
            }
            releaseCef(list);
            frame->send_process_message(frame, PID_BROWSER, message);
            releaseCef(frame);
        }
        releaseCef(context);
    }
    for (size_t i = 0; i < count; i++) releaseCef(arguments[i]);
    releaseCef(object);
    return 1;
}
static void CEF_CALLBACK rendererContextCreated(cef_render_process_handler_t *self, cef_browser_t *browser, cef_frame_t *frame, cef_v8context_t *context) {
    int id = browser->get_identifier(browser), known = 0;
    for (int i = 0; i < rendererViewCount; i++) known |= rendererViews[i] == id;
    if (known && frame->is_main(frame)) {
        cef_string_t code = cefString(bootstrap), source = cefString(L"simhost://bootstrap"), name = cefString(L"simHostNative");
        cef_v8value_t *setup = NULL;
        cef_v8exception_t *exception = NULL;
        if (context->eval(context, &code, &source, 1, &setup, &exception) && setup) {
            cef_v8value_t *arguments[1] = { createV8Function(&name, &nativeHandler) };
            releaseCef(setup->execute_function(setup, NULL, 1, arguments));
        }
        releaseCef(setup); releaseCef(exception);
    }
    releaseCef(context); releaseCef(frame); releaseCef(browser);
}
static cef_render_process_handler_t *CEF_CALLBACK getRenderer(cef_app_t *self) { return &rendererHandler; }

static void initWebviews(void) {
    initBase(&viewClient.base, sizeof(viewClient)); initBase(&viewLife.base, sizeof(viewLife));
    initBase(&viewLoad.base, sizeof(viewLoad)); initBase(&viewDisplay.base, sizeof(viewDisplay));
    initBase(&viewRequest.base, sizeof(viewRequest)); initBase(&viewDrag.base, sizeof(viewDrag));
    initBase(&viewMenu.base, sizeof(viewMenu)); initBase(&viewRender.base, sizeof(viewRender));
    initBase(&viewKeyboard.base, sizeof(viewKeyboard));
    viewKeyboard.on_pre_key_event = viewPreKey; viewClient.get_keyboard_handler = getViewKeyboard;
    initBase(&rendererHandler.base, sizeof(rendererHandler)); initBase(&nativeHandler.base, sizeof(nativeHandler));
    viewClient.get_life_span_handler = getViewLife; viewClient.get_load_handler = getViewLoad;
    viewClient.get_display_handler = getViewDisplay; viewClient.get_request_handler = getViewRequest;
    viewClient.get_drag_handler = getViewDrag; viewClient.get_context_menu_handler = getViewMenu;
    viewClient.get_render_handler = getViewRender; viewClient.on_process_message_received = viewProcessMessage;
    viewLife.on_after_created = viewAfterCreated; viewLife.on_before_popup = viewBeforePopup; viewLife.on_before_close = viewBeforeClose;
    viewLoad.on_load_start = viewLoadStart; viewLoad.on_load_end = viewLoadEnd; viewLoad.on_load_error = viewLoadError;
    viewDisplay.on_address_change = viewAddressChange; viewDisplay.on_title_change = viewTitleChange;
    viewDisplay.on_fullscreen_mode_change = viewFullscreenChange;
    viewRequest.on_before_browse = viewBeforeBrowse; viewRequest.on_open_urlfrom_tab = viewOpenFromTab;
    viewDrag.on_draggable_regions_changed = viewDragChanged; viewDrag.on_drag_enter = viewDragEnter;
    viewMenu.on_before_context_menu = viewBeforeMenu;
    viewRender.get_view_rect = viewRect; viewRender.on_paint = viewPaint;
    rendererHandler.on_browser_created = rendererBrowserCreated; rendererHandler.on_browser_destroyed = rendererBrowserDestroyed;
    rendererHandler.on_context_created = rendererContextCreated;
    nativeHandler.execute = nativeExecute;
    app.get_render_process_handler = getRenderer;
}
