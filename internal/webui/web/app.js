const icons = { home: "EF83", search: "F0D1", downloads: "EC5A", plugins: "F450", settings: "F0EE", folder: "ED6A", playlist: "F011", about: "EE59" };
const icon = code => `<i>${String.fromCodePoint(parseInt(code, 16))}</i>`;
const shownPrompts = new Set();
const escapeText = value => su5.utils.formatText(value ?? "");
const trackKey = track => `${track.source}\0${track.id}`;
const updateArtist = (element, value, inList = false) => {
    const artist = value && value !== "未知艺术家" && value.toLowerCase() !== "unknown artist" ? value : "";
    element.textContent = artist || (inList ? "—" : "");
    element.hidden = !inList && !artist;
    element.classList.toggle("unknownArtist", inList && !artist);
};
const coverUrl = (track, scene = "list") => track && (track.infoReady || track.cover) ? `cover?key=${encodeURIComponent(trackKey(track))}&scene=${scene}&rev=${track.infoRevision || 0}` : "assets/placeholder.png";
const formatTime = seconds => {
    const value = Math.max(0, Math.floor(seconds || 0));
    return value >= 3600 ? `${Math.floor(value / 3600)}:${String(Math.floor(value / 60) % 60).padStart(2, "0")}:${String(value % 60).padStart(2, "0")}` : `${Math.floor(value / 60)}:${String(value % 60).padStart(2, "0")}`;
};
const musicListSummary = tracks => {
    const known = tracks.filter(track => track.metadataReady || track.infoReady);
    const duration = formatTime(known.reduce((sum, track) => sum + (track.duration || 0), 0));
    const time = known.length === tracks.length ? duration : known.length ? `已索引 ${known.length} 首 · ${duration}` : "时长待索引";
    return `${icon("EF83")} ${tracks.length} 首歌曲　${icon("F20F")} ${time}`;
};
let catalog = { plugins: [], playlists: [], config: {} };
let player = { Queue: [], Lyrics: [], Pos: 0, Dur: 0, Volume: .8, Index: -1 };
let page = { kind: "home" };
let pageGeneration = 0;
let viewTracks = [];
let selectedKeys = new Set();
let sortKey = "";
let sortDirection = 1;
let tableTracks = [];
let tableStart = -1;
let tableFrame = 0;
let lastCurrentKey = "";
let displayedSongKey = "";
let trackDisplayGeneration = 0;
let trackDisplayPending = false;
let trackCoverImages = [];
let lyricsSignature = "";
let queueSignature = "";
let catalogSignature = "";
let activeLyric = -1;
let lyricsManualTimer;
let downloadSignature = "";
let previousVolume = .8;
let speed = 1;
let sleepState = {};
let sleepWaitSince = 0;
// The search page keeps what was typed, chosen and found while other pages are shown.
const searchState = { input: "", source: "", text: "", tracks: null, scroll: 0, busy: false, token: 0 };
let highlightRegex = null;
let downloads = [];
let lyricsShown = true;
let dragActive = false;
let dragRefresh = null;
// An extension's own page shown over the content area: a separate browser of the host, not part of
// this document. Only its place is managed here.
let shownPanel = "";
let panelSignature = "";
let menuOpening = 0;
let panelDrift = null;

