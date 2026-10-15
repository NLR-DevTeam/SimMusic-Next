// Native CEF C-API host for Linux: GTK 3 windows on X11 (XWayland under Wayland). The counterpart of host.c
// and webview.h, speaking the same protocol with the Go core: commands are lines on standard input, events
// are lines on standard output, fields tab-separated with \\ \t \n \r escaped.
//
// Beyond what the Windows host does, this one also draws what the Go core draws itself on Windows, having
// the toolkit for it: the context menus and notices ("menu", "toast" commands, "OV:" events) and the
// desktop lyrics ("lyric" commands, "DL:" events). The main window is driven by "host" commands instead
// of window messages, and reports itself with "READY:" and "STATE:" lines.
//
// A browser never gets pointer events where its page marks a region as draggable, nor along the border
// of a frameless window: those places are cut out of the input shape of the X window around the browser,
// so the press reaches the top-level window, which starts the move or the resize.
//
// No Node.js, Electron, libcef_dll_wrapper or C++ ABI dependency.
#define _GNU_SOURCE
#include <gtk/gtk.h>
#include <gdk/gdkx.h>
#include <X11/Xlib.h>
#include <X11/extensions/shape.h>
#include <fontconfig/fontconfig.h>
#include <pango/pangocairo.h>
#include <math.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <limits.h>
#include "include/cef_api_hash.h"
#include "include/capi/cef_app_capi.h"
#include "include/capi/cef_browser_capi.h"
#include "include/capi/cef_client_capi.h"
#include "include/capi/cef_drag_handler_capi.h"
#include "include/capi/cef_display_handler_capi.h"
#include "include/capi/cef_request_handler_capi.h"
#include "include/capi/cef_render_handler_capi.h"
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
    GtkWidget *window;      // a window of its own: the top-level window
    Window box;             // the X window the browser fills: a child of the main window (panel) or of `window`
    cef_browser_t *browser;
    int browserId;
    char *base;             // origin of the extension's own pages, the target of ext:// links
    int flags;
    int width, height, minWidth, minHeight, maxWidth, maxHeight;
    cairo_region_t *drag;   // in X pixels of the box
    int fullscreen;
    int insets[4], shown, holeCount, holes[16][5];
    int x, y, w, h, mapped; // where a panel lies in the main window, in X pixels
    int offset, slideFrom, slideTo, sliding;   // a covering panel slides in from below and out again
    gint64 slideStart;
    int reported;           // the Go core has been told this view is closed
    int closing;
} Webview;

static Display *xd;
static GtkWidget *mainWindow, *sleepWindow;
static Window mainBox, sleepBox;
static cef_browser_t *mainBrowser, *sleepBrowser;
static cairo_region_t *mainDrag;
static int fullscreen, shuttingDown, mainClosed, mainReady, sleepClosing;
static char *startUrl, *iconPath, *iconFont;
static Webview *webviews, *creatingView, *pendingDialog;
static cef_app_t app;
static cef_client_t client, sleepClient, viewClient;
static cef_life_span_handler_t lifeHandler, sleepLifeHandler, viewLife;
static cef_drag_handler_t dragHandler, viewDrag;
static cef_request_handler_t requestHandler, viewRequest;
static cef_context_menu_handler_t menuHandler, viewMenu;
static cef_display_handler_t displayHandler, viewDisplay;
static cef_load_handler_t viewLoad;
static cef_render_handler_t viewRender;
static cef_keyboard_handler_t viewKeyboard;
static cef_render_process_handler_t rendererHandler;
static cef_v8handler_t nativeHandler;
static struct { char *path; cef_request_context_t *context; } partitions[64];
static void updateInput(void);
static void layoutPanels(void);
static void maybeQuit(void);
static void closeView(Webview *view);

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
static void releaseCef(void *object) {
    if (object) { cef_base_ref_counted_t *base = object; base->release(base); }
}

// ---------- strings ----------

