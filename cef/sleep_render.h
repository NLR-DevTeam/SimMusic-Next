// Preserve the page's alpha channel when compositing the countdown over Windows.
static cef_render_handler_t sleepRenderHandler;
static HDC sleepDC;
static HBITMAP sleepBitmap;
static HGDIOBJ sleepPreviousBitmap;
static unsigned char *sleepPixels;
static int sleepWidth, sleepHeight;
static RECT sleepRect;
static UINT sleepDpi;
static BYTE sleepOpacity, sleepFadeFrom, sleepFadeTo;
static ULONGLONG sleepFadeStarted;
static void cancelSleepWindow(void);

static void releaseSleepSurface(void) {
    if (sleepDC) {
        if (sleepPreviousBitmap) SelectObject(sleepDC, sleepPreviousBitmap);
        if (sleepBitmap) DeleteObject(sleepBitmap);
        DeleteDC(sleepDC);
    }
    sleepDC = NULL; sleepBitmap = NULL; sleepPreviousBitmap = NULL; sleepPixels = NULL;
    sleepWidth = sleepHeight = 0;
}
static void presentSleepSurface(void) {
    if (!sleepWindow || !sleepPixels) return;
    POINT position = {sleepRect.left, sleepRect.top}, source = {0};
    SIZE size = {sleepRect.right - sleepRect.left, sleepRect.bottom - sleepRect.top};
    BLENDFUNCTION blend = {AC_SRC_OVER, 0, sleepOpacity, AC_SRC_ALPHA};
    UpdateLayeredWindow(sleepWindow, NULL, &position, &size, sleepDC, &source, 0, &blend, ULW_ALPHA);
}
static void finishSleepClose(void) {
    if (!sleepBrowser) return;
    cef_browser_host_t *host = sleepBrowser->get_host(sleepBrowser);
    host->close_browser(host, 1);
    releaseCef(host);
}
static void fadeSleepWindow(BYTE target) {
    KillTimer(sleepWindow, 1);
    sleepFadeFrom = sleepOpacity; sleepFadeTo = target; sleepFadeStarted = GetTickCount64();
    SetTimer(sleepWindow, 1, 16, NULL);
}
static void revealSleepWindow(void) {
    if (!sleepWindow || sleepClosing || !sleepPixels || !GetPropW(sleepWindow, L"SimMusicContentReady") || IsWindowVisible(sleepWindow)) return;
    sleepPreviousForeground = GetForegroundWindow();
    if (IsIconic(mainWindow)) ShowWindow(mainWindow, SW_RESTORE);
    EnableWindow(mainWindow, FALSE);
    presentSleepSurface();
    ShowWindow(sleepWindow, SW_SHOW);
    SetWindowPos(sleepWindow, HWND_TOPMOST, 0, 0, 0, 0, SWP_NOMOVE | SWP_NOSIZE | SWP_SHOWWINDOW);
    SetForegroundWindow(sleepWindow);
    SetFocus(sleepWindow);
    fadeSleepWindow(255);
}
static LRESULT CALLBACK sleepProc(HWND window, UINT message, WPARAM wParam, LPARAM lParam) {
    switch (message) {
    case WM_CLOSE:
    case WM_KEYDOWN:
    case WM_SYSKEYDOWN:
    case WM_LBUTTONDOWN:
    case WM_RBUTTONDOWN:
    case WM_MBUTTONDOWN:
    case WM_XBUTTONDOWN:
        if (!sleepClosing) cancelSleepWindow();
        return 0;
    case WM_SETCURSOR: SetCursor(NULL); return 1;
    case WM_ERASEBKGND: return 1;
    case WM_TIMER: {
        if (wParam != 1) break;
        double progress = (GetTickCount64() - sleepFadeStarted) / 300.0;
        if (progress > 1) progress = 1;
        double eased = progress * progress * (3 - 2 * progress);
        sleepOpacity = (BYTE)(sleepFadeFrom + (sleepFadeTo - sleepFadeFrom) * eased);
        presentSleepSurface();
        if (progress >= 1) {
            KillTimer(window, 1);
            if (sleepClosing) finishSleepClose();
        }
        return 0;
    }
    }
    return DefWindowProcW(window, message, wParam, lParam);
}
static void closeSleepWindow(void) {
    if (sleepClosing && !shuttingDown) return;
    sleepClosing = 1;
    if (shuttingDown || !sleepWindow || !IsWindowVisible(sleepWindow) || !sleepOpacity) {
        if (sleepWindow) KillTimer(sleepWindow, 1);
        finishSleepClose();
    } else fadeSleepWindow(0);
}
static void CEF_CALLBACK sleepViewRect(cef_render_handler_t *self, cef_browser_t *browser, cef_rect_t *rect) {
    *rect = (cef_rect_t){0, 0, ((sleepRect.right - sleepRect.left) * 96 + sleepDpi - 1) / sleepDpi,
        ((sleepRect.bottom - sleepRect.top) * 96 + sleepDpi - 1) / sleepDpi};
}
static int CEF_CALLBACK sleepScreenInfo(cef_render_handler_t *self, cef_browser_t *browser, cef_screen_info_t *info) {
    info->device_scale_factor = sleepDpi / 96.0f;
    info->depth = 32; info->depth_per_component = 8; info->is_monochrome = 0;
    sleepViewRect(self, browser, &info->rect);
    info->rect.x = MulDiv(sleepRect.left, 96, sleepDpi); info->rect.y = MulDiv(sleepRect.top, 96, sleepDpi);
    info->available_rect = info->rect;
    return 1;
}
static int CEF_CALLBACK sleepScreenPoint(cef_render_handler_t *self, cef_browser_t *browser, int x, int y, int *screenX, int *screenY) {
    *screenX = sleepRect.left + MulDiv(x, sleepDpi, 96);
    *screenY = sleepRect.top + MulDiv(y, sleepDpi, 96);
    return 1;
}
static void CEF_CALLBACK sleepPaint(cef_render_handler_t *self, cef_browser_t *browser, cef_paint_element_type_t type,
    size_t count, const cef_rect_t *dirtyRects, const void *buffer, int width, int height) {
    if (type != PET_VIEW || !sleepWindow || width <= 0 || height <= 0) return;
    if (sleepWidth != width || sleepHeight != height) {
        releaseSleepSurface();
        sleepDC = CreateCompatibleDC(NULL);
        BITMAPINFO bitmap = {.bmiHeader = {.biSize = sizeof(BITMAPINFOHEADER), .biWidth = width, .biHeight = -height,
            .biPlanes = 1, .biBitCount = 32, .biCompression = BI_RGB}};
        sleepBitmap = CreateDIBSection(sleepDC, &bitmap, DIB_RGB_COLORS, (void **)&sleepPixels, NULL, 0);
        if (!sleepBitmap) { releaseSleepSurface(); return; }
        sleepPreviousBitmap = SelectObject(sleepDC, sleepBitmap);
        sleepWidth = width; sleepHeight = height;
        memcpy(sleepPixels, buffer, (size_t)width * height * 4);
    } else for (size_t index = 0; index < count; index++) {
        cef_rect_t rect = dirtyRects[index];
        int left = max(0, rect.x), top = max(0, rect.y);
        int right = min(width, rect.x + rect.width), bottom = min(height, rect.y + rect.height);
        if (right <= left) continue;
        for (int y = top; y < bottom; y++) {
            size_t offset = ((size_t)y * width + left) * 4;
            memcpy(sleepPixels + offset, (const unsigned char *)buffer + offset, (size_t)(right - left) * 4);
        }
    }
    presentSleepSurface();
    revealSleepWindow();
}
static cef_render_handler_t *CEF_CALLBACK getSleepRender(cef_client_t *self) { return &sleepRenderHandler; }