const api = async (action, data = {}) => {
    const response = await fetch("api", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ action, ...data }) });
    const payload = await response.json();
    if (!response.ok) throw new Error(payload.error || "操作失败");
    return payload.result;
};
const run = async (action, data = {}) => {
    try {
        const result = await api(action, data);
        if (["reloadPlugin", "removePlugin", "pluginEnabled", "pluginOrder", "installPlugin", "clearIndex"].includes(action) || action === "pluginPermission" && data.key === "scheme.extend") songInfo.invalidate();
        return result;
    }
    catch (error) { showError("操作失败", error); return null; }
};
// SimUI renders dialog text that begins with "html:" as markup. Extension text, song and playlist
// names reach these dialogs and nothing here relies on markup, so the text is always shown literally:
// such text is passed on escaped, with its line breaks kept.
["alert", "confirm"].forEach(kind => {
    const show = su5.dialog[kind];
    su5.dialog[kind] = config => {
        config = typeof config === "string" ? { text: config } : { ...config };
        config.text = String(config.text ?? "");
        if (config.text.startsWith("html:")) config.text = `html:${escapeText(config.text).replace(/\n/g, "<br>")}`;
        ["buttonHtml", "cancelHtml"].forEach(key => delete config[key]);
        return show(config);
    };
});
// Toasts carry short fixed text only. Anything of unbounded length (errors, names,
// extension text) is shown in a dialog, one at a time, with the detail on its own line.
const alertQueue = [];
const showAlert = async text => {
    if (alertQueue.includes(text)) return;
    alertQueue.push(text);
    if (alertQueue.length > 1) return;
    while (alertQueue.length) { await su5.dialog.alert(alertQueue[0]); alertQueue.shift(); }
};
const showError = (summary, error) => showAlert(`${summary}：\n${error.message}`);
const refreshCatalog = async () => {
    const next = await api("catalog");
    const signature = JSON.stringify(next);
    if (signature === catalogSignature) return;
    catalogSignature = signature;
    catalog = next;
    catalog.plugins ??= [];
    catalog.playlists ??= [];
    renderNavigation();
    applyConfig();
    if (page.kind === "menu" && !catalog.plugins.some(plugin => plugin.manifest.id === page.plugin && plugin.sidebar?.items?.some(item => item.id === page.id))) await navigate({ kind: "home" });
    if (page.kind === "plugins" && !dragActive) renderPlugins();
};
const applyConfig = () => {
    const config = catalog.config;
    const classes = { playBtnColor: "list.themedButton", themeBlur: "list.themeImageBlur", darkPlayer: "play.dark", albumScale: "play.coverScale", threeEffect: "play.effect3D", themeImage: "list.themeImage", blurLyrics: "lrc.blur" };
    Object.entries(classes).forEach(([className, key]) => document.body.classList.toggle(className, key === "lrc.align" ? config[key] === "center" : !!config[key]));
    document.body.classList.toggle("staticBackdrop", !config["play.dynamicBg"]);
    // The family is quoted for CSS; names come from the system's own font list.
    const appFont = String(config["general.font"] || "").replace(/["\\]/g, "");
    if (appFont) simCss(document.documentElement).setProperty("--appFont", `"${appFont}"`); else simCss(document.documentElement).removeProperty("--appFont");
    simCss(document.documentElement).setProperty("--sideWidth", `${Math.min(360, Math.max(150, config["list.leftBarWidth"] || 200))}px`);
    simCss(lyricsView).setProperty("--lyricSize", `${(config["lrc.size"] || 1.5) * 24}px`);
    simCss(lyricsView).setProperty("--lineSpace", `${config["lrc.spacing"] ?? .5}em`);
    simCss(lyricsView).setProperty("--lrcTranslation", `${config["lrc.transSize"] ?? .8}em`);
    simCss(lyricsView).setProperty("--align", config["lrc.align"] === "center" ? "center" : "left");
    updateBackdrop();
};
const updateBackdrop = () => {
    const config = catalog.config;
    const url = config["list.themeImageType"] === "local" ? "theme" : coverUrl(player.Current);
    simCss(themeBackdrop).backgroundImage = config["list.themeImage"] ? `url("${url}")` : "none";
};
// Apply every drag locally; serialize writes and keep only the latest waiting value.
const queueValue = send => {
    let pending;
    let sending;
    return value => {
        pending = value;
        if (!sending) sending = (async () => {
            while (pending !== undefined) {
                const value = pending;
                pending = undefined;
                await send(value);
            }
        })().finally(() => sending = null);
        return sending;
    };
};
const configUpdates = new Map();
const setConfig = (key, value) => {
    catalog.config[key] = value;
    applyConfig();
    updateSettingDependencies();
    if (!configUpdates.has(key)) configUpdates.set(key, queueValue(value => run("config", { key, value })));
    return configUpdates.get(key)(value);
};

const navButton = (label, code, target) => {
    const button = document.createElement("button");
    button.className = "navItem";
    button.innerHTML = `${code ? icon(code) : ""}<span>${escapeText(label)}</span>`;
    button.navTarget = JSON.stringify(target);
    button.classList.toggle("active", button.navTarget === JSON.stringify(page));
    button.onclick = () => navigate(target);
    sidebarNav.append(button);
    return button;
};
const navGroup = (title, addAction) => {
    const group = document.createElement("div");
    group.className = "sideGroup";
    group.innerHTML = `<span>${escapeText(title)}</span>`;
    if (addAction) {
        const button = document.createElement("button");
        button.className = "quietButton";
        button.innerHTML = icon("EA13");
        button.title = `添加${title}`;
        button.onclick = addAction;
        group.append(button);
    }
    sidebarNav.append(group);
};
const renderNavigation = () => {
    sidebarNav.replaceChildren();
    // An extension's section is its own: the title, the entries, what a click opens and the
    // context menu all come from the extension. The host only draws them and passes events on.
    catalog.plugins.filter(plugin => plugin.sidebar).forEach(plugin => {
        const id = plugin.manifest.id;
        navGroup(plugin.sidebar.title, plugin.sidebar.canAdd ? () => run("sidebarAdd", { plugin: id }) : null);
        (plugin.sidebar.items || []).forEach(item => {
            const button = navButton(item.name, "", { kind: "menu", plugin: id, id: item.id });
            button.oncontextmenu = async event => {
                event.preventDefault();
                const actions = await run("sidebarMenu", { plugin: id, menu: item.id });
                const items = (actions || []).map(action => action.separator ? { separator: true } : { label: action.label, icon: action.icon, action: () => run("sidebarMenuAction", { plugin: id, menu: item.id, key: action.key }) });
                if (items.length) showContext(items, event.clientX, event.clientY);
            };
        });
    });
    navGroup("歌单", createPlaylist);
    catalog.playlists.forEach(playlist => {
        const target = { kind: "playlist", id: playlist.id };
        const button = navButton(playlist.name, "", target);
        button.oncontextmenu = event => {
            event.preventDefault();
            showContext([
                { label: "重命名", action: async () => { const name = await askName("重命名歌单", playlist.name); if (name) { await run("renamePlaylist", { id: playlist.id, text: name }); await refreshCatalog(); if (page.id === playlist.id) navigate(target); } } },
                { label: "删除歌单", action: async () => { if (!await su5.dialog.confirm(`删除「${playlist.name}」？`)) return; await run("deletePlaylist", { id: playlist.id }); await refreshCatalog(); navigate({ kind: "home" }); } }
            ], event.clientX, event.clientY);
        };
    });
    navGroup("功能");
    navButton("搜索", icons.search, { kind: "search" });
    // Always present, folded away while there are no tasks, so it can animate in and out.
    const downloadButton = navButton("下载", icons.downloads, { kind: "downloads" });
    downloadButton.id = "downloadNav";
    downloadButton.classList.add("collapsible");
    downloadButton.insertAdjacentHTML("beforeend", `<span id="downloadBadge" class="navBadge"></span>`);
    updateDownloadNav();
    navButton("扩展", icons.plugins, { kind: "plugins" });
    navButton("设置", icons.settings, { kind: "settings" });
    navButton("关于", icons.about, { kind: "about" });
};
// The badge counts the tasks still running, and only while the download page is not the one shown.
const updateDownloadNav = () => {
    if (!window.downloadNav) return;
    downloadNav.classList.toggle("collapsed", !downloads.length);
    const running = downloads.filter(task => task.status === "pending" || task.status === "download").length;
    if (running) downloadBadge.textContent = running;
    downloadBadge.classList.toggle("shown", running > 0 && page.kind !== "downloads");
};
const askName = async (title, initial = "") => {
    const result = await su5.dialog.prompt({ title, items: [{ type: "input", name: "name", placeholder: "歌单名称", value: initial }] });
    return result?.name?.trim();
};
const createPlaylist = async () => {
    const name = await askName("新建歌单");
    if (!name) return;
    const id = await run("createPlaylist", { text: name });
    await refreshCatalog();
    if (id) navigate({ kind: "playlist", id });
    return id;
};
const pageHeader = (title, description = "", code = "") => `<div class="pageHeader"><div><h1>${code ? icon(code) : ""}${escapeText(title)}</h1><p>${escapeText(description)}</p></div></div>`;
const emptyState = () => `<div class="emptyState">${icon(icons.home)}</div>`;

// The panel covers the content area from the top of the window down to the player bar. It is a native
// window above this page, so it is hidden under whatever fills the window and cut around what stays
// above it: the window buttons, and the menus and notices that reach into it.
const layoutPanel = () => {
    if (!shownPanel) return;
    requestAnimationFrame(layoutPanel);
    const content = mainContent.getBoundingClientRect();
    const box = element => { const rect = element.getBoundingClientRect(); return [Math.floor(rect.left), Math.floor(rect.top), Math.ceil(rect.width), Math.ceil(rect.height)]; };
    const value = {
        left: Math.ceil(content.left) + 1, top: 0, right: 0, bottom: Math.max(0, Math.ceil(innerHeight - playerBar.getBoundingClientRect().top)),
        visible: playPage.hidden && !document.querySelector("dialog[open]") && !document.fullscreenElement,
        holes: [...document.querySelectorAll(".titleActions, .dropdown.active, .contextSubmenu.active, #SimsvToastContainer > div, .dragGhost, #connectionBanner")].map(box).filter(rect => rect[2] > 0 && rect[3] > 0)
    };
    const signature = JSON.stringify(value);
    if (signature === panelSignature) return;
    panelSignature = signature;
    api("panelLayout", { id: shownPanel, value }).catch(() => {});
};
const showPanel = id => {
    shownPanel = id;
    panelSignature = "";
    mainContent.replaceChildren();
    layoutPanel();
};
const navigate = async target => {
    // Leaving the panel closes it; the extension is told through its close event.
    if (shownPanel && !(target.kind === "panel" && target.id === shownPanel)) { api("panelClose", { id: shownPanel }).catch(() => {}); shownPanel = ""; }
    clearInterval(locateTimer);
    cancelAnimationFrame(tableFrame);
    tableFrame = 0;
    if (page.kind === "search" && window.searchInput) Object.assign(searchState, { input: searchInput.value, source: searchSourceSelect.value, scroll: trackList.parentElement.scrollTop });
    const previous = page;
    page = target;
    const generation = ++pageGeneration;
    highlightRegex = null;
    viewTracks = [];
    selectedKeys.clear();
    sortKey = "";
    mainContent.scrollTop = 0;
    // Existing items only change state, so the highlight fades between them.
    sidebarNav.querySelectorAll(".navItem").forEach(button => button.classList.toggle("active", button.navTarget === JSON.stringify(page)));
    updateDownloadNav();
    if (page.kind === "home") {
        mainContent.innerHTML = `<div id="rightPlaceholder"><img src="assets/icon-grey.svg"></div>`;
        return;
    }
    if (page.kind === "panel") { showPanel(page.id); return; }
    if (page.kind === "about") { renderAbout(); return; }
    if (page.kind === "search") { renderSearch(); return; }
    if (page.kind === "settings") { await renderSettings(); return; }
    if (page.kind === "plugins") { renderPlugins(); return; }
    if (page.kind === "downloads") { mainContent.replaceChildren(); renderDownloads(); return; }
    if (page.kind === "playlist") {
        const playlist = catalog.playlists.find(item => item.id === page.id);
        if (!playlist) { mainContent.innerHTML = emptyState(); return; }
        viewTracks = playlist.tracks || [];
        renderTrackPage(playlist.name);
        return;
    }
    if (page.kind === "menu") {
        const name = catalog.plugins.find(item => item.manifest.id === page.plugin)?.sidebar?.items?.find(item => item.id === page.id)?.name || "";
        mainContent.innerHTML = pageHeader(name) + emptyState();
        menuOpening++;
        const list = await run("sidebarOpen", { plugin: page.plugin, menu: page.id });
        menuOpening--;
        if (generation !== pageGeneration) return;
        // The extension shows a page of its own for this entry.
        if (list?.panel) { showPanel(list.panel); return; }
        // The extension handled the click itself, or failed: there is no list to show here.
        if (!list) { navigate(JSON.stringify(previous) === JSON.stringify(page) ? { kind: "home" } : previous); return; }
        viewTracks = list.tracks || [];
        renderTrackPage(list.title || name, list.details);
    }
};

// Reordering by pointer position, without browser drag events: a floating copy follows the
// pointer, the rows between origin and target shift by one step, and the list scrolls near its edges.
const startDragSort = ({ event, element, index, count, scroller, items, step, edges = [0, 0], commit }) => {
    const grabbed = element.getBoundingClientRect();
    const grip = event.clientY - grabbed.top;
    const origin = grabbed.top + scroller.scrollTop - index * step;
    let pointerY = event.clientY;
    let target = index;
    let frame = 0;
    const copy = element.cloneNode(true);
    let ghost = copy;
    if (element.tagName === "TR") {
        // A table row needs a table around it; fixed layout takes the column widths from its cells.
        ghost = document.createElement("table");
        ghost.className = element.closest("table").className;
        ghost.append(document.createElement("tbody"));
        ghost.firstChild.append(copy);
        [...copy.children].forEach((cell, column) => simCss(cell).width = `${element.children[column].getBoundingClientRect().width}px`);
    }
    ghost.classList.add("dragGhost");
    simCss(ghost).left = `${grabbed.left}px`;
    simCss(ghost).width = `${grabbed.width}px`;
    simCss(scroller).setProperty("--dragStep", `${step}px`);
    document.body.append(ghost);
    const update = () => {
        simCss(ghost).top = `${pointerY - grip}px`;
        target = Math.max(0, Math.min(count - 1, Math.round((pointerY - grip + scroller.scrollTop - origin) / step)));
        items().forEach(([node, position]) => {
            node.classList.toggle("dragSource", position === index);
            node.classList.toggle("dragUp", position > index && position <= target);
            node.classList.toggle("dragDown", position < index && position >= target);
        });
    };
    const scroll = () => {
        const bounds = scroller.getBoundingClientRect();
        const top = bounds.top + edges[0] + 40, bottom = bounds.bottom - edges[1] - 40;
        const speed = pointerY < top ? -Math.min(18, (top - pointerY) / 3) : pointerY > bottom ? Math.min(18, (pointerY - bottom) / 3) : 0;
        if (speed) { scroller.scrollTop += speed; update(); }
        frame = requestAnimationFrame(scroll);
    };
    const move = moved => { pointerY = moved.clientY; update(); };
    const swallow = clicked => { clicked.stopPropagation(); clicked.preventDefault(); };
    const finish = cancelled => {
        cancelAnimationFrame(frame);
        removeEventListener("pointermove", move);
        removeEventListener("pointerup", drop);
        removeEventListener("pointercancel", cancel);
        removeEventListener("blur", cancel);
        removeEventListener("keydown", escape, true);
        // The release must not also select or play the row underneath.
        addEventListener("click", swallow, true);
        setTimeout(() => removeEventListener("click", swallow, true));
        ghost.remove();
        scroller.classList.remove("dragSorting");
        items().forEach(([node]) => node.classList.remove("dragSource", "dragUp", "dragDown"));
        dragActive = false;
        dragRefresh = null;
        if (!cancelled && target !== index) commit(index, target);
    };
    const drop = () => finish(false);
    const cancel = () => finish(true);
    const escape = pressed => { if (pressed.key === "Escape") { pressed.stopPropagation(); cancel(); } };
    addEventListener("pointermove", move);
    addEventListener("pointerup", drop);
    addEventListener("pointercancel", cancel);
    addEventListener("blur", cancel);
    addEventListener("keydown", escape, true);
    dragActive = true;
    dragRefresh = update;
    update();
    // Shifts animate only after the first layout, so the origin row does not slide on pick-up.
    requestAnimationFrame(() => { if (dragActive) scroller.classList.add("dragSorting"); });
    frame = requestAnimationFrame(scroll);
};
// Rows keep their click and double click: a drag begins once the pressed pointer has moved.
const dragOnMove = (event, begin) => {
    if (event.button !== 0 || event.target.closest("button")) return;
    const stop = () => { removeEventListener("pointermove", move); removeEventListener("pointerup", stop); };
    const move = moved => { if (Math.abs(moved.clientY - event.clientY) < 6) return; stop(); begin(); };
    addEventListener("pointermove", move);
    addEventListener("pointerup", stop);
};
const moveInList = (list, from, to) => list.splice(to, 0, list.splice(from, 1)[0]);
// The line under the title belongs to whoever supplies the list: any number of { icon, text } entries, or none.
const listDetails = details => (details || []).filter(item => item?.text || item?.icon).map(item => `${/^[0-9A-Fa-f]{4,5}$/.test(item.icon || "") ? `${icon(item.icon)} ` : ""}${escapeText(item.text)}`).join("　");
const renderTrackPage = (title, details) => {
    mainContent.innerHTML = `<div id="musicListContainer"><div class="playlistHeader"><img src="${coverUrl(viewTracks[0])}"><section><h1>${escapeText(title)}</h1><div class="details">${listDetails(details) ? `${listDetails(details)}<br>` : ""}<span class="listSummary">${musicListSummary(viewTracks)}</span></div><div class="buttonGroup"><button id="playAllButton">${icon("F508")} 全部播放</button><button id="appendAllButton" class="sub squareButton" title="添加到播放列表">${icon("F00F")}</button></div></section></div><div class="tableContainer"><div id="trackList"></div></div>${catalog.config["list.showLocate"] ? `<button id="locateButton" class="iconButton locateButton inactive" title="定位当前曲目">${icon("F3B5")}</button>` : ""}</div>`;
    playAllButton.disabled = appendAllButton.disabled = !viewTracks.length;
    playAllButton.onclick = () => run("play", { tracks: viewTracks, index: 0 });
    appendAllButton.onclick = () => run("appendQueue", { tracks: viewTracks });
    if (window.locateButton) {
        locateButton.onclick = locateCurrent;
        trackList.parentElement.onwheel = () => { locateUsed = false; updateLocator(); };
    }
    locateUsed = false;
    renderTable();
};
const orderedTracks = () => sortKey ? [...viewTracks].sort((a, b) => sortDirection * (sortKey === "duration" ? a.duration - b.duration : String(a[sortKey]).localeCompare(String(b[sortKey]), "zh-CN"))) : viewTracks;
const renderTable = () => {
    if (!viewTracks.length) {
        tableTracks = [];
        trackList.parentElement.onscroll = null;
        trackList.innerHTML = emptyState();
        updateLocator();
        return;
    }
    tableTracks = orderedTracks();
    tableStart = -1;
    trackList.innerHTML = `<table class="trackTable"><thead><tr><th></th>${[["title", "曲目"], ["artist", "艺术家"], ["album", "专辑"], ["duration", "时长"]].map(([key, label]) => `<th><button class="sortButton" data-sort="${key}">${label}${sortKey === key ? sortDirection === 1 ? " ↑" : " ↓" : ""}</button></th>`).join("")}</tr></thead><tbody><tr class="trackSpacer"><td colspan="5"></td></tr><tr class="trackSpacer"><td colspan="5"></td></tr></tbody></table>`;
    trackList.querySelectorAll("[data-sort]").forEach(button => button.onclick = () => {
        if (sortKey !== button.dataset.sort) { sortKey = button.dataset.sort; sortDirection = 1; }
        else if (sortDirection === 1) sortDirection = -1;
        else sortKey = "";
        renderTable();
    });
    trackList.parentElement.onscroll = () => { if (!tableFrame) tableFrame = requestAnimationFrame(() => { tableFrame = 0; renderVisibleTracks(); }); };
    renderVisibleTracks();
    updateLocator();
};
const highlightRow = (row, track) => {
    if (!highlightRegex) return;
    const cells = row.children;
    setCellText(cells[1], track.title);
    if (!cells[2].classList.contains("unknownArtist")) setCellText(cells[2], track.artist);
    setCellText(cells[3], track.album);
};
const renderVisibleTracks = () => {
    const body = window.trackList?.querySelector("tbody");
    if (!body || !tableTracks.length) return;
    const scroller = trackList.parentElement;
    const start = Math.max(0, Math.floor((scroller.scrollTop - 31) / 49) - 10);
    if (start === tableStart) return;
    tableStart = start;
    const end = Math.min(tableTracks.length, start + Math.ceil(scroller.clientHeight / 49) + 20);
    const visibleTracks = tableTracks.slice(start, end);
    songInfo.hydrate(visibleTracks);
    const rows = new Map();
    body.querySelectorAll(".trackRow").forEach(row => {
        const index = Number(row.dataset.row);
        if (index < start || index >= end) row.remove();
        else rows.set(index, row);
    });
    const spacers = trackList.querySelectorAll(".trackSpacer td");
    simCss(spacers[0]).height = `${start * 49}px`;
    simCss(spacers[1]).height = `${(tableTracks.length - end) * 49}px`;
    let anchor = body.firstElementChild.nextSibling;
    visibleTracks.forEach((track, index) => {
        const position = start + index;
        let row = rows.get(position);
        if (row) { anchor = row.nextSibling; return; }
        row = document.createElement("tr");
        row.className = "trackRow";
        row.dataset.row = position;
        row.innerHTML = `<td><img loading="lazy" src="${coverUrl(track)}"></td><td title="${escapeText(track.title)}">${escapeText(track.title)}</td><td>${escapeText(track.artist)}</td><td>${escapeText(track.album)}</td><td>${formatTime(track.duration)}</td>`;
        updateArtist(row.children[2], track.artist, true);
        highlightRow(row, track);
        body.insertBefore(row, anchor);
        row.classList.toggle("selected", selectedKeys.has(trackKey(track)));
        row.classList.toggle("current", !!player.Current && trackKey(track) === trackKey(player.Current));
        row.onclick = event => {
            // A plain click on the only selected song takes the selection away again.
            const alone = selectedKeys.size === 1 && selectedKeys.has(trackKey(track));
            if (!event.ctrlKey && !event.shiftKey) selectedKeys.clear();
            if (alone && !event.ctrlKey && !event.shiftKey || event.ctrlKey && selectedKeys.has(trackKey(track))) selectedKeys.delete(trackKey(track));
            else selectedKeys.add(trackKey(track));
            updateRowStates();
        };
        row.ondblclick = () => run("play", { tracks: tableTracks, index: position });
        row.onpointerdown = event => {
            if (page.kind !== "playlist" || sortKey) return;
            dragOnMove(event, () => startDragSort({
                event, element: row, index: position, count: tableTracks.length, scroller: trackList.parentElement, step: 49, edges: [180, 80],
                items: () => [...trackList.querySelectorAll(".trackRow")].map(node => [node, Number(node.dataset.row)]),
                commit: (from, to) => { moveInList(viewTracks, from, to); renderTable(); run("movePlaylist", { id: page.id, index: from, to }); }
            }));
        };
        row.oncontextmenu = event => {
            event.preventDefault();
            if (!selectedKeys.has(trackKey(track))) { selectedKeys.clear(); selectedKeys.add(trackKey(track)); updateRowStates(); }
            showTrackContext(viewTracks.filter(item => selectedKeys.has(trackKey(item))), event.clientX, event.clientY);
        };
        anchor = row.nextSibling;
    });
    songInfo.table(visibleTracks);
    dragRefresh?.();
};
window.addEventListener("resize", () => { tableStart = -1; renderVisibleTracks(); if (!playPage.hidden) updateLyrics(true); });
const updateRowStates = () => {
    mainContent.querySelectorAll(".trackRow").forEach(row => {
        const track = tableTracks[Number(row.dataset.row)];
        row.classList.toggle("selected", selectedKeys.has(trackKey(track)));
        row.classList.toggle("current", !!player.Current && trackKey(track) === trackKey(player.Current));
    });
    updateLocator();
};
let locateTimer;
let locateUsed = false;
const currentRow = () => player.Current && window.trackList ? tableTracks.findIndex(track => trackKey(track) === trackKey(player.Current)) : -1;
// As in 2024: shown while the list holds the current track, hidden after use until the next wheel.
const updateLocator = () => window.locateButton?.classList.toggle("inactive", locateUsed || currentRow() < 0);
const locateCurrent = () => {
    const index = currentRow();
    if (index < 0) return;
    const scroller = trackList.parentElement;
    // Centre the row between the list header (180px) and the player bar (80px); 31px is the table head.
    const top = Math.max(0, Math.min(scroller.scrollHeight - scroller.clientHeight, 211 + index * 49 + 24.5 - (100 + scroller.clientHeight) / 2));
    locateUsed = true;
    updateLocator();
    scroller.scrollTo({ top, behavior: "smooth" });
    clearInterval(locateTimer);
    const started = performance.now();
    let last = -1;
    locateTimer = setInterval(() => {
        const moving = scroller.scrollTop !== last && Math.abs(scroller.scrollTop - top) >= 1;
        last = scroller.scrollTop;
        if (moving || performance.now() - started < 200) return;
        clearInterval(locateTimer);
        const row = trackList.querySelector(`[data-row="${index}"]`);
        row?.classList.add("located");
        setTimeout(() => row?.classList.remove("located"), 300);
    }, 50);
};
const showContext = simTrackMenu.show;
const playlistActions = tracks => [
    // A playlist that already holds every selected song is ticked; choosing it takes them out again.
    ...catalog.playlists.map(playlist => {
        const keys = new Set((playlist.tracks || []).map(trackKey));
        const included = tracks.every(track => keys.has(trackKey(track)));
        return { label: playlist.name, icon: included ? "EB7B" : "", action: async () => {
            if (included) await run("removePlaylist", { id: playlist.id, tracks });
            else { const count = await run("addPlaylist", { id: playlist.id, tracks }); if (count !== null) su5.toast(`已添加 ${count} 首曲目`); }
            await refreshCatalog();
            if (page.kind === "playlist" && page.id === playlist.id) navigate(page);
        } };
    }),
    { separator: true },
    { label: "新建歌单", icon: "EA13", action: async () => { const id = await createPlaylist(); if (id) { await run("addPlaylist", { id, tracks }); await refreshCatalog(); navigate({ kind: "playlist", id }); } } }
];
let trackContextGeneration = 0;
const showTrackContext = async (tracks, x, y, scene = "list") => {
    if (!tracks.length) return;
    const generation = ++trackContextGeneration;
    // Entries that would change nothing are left out: the playing song is not offered "play",
    // the playing or upcoming song not "play next", and queued songs not "add to queue".
    const queued = new Map(player.Queue.map((track, index) => [trackKey(track), index]));
    const only = tracks.length === 1 ? queued.get(trackKey(tracks[0])) : undefined;
    const isCurrent = only !== undefined && only === player.Index;
    const items = [];
    if (!isCurrent) items.push({ label: "播放", icon: "F00B", action: () => scene === "queue" && only !== undefined ? run("playIndex", { index: only }) : run("play", { tracks, index: 0 }) });
    if (!isCurrent && !(only !== undefined && player.Index >= 0 && only === player.Index + 1)) items.push({ label: "下一首播放", action: () => run("playNext", { tracks }) });
    if (!tracks.every(track => queued.has(trackKey(track)))) items.push({ label: "添加到播放队列", icon: "F00F", action: () => run("appendQueue", { tracks }) });
    items.push({ label: "添加到歌单", icon: icons.playlist, children: () => playlistActions(tracks) });
    if (tracks.every(track => catalog.schemes?.[track.source]?.canDownload)) items.push({ label: "下载曲目", icon: icons.downloads, action: () => run("download", { tracks }) });
    if (scene === "play") items.push(
        { separator: true },
        sleepMenuItem(),
        speedMenuItem(),
        { label: "均衡器…", icon: "EC9D", action: showEqualizer },
        { label: "输出设备", icon: "F2A2", children: async () => (await outputDevices()).map(({ value, label }) => ({ label, icon: (catalog.config["audio.device"] || "") === value ? "EB7B" : "", action: () => setConfig("audio.device", value) })) },
        { label: "清空播放列表", icon: "EC2A", action: () => clearQueueButton.click() },
        { label: "停止播放", icon: "F1A1", action: () => run("stop") }
    );
    if (scene === "list" && page.kind === "playlist") items.push({ label: "从歌单移除", icon: "EC2A", action: async () => { await run("removePlaylist", { id: page.id, tracks }); await refreshCatalog(); navigate(page); } });
    const pluginItems = actions => (actions || []).map(action => ({ label: action.label, icon: action.icon, children: action.dynamic ? async () => pluginItems(await api("trackActions", { tracks, scene, parent: action.key })) : action.children?.length ? pluginItems(action.children) : null, action: () => run("trackAction", { tracks, scene, key: action.key }) }));
    const actions = await run("trackActions", { tracks, scene });
    if (generation !== trackContextGeneration) return;
    if (actions?.length) items.push({ separator: true }, ...pluginItems(actions));
    items.push({ separator: true }, { label: "刷新索引缓存", icon: "ED0F", action: () => clearSongInfo(tracks, scene) });
    showContext(items, x, y);
};

// 2024 naturalSplit: the browser's word segmenter. Extensions get these next to the raw text.
const splitWords = text => [...new Intl.Segmenter("zh-CN", { granularity: "word" }).segment(text)].map(part => part.segment.trim().toLowerCase()).filter(Boolean);
const renderSearch = () => {
    mainContent.innerHTML = `<div id="musicListContainer"><div class="searchTitle"><form id="searchForm" class="inputGroup"><div class="input sourceSelect"><select id="searchSourceSelect"><option value="">全部音源</option>${catalog.plugins.filter(plugin => plugin.running && plugin.canSearch).map(plugin => `<option value="${escapeText(plugin.manifest.id)}">${escapeText(plugin.manifest.name)}</option>`).join("")}</select><i class="clickable"></i></div><div class="input allowEmpty searchField"><input id="searchInput" placeholder="输入搜索关键字，回车搜索 ..."></div><button id="searchSubmitButton" type="submit">${icon("F0D1")} 搜索</button></form></div><div class="tableContainer"><div id="trackList">${emptyState()}</div></div></div>`;
    // Coming back to the page restores the last search instead of starting over.
    searchInput.value = searchState.input;
    searchSourceSelect.value = searchState.source;
    if (searchSourceSelect.selectedIndex < 0) searchSourceSelect.value = searchState.source = "";
    searchForm.onsubmit = event => { event?.preventDefault(); submitSearch(); };
    // One source keeps its own order and all sources are ranked by the host, so a change searches again.
    searchSourceSelect.onchange = () => { if (searchState.text) submitSearch(); };
    searchForm.querySelectorAll(".input").forEach(element => su5.form.init(element));
    showSearchResults();
    trackList.parentElement.scrollTop = searchState.scroll;
    searchInput.select();
};
const submitSearch = async () => {
    const text = searchInput.value.trim();
    if (!text) return;
    const token = ++searchState.token;
    Object.assign(searchState, { input: searchInput.value, source: searchSourceSelect.value, text, tracks: null, scroll: 0, busy: true });
    showSearchResults();
    const tracks = await run("search", { text, words: splitWords(text), plugin: searchState.source });
    if (token !== searchState.token) return;
    searchState.busy = false;
    searchState.tracks = tracks || [];
    if (page.kind === "search") showSearchResults();
};
// 2024: the words of the search text are marked in the result rows. Latin words shorter than
// three letters only match as whole words; marks separated by one space join into one.
const highlightPattern = text => {
    const words = splitWords(text).map(word => word.length < 3 && /^[A-Za-z]+$/.test(word) ? `\\b${word}\\b` : word.replace(/[-\/\\^$*+?.()|[\]{}]/g, "\\$&"));
    return words.length ? new RegExp(`(?:${words.join("|")})(?: (?:${words.join("|")}))*`, "gi") : null;
};
const setCellText = (cell, text) => {
    cell.textContent = text;
    if (!highlightRegex || !text) return;
    const parts = [];
    let last = 0;
    for (const match of text.matchAll(highlightRegex)) {
        const mark = document.createElement("m");
        mark.textContent = match[0];
        parts.push(text.slice(last, match.index), mark);
        last = match.index + match[0].length;
    }
    if (parts.length) cell.replaceChildren(...parts, text.slice(last));
};
const showSearchResults = () => {
    highlightRegex = searchState.tracks ? highlightPattern(searchState.text) : null;
    searchSubmitButton.disabled = searchState.busy;
    viewTracks = searchState.tracks || [];
    selectedKeys.clear();
    renderTable();
};

const settingGroups = [
    ["通用配置", [["general.noBackground", "不驻留后台进程", "toggle", "关闭主界面时停止播放并完全退出应用。"], ["general.font", "界面字体", "select", "", [["", "微软雅黑"]]], ["clearIndex", "清理索引缓存", "button", "定期清理可保持 SimMusic 运行性能；若您更改了音频元数据，亦可在此清理缓存以重新读取。"]]],
    ["歌单界面", [["list.showLocate", "显示曲目定位按钮", "toggle"], ["list.themedButton", "对播放按钮应用主题色", "toggle"], ["list.themeImage", "启用主题图片", "toggle", "在 SimMusic 主界面显示主题图片。"], ["list.themeImageType", "主题图片来源", "select", "", [["cover", "曲目封面"], ["local", "本地文件"]]], ["list.themeImageBlur", "图片模糊效果", "toggle"], ["chooseTheme", "选择本地主题图片", "button"]]],
    ["播放界面", [["play.dark", "播放页深色模式", "toggle"], ["play.dynamicBg", "背景动态混色", "toggle", "关闭后可减少播放页对硬件资源的占用。"], ["play.effect3D", "3D 特效", "toggle", "在播放页的歌曲信息、播放列表与歌词视图使用 3D 视觉效果。"], ["play.coverScale", "专辑封面缩放", "toggle", "在按下「播放」或「暂停」时对播放页专辑封面使用缩放动效。"]]],
    ["播放控制", [["ctl.quickReplay", "快速重播", "toggle", "在曲目即将结束时按「快退」按钮以回到当前曲目的开头。"], ["ctl.fade", "音频淡入淡出", "toggle", "在按下「播放」或「暂停」时对音频的输出音量使用渐变效果。"], ["audio.device", "音频输出设备", "select", "", [["", "系统默认"]]], ["equalizer", "均衡器", "button"]]],
    ["睡眠定时", [["sleep.extendToEnd", "延长时间至曲目结尾", "toggle", "在曲目结尾后再暂停播放；在延长时间时执行暂停、切歌操作将会退出睡眠定时。"], ["sleep.action", "定时结束后", "select", "选择「关机」或「休眠」时，SimMusic 将在执行操作前 60 秒弹出提示，若您还需使用设备，可随时取消。", [["none", "不执行操作"], ["shutdown", "关机"], ["sleep", "休眠"]]], ["sleepTimer", "开启睡眠定时", "button"]]],
    ["歌词视图", [["lrc.blur", "层级虚化", "toggle", "若无需虚化效果或需要提升性能可关闭此功能。"], ["lrc.align", "文字对齐", "select", "此配置项切换曲目生效。", [["left", "左端对齐"], ["center", "居中对齐"]]], ["lrc.size", "歌词字号", "slider", "", [1, 3]], ["lrc.spacing", "歌词间距", "slider", "", [.2, 1]], ["lrc.multiLang", "歌词多语言支持", "toggle", "开启后，时间戳一致的不同歌词将作为多语言翻译同时渲染。此配置项切换曲目生效。"], ["lrc.transSize", "翻译字号", "slider", "同时控制歌词视图与桌面歌词中的翻译字号。", [.5, 1]]]],
    ["桌面歌词", [["dl.rememberState", "记忆开启状态", "toggle"], ["dl.compact", "精简模式", "toggle", "开启后，桌面歌词窗口将在暂停时自动隐藏，且不显示播放控件"], ["dl.hideCapture", "在截图中隐藏", "toggle", "其他应用截图或录屏时隐藏桌面歌词的内容，与多数截图或录屏软件相兼容，支持 Windows 10 2004 以上版本及 Windows 11。此功能不会影响您查看歌词，对采集卡等外置硬件无效。"], ["dl.autoClose", "在播放页自动关闭", "toggle", "当 SimMusic 主窗口打开播放页时自动关闭桌面歌词。"], ["dl.showTrans", "显示歌词翻译", "toggle"], ["dl.color", "字体颜色", "color"], ["dl.border", "启用边框", "toggle"], ["dl.borderColor", "边框颜色", "color"], ["dl.size", "字体大小", "slider", "", [20, 60]], ["dl.width", "歌词区域宽度", "slider", "", [500, 1920]], ["dl.center", "始终居中", "toggle", "无视用户左右拖拽操作，保持桌面歌词在屏幕中央。"]]],
    ["曲目下载", [["down.parallel", "并行下载数", "select", "下载在线歌曲时并行下载的任务数量。", [1, 2, 3, 4, 5].map(value => [String(value), `${value} 首曲目`])], ["down.nameFormat", "命名格式", "text", "可使用 [title] 表示歌曲名，[artist] 表示艺术家。SimMusic 会自动处理 Windows 无法写入的文件名。"], ["down.writeTitle", "写入曲目标题", "toggle"], ["down.writeArtist", "写入艺术家与专辑名", "toggle"], ["down.writeCover", "写入专辑封面", "toggle"], ["down.lyricMode", "歌词下载方式", "select", "", [["embed", "嵌入音频"], ["file", "独立 LRC"], ["none", "不下载歌词"]]], ["chooseDownloadDir", "下载目录", "button"]]]
];
const settingActions = { chooseTheme: () => run("chooseTheme"), chooseDownloadDir: async () => { await run("chooseDownloadDir"); await refreshCatalog(); showDownloadDir(); }, equalizer: () => showEqualizer(), sleepTimer: () => showSleepTimer(), clearIndex: async () => { if (await run("clearIndex") === null) return; lastCurrentKey = ""; updateIndexSize(); } };
// The row shows the directory in use; it is rewritten once a new one has been chosen.
const showDownloadDir = () => { const text = mainContent.querySelector("[data-setting-key=chooseDownloadDir] .settingText p"); if (text) text.textContent = catalog.downloadDir; };
const humanSize = bytes => bytes < 1024 ? `${bytes} B` : bytes < 1048576 ? `${(bytes / 1024).toFixed(1)} KB` : `${(bytes / 1048576).toFixed(1)} MB`;
const updateIndexSize = async () => {
    const size = await run("indexSize");
    const text = mainContent.querySelector('[data-setting-key="clearIndex"] .settingText');
    if (size === null || !text) return;
    const badge = text.querySelector(".badge") || text.insertBefore(document.createElement("span"), text.children[1] || null);
    badge.className = "badge";
    badge.innerHTML = `${icon("EC16")} ${humanSize(size)}`;
};
const makeToggle = (label, checked, change) => {
    const control = document.createElement("label");
    control.className = "toggleControl";
    control.classList.toggle("on", checked);
    const input = document.createElement("input");
    input.type = "checkbox";
    input.checked = checked;

    input.addEventListener("change", () => control.classList.toggle("on", input.checked));
    input.onchange = () => change(input.checked);
    const track = document.createElement("span");
    track.className = "toggle";
    control.append(input, track);
    return control;
};
const makeSetting = (item, values, pluginId = "") => {
    const { key, label, type, desc, options, min, max, badge, button: buttonLabel } = item;
    const row = document.createElement("div");
    row.className = "settingRow";
    row.dataset.settingKey = key;
    row.innerHTML = `<div class="settingText"><span>${escapeText(label)}</span>${badge ? `<span class="badge">${escapeText(badge)}</span>` : ""}${desc ? `<p>${escapeText(desc)}</p>` : ""}</div><div class="settingControl"></div>`;
    const holder = row.lastElementChild;
    const change = pluginId ? queueValue(value => run("pluginSetting", { plugin: pluginId, key, value })) : value => setConfig(key, value);
    if (type === "toggle") {
        const toggle = makeToggle(label, !!values[key], change);
        holder.append(toggle);
        // The whole row switches; a click on the switch itself is already handled by its input.
        row.onclick = event => { if (!event.target.closest(".toggleControl")) toggle.querySelector("input").click(); };
        return row;
    }
    if (type === "color") { holder.append(createColorPicker(label, values[key], change)); return row; }
    if (type === "slider") {
        const slider = document.createElement("div");
        slider.className = "progress settingSlider";
        slider.setAttribute("min", min);
        slider.setAttribute("max", max);
        slider.setAttribute("value", values[key] ?? min);
        slider.progress = new su5.progress(slider);
        const input = slider.querySelector("input");
        input.step = max <= 4 ? .05 : 1;

        slider.progress.onchange = slider.progress.ondrag = change;
        holder.append(slider);
        return row;
    }
    if (type === "button") {
        const button = document.createElement("button");
        button.className = "sub";
        button.textContent = buttonLabel || "配置";
        button.onclick = pluginId ? async () => { await run("pluginAction", { plugin: pluginId, key }); } : settingActions[key];
        holder.append(button);
        return row;
    }
    const field = document.createElement(type === "select" ? "select" : "input");

    if (type === "select") (options || []).forEach(option => { const node = document.createElement("option"); node.value = option.value; node.textContent = option.label; field.append(node); });
    else field.type = { toggle: "checkbox", slider: "range", color: "color" }[type] || "text";
    if (type === "toggle") field.checked = !!values[key];
    else field.value = values[key] ?? "";
    if (type === "slider") { field.min = min; field.max = max; field.step = max <= 4 ? .05 : 1; }
    field.onchange = () => change(type === "toggle" ? field.checked : type === "slider" ? Number(field.value) : field.value);
    if (["select", "text"].includes(type)) {
        const wrapper = document.createElement("div");
        wrapper.className = "input allowEmpty";
        wrapper.append(field);
        if (type === "select") { const glyph = document.createElement("i"); glyph.className = "clickable"; wrapper.append(glyph); }
        holder.append(wrapper);
        su5.form.init(wrapper);
    } else holder.append(field);
    return row;
};
const appendSettingsSection = (title, items, values, parent, pluginId = "") => {
    const section = document.createElement("section");
    section.className = "settingsSection";
    section.dataset.sectionTitle = title;
    const heading = document.createElement("h3");
    heading.textContent = title;
    const card = document.createElement("div");
    card.className = "settingsCard";
    items.forEach(item => card.append(makeSetting(item, values, pluginId)));
    section.append(heading, card);
    parent.append(section);
};
// "System default" first, then the devices present. A chosen device that is unplugged stays
// listed: playback uses the default until it returns.
const outputDevices = async () => {
    const devices = await run("audioDevices") || [];
    const saved = catalog.config["audio.device"] || "";
    const options = [{ value: "", label: "系统默认" }, ...devices.map(device => ({ value: device.id, label: device.name }))];
    if (!options.some(option => option.value === saved)) options.push({ value: saved, label: "所选设备（未连接）" });
    return options;
};
// The fonts installed on this machine; the default comes first. A chosen font that has since been
// removed stays listed, the interface falls back to the default meanwhile.
const fontOptions = async () => {
    const saved = catalog.config["general.font"] || "";
    const options = [{ value: "", label: "微软雅黑" }, ...(await run("fonts") || []).map(name => ({ value: name, label: name }))];
    if (!options.some(option => option.value === saved)) options.push({ value: saved, label: saved });
    return options;
};
const renderSettings = async () => {
    const generation = pageGeneration;
    const [deviceOptions, fonts] = [await outputDevices(), await fontOptions()];
    if (generation !== pageGeneration) return;
    mainContent.innerHTML = pageHeader("软件设置", "", icons.settings) + `<div class="pageBody" id="settingsBody"></div>`;
    settingGroups.forEach(([title, items]) => appendSettingsSection(title, items.map(([key, label, type, desc = "", extra]) => ({ key, label, type, desc: key === "chooseDownloadDir" ? catalog.downloadDir : desc, button: key === "clearIndex" ? "清理" : "", options: key === "audio.device" ? deviceOptions : key === "general.font" ? fonts : type === "select" ? extra.map(([value, label]) => ({ value, label })) : undefined, min: extra?.[0], max: extra?.[1] })), catalog.config, settingsBody));
    // Each family previews itself: its entry in the list is drawn in that font.
    settingsBody.querySelectorAll('[data-setting-key="general.font"] .SimsvOptionsContainer > div').forEach(option => simCss(option).fontFamily = `"${(option.dataset.value || "Microsoft YaHei").replace(/["\\]/g, "")}", "Microsoft YaHei"`);
    updateIndexSize();
    for (const plugin of catalog.plugins.filter(item => item.running)) {
        const data = await run("pluginSettings", { plugin: plugin.manifest.id });
        if (generation !== pageGeneration) return;
        const items = (data?.sections || []).flatMap(section => section.items || []);
        if (items.length) appendSettingsSection(`扩展：${plugin.manifest.name}`, items, data.values, settingsBody, plugin.manifest.id);
    }
    updateSettingDependencies(false);
};
// A dependent row folds in and out. Its height is animated between measured pixel values,
// and it only clips while moving so an open dropdown is never cut off at rest.
const setRowShown = (row, shown, animate) => {
    if (row.classList.contains("collapsed") === !shown) return;
    clearTimeout(row.collapseTimer);
    const style = simCss(row);
    if (!animate) { row.classList.toggle("collapsed", !shown); style.height = shown ? "auto" : "0px"; return; }
    row.classList.add("collapsing");
    if (shown) {
        row.classList.add("measuring");
        row.classList.remove("collapsed");
        style.height = "auto";
        const height = row.offsetHeight;
        row.classList.add("collapsed");
        style.height = "0px";
        row.getBoundingClientRect();
        row.classList.remove("measuring", "collapsed");
        style.height = `${height}px`;
    } else {
        style.height = `${row.offsetHeight}px`;
        row.getBoundingClientRect();
        row.classList.add("collapsed");
        style.height = "0px";
    }
    row.collapseTimer = setTimeout(() => { row.classList.remove("collapsing"); if (shown) style.height = "auto"; }, 260);
};
const updateSettingDependencies = (animate = true) => {
    const config = catalog.config;
    const dependencies = { "list.themeImageType": "list.themeImage", "list.themeImageBlur": "list.themeImage", "chooseTheme": "list.themeImage", "lrc.transSize": "lrc.multiLang", "dl.showTrans": "lrc.multiLang", "dl.borderColor": "dl.border" };
    mainContent.querySelectorAll("[data-setting-key]").forEach(row => { const parent = dependencies[row.dataset.settingKey]; if (parent) setRowShown(row, !!config[parent], animate); });
};
const showEqualizer = () => {
    const { id, content } = su5.dialog.create("均衡器");
    content.parentElement.classList.add("eqDialog");
    const gains = [...(catalog.config["eq.gains"] || Array(10).fill(0))];
    const frequencies = catalog.eqBands || [70, 180, 320, 600, 1000, 3000, 6000, 12000, 14000, 16000];
    let profile = catalog.config["eq.profile"] || "basic";
    content.append(makeSetting({ key: "eq.enabled", label: "启用均衡器", type: "toggle" }, catalog.config));
    // Two modes read better as a segmented switch than as a dropdown.
    const mode = document.createElement("div");
    mode.className = "eqMode";
    mode.innerHTML = `<button data-profile="basic">基础</button><button data-profile="json">高级</button>`;
    content.parentElement.querySelector(".modalHeader").append(mode);
    mode.querySelectorAll("button").forEach(button => button.onclick = () => { profile = button.dataset.profile; sync(); setConfig("eq.profile", profile); });
    const basic = document.createElement("div");
    const bands = document.createElement("div");
    bands.className = "eqBands";
    const sliders = [];
    const labels = [];
    frequencies.forEach((frequency, index) => {
        const band = document.createElement("div");
        band.className = "eqBand";
        band.innerHTML = `<span></span><div class="eqSlider"><div min="-20" max="20" value="${gains[index]}"></div></div><span>${frequency >= 1000 ? `${frequency / 1000}k` : frequency}</span>`;
        const slider = new su5.progress(band.querySelector(".eqSlider > div"));
        band.querySelector("input").step = ".5";
        sliders.push(slider);
        labels.push(band.firstElementChild);
        slider.ondrag = value => { gains[index] = Math.round(value * 2) / 2; sync(); setConfig("eq.gains", [...gains]); };
        slider.onchange = () => setConfig("eq.gains", [...gains]);
        band.addEventListener("wheel", event => {
            event.preventDefault();
            gains[index] = Math.max(-20, Math.min(20, Math.round((gains[index] - event.deltaY / 100) * 2) / 2));
            sync();
            setConfig("eq.gains", [...gains]);
        }, { passive: false });
        bands.append(band);
    });
    basic.append(bands);
    const presets = document.createElement("div");
    presets.className = "eqPresets";
    const presetButtons = [];
    (catalog.eqPresets || []).forEach(preset => {
        const button = document.createElement("button");
        button.textContent = preset.Name;
        button.onclick = () => { gains.splice(0, gains.length, ...preset.Gains); sync(); setConfig("eq.gains", [...gains]); };
        presetButtons.push([button, preset.Gains]);
        presets.append(button);
    });
    basic.append(presets);
    content.append(basic);
    const jsonPanel = document.createElement("div");
    jsonPanel.className = "eqJson";
    jsonPanel.innerHTML = `<div class="input"><textarea spellcheck="false"></textarea></div>`;
    const jsonInput = jsonPanel.querySelector("textarea");
    // The result line sits in the free space left of the buttons, so the dialog never changes height.
    const status = document.createElement("p");
    status.className = "eqJsonStatus";
    jsonInput.value = JSON.stringify(catalog.config["eq.filters"] || frequencies.map((F, index) => ({ F, G: gains[index], Q: 1 })), null, 2);
    su5.form.init(jsonPanel.firstElementChild);
    const applyJson = async () => {
        try {
            const filters = JSON.parse(jsonInput.value);
            if (!Array.isArray(filters) || filters.length > 64 || filters.some(item => !item || ["F", "G", "Q"].some(key => typeof item[key] !== "number" || !Number.isFinite(item[key])) || item.F < 10 || item.F >= 24000 || item.G < -20 || item.G > 20 || item.Q < .0001 || item.Q > 1000)) throw new Error("须为 F、G、Q 对象数组；频率 10–23999 Hz，增益 -20–20 dB，Q 0.0001–1000。");
            await api("config", { key: "eq.filters", value: filters });
            catalog.config["eq.filters"] = filters;
            status.textContent = "配置已应用";
            status.classList.remove("invalid");
        } catch (error) { status.textContent = error.message; status.classList.add("invalid"); }
    };
    jsonInput.oninput = () => status.textContent = "";
    content.append(jsonPanel);
    const sync = () => {
        sliders.forEach((slider, index) => { slider.value = gains[index]; labels[index].textContent = `${gains[index] > 0 ? "+" : ""}${gains[index].toFixed(1)} dB`; });
        presetButtons.forEach(([button, values]) => button.classList.toggle("sub", !values.every((value, index) => Math.abs(value - gains[index]) < .001)));
        basic.hidden = profile !== "basic";
        jsonPanel.hidden = status.hidden = applyButton.hidden = profile !== "json";
        mode.querySelectorAll("button").forEach(button => button.classList.toggle("sub", button.dataset.profile !== profile));
    };
    const buttons = document.createElement("div");
    buttons.className = "buttons";
    buttons.innerHTML = `<button class="sub">重置</button><button class="sub">应用</button><button>完成</button>`;
    const [resetButton, applyButton, doneButton] = buttons.children;
    buttons.prepend(status);
    resetButton.onclick = () => {
        if (profile === "json") { jsonInput.value = JSON.stringify(frequencies.map(F => ({ F, G: 0, Q: 1 })), null, 2); applyJson(); }
        else { gains.fill(0); sync(); setConfig("eq.gains", [...gains]); }
    };
    applyButton.onclick = applyJson;
    doneButton.onclick = () => su5.dialog.show(id, false);
    content.append(buttons);
    sync();
    su5.dialog.show(id);
};
// Presets only choose a duration; the single primary button starts the timer.
const showSleepTimer = () => {
    const { id, content } = su5.dialog.create("睡眠定时");
    content.innerHTML = `<div class="timerPresets">${[5, 10, 15, 30, 60].map(minutes => `<button class="sub" data-minutes="${minutes}">${minutes} 分钟</button>`).join("")}</div><form class="timerCustom"><span>自定义时长</span><label class="input"><input name="minutes" inputmode="numeric" maxlength="4" value="30"><span>分钟</span></label></form><div class="buttons"><button class="sub">取消</button><button>开启定时</button></div>`;
    const form = content.querySelector("form");
    const field = form.querySelector(".input");
    const input = form.elements.minutes;
    const presets = [...content.querySelectorAll("[data-minutes]")];
    const sync = () => presets.forEach(button => button.classList.toggle("sub", button.dataset.minutes !== input.value.trim()));
    presets.forEach(button => button.onclick = () => { input.value = button.dataset.minutes; sync(); });
    input.oninput = sync;
    su5.form.init(field);
    form.onsubmit = async event => {
        event?.preventDefault();
        const minutes = Number(input.value);
        if (!/^\d+$/.test(input.value.trim()) || minutes < 1 || minutes > 1440) {
            field.classList.add("warning");
            setTimeout(() => field.classList.remove("warning"), 200);
            return;
        }
        if (await run("sleep", { number: minutes }) !== null) su5.dialog.show(id, false);
    };
    content.querySelector(".buttons button.sub").onclick = () => su5.dialog.show(id, false);
    content.querySelector(".buttons button:last-child").onclick = () => form.onsubmit();
    sync();
    su5.dialog.show(id);
};
const sleepIsActive = () => sleepState.WaitTrackEnd || new Date(sleepState.Until).getTime() > Date.now();
const sleepMenuItem = () => sleepIsActive() ? { label: "取消睡眠定时", icon: "EB99", action: () => run("cancelSleep") } : {
    label: "开启睡眠定时", icon: "F215", children: [
        ...[5, 10, 15, 30, 60].map(minutes => ({ label: `${minutes} 分钟`, action: () => run("sleep", { number: minutes }) })),
        { separator: true }, { label: "自定义…", action: showSleepTimer }
    ]
};
const speedPresets = [.7, .9, 1, 1.1, 1.2, 1.5];
const speedMenuItem = () => ({ label: "播放倍速", icon: "F177", children: [
    ...speedPresets.map(value => ({ label: `${value} 倍`, icon: speed === value ? "EB7B" : "", action: () => run("speed", { number: value }) })),
    { separator: true }, { label: speedPresets.includes(speed) ? "自定义…" : `自定义（${speed} 倍）…`, icon: speedPresets.includes(speed) ? "" : "EB7B", action: showSpeedDialog }
] });
// The slider applies while dragging, like the equalizer.
const showSpeedDialog = () => {
    const { id, content } = su5.dialog.create("播放倍速");
    const current = Math.max(.5, Math.min(2, speed));
    content.innerHTML = `<div class="speedValue"></div><div class="speedSlider"><span>0.5</span><div min="0.5" max="2" value="${current}"></div><span>2</span></div><div class="buttons"><button class="sub">重置</button><button>完成</button></div>`;
    const slider = new su5.progress(content.querySelector(".speedSlider > div"));
    content.querySelector(".speedSlider input").step = ".05";
    const label = content.querySelector(".speedValue");
    const send = queueValue(number => run("speed", { number }));
    const apply = value => { value = Math.round(value * 20) / 20; label.textContent = `${value} 倍`; return send(value); };
    label.textContent = `${current} 倍`;
    slider.ondrag = slider.onchange = apply;
    content.querySelector(".buttons button.sub").onclick = () => { slider.value = 1; apply(1); };
    content.querySelector(".buttons button:last-child").onclick = () => su5.dialog.show(id, false);
    su5.dialog.show(id);
};

const confirmInstall = async (manifest, development = false) => {
    if (!manifest) return;
    const denied = await showInstaller(manifest, development);
    if (!denied) { await run("cancelInstall", { id: manifest.installToken }); return; }
    await run("installPlugin", { id: manifest.installToken, denied });
    await refreshCatalog();
};
// Files dropped on the window. The page cannot read their paths; the host reported them to the core when
// the drag entered, so the drop only says which page it landed on. A .simex opens the installer, a folder
// becomes a local directory, and audio files join the playlist or local directory being viewed.
document.addEventListener("dragover", event => { if (event.dataTransfer.types.includes("Files")) event.preventDefault(); });
document.addEventListener("drop", async event => {
    if (!event.dataTransfer.types.includes("Files")) return;
    event.preventDefault();
    if (document.querySelector("dialog[open]")) return;
    const target = page;
    const result = await run("dropFiles", { text: target.kind, plugin: target.plugin || "", id: target.id || "" });
    if (result?.refresh) { await refreshCatalog(); if (page === target) navigate(target); }
    if (result?.install) confirmInstall(result.install);
});
const renderPlugins = () => {
    mainContent.innerHTML = pageHeader("扩展程序", "", icons.plugins) + `<div class="pageBody pluginBody"><div class="pluginInstall"><div><span>安装扩展</span><p>选择 .simex 扩展包。</p></div><button id="pluginFolderButton" class="iconButton" title="打开扩展目录">${icon("ED6A")}</button><button id="loadPluginFolderButton" class="sub">从文件夹加载</button><button id="installPluginButton">安装扩展</button><input id="pluginPackageInput" type="file" accept=".simex" hidden></div><div id="pluginCards"></div></div>`;
    pluginFolderButton.onclick = () => run("pluginFolder");
    installPluginButton.onclick = () => pluginPackageInput.click();
    pluginPackageInput.onchange = async () => {
        const file = pluginPackageInput.files[0];
        if (!file) return;
        try {
            const response = await fetch("extensionPackage", { method: "POST", body: file });
            const payload = await response.json();
            if (!response.ok) throw new Error(payload.error);
            await confirmInstall(payload.result);
        } catch (error) { showAlert(error.message); }
        if (window.pluginPackageInput) pluginPackageInput.value = "";
    };
    loadPluginFolderButton.onclick = async () => confirmInstall(await run("prepareInstall"), true);
    catalog.plugins.forEach(plugin => {
        const manifest = plugin.manifest;
        const card = document.createElement("div");
        card.className = "pluginCard";
        card.dataset.plugin = manifest.id;
        card.innerHTML = `<div class="pluginDetails"><h2>${escapeText(manifest.name)}${plugin.builtin ? `<span class="badge">内置</span>` : ""}${plugin.development ? `<span class="badge">开发</span>` : ""}${!plugin.running ? `<span class="badge">${plugin.enabled ? "加载失败" : "已停用"}</span>` : ""}</h2><div class="pluginMeta">${escapeText(manifest.id)} · ${escapeText(manifest.version)}${manifest.author ? ` · ${escapeText(manifest.author)}` : ""}</div><p>${escapeText(manifest.description)}</p>${plugin.error ? `<p class="pluginError">${escapeText(plugin.error)}</p>` : ""}</div><div class="pluginTools"></div>`;
        const tools = card.lastElementChild;
        const handle = document.createElement("button");
        handle.className = "iconButton pluginDragHandle";
        handle.textContent = "⋮⋮";
        handle.title = "拖动调整扩展加载顺序";
        handle.onpointerdown = event => {
            if (event.button !== 0) return;
            event.preventDefault();
            const cards = [...pluginCards.children];
            startDragSort({
                event, element: card, index: cards.indexOf(card), count: cards.length, scroller: pluginCards.closest(".pageBody"), step: card.offsetHeight + 5, edges: [70, 80],
                items: () => [...pluginCards.children].map((node, position) => [node, position]),
                commit: async (from, to) => {
                    const order = catalog.plugins.map(item => item.manifest.id);
                    moveInList(order, from, to);
                    pluginCards.insertBefore(card, pluginCards.children[to + (to > from)] || null);
                    await run("pluginOrder", { order });
                    await refreshCatalog();
                    if (page.kind === "plugins") renderPlugins();
                }
            });
        };
        card.prepend(handle);
        tools.append(makeToggle(`启用${manifest.name}`, plugin.enabled, async value => { await run("pluginEnabled", { plugin: manifest.id, value }); await refreshCatalog(); }));
        const more = document.createElement("button");
        more.className = "iconButton";
        more.title = `${manifest.name} · 更多操作`;

        more.innerHTML = icon("EF77");
        more.onclick = event => {
            const items = [
                { label: "权限管理", icon: "F0EE", action: () => showPluginPermissions(manifest) },
                { label: "查看日志", icon: "ECEF", action: () => showPluginLogs(manifest) },
                { label: "重新加载", icon: "F080", action: async () => { await run("reloadPlugin", { plugin: manifest.id }); await refreshCatalog(); } }
            ];
            if (!plugin.builtin) items.push({ label: "移除扩展", icon: "EC2A", action: async () => { if (await su5.dialog.confirm(`移除「${manifest.name}」？`)) { await run("removePlugin", { plugin: manifest.id }); await refreshCatalog(); } } });
            showContext(items, event.clientX, event.clientY);
        };
        tools.append(more);
        pluginCards.append(card);
    });
};
const dialogFooter = (id, content, label = "完成") => {
    const footer = document.createElement("div");
    footer.className = "buttons dialogFooter";
    const button = document.createElement("button");
    button.textContent = label;
    button.onclick = () => su5.dialog.show(id, false);
    footer.append(button);
    content.append(footer);
};
const showPluginLogs = async manifest => {
    const logs = await run("pluginLogs", { plugin: manifest.id });
    const { id, content } = su5.dialog.create("扩展日志", manifest.name);
    content.closest("dialog").classList.add("wideDialog");
    const pre = document.createElement("pre");
    pre.className = "logView";
    pre.textContent = (logs || []).join("\n") || "暂无日志";
    content.append(pre);
    dialogFooter(id, content, "关闭");
    su5.dialog.show(id);
};
// 2024 download page: one row per task, the progress as a bar behind the row, the status as an
// icon and a line of text, and the row's own buttons. Rows are kept and updated in place so the
// bar and the status colour can transition.
const downloadIcons = { pending: "F337", download: "EC5A", success: "EB7B", error: "F4C8" };
const renderDownloads = () => {
    if (!window.downloadTasks) mainContent.innerHTML = pageHeader("下载管理", "", icons.downloads) + `<div class="pageBody downloadBody"><div id="downloadTasks"></div></div>`;
    const rows = new Map([...downloadTasks.querySelectorAll(".downloadRow")].map(row => [row.dataset.task, row]));
    downloadTasks.querySelector(".emptyState")?.remove();
    if (!downloads.length) downloadTasks.innerHTML = emptyState();
    downloads.forEach(task => {
        const id = String(task.id);
        let row = rows.get(id);
        rows.delete(id);
        if (!row) {
            row = document.createElement("div");
            row.className = "downloadRow";
            row.dataset.task = id;
            row.innerHTML = `<div class="progressBar"></div><div class="info"><div class="music"><b></b><span><i></i> <span class="progressText"></span></span></div><div class="buttons"><button class="iconButton errorOnly" title="重试">${icon("F33D")}</button><button class="iconButton successOnly" title="播放">${icon("F509")}</button><button class="iconButton successOnly" title="在资源管理器中显示">${icon("ED6A")}</button><button class="iconButton successOnly" title="删除任务和文件">${icon("EC2A")}</button><button class="iconButton" title="删除任务">${icon("F4C8")}</button></div></div>`;
            const [retryButton, playButton, revealButton, deleteButton, removeButton] = row.querySelectorAll("button");
            retryButton.onclick = () => run("retryDownload", { task: task.id });
            playButton.onclick = () => run("playDownload", { task: task.id });
            revealButton.onclick = () => run("revealDownload", { task: task.id });
            deleteButton.onclick = async () => { if (await su5.dialog.confirm("确实要删除此任务与本地已下载的文件吗？")) run("deleteDownload", { task: task.id }); };
            removeButton.onclick = () => run("removeDownload", { task: task.id });
            downloadTasks.append(row);
        }
        const artist = task.track.artist && task.track.artist !== "未知艺术家" ? ` - ${task.track.artist}` : "";
        row.dataset.status = task.status;
        row.querySelector("b").textContent = `${task.track.title || "未知歌曲"}${artist}`;
        row.querySelector(".music i").textContent = String.fromCodePoint(parseInt(downloadIcons[task.status], 16));
        row.querySelector(".progressText").textContent = task.text;
        simCss(row).setProperty("--progressWidth", `${Math.round(task.progress * 100)}%`);
    });
    rows.forEach(row => row.remove());
};
// Android-style installer: what is being installed, then its permissions, which open into a list
// where each one can be switched off before installing. Resolves to the denied permissions, or null when cancelled.
const showInstaller = (manifest, development) => new Promise(resolve => {
    const update = manifest.installedVersion !== undefined;
    const { id, content } = su5.dialog.create(development ? "从文件夹加载" : update ? "更新扩展" : "安装扩展");
    content.closest("dialog").classList.add("installDialog");
    const permissions = manifest.permissionList || [];
    const denied = new Set();
    content.innerHTML = `<div class="installHeader"><div><div class="installName">${escapeText(manifest.name || manifest.id)}</div><div class="installMeta">${escapeText([`版本 ${manifest.version || "未知"}`, manifest.author].filter(Boolean).join(" · "))}</div></div></div>${manifest.description ? `<p class="installDesc">${escapeText(manifest.description)}</p>` : ""}<button class="installSummary"><span>权限</span><small></small>${icon("EA6E")}</button><div class="permissionList installPermissions"></div><p class="installNote">${update ? `将替换已安装的 ${escapeText(manifest.installedVersion || "旧")} 版本。` : ""}${development ? "直接加载原文件夹，不复制源文件。" : ""}权限可在安装后随时更改。</p><div class="buttons"><button class="sub">取消</button><button>${update ? "更新" : "安装"}</button></div>`;
    const summary = content.querySelector(".installSummary");
    const list = content.querySelector(".installPermissions");
    const sync = () => summary.querySelector("small").textContent = !permissions.length ? "无需任何权限" : denied.size ? `${permissions.length} 项，已关闭 ${denied.size} 项` : `${permissions.length} 项`;
    permissions.forEach(info => {
        const row = document.createElement("div");
        row.className = "permissionRow";
        const hosts = info.Key === "network" && manifest.hosts?.length ? `仅限 ${manifest.hosts.join("、")}` : "";
        row.innerHTML = `<div><span>${escapeText(info.Name)}</span><p>${escapeText(info.Desc)}${hosts ? `<br>${escapeText(hosts)}` : ""}</p></div>`;
        row.append(makeToggle(info.Name, true, value => { denied[value ? "delete" : "add"](info.Key); sync(); }));
        list.append(row);
    });
    summary.disabled = !permissions.length;
    summary.onclick = () => { summary.classList.toggle("open"); list.classList.toggle("open"); };
    const finish = result => { resolve(result); su5.dialog.show(id, false); };
    content.querySelector(".buttons button.sub").onclick = () => finish(null);
    content.querySelector(".buttons button:last-child").onclick = () => finish([...denied]);
    sync();
    su5.dialog.show(id);
});
const renderAbout = () => {
    mainContent.innerHTML = `<div class="aboutPage"><img src="assets/icon.png"><h1>SimMusic Next</h1><p>高颜值插件化音频播放器</p><small>Copyright © ${new Date().getFullYear()} NLR DevTeam</small></div>`;
};
const showPluginPermissions = async manifest => {
    const permissions = await run("pluginPermissions", { plugin: manifest.id });
    const { id, content } = su5.dialog.create("权限管理", manifest.name);
    content.closest("dialog").classList.add("permissionsDialog");
    if (!permissions?.length) content.textContent = "此扩展未声明任何权限，仅能访问自身的数据目录。";
    // One grouped list; entries of the song-menu permission follow it as indented rows.
    const list = document.createElement("div");
    list.className = "permissionList";
    const addRow = (label, desc, granted, action, key, child) => {
        const row = document.createElement("div");
        row.className = child ? "permissionRow childPermission" : "permissionRow";
        row.innerHTML = `<div><span>${escapeText(label)}</span><p>${escapeText(desc)}</p></div>`;
        row.append(makeToggle(label, granted, async value => { await run(action, { plugin: manifest.id, key, value }); await refreshCatalog(); }));
        list.append(row);
    };
    (permissions || []).forEach(permission => {
        const info = permission.info;
        addRow(info.Name, info.Desc, permission.granted, "pluginPermission", info.Key);
        (permission.menus || []).forEach(menu => addRow(menu.label, (menu.scenes || ["list", "play", "queue"]).map(scene => ({ list: "歌曲列表", play: "播放详情", queue: "播放队列" }[scene] || scene)).join("、"), menu.granted, "trackMenuPermission", menu.key, true));
    });
    if (permissions?.length) content.append(list);
    dialogFooter(id, content);
    su5.dialog.show(id);
};

const renderQueue = () => {
    const tracks = player.Queue || [];
    queueCount.textContent = `${tracks.length} 首`;
    queueTracks.replaceChildren();
    if (!tracks.length) { queueTracks.innerHTML = emptyState(); return; }
    tracks.forEach((track, index) => {
        const row = document.createElement("div");
        row.className = "queueRow";
        row.dataset.index = index;
        row.classList.toggle("current", index === player.Index);
        row.innerHTML = `<img loading="lazy" src="${coverUrl(track)}"><div><p>${escapeText(track.title)}</p><small>${escapeText(track.artist)}</small></div><button class="iconButton" title="移出队列">×</button>`;
        updateArtist(row.querySelector("small"), track.artist);
        row.onclick = event => { if (!event.target.closest("button")) run("playIndex", { index }); };
        row.onpointerdown = event => dragOnMove(event, () => startDragSort({
            event, element: row, index, count: tracks.length, scroller: queueTracks, step: 80,
            items: () => [...queueTracks.querySelectorAll(".queueRow")].map(node => [node, Number(node.dataset.index)]),
            commit: (from, to) => {
                moveInList(player.Queue, from, to);
                player.Index = player.Index === from ? to : player.Index > from && player.Index <= to ? player.Index - 1 : player.Index < from && player.Index >= to ? player.Index + 1 : player.Index;
                renderQueue();
                run("moveQueue", { index: from, to });
            }
        }));
        row.querySelector("button").onclick = () => run("removeQueue", { index });
        row.oncontextmenu = event => { event.preventDefault(); showTrackContext([track], event.clientX, event.clientY, "queue"); };
        queueTracks.append(row);
    });
    queueTracks.onscroll = songInfo.queue;
    songInfo.queue();
};
let queuePanelTimer;
const setPlaySide = (showQueue, showLyrics) => {
    clearTimeout(queuePanelTimer);
    if (showQueue) { queuePanel.hidden = false; queuePanel.getBoundingClientRect(); }
    document.body.classList.toggle("hideList", !showQueue);
    document.body.classList.toggle("hideLyrics", !showLyrics);
    largeQueueButton.classList.toggle("active", showQueue);
    lyricsButton.classList.toggle("active", showLyrics);
    if (!showQueue) queuePanelTimer = setTimeout(() => queuePanel.hidden = document.body.classList.contains("hideList"), 300);
    if (showQueue) {
        renderQueue();
        queueTracks.querySelector(".current")?.scrollIntoView({ block: "center" });
    }
    if (showLyrics) updateLyrics(true);
};
const openQueue = () => { openPlayPage(true); setPlaySide(document.body.classList.contains("hideList"), false); };
let playPageTimer;
let playOpeningTimer;
const openPlayPage = async open => {
    if (open && !player.Current) return;
    if (document.body.classList.contains("playerShown") === open) return;
    clearTimeout(playPageTimer);
    clearTimeout(playOpeningTimer);
    // While the page slides in, its own animations and transitions rest so the slide stays smooth,
    // and the lyrics and queue are already at the current position instead of scrolling to it.
    document.body.classList.toggle("playerOpening", open);
    // A finished song-change fade must not replay once the page has opened.
    if (open) [largeCover, largeTitle.parentElement, lyricsView].forEach(element => element.classList.remove("trackChanging"));
    if (open) {
        playPage.hidden = false;
        updateLyrics(true);
        queueTracks.querySelector(".current")?.scrollIntoView({ block: "center" });
        playPage.getBoundingClientRect();
        playOpeningTimer = setTimeout(() => document.body.classList.remove("playerOpening"), 300);
    }
    document.body.classList.toggle("playerShown", open);
    if (!open) playPageTimer = setTimeout(() => playPage.hidden = !document.body.classList.contains("playerShown"), 300);
    collapseButton.hidden = !open;
    await run("playPage", { value: open });
};
const updatePlayer = snapshot => {
    maximizeButton.textContent = String.fromCodePoint(snapshot.windowMaximized ? 0xEABB : 0xEAB9);
    maximizeButton.title = snapshot.windowMaximized ? "还原" : "最大化";
    player = snapshot.player;
    player.Queue ??= [];
    player.Lyrics ??= [];
    downloads = snapshot.downloads || [];
    updateDownloadNav();
    document.body.classList.toggle("hasCurrent", !!player.Current);
    document.body.classList.toggle("playing", player.Playing);
    document.body.classList.toggle("musicLoading", !!player.Loading);
    if (!player.Current && !playPage.hidden) openPlayPage(false);
    const key = player.Current ? `${trackKey(player.Current)}:${player.Current.infoRevision || 0}` : "";
    if (lastCurrentKey !== key) {
        const nextSongKey = player.Current ? trackKey(player.Current) : "";
        const changed = displayedSongKey && nextSongKey && displayedSongKey !== nextSongKey;
        displayedSongKey = nextSongKey;
        lastCurrentKey = key;
        resetLyricsScroll();
        activeLyric = -1;
        updateTrackDisplay(player.Current, changed);
        updateBackdrop();
        updateRowStates();
    }
    const modes = ["列表循环", "单曲循环", "随机播放"];
    const modeIcon = icon(["F072", "F075", "F124"][player.Mode || 0]);
    if (modeButton.innerHTML !== modeIcon) modeButton.innerHTML = largeModeButton.innerHTML = modeIcon;
    modeButton.title = largeModeButton.title = modes[player.Mode || 0];
    const duration = Math.max(1, player.Dur);
    if (Number(seekBar.progress.max) !== duration) seekBar.progress.max = duration;
    if (Number(largeSeekBar.progress.max) !== duration) largeSeekBar.progress.max = duration;
    seekBar.progress.value = largeSeekBar.progress.value = player.Pos;
    volumeBar.progress.value = largeVolumeBar.progress.value = player.Volume;
    updateVolumeIcons();
    speed = player.Speed || 1;
    speedButton.textContent = `${speed} 倍`;
    timeLabel.textContent = `${formatTime(player.Pos)} / ${formatTime(player.Dur)}`;
    positionLabel.textContent = formatTime(player.Pos);
    durationLabel.textContent = formatTime(player.Dur);
    desktopButton.classList.toggle("active", snapshot.deskVisible);
    if (!trackDisplayPending) renderLyrics();
    const nextQueueSignature = JSON.stringify([player.Queue, player.Index]);
    if (queueSignature !== nextQueueSignature && !dragActive) {
        queueSignature = nextQueueSignature;
        if (!queuePanel.hidden) renderQueue();
        updateRowStates();
    }
    if (page.kind === "downloads") {
        const nextSignature = JSON.stringify(snapshot.downloads);
        if (downloadSignature !== nextSignature) { downloadSignature = nextSignature; renderDownloads(); }
    }
    const sleep = sleepState = snapshot.sleep;
    const until = new Date(sleep.Until).getTime();
    const sleepActive = sleep.WaitTrackEnd || until > Date.now();
    // 2024: while a timer runs, the play page's menu button shows the countdown, then "+time" once it waits for the track to end.
    sleepWaitSince = sleep.WaitTrackEnd ? sleepWaitSince || Date.now() : 0;
    document.body.classList.toggle("sleepMode", sleepActive);
    const moreLabel = !sleepActive ? icon("EF77") : sleep.WaitTrackEnd ? `+${formatTime((Date.now() - sleepWaitSince) / 1000)}` : formatTime((until - Date.now()) / 1000);
    if (trackMoreButton.innerHTML !== moreLabel) trackMoreButton.innerHTML = moreLabel;
    sleepButton.textContent = sleepActive ? sleep.WaitTrackEnd ? "曲目结束后暂停 · 取消" : `剩余 ${formatTime((until - Date.now()) / 1000)} · 取消` : "睡眠定时";
    sleepButton.onclick = sleepActive ? () => run("cancelSleep") : showSleepTimer;
    const timerSetting = mainContent.querySelector('[data-setting-key="sleepTimer"] .settingControl button');
    if (timerSetting) { timerSetting.textContent = sleepActive ? "取消定时" : "开启定时"; timerSetting.onclick = sleepButton.onclick; }
    (snapshot.toasts || []).forEach(message => su5.toast(message));
    (snapshot.alerts || []).forEach(showAlert);
    (snapshot.musicInvalidations || []).forEach(id => songInfo.invalidate(id));
    // An extension asked for one of its own sidebar entries to be shown.
    (snapshot.navigations || []).forEach(item => navigate({ kind: "menu", plugin: item.plugin, id: item.id }));
    // A panel opened outside a sidebar click takes the content area; one the extension closed gives it back.
    // A snapshot may predate what a click has just changed, so a difference counts once it is seen twice.
    const livePanel = snapshot.panel?.id || "";
    if (menuOpening || livePanel === shownPanel) panelDrift = null;
    else if (panelDrift !== livePanel) panelDrift = livePanel;
    else { panelDrift = null; navigate(livePanel ? { kind: "panel", plugin: snapshot.panel.plugin, id: livePanel } : { kind: "home" }); }
    (snapshot.prompts || []).forEach(async prompt => {
        if (shownPrompts.size) return;
        shownPrompts.add(prompt.id);
        const text = prompt.kind === "confirm" ? await su5.dialog.confirm(prompt.title) ? "1" : "" : (await su5.dialog.prompt({ title: prompt.title, items: [{ type: "input", name: "value", placeholder: prompt.placeholder, value: prompt.value }] }))?.value || "";
        await run("promptResult", { id: prompt.id, text });
        shownPrompts.delete(prompt.id);
    });
};
const resetLyricsScroll = () => {
    clearTimeout(lyricsManualTimer);
    lyricsView.classList.remove("manualScroll");
};
const renderLyrics = () => {
    const signature = JSON.stringify(player.Lyrics);
    if (signature !== lyricsSignature) {
        lyricsSignature = signature;
        resetLyricsScroll();
        lyricsView.innerHTML = player.Lyrics.length ? player.Lyrics.map((line, index) => `<div class="lyricLine" data-lyric="${index}">${escapeText(line.Text)}${line.Trans ? `<span>${escapeText(line.Trans)}</span>` : ""}</div>`).join("") : `<div class="emptyLyrics">暂无歌词</div>`;
        lyricsView.querySelectorAll(".lyricLine").forEach((node, index) => node.onclick = async () => { await run("seek", { number: player.Lyrics[index].Time + .1 }); updateLyrics(true); });
        activeLyric = -1;
    }
    if (!playPage.hidden) updateLyrics();
};
const updateLyrics = (forceScroll = false) => {
    if (trackDisplayPending) return;
    if (forceScroll) resetLyricsScroll();
    let index = -1;
    player.Lyrics.forEach((line, i) => { if (line.Time <= player.Pos) index = i; });
    if (index === activeLyric && !forceScroll) return;
    activeLyric = index;
    const lines = lyricsView.querySelectorAll(".lyricLine");
    lines.forEach((line, i) => {
        line.classList.toggle("active", i === index);
        const distance = Math.abs(i - index);
        line.dataset.blur = index < 0 || !distance ? 0 : Math.min(distance + 1, 5);
    });
    if (index >= 0 && !lyricsView.classList.contains("manualScroll") && !playPage.hidden && !document.body.classList.contains("hideLyrics")) {
        const line = lines[index];
        lyricsView.scrollTo({ top: line.offsetTop - lyricsView.offsetTop - lyricsView.clientHeight / 2 + line.clientHeight / 2, behavior: document.body.classList.contains("playerOpening") ? "auto" : "smooth" });
    }
};
lyricsView.addEventListener("wheel", event => {
    if (!event.deltaY || !largeVolumeBar.hidden || !player.Lyrics.length) return;
    clearTimeout(lyricsManualTimer);
    // Stop an in-flight follow animation before the browser applies the user's wheel delta.
    if (!lyricsView.classList.contains("manualScroll")) lyricsView.scrollTo({ top: lyricsView.scrollTop, behavior: "auto" });
    lyricsView.classList.add("manualScroll");
    lyricsManualTimer = setTimeout(() => updateLyrics(true), 3000);
}, { passive: true });

minimizeButton.onclick = () => run("window", { text: "minimize" });
maximizeButton.onclick = () => run("window", { text: "maximize" });
closeButton.onclick = () => run("window", { text: "close" });
desktopButton.onclick = () => run("desktopLyrics");
previousButton.onclick = largePreviousButton.onclick = () => run("previous");
nextButton.onclick = largeNextButton.onclick = () => run("next");
toggleButton.onclick = largeToggleButton.onclick = () => run("toggle");
modeButton.onclick = largeModeButton.onclick = () => run("mode");
largeQueueButton.onclick = openQueue;
queueButton.onclick = () => { openPlayPage(true); setPlaySide(true, false); };
hideQueueButton.onclick = () => setPlaySide(false, lyricsShown);
clearQueueButton.onclick = async () => { if (await su5.dialog.confirm("清空播放队列？")) await run("clearQueue"); };
miniCoverButton.onclick = () => { setPlaySide(false, lyricsShown); openPlayPage(true); };
collapseButton.onclick = () => openPlayPage(false);
trackMoreButton.onclick = event => showTrackContext(player.Current ? [player.Current] : [], event.clientX, event.clientY, "play");
largeCover.oncontextmenu = event => { event.preventDefault(); showTrackContext(player.Current ? [player.Current] : [], event.clientX, event.clientY, "play"); };
eqButton.onclick = showEqualizer;
speedButton.onclick = event => showContext(speedMenuItem().children, event.clientX, event.clientY);
muteButton.onclick = () => { if (player.Volume > 0) { previousVolume = player.Volume; run("volume", { number: 0 }); } else run("volume", { number: previousVolume }); };
seekBar.progress.onchange = largeSeekBar.progress.onchange = number => run("seek", { number });
const updateVolumeIcons = () => {
    const glyph = icon(player.Volume <= 0 ? "F29E" : "F2A2");
    if (muteButton.innerHTML !== glyph) muteButton.innerHTML = largeVolumeButton.innerHTML = glyph;
    muteButton.title = player.Volume <= 0 ? "取消静音" : "静音";
};
let trackLeaveAt = 0;
const updateTrackDisplay = async (track, changed) => {
    const generation = ++trackDisplayGeneration;
    const elements = [playerBar.querySelector(".info"), largeCover, largeTitle.parentElement, lyricsView];
    // A refresh of the same song (new index revision) joins a fade-out still in flight but
    // never starts another transition: one song change plays exactly one animation.
    const leaving = elements.some(element => element.classList.contains("trackLoading"));
    changed = !!changed || leaving;
    if (changed && !leaving) trackLeaveAt = performance.now();
    trackDisplayPending = !!track;
    trackCoverImages.forEach(image => image.src = "");
    trackCoverImages = [];
    if (changed) elements.forEach(element => {
        element.classList.remove("trackChanging");
        element.classList.toggle("trackLoading", !!track);
    });
    const covers = [coverUrl(track), coverUrl(track, "play")];
    if (track) {
        trackCoverImages = covers.map(() => new Image());
        await Promise.all([
            ...trackCoverImages.map(async (image, index) => {
                image.src = covers[index];
                try { await image.decode(); }
                catch {
                    if (generation !== trackDisplayGeneration) return;
                    covers[index] = "assets/placeholder.png";
                    image.src = covers[index];
                    await image.decode().catch(() => {});
                }
            }),
            changed ? new Promise(resolve => setTimeout(resolve, Math.max(0, 130 - (performance.now() - trackLeaveAt)))) : Promise.resolve()
        ]);
        if (generation !== trackDisplayGeneration) return;
    }
    miniCover.src = covers[0];
    largeCover.src = covers[1];
    await Promise.all([miniCover.decode(), largeCover.decode()].map(ready => ready.catch(() => {})));
    if (generation !== trackDisplayGeneration) return;
    miniTitle.textContent = largeTitle.textContent = track?.title || "未播放";
    updateArtist(miniArtist, track?.artist);
    updateArtist(largeArtist, track?.artist);
    updateCoverTheme();
    trackDisplayPending = false;
    renderLyrics();
    elements.forEach(element => element.classList.remove("trackLoading"));
    if (track && changed) {
        void playerBar.offsetWidth;
        elements.forEach(element => element.classList.add("trackChanging"));
    }
};
const sendVolume = queueValue(number => run("volume", { number }));
const setVolume = number => {
    player.Volume = Math.max(0, Math.min(1, number));
    volumeBar.progress.value = largeVolumeBar.progress.value = player.Volume;
    updateVolumeIcons();
    return sendVolume(player.Volume);
};
volumeBar.progress.onchange = volumeBar.progress.ondrag = setVolume;
largeVolumeBar.progress.onchange = largeVolumeBar.progress.ondrag = setVolume;
const showVolume = open => { largeVolumeBar.hidden = !open; };
largeVolumeButton.onclick = () => showVolume(largeVolumeBar.hidden);
const volumeWheel = event => {
    if (!event.deltaY) return;
    event.preventDefault();
    setVolume(player.Volume + (event.deltaY < 0 ? .05 : -.05));
};
document.querySelector(".volBtnBottom").addEventListener("wheel", volumeWheel, { passive: false });
document.addEventListener("wheel", event => {
    if (!document.body.classList.contains("playerShown") || document.querySelector("dialog[open]")) return;
    if (event.target.closest(".largeVolumeControl")) showVolume(true);
    if (!largeVolumeBar.hidden) volumeWheel(event);
}, { passive: false });
document.addEventListener("pointerdown", event => { if (!event.target.closest(".largeVolumeControl")) showVolume(false); });
window.addEventListener("blur", () => showVolume(false));
lyricsButton.onclick = () => { lyricsShown = document.body.classList.contains("hideLyrics"); setPlaySide(false, lyricsShown); run("lyricsShown", { value: lyricsShown }); };

sideResizer.onpointerdown = event => {
    sideResizer.setPointerCapture(event.pointerId);
    sideResizer.onpointermove = event => simCss(document.documentElement).setProperty("--sideWidth", `${Math.max(150, Math.min(360, event.clientX))}px`);
    sideResizer.onpointerup = event => { sideResizer.onpointermove = null; sideResizer.releasePointerCapture(event.pointerId); setConfig("list.leftBarWidth", Math.max(150, Math.min(360, event.clientX))); };
};
document.addEventListener("keydown", event => {
    const key = event.key.toLowerCase();
    if (event.key === "Tab") {
        event.preventDefault();
        if (event.target.id === "searchInput") {
            searchSourceSelect.selectedIndex = (searchSourceSelect.selectedIndex + 1) % searchSourceSelect.options.length;
            searchSourceSelect.onchange();
        }
        return;
    }
    if (event.ctrlKey && ["i", "r"].includes(key)) { event.preventDefault(); return; }
    if (event.target.closest("dialog[open],textarea,select,[contenteditable]") || document.querySelector("dialog[open]")) return;
    const input = event.target.closest("input");
    if (input && !(input.type === "range" && input.closest("#playerBar,#playPage"))) return;
    if (event.key === " ") { event.preventDefault(); if (!event.repeat) run("toggle"); }
    if (["ArrowUp", "ArrowDown"].includes(event.key)) { event.preventDefault(); setVolume(player.Volume + (event.key === "ArrowUp" ? .05 : -.05)); }
    if (["ArrowLeft", "ArrowRight"].includes(event.key)) {
        event.preventDefault();
        if (event.ctrlKey) run(event.key === "ArrowRight" ? "next" : "previous");
        else run("seek", { number: Math.max(0, Math.min(player.Dur, player.Pos + (event.key === "ArrowRight" ? 5 : -5))) });
    }
    if (["w", "a", "s", "d"].includes(key) && !event.altKey && !event.metaKey) {
        event.preventDefault();
        const step = event.ctrlKey ? 10 : 1;
        run("moveDesktopLyrics", { dx: key === "a" ? -step : key === "d" ? step : 0, dy: key === "w" ? -step : key === "s" ? step : 0 });
    }
    if (event.ctrlKey && event.key.toLowerCase() === "f") { event.preventDefault(); navigate({ kind: "search" }); }
    if (event.ctrlKey && event.key.toLowerCase() === "o") { event.preventDefault(); run("openFiles"); }
    if (event.key === "Escape") { setPlaySide(false, lyricsShown); showVolume(false); openPlayPage(false); if (document.fullscreenElement) document.exitFullscreen(); }
    if (event.key === "F11" && document.body.classList.contains("playerShown")) { event.preventDefault(); if (document.fullscreenElement) document.exitFullscreen(); else document.documentElement.requestFullscreen(); }
}, true);

let catalogPollAt = 0;
let failureCount = 0;
const poll = async () => {
    try {
        updatePlayer(await api("snapshot"));
        failureCount = 0;
        connectionBanner.hidden = true;
        if (Date.now() > catalogPollAt) { catalogPollAt = Date.now() + 2000; await refreshCatalog(); }
    } catch (error) { if (++failureCount >= 3) connectionBanner.hidden = false; }
    setTimeout(poll, document.hidden ? 1000 : 250);
};
const start = async () => {
    await refreshCatalog();
    lyricsShown = catalog.lyricsShown;
    setPlaySide(false, lyricsShown);
    await navigate({ kind: "home" });
    poll();
};
window.simInitialReady = start().catch(() => { mainContent.innerHTML = emptyState(); connectionBanner.hidden = false; setTimeout(start, 1500); });



const pageToken = crypto.randomUUID();
const pageLoaded = document.readyState === "complete" ? Promise.resolve() : new Promise(resolve => window.addEventListener("load", resolve, { once: true }));
Promise.all([pageLoaded, document.fonts.ready, window.simInitialReady]).then(() => api("pageReady", { id: pageToken }));
window.addEventListener("beforeunload", () => navigator.sendBeacon("api", new Blob([JSON.stringify({ action: "pageLoading", id: pageToken })], { type: "application/json" })));