// CEF strings are UTF-16; everything else here is UTF-8. A made string is dropped after use.
static cef_string_t make(const char *text) {
    cef_string_t value = {0};
    cef_string_utf8_to_utf16(text ? text : "", text ? strlen(text) : 0, &value);
    return value;
}
static void drop(cef_string_t *value) { cef_string_utf16_clear(value); }
static char *utf8(const cef_string_t *text) {
    if (!text || !text->str || !text->length) return strdup("");
    cef_string_utf8_t out = {0};
    cef_string_utf16_to_utf8(text->str, text->length, &out);
    char *result = strndup(out.str ? out.str : "", out.length);
    cef_string_utf8_clear(&out);
    return result;
}
static void runScript(cef_browser_t *browser, const char *script) {
    if (!browser) return;
    cef_frame_t *frame = browser->get_main_frame(browser);
    if (!frame) return;
    cef_string_t code = make(script), source = make("simhost://command");
    frame->execute_java_script(frame, &code, &source, 0);
    drop(&code); drop(&source);
    releaseCef(frame);
}
static void focusBrowser(cef_browser_t *browser) {
    if (!browser) return;
    cef_browser_host_t *host = browser->get_host(browser);
    host->set_focus(host, 1);
    releaseCef(host);
}
static Window browserWindow(cef_browser_t *browser) {
    if (!browser) return 0;
    cef_browser_host_t *host = browser->get_host(browser);
    Window window = host->get_window_handle(host);
    releaseCef(host);
    return window;
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
static void emitString(const cef_string_t *text) {
    char *value = utf8(text);
    emitBytes(value, strlen(value));
    free(value);
}
static void emitUserFree(cef_string_userfree_t text) { emitString(text); if (text) cef_string_userfree_utf16_free(text); }
static void emitNumber(long long value) { printf("\t%lld", value); }
static void emitEnd(void) { putchar('\n'); fflush(stdout); }
// A line of another kind: "OV:" for the menus and notices, "DL:" for the desktop lyrics.
static void emitLine(const char *prefix, const char *event, const char *a, const char *b) {
    printf("%s%s", prefix, event);
    if (a) emitBytes(a, strlen(a));
    if (b) emitBytes(b, strlen(b));
    emitEnd();
}

static Webview *findView(const char *id) {
    for (Webview *view = webviews; view; view = view->next) if (!strcmp(view->id, id)) return view;
    return NULL;
}
static Webview *viewOf(cef_browser_t *browser) {
    int id = browser->get_identifier(browser);
    for (Webview *view = webviews; view; view = view->next) if (view->browserId == id) return view;
    return creatingView;
}

// ---------- sizes and shapes ----------

// Sizes of the protocol are CSS pixels. GTK counts in its own units, which the font resolution scales;
// the X server counts in device pixels, GTK's units times the window's scale factor.
static double unitScale(void) {
    double dpi = gdk_screen_get_resolution(gdk_screen_get_default());
    return dpi > 0 ? fmax(1, dpi / 96) : 1;
}
static int deviceScale(GtkWidget *window) { return window ? gtk_widget_get_scale_factor(window) : 1; }
static int units(double css) { return (int)lround(css * unitScale()); }
static int pixels(GtkWidget *window, double css) { return (int)lround(css * unitScale() * deviceScale(window)); }
static void sizeOf(GtkWidget *window, int *width, int *height) {
    gtk_window_get_size(GTK_WINDOW(window), width, height);
    *width *= deviceScale(window); *height *= deviceScale(window);
}
static int isMaximized(GtkWidget *window) {
    GdkWindow *gdk = gtk_widget_get_window(window);
    return gdk && (gdk_window_get_state(gdk) & GDK_WINDOW_STATE_MAXIMIZED);
}
static cairo_region_t *rectRegion(int x, int y, int width, int height) {
    cairo_rectangle_int_t rect = { x, y, width > 0 ? width : 0, height > 0 ? height : 0 };
    return cairo_region_create_rectangle(&rect);
}
static void unionRect(cairo_region_t *region, int x, int y, int width, int height) {
    cairo_rectangle_int_t rect = { x, y, width, height };
    if (width > 0 && height > 0) cairo_region_union_rectangle(region, &rect);
}
static void subtractRect(cairo_region_t *region, int x, int y, int width, int height) {
    cairo_rectangle_int_t rect = { x, y, width, height };
    if (width > 0 && height > 0) cairo_region_subtract_rectangle(region, &rect);
}
// A rectangle with rounded corners, taken out row by row where the corners curve.
static void subtractRounded(cairo_region_t *region, int left, int top, int right, int bottom, int radius) {
    int width = right - left, height = bottom - top;
    if (radius * 2 > width) radius = width / 2;
    if (radius * 2 > height) radius = height / 2;
    subtractRect(region, left, top + radius, width, height - 2 * radius);
    for (int row = 0; row < radius; row++) {
        double reach = radius - row - .5;
        int inset = radius - (int)floor(sqrt(radius * radius - reach * reach));
        subtractRect(region, left + inset, top + row, width - 2 * inset, 1);
        subtractRect(region, left + inset, bottom - 1 - row, width - 2 * inset, 1);
    }
}
static void setShape(Window window, int kind, cairo_region_t *region) {
    if (!window) return;
    int count = cairo_region_num_rectangles(region);
    XRectangle *rects = malloc(sizeof(XRectangle) * (count ? count : 1));
    for (int i = 0; i < count; i++) {
        cairo_rectangle_int_t rect;
        cairo_region_get_rectangle(region, i, &rect);
        rects[i] = (XRectangle){ (short)rect.x, (short)rect.y, (unsigned short)rect.width, (unsigned short)rect.height };
    }
    XShapeCombineRectangles(xd, window, kind, 0, 0, rects, count, ShapeSet, Unsorted);
    free(rects);
}
// The X window a browser is created in. It is ours, so its shapes are ours to set; the browser's own
// window fills it.
static Window makeBox(Window parent, int width, int height, unsigned long background) {
    Window box = XCreateSimpleWindow(xd, parent, 0, 0, width > 0 ? width : 1, height > 0 ? height : 1, 0, 0, background);
    XFlush(xd);
    return box;
}
static void fillBox(Window box, cef_browser_t *browser, int width, int height) {
    Window child = browserWindow(browser);
    if (child && width > 0 && height > 0) XMoveResizeWindow(xd, child, 0, 0, width, height);
}
// The regions a page marks as draggable (-webkit-app-region), in X pixels. What a page marks as not
// draggable stays so wherever it stands in the document, a button over a draggable header included.
static cairo_region_t *dragRegionOf(GtkWidget *window, size_t count, const cef_draggable_region_t *regions) {
    cairo_region_t *region = cairo_region_create();
    for (int pass = 1; pass >= 0; pass--) for (size_t i = 0; i < count; i++) {
        if (regions[i].draggable != pass) continue;
        cef_rect_t r = regions[i].bounds;
        int left = pixels(window, r.x), top = pixels(window, r.y), right = pixels(window, r.x + r.width), bottom = pixels(window, r.y + r.height);
        if (pass) unionRect(region, left, top, right - left, bottom - top); else subtractRect(region, left, top, right - left, bottom - top);
    }
    return region;
}
// The border a frameless window is resized by: none while it fills the screen.
static int borderOf(GtkWidget *window, int resizable, int full) {
    return resizable && !full && !isMaximized(window) ? pixels(window, 6) : 0;
}
static void unionBorder(cairo_region_t *region, int width, int height, int border) {
    if (border <= 0) return;
    unionRect(region, 0, 0, width, border); unionRect(region, 0, height - border, width, border);
    unionRect(region, 0, 0, border, height); unionRect(region, width - border, 0, border, height);
}

// ---------- frameless windows: moving and resizing ----------

// What only reaches a top-level window is a press in a draggable region or on its border.
static int frameResizable(Webview *view) { return view ? (view->flags & viewResizable) != 0 : 1; }
static int frameFull(Webview *view) { return view ? view->fullscreen : fullscreen; }
static int edgeAt(GtkWidget *window, Webview *view, double x, double y, GdkWindowEdge *edge) {
    int border = borderOf(window, frameResizable(view), frameFull(view)) / deviceScale(window);
    if (border <= 0) return 0;
    int width, height;
    gtk_window_get_size(GTK_WINDOW(window), &width, &height);
    int left = x < border, right = x >= width - border, top = y < border, bottom = y >= height - border;
    if (top) *edge = left ? GDK_WINDOW_EDGE_NORTH_WEST : right ? GDK_WINDOW_EDGE_NORTH_EAST : GDK_WINDOW_EDGE_NORTH;
    else if (bottom) *edge = left ? GDK_WINDOW_EDGE_SOUTH_WEST : right ? GDK_WINDOW_EDGE_SOUTH_EAST : GDK_WINDOW_EDGE_SOUTH;
    else if (left) *edge = GDK_WINDOW_EDGE_WEST;
    else if (right) *edge = GDK_WINDOW_EDGE_EAST;
    else return 0;
    return 1;
}
static void toggleMaximize(GtkWidget *window) {
    if (isMaximized(window)) gtk_window_unmaximize(GTK_WINDOW(window)); else gtk_window_maximize(GTK_WINDOW(window));
}
static gboolean framePress(GtkWidget *window, GdkEventButton *event, gpointer data) {
    Webview *view = data;
    if (event->button != 1) return FALSE;
    GdkWindowEdge edge;
    if (edgeAt(window, view, event->x, event->y, &edge)) {
        if (event->type == GDK_BUTTON_PRESS) gtk_window_begin_resize_drag(GTK_WINDOW(window), edge, event->button, (int)event->x_root, (int)event->y_root, event->time);
    } else if (event->type == GDK_2BUTTON_PRESS) {
        if (frameResizable(view) && !frameFull(view)) toggleMaximize(window);
    } else if (event->type == GDK_BUTTON_PRESS && !frameFull(view)) {
        gtk_window_begin_move_drag(GTK_WINDOW(window), event->button, (int)event->x_root, (int)event->y_root, event->time);
    }
    return TRUE;
}
static gboolean frameMotion(GtkWidget *window, GdkEventMotion *event, gpointer data) {
    static const char *names[] = { "nw-resize", "n-resize", "ne-resize", "w-resize", "e-resize", "sw-resize", "s-resize", "se-resize" };
    GdkWindowEdge edge;
    GdkCursor *cursor = edgeAt(window, data, event->x, event->y, &edge) ? gdk_cursor_new_from_name(gtk_widget_get_display(window), names[edge]) : NULL;
    gdk_window_set_cursor(gtk_widget_get_window(window), cursor);
    if (cursor) g_object_unref(cursor);
    return FALSE;
}
static void watchFrame(GtkWidget *window, Webview *view) {
    gtk_widget_add_events(window, GDK_BUTTON_PRESS_MASK | GDK_POINTER_MOTION_MASK);
    g_signal_connect(window, "button-press-event", G_CALLBACK(framePress), view);
    g_signal_connect(window, "motion-notify-event", G_CALLBACK(frameMotion), view);
}

// ---------- the main window ----------

static void reportState(void) {
    if (!mainWindow) return;
    printf("STATE:%d\t%d\n", isMaximized(mainWindow), gtk_window_is_active(GTK_WINDOW(mainWindow)));
    fflush(stdout);
}
static Webview *coverShown(void) {
    for (Webview *view = webviews; view; view = view->next) if (view->kind == kindPanel && (view->flags & viewCover) && view->shown) return view;
    return NULL;
}
// The area of the main window a panel is seen in: its rectangle less the places cut out of it.
static cairo_region_t *panelArea(Webview *view) {
    cairo_region_t *area = rectRegion(view->x, view->y, view->w, view->h);
    for (int i = 0; i < view->holeCount; i++) {
        int *h = view->holes[i];
        int left = pixels(mainWindow, h[0]), top = pixels(mainWindow, h[1]), right = pixels(mainWindow, h[0] + h[2]), bottom = pixels(mainWindow, h[1] + h[3]);
        subtractRounded(area, left, top, right, bottom, pixels(mainWindow, h[4]));
    }
    return area;
}
// Sets what of the main window takes the pointer. The places the window is moved and resized by go through
// to the window itself: the host page's draggable regions where no panel lies over them, the draggable
// regions of the panels, and the border. Everything else belongs to the host page or to the panel above it.
static void updateInput(void) {
    if (!mainWindow || !mainBox) return;
    int width, height;
    sizeOf(mainWindow, &width, &height);
    cairo_region_t *through = mainDrag ? cairo_region_copy(mainDrag) : cairo_region_create();
    // The covering panel (the play page) lies above the content panel: it is taken last.
    for (int cover = 0; cover < 2; cover++) for (Webview *view = webviews; view; view = view->next) {
        if (view->kind != kindPanel || !view->mapped || !view->box || ((view->flags & viewCover) != 0) != cover) continue;
        cairo_region_t *area = panelArea(view);
        cairo_region_subtract(through, area);
        if (view->drag) {
            cairo_region_t *own = cairo_region_copy(view->drag);
            cairo_region_translate(own, view->x, view->y);
            cairo_region_intersect(own, area);
            cairo_region_union(through, own);
            cairo_region_destroy(own);
        }
        cairo_region_destroy(area);
    }
    if (fullscreen) { cairo_region_destroy(through); through = cairo_region_create(); }
    else unionBorder(through, width, height, borderOf(mainWindow, 1, 0));
    cairo_region_t *page = rectRegion(0, 0, width, height);
    cairo_region_subtract(page, through);
    setShape(mainBox, ShapeInput, page);
    cairo_region_destroy(page);
    for (Webview *view = webviews; view; view = view->next) {
        if (view->kind != kindPanel || !view->mapped || !view->box) continue;
        cairo_region_t *own = rectRegion(0, 0, view->w, view->h), *cut = cairo_region_copy(through);
        cairo_region_translate(cut, -view->x, -view->y);
        cairo_region_subtract(own, cut);
        setShape(view->box, ShapeInput, own);
        cairo_region_destroy(own); cairo_region_destroy(cut);
    }
    cairo_region_destroy(through);
    XFlush(xd);
}
static void resizeMain(void) {
    if (!mainWindow || !mainBox) return;
    int width, height;
    sizeOf(mainWindow, &width, &height);
    if (width <= 0 || height <= 0) return;
    XMoveResizeWindow(xd, mainBox, 0, 0, width, height);
    fillBox(mainBox, mainBrowser, width, height);
    layoutPanels();
    updateInput();
}
static void requestClose(void) { runScript(mainBrowser, "closeButton.click()"); }
static gboolean mainConfigure(GtkWidget *window, GdkEvent *event, gpointer data) { resizeMain(); return FALSE; }
static gboolean mainState(GtkWidget *window, GdkEventWindowState *event, gpointer data) { resizeMain(); reportState(); return FALSE; }
static gboolean mainFocus(GtkWidget *window, GdkEventFocus *event, gpointer data) {
    if (event->in) {
        // The keyboard belongs to the page seen: the play page while it covers the window.
        Webview *cover = coverShown();
        focusBrowser(cover && cover->browser ? cover->browser : mainBrowser);
    }
    reportState();
    return FALSE;
}
static gboolean mainDelete(GtkWidget *window, GdkEvent *event, gpointer data) {
    if (!shuttingDown) requestClose();
    return TRUE;
}
static void showMain(void) {
    if (!mainWindow) return;
    gtk_widget_show(mainWindow);
    gtk_window_deiconify(GTK_WINDOW(mainWindow));
    gtk_window_present(GTK_WINDOW(mainWindow));
    XMapWindow(xd, mainBox);
    resizeMain();
}
// A page that never reports itself ready must not leave the program without a window.
static gboolean showAnyway(gpointer data) {
    if (!mainReady && !shuttingDown) { mainReady = 1; showMain(); }
    return G_SOURCE_REMOVE;
}
static void reportFullscreen(int enabled) {
    for (Webview *view = webviews; view; view = view->next)
        if (view->kind == kindPanel && (view->flags & viewCover)) { emitBegin("fullscreen", view->id); emitNumber(enabled); emitEnd(); }
}
static void setMainFullscreen(int enabled) {
    if (fullscreen == enabled || !mainWindow) return;
    fullscreen = enabled;
    if (enabled) gtk_window_fullscreen(GTK_WINDOW(mainWindow)); else gtk_window_unfullscreen(GTK_WINDOW(mainWindow));
    updateInput();
    reportFullscreen(enabled);
}
static void createMainWindow(void) {
    mainWindow = gtk_window_new(GTK_WINDOW_TOPLEVEL);
    gtk_window_set_title(GTK_WINDOW(mainWindow), "SimMusic Next");
    gtk_window_set_decorated(GTK_WINDOW(mainWindow), FALSE);
    gtk_window_set_default_size(GTK_WINDOW(mainWindow), units(1120), units(780));
    gtk_window_set_position(GTK_WINDOW(mainWindow), GTK_WIN_POS_CENTER);
    GdkGeometry limits = { .min_width = units(880), .min_height = units(620) };
    gtk_window_set_geometry_hints(GTK_WINDOW(mainWindow), NULL, &limits, GDK_HINT_MIN_SIZE);
    watchFrame(mainWindow, NULL);
    g_signal_connect(mainWindow, "configure-event", G_CALLBACK(mainConfigure), NULL);
    g_signal_connect(mainWindow, "window-state-event", G_CALLBACK(mainState), NULL);
    g_signal_connect(mainWindow, "focus-in-event", G_CALLBACK(mainFocus), NULL);
    g_signal_connect(mainWindow, "focus-out-event", G_CALLBACK(mainFocus), NULL);
    g_signal_connect(mainWindow, "delete-event", G_CALLBACK(mainDelete), NULL);
    gtk_widget_realize(mainWindow);
    int width, height;
    sizeOf(mainWindow, &width, &height);
    mainBox = makeBox(GDK_WINDOW_XID(gtk_widget_get_window(mainWindow)), width, height, 0);
    XMapWindow(xd, mainBox);
    cef_window_info_t info = { .parent_window = mainBox };
    info.bounds.width = width; info.bounds.height = height;
    cef_browser_settings_t settings = { .size = sizeof(settings), .background_color = 0xfff9f9fb };
    cef_string_t url = make(startUrl);
    if (!cef_browser_host_create_browser(&info, &client, &url, &settings, NULL, NULL)) { gtk_widget_destroy(mainWindow); mainWindow = NULL; cef_quit_message_loop(); }
    drop(&url);
    g_timeout_add(8000, showAnyway, NULL);
}

// ---------- the sleep timer's window ----------

// The last minute before the computer is put to sleep is shown over the whole screen. The page is drawn
// by a browser of its own in a window above everything; unlike on Windows it is not see-through.
static void showSleepWindow(void) {
    if (sleepWindow || shuttingDown) return;
    sleepClosing = 0;
    sleepWindow = gtk_window_new(GTK_WINDOW_TOPLEVEL);
    gtk_window_set_title(GTK_WINDOW(sleepWindow), "SimMusic 睡眠定时");
    gtk_window_set_decorated(GTK_WINDOW(sleepWindow), FALSE);
    gtk_window_set_keep_above(GTK_WINDOW(sleepWindow), TRUE);
    gtk_window_set_skip_taskbar_hint(GTK_WINDOW(sleepWindow), TRUE);
    GdkRectangle screen = {0};
    GdkMonitor *monitor = of ? gdk_display_get_monitor_at_window(gdk_display_get_default(), of) : gdk_display_get_primary_monitor(gdk_display_get_default());
    if (monitor) gdk_monitor_get_geometry(monitor, &screen);
    gtk_window_move(GTK_WINDOW(sleepWindow), screen.x, screen.y);
    gtk_window_set_default_size(GTK_WINDOW(sleepWindow), screen.width > 0 ? screen.width : 1280, screen.height > 0 ? screen.height : 720);
    gtk_widget_realize(sleepWindow);
    int width, height;
    sizeOf(sleepWindow, &width, &height);
    sleepBox = makeBox(GDK_WINDOW_XID(gtk_widget_get_window(sleepWindow)), width, height, 0);
    XMapWindow(xd, sleepBox);
    cef_window_info_t info = { .parent_window = sleepBox };
    info.bounds.width = width; info.bounds.height = height;
    cef_browser_settings_t settings = { .size = sizeof(settings), .background_color = 0xff000000 };
    char *address = g_strdup_printf("%ssleep.html", startUrl);
    cef_string_t url = make(address);
    if (!cef_browser_host_create_browser(&info, &sleepClient, &url, &settings, NULL, NULL)) { gtk_widget_destroy(sleepWindow); sleepWindow = NULL; sleepBox = 0; }
    drop(&url);
    g_free(address);
}
static void revealSleepWindow(void) {
    if (!sleepWindow || sleepClosing) return;
    gtk_widget_show(sleepWindow);
    gtk_window_fullscreen(GTK_WINDOW(sleepWindow));
    gtk_window_present(GTK_WINDOW(sleepWindow));
    int width, height;
    sizeOf(sleepWindow, &width, &height);
    XMoveResizeWindow(xd, sleepBox, 0, 0, width, height);
    fillBox(sleepBox, sleepBrowser, width, height);
    focusBrowser(sleepBrowser);
}
static gboolean sleepConfigure(GtkWidget *window, GdkEvent *event, gpointer data) {
    int width, height;
    sizeOf(window, &width, &height);
    if (sleepBox) { XMoveResizeWindow(xd, sleepBox, 0, 0, width, height); fillBox(sleepBox, sleepBrowser, width, height); }
    return FALSE;
}
static void closeSleepWindow(void) {
    if (!sleepWindow || sleepClosing) return;
    sleepClosing = 1;
    gtk_widget_hide(sleepWindow);
    if (sleepBrowser) {
        cef_browser_host_t *host = sleepBrowser->get_host(sleepBrowser);
        host->close_browser(host, 1);
        releaseCef(host);
    }
}
static void CEF_CALLBACK sleepAfterCreated(cef_life_span_handler_t *self, cef_browser_t *browser) {
    browser->base.add_ref(&browser->base);
    sleepBrowser = browser;
    if (sleepWindow) g_signal_connect(sleepWindow, "configure-event", G_CALLBACK(sleepConfigure), NULL);
    if (sleepClosing) { sleepClosing = 0; closeSleepWindow(); }
}
static void CEF_CALLBACK sleepBeforeClose(cef_life_span_handler_t *self, cef_browser_t *browser) {
    releaseCef(sleepBrowser); sleepBrowser = NULL;
    if (sleepWindow) gtk_widget_destroy(sleepWindow);
    sleepWindow = NULL; sleepBox = 0; sleepClosing = 0;
    if (!shuttingDown && mainWindow && gtk_widget_get_visible(mainWindow)) gtk_window_present(GTK_WINDOW(mainWindow));
}
static cef_life_span_handler_t *CEF_CALLBACK getSleepLife(cef_client_t *self) { return &sleepLifeHandler; }

// ---------- extension views: windows ----------

static void sizeViewBox(Webview *view) {
    if (!view->window || !view->box) return;
    int width, height;
    sizeOf(view->window, &width, &height);
    if (width <= 0 || height <= 0) return;
    XMoveResizeWindow(xd, view->box, 0, 0, width, height);
    fillBox(view->box, view->browser, width, height);
    // A frameless window is moved by what its page marks as draggable and resized by its border.
    cairo_region_t *page = rectRegion(0, 0, width, height);
    if (!(view->flags & viewFrame)) {
        cairo_region_t *through = view->drag && !view->fullscreen ? cairo_region_copy(view->drag) : cairo_region_create();
        unionBorder(through, width, height, borderOf(view->window, frameResizable(view), view->fullscreen));
        cairo_region_subtract(page, through);
        cairo_region_destroy(through);
    }
    setShape(view->box, ShapeInput, page);
    cairo_region_destroy(page);
    XFlush(xd);
}
static gboolean viewConfigure(GtkWidget *window, GdkEvent *event, gpointer data) { sizeViewBox(data); return FALSE; }
static gboolean viewFocus(GtkWidget *window, GdkEventFocus *event, gpointer data) {
    Webview *view = data;
    if (event->in) focusBrowser(view->browser);
    return FALSE;
}
static gboolean viewDelete(GtkWidget *window, GdkEvent *event, gpointer data) { closeView(data); return TRUE; }
static void setViewFullscreen(Webview *view, int enabled) {
    if (view->kind != kindWindow || !view->window || view->fullscreen == enabled) return;
    view->fullscreen = enabled;
    if (enabled) gtk_window_fullscreen(GTK_WINDOW(view->window)); else gtk_window_unfullscreen(GTK_WINDOW(view->window));
    sizeViewBox(view);
}
static void watchView(Webview *view) {
    if (!(view->flags & viewFrame)) watchFrame(view->window, view);
    g_signal_connect(view->window, "configure-event", G_CALLBACK(viewConfigure), view);
    g_signal_connect(view->window, "window-state-event", G_CALLBACK(viewConfigure), view);
    g_signal_connect(view->window, "focus-in-event", G_CALLBACK(viewFocus), view);
    g_signal_connect(view->window, "delete-event", G_CALLBACK(viewDelete), view);
}

// ---------- extension views: panels ----------

// The timing of a CSS "ease" transition: cubic-bezier(.25, .1, .25, 1).
static double easeCss(double t) {
    double low = 0, high = 1, s = t;
    for (int i = 0; i < 24; i++) {
        s = (low + high) / 2;
        if (3 * (1 - s) * (1 - s) * s * .25 + 3 * (1 - s) * s * s * .25 + s * s * s < t) low = s; else high = s;
    }
    return 3 * (1 - s) * (1 - s) * s * .1 + 3 * (1 - s) * s * s + s * s * s;
}
// A panel covers the main window less its insets, given in CSS pixels. Rectangles of host elements
// shown above it are cut out of its shape. A covering panel (the play page) that is not shown is parked
// below the window, clipped away by it: its page is laid out at its real size and keeps a rendered frame.
static void layoutPanel(Webview *view) {
    if (view->kind != kindPanel || !view->box || !mainWindow) return;
    int clientWidth, clientHeight;
    sizeOf(mainWindow, &clientWidth, &clientHeight);
    int left = pixels(mainWindow, view->insets[0]), top = pixels(mainWindow, view->insets[1]);
    int width = clientWidth - left - pixels(mainWindow, view->insets[2]), height = clientHeight - top - pixels(mainWindow, view->insets[3]);
    if (view->flags & viewCover) {
        left = 0; top = 0; width = clientWidth; height = clientHeight;
        if (!view->shown && !view->sliding && width > 0 && height > 0) {
            view->offset = height * 6 / 5;
            view->x = 0; view->y = view->offset; view->w = width; view->h = height; view->mapped = 0;
            XShapeCombineMask(xd, view->box, ShapeBounding, 0, 0, None, ShapeSet);
            XMoveResizeWindow(xd, view->box, 0, view->offset, width, height);
            fillBox(view->box, view->browser, width, height);
            XMapWindow(xd, view->box);
            XLowerWindow(xd, view->box);
            return;
        }
    }
    if (!(view->shown || view->sliding) || width <= 0 || height <= 0) {
        view->mapped = 0;
        XUnmapWindow(xd, view->box);
        return;
    }
    view->x = left; view->y = top + view->offset; view->w = width; view->h = height; view->mapped = 1;
    cairo_region_t *area = panelArea(view);
    cairo_region_translate(area, -view->x, -view->y);
    setShape(view->box, ShapeBounding, area);
    cairo_region_destroy(area);
    XMoveResizeWindow(xd, view->box, view->x, view->y, width, height);
    fillBox(view->box, view->browser, width, height);
    XMapRaised(xd, view->box);
}
static void layoutPanels(void) {
    // The covering panel is raised last, above the content panel.
    for (int cover = 0; cover < 2; cover++) for (Webview *view = webviews; view; view = view->next)
        if (view->kind == kindPanel && ((view->flags & viewCover) != 0) == cover) layoutPanel(view);
    XFlush(xd);
}
static guint slideTimer;
static gboolean stepSlides(gpointer data) {
    gint64 now = g_get_monotonic_time();
    int sliding = 0;
    for (Webview *view = webviews; view; view = view->next) {
        if (!view->sliding) continue;
        double t = (now - view->slideStart) / 1e6 / .3;
        if (t >= 1) { view->offset = view->slideTo; view->sliding = 0; }
        else { view->offset = view->slideFrom + (int)((view->slideTo - view->slideFrom) * easeCss(t)); sliding = 1; }
        layoutPanel(view);
    }
    updateInput();
    if (!sliding) { slideTimer = 0; return G_SOURCE_REMOVE; }
    return G_SOURCE_CONTINUE;
}
// A covering panel moves like the page it replaces: .3s from below the window, and back.
static void slidePanel(Webview *view, int wasShown) {
    int width, height;
    sizeOf(mainWindow, &width, &height);
    int travel = height * 6 / 5;
    if (!view->sliding && !wasShown) view->offset = travel;
    view->slideFrom = view->offset;
    view->slideTo = view->shown ? 0 : travel;
    view->sliding = 1;
    view->slideStart = g_get_monotonic_time();
    if (!slideTimer) slideTimer = g_timeout_add(8, stepSlides, NULL);
}

// ---------- extension views: lifetime ----------

static void forgetView(Webview *view) {
    for (Webview **link = &webviews; *link; link = &(*link)->next) if (*link == view) { *link = view->next; break; }
    if (pendingDialog == view) pendingDialog = NULL;
    if (!view->reported) { view->reported = 1; emitBegin("closed", view->id); emitEnd(); }
    releaseCef(view->browser);
    if (view->window) { g_signal_handlers_disconnect_by_data(view->window, view); gtk_widget_destroy(view->window); }
    else if (view->box) XDestroyWindow(xd, view->box);
    if (view->drag) cairo_region_destroy(view->drag);
    free(view->base);
    free(view);
    updateInput();
}
// On X11 a browser is closed before the window around it is destroyed. The view is over for the extension
// at once; its window goes when the browser has finished closing.
static void closeView(Webview *view) {
    if (view->closing) return;
    view->closing = 1;
    if (view->window) gtk_widget_hide(view->window);
    else if (view->box) { view->mapped = 0; view->shown = 0; view->sliding = 0; XUnmapWindow(xd, view->box); }
    if (!view->reported) { view->reported = 1; emitBegin("closed", view->id); emitEnd(); }
    if (view->window && (view->flags & viewModal) && mainWindow && !shuttingDown && gtk_widget_get_visible(mainWindow)) gtk_window_present(GTK_WINDOW(mainWindow));
    if (!view->browser) { forgetView(view); return; }
    cef_browser_host_t *host = view->browser->get_host(view->browser);
    host->close_browser(host, 1);
    releaseCef(host);
    updateInput();
}
static gboolean closeLater(gpointer data) {
    Webview *view = findView(data);
    if (view) closeView(view);
    g_free(data);
    return G_SOURCE_REMOVE;
}
static void maybeQuit(void) {
    if (mainClosed && !webviews) cef_quit_message_loop();
}
// At shutdown a browser that never finishes closing must not keep the process alive.
static gboolean quitAnyway(gpointer data) { cef_quit_message_loop(); return G_SOURCE_REMOVE; }
static void closeAllViews(void) {
    for (Webview *view = webviews, *next; view; view = next) { next = view->next; closeView(view); }
    g_timeout_add(3000, quitAnyway, NULL);
}
static void destroyHost(void) {
    if (shuttingDown) return;
    shuttingDown = 1;
    closeSleepWindow();
    closeAllViews();
    if (mainWindow) gtk_widget_hide(mainWindow);
    if (mainBrowser) {
        cef_browser_host_t *host = mainBrowser->get_host(mainBrowser);
        host->close_browser(host, 1);
        releaseCef(host);
    } else { mainClosed = 1; maybeQuit(); }
}

// ---------- browser callbacks: the host page ----------

static void CEF_CALLBACK afterCreated(cef_life_span_handler_t *self, cef_browser_t *browser) {
    browser->base.add_ref(&browser->base);
    mainBrowser = browser;
    resizeMain();
    printf("READY:1\n"); fflush(stdout);
}
static void CEF_CALLBACK beforeClose(cef_life_span_handler_t *self, cef_browser_t *browser) {
    releaseCef(mainBrowser); mainBrowser = NULL;
    if (mainWindow) { gtk_widget_destroy(mainWindow); mainWindow = NULL; mainBox = 0; }
    // The message loop ends once the extension webviews have closed as well.
    mainClosed = 1;
    maybeQuit();
}
// window.open of the host page with a name beginning "simDialog" makes a dialog: a frameless window modal to
// the main window, with the page's own browser inside. The host page fills it with the dialog it built; being
// opened by that page, it stays in the same renderer, so the dialog's elements and handlers simply move over.
// The window stays hidden until the Go core shows it; it is sized, shown and closed like any webview window.
static int openDialogPopup(const cef_string_t *name, const cef_popup_features_t *features, cef_window_info_t *info, cef_client_t **newClient, cef_browser_settings_t *settings) {
    char *id = utf8(name);
    if (strlen(id) < 10 || strlen(id) > 30 || strncmp(id, "simDialog", 9) || shuttingDown || !mainWindow) { free(id); return 0; }
    if (pendingDialog) forgetView(pendingDialog);
    Webview *view = calloc(1, sizeof(Webview));
    snprintf(view->id, sizeof(view->id), "%s", id);
    free(id);
    view->kind = kindWindow;
    view->flags = viewModal;
    view->base = strdup("");
    view->width = features && features->widthSet && features->width > 0 ? features->width : 500;
    view->height = features && features->heightSet && features->height > 0 ? features->height : 300;
    view->window = gtk_window_new(GTK_WINDOW_TOPLEVEL);
    gtk_window_set_title(GTK_WINDOW(view->window), "SimMusic");
    gtk_window_set_decorated(GTK_WINDOW(view->window), FALSE);
    gtk_window_set_resizable(GTK_WINDOW(view->window), FALSE);
    gtk_window_set_transient_for(GTK_WINDOW(view->window), GTK_WINDOW(mainWindow));
    gtk_window_set_modal(GTK_WINDOW(view->window), TRUE);
    gtk_window_set_type_hint(GTK_WINDOW(view->window), GDK_WINDOW_TYPE_HINT_DIALOG);
    gtk_window_set_position(GTK_WINDOW(view->window), GTK_WIN_POS_CENTER_ON_PARENT);
    gtk_window_set_default_size(GTK_WINDOW(view->window), units(view->width), units(view->height));
    gtk_widget_set_size_request(view->window, units(view->width), units(view->height));
    watchView(view);
    gtk_widget_realize(view->window);
    int width, height;
    sizeOf(view->window, &width, &height);
    view->box = makeBox(GDK_WINDOW_XID(gtk_widget_get_window(view->window)), width, height, 0xf9f9fb);
    XMapWindow(xd, view->box);
    info->parent_window = view->box;
    info->bounds.x = info->bounds.y = 0;
    info->bounds.width = width; info->bounds.height = height;
    settings->background_color = 0xfff9f9fb;
    *newClient = &viewClient;
    view->next = webviews;
    webviews = view;
    pendingDialog = view;
    return 1;
}
static int CEF_CALLBACK beforePopup(cef_life_span_handler_t *self, cef_browser_t *browser, cef_frame_t *frame,
    const cef_string_t *targetUrl, const cef_string_t *targetName,
    cef_window_open_disposition_t disposition, int gesture, const cef_popup_features_t *features,
    cef_window_info_t *info, cef_client_t **newClient, cef_browser_settings_t *settings,
    cef_dictionary_value_t **extra, int *noJs) {
    // The only window a page of the host may open is one of its own dialogs.
    return !mainBrowser || browser->get_identifier(browser) != mainBrowser->get_identifier(mainBrowser) || !openDialogPopup(targetName, features, info, newClient, settings);
}
static int CEF_CALLBACK beforeBrowse(cef_request_handler_t *self, cef_browser_t *browser, cef_frame_t *frame,
    cef_request_t *request, int gesture, int redirect) {
    cef_string_userfree_t url = request->get_url(request);
    char *address = utf8(url);
    int block = strncmp(address, startUrl, strlen(startUrl)) != 0;
    free(address);
    if (url) cef_string_userfree_utf16_free(url);
    return block;
}
static void CEF_CALLBACK dragChanged(cef_drag_handler_t *self, cef_browser_t *browser, cef_frame_t *frame,
    size_t count, const cef_draggable_region_t *regions) {
    if (!mainWindow) return;
    if (mainDrag) cairo_region_destroy(mainDrag);
    mainDrag = dragRegionOf(mainWindow, count, regions);
    updateInput();
}
// A page never learns the paths of dropped files. They are reported to the Go core when the drag
// enters, one "DRAG:" line of tab-separated UTF-8 paths; the page then only signals the drop.
static int CEF_CALLBACK dragEnter(cef_drag_handler_t *self, cef_browser_t *browser, cef_drag_data_t *data,
    cef_drag_operations_mask_t mask) {
    cef_string_list_t names = cef_string_list_alloc();
    printf("DRAG:");
    if (data->get_file_names(data, names)) {
        size_t count = cef_string_list_size(names);
        for (size_t i = 0; i < count; i++) {
            cef_string_t name = {0};
            if (!cef_string_list_value(names, i, &name)) continue;
            char *path = utf8(&name);
            if (*path) { if (i) putchar('\t'); fputs(path, stdout); }
            free(path);
            cef_string_utf16_clear(&name);
        }
    }
    printf("\n"); fflush(stdout);
    cef_string_list_free(names);
    data->base.release(&data->base);
    browser->base.release(&browser->base);
    return 0;
}
static void CEF_CALLBACK beforeMenu(cef_context_menu_handler_t *self, cef_browser_t *browser, cef_frame_t *frame,
    cef_context_menu_params_t *params, cef_menu_model_t *model) { model->clear(model); }
static void CEF_CALLBACK fullscreenChanged(cef_display_handler_t *self, cef_browser_t *browser, int enabled) { setMainFullscreen(enabled); }
static cef_life_span_handler_t *CEF_CALLBACK getLife(cef_client_t *self) { return &lifeHandler; }
static cef_drag_handler_t *CEF_CALLBACK getDrag(cef_client_t *self) { return &dragHandler; }
static cef_request_handler_t *CEF_CALLBACK getRequest(cef_client_t *self) { return &requestHandler; }
static cef_context_menu_handler_t *CEF_CALLBACK getMenu(cef_client_t *self) { return &menuHandler; }
static cef_display_handler_t *CEF_CALLBACK getDisplay(cef_client_t *self) { return &displayHandler; }

// ---------- browser callbacks: extension views ----------

static void CEF_CALLBACK viewAfterCreated(cef_life_span_handler_t *self, cef_browser_t *browser) {
    if (creatingView) creatingView->browserId = browser->get_identifier(browser);
    else if (pendingDialog && browser->is_popup(browser)) {
        // The browser of a dialog the host page opened: it fills the window made for it.
        Webview *view = pendingDialog;
        pendingDialog = NULL;
        browser->base.add_ref(&browser->base);
        view->browser = browser;
        view->browserId = browser->get_identifier(browser);
        sizeViewBox(view);
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
    for (Webview *view = webviews; view; view = view->next) if (view->browserId == id) { forgetView(view); break; }
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
        if (view->kind == kindWindow && view->window && !(view->flags & viewTitleFixed) && title && title->str) {
            char *text = utf8(title);
            gtk_window_set_title(GTK_WINDOW(view->window), text);
            free(text);
        }
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
    char *address = utf8(url);
    int handled = 0;
    if (view && view->base && strlen(address) > 6 && !g_ascii_strncasecmp(address, "ext://", 6)) {
        const char *rest = address + 6;
        while (*rest == '/') rest++;
        char *full = g_strdup_printf("%s%s", view->base, rest);
        cef_string_t target = make(full);
        cef_frame_t *top = browser->get_main_frame(browser);
        top->load_url(top, &target);
        releaseCef(top);
        drop(&target);
        g_free(full);
        handled = 1;
    }
    free(address);
    if (url) cef_string_userfree_utf16_free(url);
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
    if (view && view->box && (view->kind == kindPanel || (view->kind == kindWindow && !(view->flags & viewFrame)))) {
        if (view->drag) cairo_region_destroy(view->drag);
        view->drag = dragRegionOf(view->window ? view->window : mainWindow, count, regions);
        if (view->kind == kindPanel) updateInput(); else sizeViewBox(view);
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
    int close = view && view->kind == kindWindow && view->window && event->windows_key_code == 0x1B;
    if (close && event->type == KEYEVENT_RAWKEYDOWN) g_idle_add(closeLater, g_strdup(view->id));
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
    char *channel = utf8(name);
    int handled = !strcmp(channel, "simHost");
    free(channel);
    if (handled && view && frame->is_main(frame)) {
        cef_list_value_t *list = message->get_argument_list(message);
        emitBegin("msg", view->id);
        emitUserFree(frame->get_url(frame));
        emitUserFree(list->get_string(list, 0));
        emitUserFree(list->get_string(list, 1));
        emitEnd();
        releaseCef(list);
    }
    if (name) cef_string_userfree_utf16_free(name);
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
typedef struct { cef_base_ref_counted_t base; void *function; int references; int visitor, done; char request[32]; } CookieCallback;
// The callbacks may come on any thread; the line is written by the main loop.
static gboolean writeLine(gpointer data) {
    printf("WV:%s\n", (char *)data); fflush(stdout);
    g_free(data);
    return G_SOURCE_REMOVE;
}
static void postCookieLine(const char *event, const char *request, int value) {
    g_idle_add(writeLine, g_strdup_printf("%s\t%s\t%d", event, request, value));
}
static void CEF_CALLBACK cookieAddRef(cef_base_ref_counted_t *self) { __sync_add_and_fetch(&((CookieCallback *)self)->references, 1); }
static int CEF_CALLBACK cookieRelease(cef_base_ref_counted_t *self) {
    CookieCallback *callback = (CookieCallback *)self;
    if (__sync_sub_and_fetch(&callback->references, 1)) return 0;
    if (callback->visitor) postCookieLine("cookieEnd", callback->request, 0);
    else if (!callback->done) postCookieLine("cookieDone", callback->request, 0);
    free(callback);
    return 1;
}
static int CEF_CALLBACK cookieHasOne(cef_base_ref_counted_t *self) { return ((CookieCallback *)self)->references == 1; }
static int CEF_CALLBACK cookieHasAny(cef_base_ref_counted_t *self) { return ((CookieCallback *)self)->references > 0; }
// A visited cookie is one line as well, composed here and written by the main loop.
static void appendField(GString *line, const cef_string_t *text) {
    char *value = utf8(text);
    g_string_append_c(line, '\t');
    for (const char *c = value; *c; c++) {
        if (*c == '\\') g_string_append(line, "\\\\");
        else if (*c == '\t') g_string_append(line, "\\t");
        else if (*c == '\n') g_string_append(line, "\\n");
        else if (*c == '\r') g_string_append(line, "\\r");
        else g_string_append_c(line, *c);
    }
    free(value);
}
static int CEF_CALLBACK cookieVisit(cef_cookie_visitor_t *self, const cef_cookie_t *cookie, int count, int total, int *deleteCookie) {
    GString *line = g_string_new("cookie\t");
    g_string_append(line, ((CookieCallback *)self)->request);
    appendField(line, &cookie->name); appendField(line, &cookie->value); appendField(line, &cookie->domain); appendField(line, &cookie->path);
    g_string_append_printf(line, "\t%d\t%d\t%lld", cookie->secure, cookie->httponly, cookie->has_expires ? (long long)(cookie->expires.val / 1000000 - 11644473600LL) : 0LL);
    g_idle_add(writeLine, g_string_free(line, FALSE));
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
// No spell checking and no form or password autofill, in the interface and in extension pages alike.
static void quietContext(cef_request_context_t *context) {
    static const char *names[] = { "browser.enable_spellchecking", "spellcheck.use_spelling_service", "autofill.enabled", "autofill.profile_enabled",
        "autofill.credit_card_enabled", "credentials_enable_service", "credentials_enable_autosignin" };
    if (!context) return;
    for (int i = 0; i < (int)(sizeof(names) / sizeof(*names)); i++) {
        cef_string_t name = make(names[i]), error = {0};
        if (context->has_preference(context, &name)) {
            cef_value_t *value = cef_value_create();
            value->set_bool(value, 0);
            context->set_preference(context, &name, value, &error);
            drop(&error);
        }
        drop(&name);
    }
}
static cef_request_context_t *partition(const char *path) {
    int count = sizeof(partitions) / sizeof(*partitions);
    for (int i = 0; i < count; i++) {
        if (partitions[i].path && !strcmp(partitions[i].path, path)) return partitions[i].context;
        if (partitions[i].path) continue;
        cef_request_context_settings_t settings = { .size = sizeof(settings) };
        settings.cache_path = make(path);
        cef_request_context_t *context = cef_request_context_create_context(&settings, NULL);
        drop(&settings.cache_path);
        if (!context) break;
        quietContext(context);
        partitions[i].path = strdup(path);
        partitions[i].context = context;
        return context;
    }
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
    const char *address = count > 2 ? f[2] : "";
    cef_string_t url = make(address);
    if (visitor) {
        if (*address) manager->visit_url_cookies(manager, &url, 1, (cef_cookie_visitor_t *)callback);
        else manager->visit_all_cookies(manager, (cef_cookie_visitor_t *)callback);
    } else if (!strcmp(command, "cookieSet") && count >= 10) {
        cef_cookie_t cookie = { .name = make(f[3]), .value = make(f[4]), .domain = make(f[5]), .path = make(f[6]),
            .secure = f[7][0] == '1', .httponly = f[8][0] == '1' };
        long long expires = atoll(f[9]);
        if (expires > 0) { cookie.has_expires = 1; cookie.expires.val = (expires + 11644473600LL) * 1000000; }
        manager->set_cookie(manager, &url, &cookie, (cef_set_cookie_callback_t *)callback);
        drop(&cookie.name); drop(&cookie.value); drop(&cookie.domain); drop(&cookie.path);
    } else {
        const char *name = count > 3 ? f[3] : "";
        cef_string_t cookieName = make(name);
        manager->delete_cookies(manager, *address ? &url : NULL, *name ? &cookieName : NULL, (cef_delete_cookies_callback_t *)callback);
        drop(&cookieName);
    }
    drop(&url);
    releaseCef(manager);
}

// ---------- commands: views ----------

static void createView(char **f, int count) {
    // id kind url base partition title icon background width height minWidth minHeight maxWidth maxHeight x y flags
    if (count < 17 || findView(f[0]) || shuttingDown || !mainWindow) return;
    Webview *view = calloc(1, sizeof(Webview));
    snprintf(view->id, sizeof(view->id), "%s", f[0]);
    view->kind = !strcmp(f[1], "panel") ? kindPanel : !strcmp(f[1], "offscreen") ? kindOffscreen : kindWindow;
    view->base = strdup(f[3]);
    view->width = atoi(f[8]); view->height = atoi(f[9]);
    view->minWidth = atoi(f[10]); view->minHeight = atoi(f[11]); view->maxWidth = atoi(f[12]); view->maxHeight = atoi(f[13]);
    view->flags = atoi(f[16]);
    unsigned int color = 0xffffff, parsed;
    if (f[7][0] == '#' && strlen(f[7]) == 7 && sscanf(f[7] + 1, "%6x", &parsed) == 1) color = parsed;
    cef_window_info_t info = {0};
    if (view->kind == kindPanel) {
        view->box = makeBox(GDK_WINDOW_XID(gtk_widget_get_window(mainWindow)), 1, 1, color);
        info.parent_window = view->box;
        info.bounds.width = info.bounds.height = 1;
    } else if (view->kind == kindWindow) {
        GtkWindow *window = GTK_WINDOW(view->window = gtk_window_new(GTK_WINDOW_TOPLEVEL));
        gtk_window_set_title(window, f[5][0] ? f[5] : "SimMusic");
        gtk_window_set_decorated(window, (view->flags & viewFrame) != 0);
        gtk_window_set_resizable(window, (view->flags & viewResizable) != 0);
        gtk_window_set_skip_taskbar_hint(window, (view->flags & viewSkipTaskbar) != 0);
        int width = units(view->width > 0 ? view->width : 800), height = units(view->height > 0 ? view->height : 600);
        gtk_window_set_default_size(window, width, height);
        if (!(view->flags & viewResizable)) gtk_widget_set_size_request(view->window, width, height);
        GdkGeometry limits = { .min_width = view->minWidth > 0 ? units(view->minWidth) : 1, .min_height = view->minHeight > 0 ? units(view->minHeight) : 1,
            .max_width = view->maxWidth > 0 ? units(view->maxWidth) : G_MAXINT, .max_height = view->maxHeight > 0 ? units(view->maxHeight) : G_MAXINT };
        if (view->flags & viewResizable) gtk_window_set_geometry_hints(window, NULL, &limits, GDK_HINT_MIN_SIZE | GDK_HINT_MAX_SIZE);
        if (view->flags & viewPositioned) gtk_window_move(window, units(atoi(f[14])), units(atoi(f[15]))); else gtk_window_set_position(window, GTK_WIN_POS_CENTER);
        if (f[6][0]) gtk_window_set_icon_from_file(window, f[6], NULL);
        watchView(view);
        gtk_widget_realize(view->window);
        int boxWidth, boxHeight;
        sizeOf(view->window, &boxWidth, &boxHeight);
        view->box = makeBox(GDK_WINDOW_XID(gtk_widget_get_window(view->window)), boxWidth, boxHeight, color);
        XMapWindow(xd, view->box);
        info.parent_window = view->box;
        info.bounds.width = boxWidth; info.bounds.height = boxHeight;
    } else {
        info.windowless_rendering_enabled = 1;
        info.parent_window = mainBox;
    }
    cef_browser_settings_t settings = { .size = sizeof(settings), .background_color = 0xff000000 | color, .windowless_frame_rate = 1 };
    cef_dictionary_value_t *extra = cef_dictionary_value_create();
    cef_string_t key = make("simWebview");
    extra->set_bool(extra, &key, 1);
    drop(&key);
    cef_request_context_t *context = partition(f[4]);
    if (context) context->base.add_ref(&context->base);
    cef_string_t url = make(f[2]);
    view->next = webviews;
    webviews = view;
    creatingView = view;
    view->browser = cef_browser_host_create_browser_sync(&info, &viewClient, &url, &settings, extra, context);
    creatingView = NULL;
    drop(&url);
    if (!view->browser) { forgetView(view); return; }
    view->browserId = view->browser->get_identifier(view->browser);
    if (view->kind == kindPanel && (view->flags & viewCover)) layoutPanel(view);
    if (view->kind == kindWindow) {
        sizeViewBox(view);
        if (view->flags & viewFullscreen) setViewFullscreen(view, 1);
        if (view->flags & viewTop) gtk_window_set_keep_above(GTK_WINDOW(view->window), TRUE);
        if (view->flags & viewShow) { gtk_widget_show(view->window); gtk_window_present(GTK_WINDOW(view->window)); }
    }
}
static void windowCommand(Webview *view, const char *action, const char *a, const char *b) {
    if (view->kind != kindWindow || !view->window || view->closing) return;
    GtkWindow *window = GTK_WINDOW(view->window);
    if (!strcmp(action, "show")) { gtk_widget_show(view->window); gtk_window_deiconify(window); gtk_window_present(window); sizeViewBox(view); focusBrowser(view->browser); }
    else if (!strcmp(action, "hide")) gtk_widget_hide(view->window);
    else if (!strcmp(action, "focus")) { gtk_window_deiconify(window); gtk_window_present(window); }
    else if (!strcmp(action, "minimize")) gtk_window_iconify(window);
    else if (!strcmp(action, "maximize")) gtk_window_maximize(window);
    else if (!strcmp(action, "restore")) { gtk_window_deiconify(window); gtk_window_unmaximize(window); }
    else if (!strcmp(action, "title")) { gtk_window_set_title(window, a); view->flags |= viewTitleFixed; }
    else if (!strcmp(action, "top")) gtk_window_set_keep_above(window, a[0] == '1');
    else if (!strcmp(action, "fullscreen")) setViewFullscreen(view, a[0] == '1');
    else if (!strcmp(action, "size")) {
        int width = units(atoi(a)), height = units(atoi(b));
        // A window that cannot be resized by the user is held to its size by a size request.
        if (!gtk_window_get_resizable(window)) gtk_widget_set_size_request(view->window, width, height);
        gtk_window_resize(window, width, height);
        sizeViewBox(view);
    }
    else if (!strcmp(action, "position")) gtk_window_move(window, units(atoi(a)), units(atoi(b)));
    else if (!strcmp(action, "center")) {
        GdkWindow *gdk = gtk_widget_get_window(view->window);
        GdkMonitor *monitor = gdk ? gdk_display_get_monitor_at_window(gdk_display_get_default(), gdk) : NULL;
        GdkRectangle area;
        int width, height;
        if (!monitor) return;
        gdk_monitor_get_workarea(monitor, &area);
        gtk_window_get_size(window, &width, &height);
        gtk_window_move(window, area.x + (area.width - width) / 2, area.y + (area.height - height) / 2);
    }
}

// ---------- menus ----------

// A context menu is a GTK menu: the rows come from the Go core, a row with children opens a list of its own,
// and the rows of a lazy row are asked for when it is first pointed at. Opening waits for the mouse buttons to
// be released: the press that asked for the menu is still held by the page it happened in.
static GtkWidget *menuTop;
static GHashTable *menuLists;   // path of a row → the list of its children; "" is the top list
static GtkCssProvider *styleProvider;
static char *styleFamily;
static int menuId, menuActed;
static guint menuWait;

static void applyStyle(const char *family) {
    if (styleFamily && !strcmp(styleFamily, family ? family : "")) return;
    g_free(styleFamily);
    styleFamily = g_strdup(family ? family : "");
    GString *clean = g_string_new("");
    for (const char *c = styleFamily; *c; c++) if (*c != '"' && *c != '\\' && *c != ';' && *c != '{' && *c != '}' && *c != '\n') g_string_append_c(clean, *c);
    char *font = clean->len ? g_strdup_printf("font-family: \"%s\";", clean->str) : g_strdup("");
    char *css = g_strdup_printf(
        "menu.simMenu { %s font-size: 14px; padding: 5px; border-radius: 8px; }"
        "menu.simMenu menuitem { padding: 6px 12px; border-radius: 5px; min-height: 0; }"
        "menu.simMenu separator { margin: 5px 0; }", font);
    if (!styleProvider) {
        styleProvider = gtk_css_provider_new();
        gtk_style_context_add_provider_for_screen(gdk_screen_get_default(), GTK_STYLE_PROVIDER(styleProvider), GTK_STYLE_PROVIDER_PRIORITY_APPLICATION);
    }
    gtk_css_provider_load_from_data(styleProvider, css, -1, NULL);
    g_free(css); g_free(font);
    g_string_free(clean, TRUE);
}
static GtkWidget *newMenuList(void) {
    GtkWidget *list = gtk_menu_new();
    gtk_style_context_add_class(gtk_widget_get_style_context(list), "simMenu");
    gtk_menu_set_reserve_toggle_size(GTK_MENU(list), FALSE);
    return list;
}
static void menuActivate(GtkMenuItem *item, gpointer data) {
    char number[16];
    snprintf(number, sizeof(number), "%d", menuId);
    menuActed = 1;
    emitLine("OV:", "action", number, g_object_get_data(G_OBJECT(item), "path"));
}
static void menuSelect(GtkMenuItem *item, gpointer data) {
    if (g_object_get_data(G_OBJECT(item), "asked")) return;
    g_object_set_data(G_OBJECT(item), "asked", GINT_TO_POINTER(1));
    char number[16];
    snprintf(number, sizeof(number), "%d", menuId);
    emitLine("OV:", "children", number, g_object_get_data(G_OBJECT(item), "path"));
}
static void addMenuRow(const char *path, int flags, const char *icon, const char *label) {
    const char *dot = strrchr(path, '.');
    char *parent = dot ? g_strndup(path, dot - path) : g_strdup("");
    GtkWidget *list = g_hash_table_lookup(menuLists, parent);
    g_free(parent);
    if (!list) return;
    GtkWidget *item;
    if (flags & 1) item = gtk_separator_menu_item_new();
    else {
        item = gtk_menu_item_new();
        GtkWidget *row = gtk_box_new(GTK_ORIENTATION_HORIZONTAL, 10);
        GtkWidget *glyph = gtk_label_new(icon);
        PangoAttrList *attributes = pango_attr_list_new();
        pango_attr_list_insert(attributes, pango_attr_family_new("remixicon"));
        gtk_label_set_attributes(GTK_LABEL(glyph), attributes);
        pango_attr_list_unref(attributes);
        gtk_widget_set_size_request(glyph, 18, -1);
        GtkWidget *text = gtk_label_new(label);
        gtk_label_set_xalign(GTK_LABEL(text), 0);
        gtk_box_pack_start(GTK_BOX(row), glyph, FALSE, FALSE, 0);
        gtk_box_pack_start(GTK_BOX(row), text, TRUE, TRUE, 0);
        gtk_container_add(GTK_CONTAINER(item), row);
        g_object_set_data_full(G_OBJECT(item), "path", g_strdup(path), g_free);
        if (flags & 6) {
            GtkWidget *children = newMenuList();
            gtk_menu_item_set_submenu(GTK_MENU_ITEM(item), children);
            g_hash_table_insert(menuLists, g_strdup(path), children);
            if (!(flags & 4)) {
                GtkWidget *waiting = gtk_menu_item_new_with_label("正在读取…");
                gtk_widget_set_sensitive(waiting, FALSE);
                gtk_menu_shell_append(GTK_MENU_SHELL(children), waiting);
                gtk_widget_show_all(waiting);
                g_signal_connect(item, "select", G_CALLBACK(menuSelect), NULL);
            }
        } else g_signal_connect(item, "activate", G_CALLBACK(menuActivate), NULL);
    }
    gtk_menu_shell_append(GTK_MENU_SHELL(list), item);
    gtk_widget_show_all(item);
}
static void closeMenu(void) {
    if (menuWait) { g_source_remove(menuWait); menuWait = 0; }
    if (menuTop) { GtkWidget *menu = menuTop; menuTop = NULL; gtk_menu_popdown(GTK_MENU(menu)); gtk_widget_destroy(menu); }
    if (menuLists) { g_hash_table_destroy(menuLists); menuLists = NULL; }
}
// A row acts after the menu is told it is done with: whether one acted is known a moment later.
static gboolean menuDone(gpointer data) {
    if (GPOINTER_TO_INT(data) != menuId || !menuTop) return G_SOURCE_REMOVE;
    if (!menuActed) { char number[16]; snprintf(number, sizeof(number), "%d", menuId); emitLine("OV:", "closed", number, NULL); }
    closeMenu();
    return G_SOURCE_REMOVE;
}
static void menuDeactivate(GtkMenuShell *shell, gpointer data) { g_idle_add(menuDone, GINT_TO_POINTER(menuId)); }
static gboolean popupMenu(gpointer data) {
    if (!menuTop) { menuWait = 0; return G_SOURCE_REMOVE; }
    GdkSeat *seat = gdk_display_get_default_seat(gdk_display_get_default());
    GdkModifierType buttons = 0;
    gdk_device_get_state(gdk_seat_get_pointer(seat), gdk_get_default_root_window(), NULL, &buttons);
    if (buttons & (GDK_BUTTON1_MASK | GDK_BUTTON2_MASK | GDK_BUTTON3_MASK)) return G_SOURCE_CONTINUE;
    menuWait = 0;
    gtk_menu_popup_at_pointer(GTK_MENU(menuTop), NULL);
    return G_SOURCE_REMOVE;
}
static void addMenuRows(char **f, int count) {
    for (int i = 0; i + 3 < count; i += 4) addMenuRow(f[i], atoi(f[i + 1]), f[i + 2], f[i + 3]);
}
static void menuCommand(char **f, int count) {
    // show id family rows… | children id path rows… | close; a menu opens where the pointer is
    if (count < 1) return;
    if (!strcmp(f[0], "close")) { closeMenu(); return; }
    if (!strcmp(f[0], "show") && count >= 3) {
        closeMenu();
        menuId = atoi(f[1]); menuActed = 0;
        applyStyle(f[2]);
        menuTop = newMenuList();
        g_object_ref_sink(menuTop);
        menuLists = g_hash_table_new_full(g_str_hash, g_str_equal, g_free, NULL);
        g_hash_table_insert(menuLists, g_strdup(""), menuTop);
        addMenuRows(f + 3, count - 3);
        g_signal_connect(menuTop, "deactivate", G_CALLBACK(menuDeactivate), NULL);
        menuWait = g_timeout_add(15, popupMenu, NULL);
    } else if (!strcmp(f[0], "children") && count >= 3 && menuTop && atoi(f[1]) == menuId) {
        GtkWidget *list = g_hash_table_lookup(menuLists, f[2]);
        if (!list) return;
        GList *old = gtk_container_get_children(GTK_CONTAINER(list));
        for (GList *row = old; row; row = row->next) gtk_widget_destroy(row->data);
        g_list_free(old);
        addMenuRows(f + 3, count - 3);
        gtk_menu_reposition(GTK_MENU(list));
    }
}

// ---------- notices ----------

// A notice is a small window above the bottom of the main window: white text on the accent colour.
// It leaves after three seconds; several stand one above the other.
typedef struct { GtkWidget *window; char *text, *family; int width, height, textWidth, textHeight; double opacity; int leaving; } Toast;
static GList *toasts;
static PangoLayout *toastLayout(Toast *toast, cairo_t *cr) {
    PangoLayout *layout = pango_cairo_create_layout(cr);
    PangoFontDescription *font = pango_font_description_new();
    pango_font_description_set_family(font, toast->family && *toast->family ? toast->family : "sans-serif");
    pango_font_description_set_absolute_size(font, units(16) * PANGO_SCALE);
    pango_layout_set_font_description(layout, font);
    pango_font_description_free(font);
    pango_layout_set_text(layout, toast->text, -1);
    pango_layout_set_width(layout, units(270) * PANGO_SCALE);
    pango_layout_set_wrap(layout, PANGO_WRAP_WORD_CHAR);
    return layout;
}
static void roundedPath(cairo_t *cr, double x, double y, double width, double height, double radius) {
    cairo_new_sub_path(cr);
    cairo_arc(cr, x + width - radius, y + radius, radius, -G_PI / 2, 0);
    cairo_arc(cr, x + width - radius, y + height - radius, radius, 0, G_PI / 2);
    cairo_arc(cr, x + radius, y + height - radius, radius, G_PI / 2, G_PI);
    cairo_arc(cr, x + radius, y + radius, radius, G_PI, 3 * G_PI / 2);
    cairo_close_path(cr);
}
static gboolean toastDraw(GtkWidget *widget, cairo_t *cr, gpointer data) {
    Toast *toast = data;
    cairo_set_operator(cr, CAIRO_OPERATOR_SOURCE);
    cairo_set_source_rgba(cr, 0, 0, 0, 0);
    cairo_paint(cr);
    cairo_set_operator(cr, CAIRO_OPERATOR_OVER);
    roundedPath(cr, 0, 0, toast->width, toast->height, units(5));
    cairo_set_source_rgba(cr, 0x1e / 255., 0x9f / 255., 1, 1);
    cairo_fill(cr);
    PangoLayout *layout = toastLayout(toast, cr);
    cairo_set_source_rgb(cr, 1, 1, 1);
    cairo_move_to(cr, units(15), (toast->height - toast->textHeight) / 2.);
    pango_cairo_show_layout(cr, layout);
    g_object_unref(layout);
    return TRUE;
}
static void placeToasts(void) {
    int x = 0, y = 0, width = 0, height = 0;
    GdkWindow *of = mainWindow && gtk_widget_get_visible(mainWindow) ? gtk_widget_get_window(mainWindow) : NULL;
    if (of) { gdk_window_get_origin(of, &x, &y); gtk_window_get_size(GTK_WINDOW(mainWindow), &width, &height); }
    else {
        GdkRectangle area = {0};
        GdkMonitor *monitor = gdk_display_get_primary_monitor(gdk_display_get_default());
        if (monitor) gdk_monitor_get_workarea(monitor, &area);
        x = area.x; y = area.y; width = area.width; height = area.height;
    }
    int bottom = y + height - units(110);
    for (GList *item = toasts; item; item = item->next) {
        Toast *toast = item->data;
        bottom -= toast->height;
        gtk_window_move(GTK_WINDOW(toast->window), x + (width - toast->width) / 2, bottom);
        bottom -= units(10);
    }
}
static gboolean toastFade(gpointer data) {
    Toast *toast = data;
    toast->opacity += toast->leaving ? -.12 : .12;
    if (!toast->leaving && toast->opacity >= 1) { toast->opacity = 1; gtk_widget_set_opacity(toast->window, 1); return G_SOURCE_REMOVE; }
    if (toast->leaving && toast->opacity <= 0) {
        toasts = g_list_remove(toasts, toast);
        gtk_widget_destroy(toast->window);
        g_free(toast->text); g_free(toast->family); g_free(toast);
        placeToasts();
        emitLine("OV:", "toastGone", NULL, NULL);
        return G_SOURCE_REMOVE;
    }
    gtk_widget_set_opacity(toast->window, toast->opacity);
    return G_SOURCE_CONTINUE;
}
static gboolean toastLeave(gpointer data) {
    Toast *toast = data;
    toast->leaving = 1;
    g_timeout_add(16, toastFade, toast);
    return G_SOURCE_REMOVE;
}
static void showToast(const char *text, const char *family) {
    Toast *toast = g_new0(Toast, 1);
    toast->text = g_strdup(text); toast->family = g_strdup(family);
    toast->window = gtk_window_new(GTK_WINDOW_POPUP);
    gtk_widget_set_app_paintable(toast->window, TRUE);
    GdkVisual *visual = gdk_screen_get_rgba_visual(gdk_screen_get_default());
    if (visual) gtk_widget_set_visual(toast->window, visual);
    // Measured with a layout of its own: the window is sized before it is drawn.
    cairo_surface_t *surface = cairo_image_surface_create(CAIRO_FORMAT_ARGB32, 1, 1);
    cairo_t *cr = cairo_create(surface);
    PangoLayout *layout = toastLayout(toast, cr);
    pango_layout_get_pixel_size(layout, &toast->textWidth, &toast->textHeight);
    g_object_unref(layout);
    cairo_destroy(cr); cairo_surface_destroy(surface);
    toast->width = toast->textWidth + units(30); toast->height = toast->textHeight + units(15);
    gtk_window_set_default_size(GTK_WINDOW(toast->window), toast->width, toast->height);
    g_signal_connect(toast->window, "draw", G_CALLBACK(toastDraw), toast);
    gtk_widget_set_opacity(toast->window, 0);
    toasts = g_list_prepend(toasts, toast);
    placeToasts();
    gtk_widget_show(toast->window);
    g_timeout_add(16, toastFade, toast);
    g_timeout_add(3000, toastLeave, toast);
}

// ---------- desktop lyrics ----------

// The line being sung, in a window of its own above the desktop: see-through, kept above other windows,
// moved by dragging. While the pointer is over it a row of controls shows. A line sung word by word is
// drawn at half opacity and in full up to where the singing has got; the Go core gives the position and
// the rate it advances at, the window moves it on by itself between two reports.
typedef struct { double start, end; char *text; } LyricWord;
static struct {
    GtkWidget *window;
    double color[3], strokeColor[3], size, width, transScale;
    int stroke, center, showTrans, compact;
    char *family, *main, *sub;
    LyricWord *words; int wordCount;
    double position, rate; gint64 positionAt;
    int visible, playing, mode, hover, hoverControl, pressedControl;
    int x, y, placed, lastX, lastY;
    guint ticker, moved;
} lyric = { .color = { 0x1e / 255., 0x9f / 255., 1 }, .size = 30, .width = 800, .transScale = .6, .hoverControl = -1, .pressedControl = -1 };
static const char *lyricControls[] = { "mode", "previous", "toggle", "next", "close" };

static void parseColor(const char *text, double *out) {
    unsigned int value;
    if (sscanf(text, "%6x", &value) != 1) return;
    out[0] = ((value >> 16) & 255) / 255.; out[1] = ((value >> 8) & 255) / 255.; out[2] = (value & 255) / 255.;
}
static double lyricTop(void) { return units(36); }
static int lyricHeight(void) {
    double height = 36 + lyric.size * 1.5 + (lyric.showTrans ? lyric.size * lyric.transScale * 1.5 : 0) + 10;
    return units(height);
}
static void controlBox(int index, int *x, int *y, int *width, int *height) {
    int each = units(32), gap = units(6), total = 5 * each + 4 * gap, windowWidth = units(lyric.width);
    *x = (windowWidth - total) / 2 + index * (each + gap); *y = units(4); *width = each; *height = units(28);
}
static int controlAt(double px, double py) {
    if (lyric.compact) return -1;
    for (int i = 0; i < 5; i++) {
        int x, y, width, height;
        controlBox(i, &x, &y, &width, &height);
        if (px >= x && px < x + width && py >= y && py < y + height) return i;
    }
    return -1;
}
static PangoLayout *lyricLayout(cairo_t *cr, const char *text, double size, int bold, int width) {
    PangoLayout *layout = pango_cairo_create_layout(cr);
    PangoFontDescription *font = pango_font_description_new();
    pango_font_description_set_family(font, lyric.family && *lyric.family ? lyric.family : "sans-serif");
    pango_font_description_set_weight(font, bold ? PANGO_WEIGHT_BOLD : PANGO_WEIGHT_NORMAL);
    pango_font_description_set_absolute_size(font, units(size) * PANGO_SCALE);
    pango_layout_set_font_description(layout, font);
    pango_font_description_free(font);
    pango_layout_set_single_paragraph_mode(layout, TRUE);
    pango_layout_set_text(layout, text ? text : "", -1);
    pango_layout_set_width(layout, width * PANGO_SCALE);
    pango_layout_set_ellipsize(layout, PANGO_ELLIPSIZE_END);
    pango_layout_set_alignment(layout, lyric.center ? PANGO_ALIGN_CENTER : PANGO_ALIGN_LEFT);
    return layout;
}
// How far the singing has got in the line, as a horizontal position in the layout; a negative value
// when the line is not sung word by word.
static double sungUpTo(PangoLayout *layout) {
    if (!lyric.wordCount || !lyric.main) return -1;
    double now = lyric.position + (g_get_monotonic_time() - lyric.positionAt) / 1e6 * lyric.rate;
    size_t total = strlen(lyric.main), offset = 0, sum = 0;
    for (int i = 0; i < lyric.wordCount; i++) sum += strlen(lyric.words[i].text);
    PangoRectangle ink, logical;
    pango_layout_get_pixel_extents(layout, &ink, &logical);
    // The words are the line cut into pieces; where they do not add up to it, the time alone decides.
    if (sum != total) {
        double from = lyric.words[0].start, to = lyric.words[lyric.wordCount - 1].end;
        double part = to > from ? (now - from) / (to - from) : 1;
        return logical.x + logical.width * fmax(0, fmin(1, part));
    }
    for (int i = 0; i < lyric.wordCount; i++) {
        LyricWord *word = &lyric.words[i];
        size_t length = strlen(word->text);
        if (now < word->end) {
            double part = now <= word->start || word->end <= word->start ? 0 : (now - word->start) / (word->end - word->start);
            PangoRectangle from, to;
            pango_layout_index_to_pos(layout, (int)offset, &from);
            pango_layout_index_to_pos(layout, (int)(offset + length), &to);
            return (from.x + (to.x - from.x) * part) / PANGO_SCALE;
        }
        offset += length;
    }
    return logical.x + logical.width + 1;
}
static void drawLyricLine(cairo_t *cr, PangoLayout *layout, double x, double y, double sung, double alpha, int windowHeight) {
    cairo_save(cr);
    cairo_move_to(cr, x, y);
    pango_cairo_layout_path(cr, layout);
    cairo_path_t *path = cairo_copy_path(cr);
    if (lyric.stroke) {
        cairo_set_source_rgba(cr, lyric.strokeColor[0], lyric.strokeColor[1], lyric.strokeColor[2], alpha);
        cairo_set_line_width(cr, fmax(2, units(lyric.size) / 9.));
        cairo_set_line_join(cr, CAIRO_LINE_JOIN_ROUND);
        cairo_stroke(cr);
    } else cairo_new_path(cr);
    cairo_append_path(cr, path);
    cairo_set_source_rgba(cr, lyric.color[0], lyric.color[1], lyric.color[2], sung < 0 ? alpha : alpha * .5);
    cairo_fill(cr);
    if (sung >= 0) {
        cairo_rectangle(cr, 0, 0, x + sung, windowHeight);
        cairo_clip(cr);
        cairo_append_path(cr, path);
        cairo_set_source_rgba(cr, lyric.color[0], lyric.color[1], lyric.color[2], alpha);
        cairo_fill(cr);
    }
    cairo_path_destroy(path);
    cairo_restore(cr);
}
static gboolean lyricDraw(GtkWidget *widget, cairo_t *cr, gpointer data) {
    int width = gtk_widget_get_allocated_width(widget), height = gtk_widget_get_allocated_height(widget);
    cairo_set_operator(cr, CAIRO_OPERATOR_SOURCE);
    cairo_set_source_rgba(cr, 0, 0, 0, 0);
    cairo_paint(cr);
    cairo_set_operator(cr, CAIRO_OPERATOR_OVER);
    if (lyric.hover) {
        roundedPath(cr, 0, 0, width, height, units(8));
        cairo_set_source_rgba(cr, 0, 0, 0, .28);
        cairo_fill(cr);
        if (!lyric.compact) {
            // mode (loop, single, shuffle), previous, play or pause, next, close: glyphs of the icon font
            static const gunichar modes[] = { 0xF072, 0xF075, 0xF124 };
            gunichar glyphs[5] = { modes[lyric.mode < 0 || lyric.mode > 2 ? 0 : lyric.mode], 0xF140, lyric.playing ? 0xF507 : 0xF508, 0xF144, 0xEB99 };
            for (int i = 0; i < 5; i++) {
                int x, y, boxWidth, boxHeight;
                controlBox(i, &x, &y, &boxWidth, &boxHeight);
                roundedPath(cr, x, y, boxWidth, boxHeight, units(5));
                cairo_set_source_rgba(cr, i == lyric.hoverControl ? .18 : 0, i == lyric.hoverControl ? .18 : 0, i == lyric.hoverControl ? .18 : 0, .55);
                cairo_fill(cr);
                char text[8] = {0};
                g_unichar_to_utf8(glyphs[i], text);
                PangoLayout *layout = pango_cairo_create_layout(cr);
                PangoFontDescription *font = pango_font_description_new();
                pango_font_description_set_family(font, "remixicon");
                pango_font_description_set_absolute_size(font, units(20) * PANGO_SCALE);
                pango_layout_set_font_description(layout, font);
                pango_font_description_free(font);
                pango_layout_set_text(layout, text, -1);
                int glyphWidth, glyphHeight;
                pango_layout_get_pixel_size(layout, &glyphWidth, &glyphHeight);
                cairo_set_source_rgba(cr, .92, .92, .92, .92);
                cairo_move_to(cr, x + (boxWidth - glyphWidth) / 2., y + (boxHeight - glyphHeight) / 2.);
                pango_cairo_show_layout(cr, layout);
                g_object_unref(layout);
            }
        }
    }
    int pad = units(12), textWidth = width - 2 * pad;
    PangoLayout *layout = lyricLayout(cr, lyric.main, lyric.size, 1, textWidth);
    drawLyricLine(cr, layout, pad, lyricTop(), sungUpTo(layout), 1, height);
    g_object_unref(layout);
    if (lyric.showTrans && lyric.sub && *lyric.sub) {
        layout = lyricLayout(cr, lyric.sub, lyric.size * lyric.transScale, 1, textWidth);
        drawLyricLine(cr, layout, pad, lyricTop() + units(lyric.size * 1.5), -1, .9, height);
        g_object_unref(layout);
    }
    return TRUE;
}
static gboolean lyricTick(gpointer data) {
    if (!lyric.window || !lyric.visible || !lyric.wordCount || lyric.rate == 0) { lyric.ticker = 0; return G_SOURCE_REMOVE; }
    gtk_widget_queue_draw(lyric.window);
    return G_SOURCE_CONTINUE;
}
static void lyricRefresh(void) {
    if (!lyric.window) return;
    gtk_widget_queue_draw(lyric.window);
    if (!lyric.ticker && lyric.visible && lyric.wordCount && lyric.rate != 0) lyric.ticker = g_timeout_add(16, lyricTick, NULL);
}
static gboolean lyricMovedLater(gpointer data) {
    char x[16], y[16];
    snprintf(x, sizeof(x), "%d", lyric.x); snprintf(y, sizeof(y), "%d", lyric.y);
    lyric.moved = 0;
    emitLine("DL:", "moved", x, y);
    return G_SOURCE_REMOVE;
}
static gboolean lyricConfigure(GtkWidget *window, GdkEventConfigure *event, gpointer data) {
    // Where the window is, once it has stopped moving, is where it is shown again the next time.
    if (lyric.visible && (event->x != lyric.x || event->y != lyric.y)) {
        lyric.x = event->x; lyric.y = event->y; lyric.placed = 1;
        if (lyric.moved) g_source_remove(lyric.moved);
        lyric.moved = g_timeout_add(400, lyricMovedLater, NULL);
    }
    return FALSE;
}
static gboolean lyricCross(GtkWidget *window, GdkEventCrossing *event, gpointer data) {
    lyric.hover = event->type == GDK_ENTER_NOTIFY;
    if (!lyric.hover) lyric.hoverControl = -1;
    lyricRefresh();
    return FALSE;
}
static gboolean lyricMotion(GtkWidget *window, GdkEventMotion *event, gpointer data) {
    int control = controlAt(event->x, event->y);
    if (control != lyric.hoverControl || !lyric.hover) { lyric.hover = 1; lyric.hoverControl = control; lyricRefresh(); }
    return FALSE;
}
static gboolean lyricButton(GtkWidget *window, GdkEventButton *event, gpointer data) {
    if (event->button != 1) return FALSE;
    int control = controlAt(event->x, event->y);
    if (event->type == GDK_BUTTON_PRESS) {
        lyric.pressedControl = control;
        if (control < 0) gtk_window_begin_move_drag(GTK_WINDOW(window), event->button, (int)event->x_root, (int)event->y_root, event->time);
    } else if (event->type == GDK_BUTTON_RELEASE) {
        if (control >= 0 && control == lyric.pressedControl) emitLine("DL:", "control", lyricControls[control], NULL);
        lyric.pressedControl = -1;
    }
    return TRUE;
}
static gboolean lyricScroll(GtkWidget *window, GdkEventScroll *event, gpointer data) {
    double dx = 0, dy = 0;
    if (event->direction == GDK_SCROLL_UP) dy = -1;
    else if (event->direction == GDK_SCROLL_DOWN) dy = 1;
    else if (event->direction == GDK_SCROLL_SMOOTH) gdk_event_get_scroll_deltas((GdkEvent *)event, &dx, &dy);
    if (dy) emitLine("DL:", "control", dy < 0 ? "volumeUp" : "volumeDown", NULL);
    return TRUE;
}
static void placeLyric(void) {
    if (!lyric.window) return;
    int width = units(lyric.width), height = lyricHeight();
    gtk_widget_set_size_request(lyric.window, width, height);
    gtk_window_resize(GTK_WINDOW(lyric.window), width, height);
    if (!lyric.placed) {
        // First shown: above the bottom of the screen, in the middle.
        GdkRectangle area = {0};
        GdkMonitor *monitor = gdk_display_get_primary_monitor(gdk_display_get_default());
        if (monitor) gdk_monitor_get_workarea(monitor, &area);
        lyric.x = area.x + (area.width - width) / 2; lyric.y = area.y + area.height - height - units(60);
    }
    gtk_window_move(GTK_WINDOW(lyric.window), lyric.x, lyric.y);
}
static void createLyricWindow(void) {
    if (lyric.window) return;
    GtkWindow *window = GTK_WINDOW(lyric.window = gtk_window_new(GTK_WINDOW_TOPLEVEL));
    gtk_window_set_title(window, "SimMusic 桌面歌词");
    gtk_window_set_decorated(window, FALSE);
    gtk_window_set_resizable(window, FALSE);
    gtk_window_set_keep_above(window, TRUE);
    gtk_window_set_skip_taskbar_hint(window, TRUE);
    gtk_window_set_skip_pager_hint(window, TRUE);
    gtk_window_set_accept_focus(window, FALSE);
    gtk_window_set_focus_on_map(window, FALSE);
    gtk_window_set_type_hint(window, GDK_WINDOW_TYPE_HINT_UTILITY);
    gtk_window_stick(window);
    gtk_widget_set_app_paintable(lyric.window, TRUE);
    GdkVisual *visual = gdk_screen_get_rgba_visual(gdk_screen_get_default());
    if (visual) gtk_widget_set_visual(lyric.window, visual);
    gtk_widget_add_events(lyric.window, GDK_BUTTON_PRESS_MASK | GDK_BUTTON_RELEASE_MASK | GDK_POINTER_MOTION_MASK | GDK_ENTER_NOTIFY_MASK | GDK_LEAVE_NOTIFY_MASK | GDK_SCROLL_MASK | GDK_SMOOTH_SCROLL_MASK);
    g_signal_connect(lyric.window, "draw", G_CALLBACK(lyricDraw), NULL);
    g_signal_connect(lyric.window, "configure-event", G_CALLBACK(lyricConfigure), NULL);
    g_signal_connect(lyric.window, "enter-notify-event", G_CALLBACK(lyricCross), NULL);
    g_signal_connect(lyric.window, "leave-notify-event", G_CALLBACK(lyricCross), NULL);
    g_signal_connect(lyric.window, "motion-notify-event", G_CALLBACK(lyricMotion), NULL);
    g_signal_connect(lyric.window, "button-press-event", G_CALLBACK(lyricButton), NULL);
    g_signal_connect(lyric.window, "button-release-event", G_CALLBACK(lyricButton), NULL);
    g_signal_connect(lyric.window, "scroll-event", G_CALLBACK(lyricScroll), NULL);
    g_signal_connect(lyric.window, "delete-event", G_CALLBACK(gtk_true), NULL);
}
static void lyricCommand(char **f, int count) {
    // opts color stroke strokeColor size width center showTrans transScale compact family | pos x y | show 0|1
    // | play playing mode | line main sub position rate {start end text}…
    if (count < 1 || shuttingDown) return;
    if (!strcmp(f[0], "opts") && count >= 11) {
        parseColor(f[1], lyric.color); lyric.stroke = f[2][0] == '1'; parseColor(f[3], lyric.strokeColor);
        lyric.size = atof(f[4]) > 0 ? atof(f[4]) : 30; lyric.width = atof(f[5]) > 0 ? atof(f[5]) : 800;
        lyric.center = f[6][0] == '1'; lyric.showTrans = f[7][0] == '1'; lyric.transScale = atof(f[8]) > 0 ? atof(f[8]) : .6; lyric.compact = f[9][0] == '1';
        g_free(lyric.family); lyric.family = g_strdup(f[10]);
        placeLyric();
    } else if (!strcmp(f[0], "pos") && count >= 3) {
        lyric.x = atoi(f[1]); lyric.y = atoi(f[2]); lyric.placed = 1;
        if (lyric.window) gtk_window_move(GTK_WINDOW(lyric.window), lyric.x, lyric.y);
    } else if (!strcmp(f[0], "show") && count >= 2) {
        lyric.visible = f[1][0] == '1';
        if (lyric.visible) { createLyricWindow(); placeLyric(); gtk_widget_show(lyric.window); gtk_window_move(GTK_WINDOW(lyric.window), lyric.x, lyric.y); }
        else if (lyric.window) { gtk_widget_hide(lyric.window); lyric.hover = 0; lyric.hoverControl = -1; }
    } else if (!strcmp(f[0], "play") && count >= 3) {
        lyric.playing = f[1][0] == '1'; lyric.mode = atoi(f[2]);
    } else if (!strcmp(f[0], "line") && count >= 5) {
        g_free(lyric.main); g_free(lyric.sub);
        lyric.main = g_strdup(f[1]); lyric.sub = g_strdup(f[2]);
        lyric.position = atof(f[3]); lyric.rate = atof(f[4]); lyric.positionAt = g_get_monotonic_time();
        for (int i = 0; i < lyric.wordCount; i++) g_free(lyric.words[i].text);
        g_free(lyric.words);
        lyric.wordCount = (count - 5) / 3;
        lyric.words = lyric.wordCount ? g_new0(LyricWord, lyric.wordCount) : NULL;
        for (int i = 0; i < lyric.wordCount; i++) lyric.words[i] = (LyricWord){ atof(f[5 + i * 3]), atof(f[6 + i * 3]), g_strdup(f[7 + i * 3]) };
    }
    lyricRefresh();
}

// ---------- commands ----------

static void hostAction(const char *action) {
    if (!strcmp(action, "destroy")) { destroyHost(); return; }
    if (shuttingDown || !mainWindow) return;
    if (!strcmp(action, "show")) { mainReady = 1; showMain(); }
    else if (!strcmp(action, "hide")) gtk_widget_hide(mainWindow);
    else if (!strcmp(action, "pageReady")) { if (!mainReady) { mainReady = 1; showMain(); } }
    else if (!strcmp(action, "pageLoading")) { mainReady = 0; gtk_widget_hide(mainWindow); g_timeout_add(8000, showAnyway, NULL); }
    else if (!strcmp(action, "minimize")) gtk_window_iconify(GTK_WINDOW(mainWindow));
    else if (!strcmp(action, "maximize")) toggleMaximize(mainWindow);
    else if (!strcmp(action, "sleepShow")) showSleepWindow();
    else if (!strcmp(action, "sleepHide")) closeSleepWindow();
    else if (!strcmp(action, "sleepPageReady")) revealSleepWindow();
}
// One command line of the Go core; the line is modified in place.
static void hostCommand(char *line) {
    GPtrArray *fields = g_ptr_array_new();
    for (char *field = line; field;) {
        char *end = strchr(field, '\t');
        if (end) *end = 0;
        char *out = field;
        for (char *in = field; *in; in++) {
            if (*in == '\\' && in[1]) { in++; *out++ = *in == 't' ? '\t' : *in == 'n' ? '\n' : *in == 'r' ? '\r' : *in; }
            else *out++ = *in;
        }
        *out = 0;
        g_ptr_array_add(fields, field);
        field = end ? end + 1 : NULL;
    }
    char **f = (char **)fields->pdata;
    int count = (int)fields->len;
    const char *command = f[0];
    if (count >= 2 && !strcmp(command, "host")) hostAction(f[1]);
    else if (count < 2 || shuttingDown) {}
    else if (!strcmp(command, "menu")) menuCommand(f + 1, count - 1);
    else if (!strcmp(command, "toast")) showToast(f[1], count > 2 ? f[2] : "");
    else if (!strcmp(command, "lyric")) lyricCommand(f + 1, count - 1);
    else if (!strcmp(command, "create")) createView(f + 1, count - 1);
    else if (!strncmp(command, "cookie", 6)) cookieCommand(command, f + 1, count - 1);
    else {
        Webview *view = findView(f[1]);
        if (!view || !view->browser || view->closing) {}
        else if (!strcmp(command, "close")) closeView(view);
        else if (!strcmp(command, "reload")) view->browser->reload(view->browser);
        else if (!strcmp(command, "eval") && count >= 3) runScript(view->browser, f[2]);
        else if (!strcmp(command, "load") && count >= 3) {
            cef_string_t value = make(f[2]);
            cef_frame_t *frame = view->browser->get_main_frame(view->browser);
            if (frame) { frame->load_url(frame, &value); releaseCef(frame); }
            drop(&value);
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
                slidePanel(view, wasShown);
                // The keyboard follows the page seen: to the play page as it comes, back to the host page as it goes.
                focusBrowser(view->shown ? view->browser : mainBrowser);
            }
            layoutPanel(view);
            updateInput();
        }
        else if (!strcmp(command, "fullscreen") && count >= 3) { if (view->kind == kindPanel && (view->flags & viewCover)) setMainFullscreen(f[2][0] == '1'); }
        else if (!strcmp(command, "win") && count >= 5) windowCommand(view, f[2], f[3], f[4]);
    }
    g_ptr_array_free(fields, TRUE);
}
// Commands arrive on standard input, one line each, and are handled by the main loop.
static gboolean readCommands(GIOChannel *channel, GIOCondition condition, gpointer data) {
    for (;;) {
        gchar *line = NULL;
        gsize end = 0;
        GIOStatus status = g_io_channel_read_line(channel, &line, NULL, &end, NULL);
        if (status == G_IO_STATUS_NORMAL && line) { line[end] = 0; hostCommand(line); g_free(line); continue; }
        g_free(line);
        // The Go core is gone: nothing is left to serve.
        if (status == G_IO_STATUS_EOF || status == G_IO_STATUS_ERROR) { destroyHost(); return G_SOURCE_REMOVE; }
        return G_SOURCE_CONTINUE;
    }
}

// ---------- render process ----------

// Runs before the page's own scripts. The page gets simHost: postMessage, onmessage and the
// message event. The native function stays inside this closure.
static const char bootstrap[] = "(function(native){"
    "if(window.simHost)return;"
    "var target=new EventTarget(),handler=null,running={};"
    "var send=function(kind,value){var text;try{text=JSON.stringify(value===undefined?null:value)}catch(error){throw new TypeError('simHost: value is not JSON-serializable')}native(kind,text===undefined?'null':text)};"
    "var api={postMessage:function(data){send('m',data)},"
    "addEventListener:function(){return target.addEventListener.apply(target,arguments)},"
    "removeEventListener:function(){return target.removeEventListener.apply(target,arguments)}};"
    "Object.defineProperty(api,'onmessage',{enumerable:true,get:function(){return handler},set:function(value){handler=typeof value==='function'?value:null}});"
    "target.addEventListener('message',function(event){if(handler)handler.call(api,event)});"
    "Object.defineProperty(window,'simHost',{value:Object.freeze(api)});"
    "var reply=function(id,value,error){try{send('r',error===undefined?{id:id,value:value}:{id:id,error:error})}catch(failure){native('r',JSON.stringify({id:id,error:'result is not JSON-serializable'}))}};"
    "Object.defineProperty(window,'__simHostReceive',{value:function(kind,a,b){"
    "if(kind==='m')target.dispatchEvent(new MessageEvent('message',{data:JSON.parse(a)}));"
    "else if(kind==='e'){running[a]=1;Promise.resolve().then(b).then(function(value){reply(a,value)},function(error){reply(a,null,String(error&&error.message||error))})}"
    "else if(kind==='c'){if(!running[a])reply(a,null,'script could not be parsed');delete running[a]}"
    "}});})";
static int rendererViews[256], rendererViewCount;
static void CEF_CALLBACK rendererBrowserCreated(cef_render_process_handler_t *self, cef_browser_t *browser, cef_dictionary_value_t *extra) {
    cef_string_t key = make("simWebview");
    if (extra && extra->has_key(extra, &key) && rendererViewCount < 256) rendererViews[rendererViewCount++] = browser->get_identifier(browser);
    drop(&key);
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
        cef_v8context_t *context = cef_v8context_get_current_context();
        cef_frame_t *frame = context ? context->get_frame(context) : NULL;
        if (frame) {
            cef_string_t channel = make("simHost"), empty = make("");
            cef_process_message_t *message = cef_process_message_create(&channel);
            cef_list_value_t *list = message->get_argument_list(message);
            for (int i = 0; i < 2; i++) {
                cef_string_userfree_t text = arguments[i]->get_string_value(arguments[i]);
                list->set_string(list, i, text ? text : &empty);
                if (text) cef_string_userfree_utf16_free(text);
            }
            releaseCef(list);
            frame->send_process_message(frame, PID_BROWSER, message);
            releaseCef(frame);
            drop(&channel); drop(&empty);
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
        cef_string_t code = make(bootstrap), source = make("simhost://bootstrap"), name = make("simHostNative");
        cef_v8value_t *setup = NULL;
        cef_v8exception_t *exception = NULL;
        if (context->eval(context, &code, &source, 1, &setup, &exception) && setup) {
            cef_v8value_t *arguments[1] = { cef_v8value_create_function(&name, &nativeHandler) };
            releaseCef(setup->execute_function(setup, NULL, 1, arguments));
        }
        releaseCef(setup); releaseCef(exception);
        drop(&code); drop(&source); drop(&name);
    }
    releaseCef(context); releaseCef(frame); releaseCef(browser);
}
static cef_render_process_handler_t *CEF_CALLBACK getRenderer(cef_app_t *self) { return &rendererHandler; }

// ---------- start ----------

static void initHandlers(void) {
    initBase(&app.base, sizeof(app));
    initBase(&client.base, sizeof(client)); initBase(&lifeHandler.base, sizeof(lifeHandler));
    initBase(&dragHandler.base, sizeof(dragHandler)); initBase(&requestHandler.base, sizeof(requestHandler));
    initBase(&menuHandler.base, sizeof(menuHandler)); initBase(&displayHandler.base, sizeof(displayHandler));
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
// The host only runs as a child of the program it belongs to: SimMusicNext, one directory above this one.
static int launchedByMain(void) {
    char own[PATH_MAX], parent[PATH_MAX], link[64];
    ssize_t length = readlink("/proc/self/exe", own, sizeof(own) - 1);
    if (length <= 0) return 0;
    own[length] = 0;
    snprintf(link, sizeof(link), "/proc/%d/exe", (int)getppid());
    length = readlink(link, parent, sizeof(parent) - 1);
    if (length <= 0) return 0;
    parent[length] = 0;
    char *runtime = g_path_get_dirname(own), *root = g_path_get_dirname(runtime);
    char *expected = g_build_filename(root, "SimMusicNext", NULL);
    int valid = !strcmp(parent, expected);
    g_free(runtime); g_free(root); g_free(expected);
    return valid;
}
// Chromium and GTK both talk to the X server; an error of one request must not end the process.
static int xError(Display *display, XErrorEvent *event) { return 0; }
static int xIoError(Display *display) { return 0; }
int main(int argc, char **argv) {
    char *cache = NULL, *logPath = NULL;
    int subprocess = 0;
    for (int i = 1; i < argc; i++) {
        if (!strncmp(argv[i], "--type=", 7)) subprocess = 1;
        if (!strncmp(argv[i], "--simmusic-url=", 15)) startUrl = argv[i] + 15;
        if (!strncmp(argv[i], "--simmusic-cache=", 17)) cache = argv[i] + 17;
        if (!strncmp(argv[i], "--simmusic-log=", 15)) logPath = argv[i] + 15;
        if (!strncmp(argv[i], "--simmusic-icon=", 16)) iconPath = argv[i] + 16;
        if (!strncmp(argv[i], "--simmusic-icon-font=", 21)) iconFont = argv[i] + 21;
    }
    if (!subprocess && (!startUrl || !cache || !launchedByMain())) return 0;
    if (strcmp(cef_api_hash(0), CEF_API_HASH_PLATFORM)) { fprintf(stderr, "CEF API hash mismatch\n"); return 3; }
    initHandlers();
    cef_main_args_t args = { .argc = argc, .argv = argv };
    int result = cef_execute_process(&args, &app, NULL);
    if (result >= 0) return result;
    cef_settings_t settings = { .size = sizeof(settings), .no_sandbox = 1, .windowless_rendering_enabled = 1, .background_color = 0 };
    settings.root_cache_path = make(cache);
    settings.cache_path = make(cache);
    if (logPath) settings.log_file = make(logPath);
    settings.log_severity = LOGSEVERITY_WARNING;
    settings.locale = make("zh-CN");
    if (!cef_initialize(&args, &settings, &app, NULL)) return 5;
    // GTK is started after CEF, as Chromium's start-up wants a single thread, and is held to X11: the
    // windows are placed, shaped and parented through it.
    gdk_set_allowed_backends("x11");
    gdk_set_program_class("SimMusic.Next");
    gtk_init(NULL, NULL);
    xd = GDK_DISPLAY_XDISPLAY(gdk_display_get_default());
    XSetErrorHandler(xError);
    XSetIOErrorHandler(xIoError);
    // The glyphs of the menus and of the desktop lyrics' controls come from the icon font of the interface.
    if (iconFont) FcConfigAppFontAddFile(FcConfigGetCurrent(), (const FcChar8 *)iconFont);
    if (iconPath) gtk_window_set_default_icon_from_file(iconPath, NULL);
    createMainWindow();
    GIOChannel *input = g_io_channel_unix_new(STDIN_FILENO);
    g_io_channel_set_encoding(input, NULL, NULL);
    g_io_add_watch(input, G_IO_IN | G_IO_HUP | G_IO_ERR, readCommands, NULL);
    cef_run_message_loop();
    releasePartitions();
    cef_shutdown();
    if (mainDrag) cairo_region_destroy(mainDrag);
    return 0;
}
