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
let queueSignature = "";
let catalogSignature = "";
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
// The play page is an extension's page, the bundled one or another: also a separate browser of the host,
// over the whole window. Opening, closing and what is shown beside the player are passed on to it.
let externalShown = false;
let externalKey = "";
let externalLive = true;
let playSide = "";
let playLayoutSignature = "";
let playWaiting = false;

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
    applyPlayTitle();
    // The page in use was exchanged (another style chosen, or the page went away and another one serves):
    // the play page stays open and shows the new one. It only closes when no page is left.
    if (externalShown && catalog.playPage?.key !== externalKey) {
        if (catalog.playPage) { setExternalShown(true); sendPlayView({ value: true, text: playSide }); }
        else closeExternalPlay();
    }
    if (page.kind === "menu" && !sidebarEntry(page.plugin, page.id)) await navigate({ kind: "home" });
    if (page.kind === "playlist" && !dragActive && !listMoving) syncPlaylistPage();
    if (page.kind === "plugins" && !dragActive) renderPlugins();
    if (page.kind === "plugin") syncPluginPage();
};
const applyConfig = () => {
    const config = catalog.config;
    const classes = { playBtnColor: "list.themedButton", themeBlur: "list.themeImageBlur", themeImage: "list.themeImage" };
    Object.entries(classes).forEach(([className, key]) => document.body.classList.toggle(className, !!config[key]));
    // The family is quoted for CSS; names come from the system's own font list.
    const appFont = String(config["general.font"] || "").replace(/["\\]/g, "");
    if (appFont) simCss(document.documentElement).setProperty("--appFont", `"${appFont}"`); else simCss(document.documentElement).removeProperty("--appFont");
    simCss(document.documentElement).setProperty("--sideWidth", `${Math.min(360, Math.max(150, config["list.leftBarWidth"] || 200))}px`);
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

// An entry an extension keeps in the sidebar: in its own section, or in the host's tools group.
const sidebarEntry = (pluginId, id) => {
    const sidebar = catalog.plugins.find(item => item.manifest.id === pluginId)?.sidebar;
    return [...(sidebar?.items || []), ...(sidebar?.tools || [])].find(item => item.id === id);
};
// The entry of the sidebar that stands for the page shown: the page of one extension belongs to "扩展".
const activeNavTarget = () => JSON.stringify(page.kind === "plugin" ? { kind: "plugins" } : page);
const navButton = (label, code, target) => {
    const button = document.createElement("button");
    button.className = "navItem";
    button.innerHTML = `${code ? icon(code) : ""}<span>${escapeText(label)}</span>`;
    button.navTarget = JSON.stringify(target);
    button.classList.toggle("active", button.navTarget === activeNavTarget());
    // A click on the entry already shown leaves every page for the placeholder shown at start.
    button.onclick = () => navigate(button.classList.contains("active") ? { kind: "home" } : target);
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
    const pluginEntry = (id, item, code = "") => {
        const button = navButton(item.name, code, { kind: "menu", plugin: id, id: item.id });
        button.oncontextmenu = async event => {
            event.preventDefault();
            const actions = await run("sidebarMenu", { plugin: id, menu: item.id });
            const items = (actions || []).map(action => action.separator ? { separator: true } : { label: action.label, icon: action.icon, action: () => run("sidebarMenuAction", { plugin: id, menu: item.id, key: action.key }) });
            if (items.length) showContext(items);
        };
    };
    catalog.plugins.filter(plugin => plugin.sidebar?.title).forEach(plugin => {
        const id = plugin.manifest.id;
        navGroup(plugin.sidebar.title, plugin.sidebar.canAdd ? () => run("sidebarAdd", { plugin: id }) : null);
        (plugin.sidebar.items || []).forEach(item => pluginEntry(id, item));
    });
    navGroup("歌单", createPlaylist);
    catalog.playlists.forEach(playlist => {
        const target = { kind: "playlist", id: playlist.id };
        const button = navButton(playlist.name, "", target);
        button.oncontextmenu = event => {
            event.preventDefault();
            showContext([
                { label: "重命名", action: async () => { const name = await askName("重命名歌单", playlist.name); if (name) { await run("renamePlaylist", { id: playlist.id, text: name }); await refreshCatalog(); } } },
                { label: "删除歌单", action: async () => { if (!await su5.dialog.confirm(`删除「${playlist.name}」？`)) return; await run("deletePlaylist", { id: playlist.id }); await refreshCatalog(); } }
            ]);
        };
    });
    // The tools group is the host's: its name and the absence of an add button are fixed. Extensions with
    // the sidebar permission put entries of their own after the host's two.
    navGroup("工具");
    navButton("搜索", icons.search, { kind: "search" });
    // Always present, folded away while there are no tasks, so it can animate in and out.
    const downloadButton = navButton("下载", icons.downloads, { kind: "downloads" });
    downloadButton.id = "downloadNav";
    downloadButton.classList.add("collapsible");
    downloadButton.insertAdjacentHTML("beforeend", `<span id="downloadBadge" class="navBadge"></span>`);
    updateDownloadNav();
    catalog.plugins.filter(plugin => plugin.sidebar).forEach(plugin => (plugin.sidebar.tools || []).forEach(item => pluginEntry(plugin.manifest.id, item, /^[0-9a-f]{4,5}$/i.test(item.icon || "") ? item.icon : icons.plugins)));
    navGroup("配置");
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
const elementBox = element => { const rect = element.getBoundingClientRect(); return [Math.floor(rect.left), Math.floor(rect.top), Math.ceil(rect.width), Math.ceil(rect.height)]; };
const layoutPanel = () => {
    if (!shownPanel) return;
    requestAnimationFrame(layoutPanel);
    const content = mainContent.getBoundingClientRect();
    const box = elementBox;
    const value = {
        left: Math.ceil(content.left) + 1, top: 0, right: 0, bottom: Math.max(0, Math.ceil(innerHeight - playerBar.getBoundingClientRect().top)),
        visible: !externalShown && !document.fullscreenElement,
        holes: [...document.querySelectorAll(".titleActions, .dropdown.active, .dragGhost, #connectionBanner")].map(box).filter(rect => rect[2] > 0 && rect[3] > 0)
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
    sidebarNav.querySelectorAll(".navItem").forEach(button => button.classList.toggle("active", button.navTarget === activeNavTarget()));
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
    if (page.kind === "plugin") { renderPluginPage(); return; }
    if (page.kind === "downloads") { mainContent.replaceChildren(); renderDownloads(); return; }
    if (page.kind === "playlist") {
        const playlist = catalog.playlists.find(item => item.id === page.id);
        if (!playlist) { mainContent.innerHTML = emptyState(); return; }
        viewTracks = playlist.tracks || [];
        renderTrackPage(playlist.name);
        return;
    }
    if (page.kind === "menu") {
        const name = sidebarEntry(page.plugin, page.id)?.name || "";
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
const trackPageDetails = details => `${listDetails(details) ? `${listDetails(details)}<br>` : ""}<span class="listSummary">${musicListSummary(viewTracks)}</span>`;
const renderTrackPage = (title, details) => {
    mainContent.innerHTML = `<div id="musicListContainer"><div class="playlistHeader"><img src="${coverUrl(viewTracks[0])}"><section><h1>${escapeText(title)}</h1><div class="details">${trackPageDetails(details)}</div><div class="buttonGroup"><button id="playAllButton">${icon("F508")} 全部播放</button><button id="appendAllButton" class="sub squareButton" title="添加到播放列表">${icon("F00F")}</button></div></section></div><div class="tableContainer"><div id="trackList"></div></div>${catalog.config["list.showLocate"] ? `<button id="locateButton" class="iconButton locateButton inactive" title="定位当前曲目">${icon("F3B5")}</button>` : ""}</div>`;
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
// The list shown changed: songs added, removed or moved. The page stays where it is scrolled; songs that
// leave fade out first, then the others slide to their new places and new ones fade in.
let listUpdateToken = 0;
let listMoving = 0;
const updateTrackPage = async (tracks, title, details) => {
    if (!window.trackList || !mainContent.querySelector(".playlistHeader")) return;
    const token = ++listUpdateToken;
    const generation = pageGeneration;
    const keys = new Set(tracks.map(trackKey));
    const leaving = [...trackList.querySelectorAll(".trackRow")].filter(row => !keys.has(trackKey(tableTracks[Number(row.dataset.row)])));
    if (leaving.length) {
        leaving.forEach(row => row.classList.add("trackLeaving"));
        await new Promise(resolve => setTimeout(resolve, 150));
        if (token !== listUpdateToken || generation !== pageGeneration) return;
    }
    songInfo.hydrate(tracks);
    viewTracks = tracks;
    selectedKeys.forEach(key => { if (!keys.has(key)) selectedKeys.delete(key); });
    const header = mainContent.querySelector(".playlistHeader");
    header.querySelector("h1").textContent = title;
    header.querySelector(".details").innerHTML = trackPageDetails(details);
    if (header.firstElementChild.getAttribute("src") !== coverUrl(viewTracks[0])) header.firstElementChild.src = coverUrl(viewTracks[0]);
    playAllButton.disabled = appendAllButton.disabled = !viewTracks.length;
    refreshTable(true);
};
// The playlist shown follows the catalog: a change of its songs or its name is applied in place.
const syncPlaylistPage = () => {
    const playlist = catalog.playlists.find(item => item.id === page.id);
    if (!playlist) { navigate({ kind: "home" }); return; }
    const tracks = playlist.tracks || [];
    const shown = mainContent.querySelector(".playlistHeader h1")?.textContent;
    if (shown === playlist.name && tracks.map(trackKey).join("\n") === viewTracks.map(trackKey).join("\n")) return;
    updateTrackPage(tracks, playlist.name);
};
// The entry of an extension shown is opened again: its new list replaces the old one in place.
const reloadMenuPage = async () => {
    const target = page;
    menuOpening++;
    const list = await run("sidebarOpen", { plugin: target.plugin, menu: target.id });
    menuOpening--;
    if (page !== target || list?.panel || !list?.tracks) return;
    updateTrackPage(list.tracks, list.title || sidebarEntry(target.plugin, target.id)?.name || "", list.details);
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
// The rows are drawn again for the songs listed now, where the list is scrolled. Animated, the rows that
// were on screen slide over from where they were, and songs new to the list fade in.
const refreshTable = animate => {
    const body = trackList.querySelector("tbody");
    if (!viewTracks.length || !body) { renderTable(); return; }
    const known = new Set(tableTracks.map(trackKey));
    const places = new Map([...body.querySelectorAll(".trackRow")].map(row => [trackKey(tableTracks[Number(row.dataset.row)]), row.getBoundingClientRect().top]));
    tableTracks = orderedTracks();
    tableStart = -1;
    renderVisibleTracks(true);
    updateLocator();
    if (!animate) return;
    body.querySelectorAll(".trackRow").forEach(row => {
        const key = trackKey(tableTracks[Number(row.dataset.row)]);
        if (!known.has(key)) { row.classList.add("trackEntering"); return; }
        const shift = places.has(key) ? places.get(key) - row.getBoundingClientRect().top : 0;
        if (!shift) return;
        const style = simCss(row);
        style.transform = `translateY(${shift}px)`;
        row.getBoundingClientRect();
        row.classList.add("trackMoving");
        style.removeProperty("transform");
        setTimeout(() => row.classList.remove("trackMoving"), 250);
    });
};
const highlightRow = (row, track) => {
    if (!highlightRegex) return;
    const cells = row.children;
    setCellText(cells[1], track.title);
    if (!cells[2].classList.contains("unknownArtist")) setCellText(cells[2], track.artist);
    setCellText(cells[3], track.album);
};
// Rows are kept by position while scrolling; rebuild draws every row again after the list changed.
const renderVisibleTracks = (rebuild = false) => {
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
        if (rebuild || index < start || index >= end) row.remove();
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
                commit: async (from, to) => {
                    // The catalog read meanwhile still has the old order; the list is compared again once the move is saved.
                    const shown = page;
                    moveInList(viewTracks, from, to);
                    refreshTable(false);
                    listMoving++;
                    await run("movePlaylist", { id: page.id, index: from, to });
                    listMoving--;
                    await refreshCatalog();
                    if (page === shown) syncPlaylistPage();
                }
            }));
        };
        row.oncontextmenu = event => {
            event.preventDefault();
            if (!selectedKeys.has(trackKey(track))) { selectedKeys.clear(); selectedKeys.add(trackKey(track)); updateRowStates(); }
            showTrackContext(viewTracks.filter(item => selectedKeys.has(trackKey(item))));
        };
        anchor = row.nextSibling;
    });
    songInfo.table(visibleTracks);
    dragRefresh?.();
};
window.addEventListener("resize", () => { tableStart = -1; renderVisibleTracks(); });
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
// Menus are drawn by the host as windows above this one: they fade and cast their shadow whatever is
// beneath them, the play page included, and open where the pointer is. The rows are described once; what
// a row does stays here, and
// the rows of a row that reads them when opened are sent when the host asks.
let shownMenu = null;
let menuSerial = 0;
// A separator only stands between rows: one that would open or close a list, or follow another, is left out.
const menuRows = items => shownRows(items).map(item => item.separator ? { separator: true } : { label: String(item.label ?? ""), icon: menuIcon(item.icon), children: Array.isArray(item.children) ? menuRows(item.children) : undefined, lazy: typeof item.children === "function" });
// The rows as they were sent: a path counts the rows shown, without the separators left out.
const shownRows = items => (items || []).filter((item, index, list) => !item.separator || index > 0 && index < list.length - 1 && !list[index - 1].separator && list.slice(index + 1).some(later => !later.separator));
const menuRow = path => path.reduce((row, index) => shownRows(row.loaded || row.children)[index], { children: shownMenu.items });
const showContext = items => {
    shownMenu = { id: ++menuSerial, items };
    api("menu", { value: { id: shownMenu.id, items: menuRows(items) } }).catch(() => {});
};
const handleMenuEvent = async request => {
    if (!shownMenu || request.menu !== shownMenu.id) return;
    const menu = shownMenu;
    if (request.event === "closed") { shownMenu = null; return; }
    const row = menuRow(request.path);
    if (request.event === "action") { shownMenu = null; row.action?.(); return; }
    try { row.loaded = await row.children() || []; }
    catch (error) { row.loaded = [{ label: error.message }]; }
    if (shownMenu === menu) api("menuChildren", { value: { id: menu.id, path: request.path, items: menuRows(row.loaded) } }).catch(() => {});
};
// Notices are windows of the host as well.
su5.toast = text => { api("toast", { text: String(text) }).catch(() => {}); };
// A dialog is a window of its own, modal to this one. It is built here as before; showing it opens the
// window and moves the dialog into it, with its handlers, and the window is as large as the dialog.
const dialogWindows = new Map();
let dialogSerial = 0;
const dialogOpen = () => dialogWindows.size > 0;
su5.dialog.find = id => { for (const entry of dialogWindows.values()) { const found = entry.popup?.document.getElementById(id); if (found) return found; } };
su5.dialog.show = (id, isShow = true) => {
    const shown = dialogWindows.get(id);
    if (!isShow) {
        document.getElementById(id)?.remove();
        if (!shown || shown.closing) return;
        shown.closing = true;
        dialogWindows.delete(id);
        api("dialogWindow", { id: shown.name, text: "close" }).catch(() => {});
        return;
    }
    const dialog = document.getElementById(id);
    if (!dialog || shown) return;
    const box = dialog.firstElementChild;
    const entry = { id, dialog, name: `simDialog${++dialogSerial}`, width: box.offsetWidth, closing: false };
    dialogWindows.set(id, entry);
    entry.popup = window.open("dialog.html", entry.name, `popup,width=${entry.width},height=${box.offsetHeight}`);
    if (!entry.popup) dialogWindows.delete(id);
};
// The window has loaded: the dialog moves in and the window is shown at its size.
window.dialogReady = popup => {
    const entry = [...dialogWindows.values()].find(item => item.name === popup.name);
    if (!entry) { popup.close(); return; }
    const page = popup.document;
    page.body.append(entry.dialog);
    simCssAdopt(entry.dialog);
    const font = getComputedStyle(document.documentElement).getPropertyValue("--appFont");
    if (font) simCss(page.documentElement).setProperty("--appFont", font);
    simCss(page.documentElement).setProperty("--dialogMax", `${Math.max(320, Math.round(screen.availHeight * .85))}px`);
    const box = entry.dialog.firstElementChild;
    let height = 0;
    const fit = () => { if (box.offsetHeight === height) return; height = box.offsetHeight; return api("dialogWindow", { id: entry.name, text: "size", index: entry.width, to: height }).catch(() => {}); };
    new popup.ResizeObserver(fit).observe(box);
    // Closed from outside (its close button, Escape, Alt+F4): the dialog is cancelled. A dialog that asks
    // something answers as its cancel button does, one with a single button as that button does; any
    // other dialog simply goes, and none of its buttons (a reset, say) is pressed for it.
    popup.addEventListener("pagehide", () => {
        if (entry.closing) return;
        entry.closing = true;
        dialogWindows.delete(entry.id);
        const footer = [entry.dialog.querySelector(".buttons"), entry.dialog.querySelector(".modalContent > :last-child")].find(element => element?.querySelector("button")) || entry.dialog;
        const buttons = footer.querySelectorAll("button");
        (footer.querySelector("button.cancel") || (buttons.length === 1 ? buttons[0] : null))?.click();
    });
    Promise.resolve(fit()).then(() => api("dialogWindow", { id: entry.name, text: "show" })).then(() => entry.dialog.querySelector("input:not([type=checkbox]):not([type=range]), textarea")?.select?.()).catch(() => {});
};
const playlistActions = tracks => [
    // A playlist that already holds every selected song is ticked; choosing it takes them out again.
    ...catalog.playlists.map(playlist => {
        const keys = new Set((playlist.tracks || []).map(trackKey));
        const included = tracks.every(track => keys.has(trackKey(track)));
        return { label: playlist.name, icon: included ? "EB7B" : "", action: async () => {
            if (included) await run("removePlaylist", { id: playlist.id, tracks });
            else { const count = await run("addPlaylist", { id: playlist.id, tracks }); if (count !== null) su5.toast(`已添加 ${count} 首曲目`); }
            await refreshCatalog();
        } };
    }),
    { separator: true },
    { label: "新建歌单", icon: "EA13", action: async () => { const id = await createPlaylist(); if (id) { await run("addPlaylist", { id, tracks }); await refreshCatalog(); } } }
];
let trackContextGeneration = 0;
const showTrackContext = async (tracks, scene = "list", context = null) => {
    if (!tracks.length) return;
    const generation = ++trackContextGeneration;
    // Entries that would change nothing are left out: the playing song is not offered "play",
    // the playing or upcoming song not "play next", and queued songs not "add to queue".
    const queued = new Map(player.Queue.map((track, index) => [trackKey(track), index]));
    const only = tracks.length === 1 ? queued.get(trackKey(tracks[0])) : undefined;
    const isCurrent = only !== undefined && only === player.Index;
    const items = [];
    const playList = tracks.length === 1 && context?.list?.length ? context.list : tracks;
    const playAt = Math.max(0, playList.findIndex(track => trackKey(track) === trackKey(tracks[0])));
    if (!isCurrent) items.push({ label: "播放", icon: "F00B", action: () => scene === "queue" && only !== undefined ? run("playIndex", { index: only }) : run("play", { tracks: playList, index: playAt }) });
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
        { label: "清空播放列表", icon: "EC2A", action: async () => { if (await su5.dialog.confirm("清空播放队列？")) await run("clearQueue"); } },
        { label: "停止播放", icon: "F1A1", action: () => run("stop") },
        { label: "播放页样式", icon: "F2C4", action: () => showPlayPages() }
    );
    if (scene === "list" && !context?.external && page.kind === "playlist") items.push({ label: "从歌单移除", icon: "EC2A", action: async () => { await run("removePlaylist", { id: page.id, tracks }); await refreshCatalog(); } });
    const pluginItems = actions => (actions || []).map(action => ({ label: action.label, icon: action.icon, children: action.dynamic ? async () => pluginItems(await api("trackActions", { tracks, scene, parent: action.key })) : action.children?.length ? pluginItems(action.children) : null, action: () => run("trackAction", { tracks, scene, key: action.key }) }));
    const actions = await run("trackActions", { tracks, scene });
    if (generation !== trackContextGeneration) return;
    if (actions?.length) items.push({ separator: true }, ...pluginItems(actions));
    items.push({ separator: true }, { label: "刷新索引缓存", icon: "ED0F", action: () => clearSongInfo(tracks, scene) });
    showContext(items);
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
// 2024: the words of the search text are marked in the result rows. A word in an alphabet (Latin,
// Cyrillic, Greek, digits) only matches as a whole word: "never" is not marked inside "Whenever".
// Other text (Chinese and the like) matches anywhere; marks separated by one space join into one.
const highlightPattern = text => {
    const letter = "[\\p{Script=Latin}\\p{Script=Cyrillic}\\p{Script=Greek}\\p{N}]";
    const alphabetic = new RegExp(`^(?:${letter}|['’-])+$`, "u");
    const words = splitWords(text).map(word => {
        const escaped = word.replace(/[\/\\^$*+?.()|[\]{}]/g, "\\$&");
        return alphabetic.test(word) ? `(?<!${letter})${escaped}(?!${letter})` : escaped;
    });
    return words.length ? new RegExp(`(?:${words.join("|")})(?: (?:${words.join("|")}))*`, "giu") : null;
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
    ["界面设置", [["list.showLocate", "显示曲目定位按钮", "toggle"], ["list.themedButton", "对播放按钮应用主题色", "toggle"], ["list.themeImage", "启用主题图片", "toggle", "在 SimMusic 主界面显示主题图片。"], ["list.themeImageType", "主题图片来源", "select", "", [["cover", "曲目封面"], ["local", "本地文件"]]], ["list.themeImageBlur", "图片模糊效果", "toggle"], ["chooseTheme", "选择本地主题图片", "button"], ["playPage", "播放页选项", "button"]]],
    ["播放控制", [["ctl.quickReplay", "快速重播", "toggle", "在曲目即将结束时按「快退」按钮以回到当前曲目的开头。"], ["ctl.fade", "音频淡入淡出", "toggle", "在按下「播放」或「暂停」时对音频的输出音量使用渐变效果。"], ["audio.device", "音频输出设备", "select", "", [["", "系统默认"]]], ["equalizer", "均衡器", "button"], ["lrc.perWord", "逐字歌词解析", "select", "", [["source", "按原歌词处理"], ["extend", "将原歌词中存在的逐字歌词扩展到全曲"], ["all", "所有歌曲使用逐字歌词"]]]]],
    ["睡眠定时", [["sleep.extendToEnd", "延长时间至曲目结尾", "toggle", "在曲目结尾后再暂停播放；在延长时间时执行暂停、切歌操作将会退出睡眠定时。"], ["sleep.action", "定时结束后", "select", "选择「关机」或「休眠」时，SimMusic 将在执行操作前 60 秒弹出提示，若您还需使用设备，可随时取消。", [["none", "不执行操作"], ["shutdown", "关机"], ["sleep", "休眠"]]], ["sleepTimer", "开启睡眠定时", "button"]]],
    ["歌词视图", [["lrc.multiLang", "歌词多语言支持", "toggle", "开启后，时间戳一致的不同歌词将作为多语言翻译同时渲染。此配置项切换曲目生效。"], ["lrc.transSize", "翻译字号", "slider", "同时控制歌词视图与桌面歌词中的翻译字号。", [.5, 1]]]],
    ["桌面歌词", [["dl.rememberState", "记忆开启状态", "toggle"], ["dl.compact", "精简模式", "toggle", "开启后，桌面歌词窗口将在暂停时自动隐藏，且不显示播放控件"], ["dl.hideCapture", "在截图中隐藏", "toggle", "其他应用截图或录屏时隐藏桌面歌词的内容，与多数截图或录屏软件相兼容，支持 Windows 10 2004 以上版本及 Windows 11。此功能不会影响您查看歌词，对采集卡等外置硬件无效。"], ["dl.autoClose", "在播放页自动关闭", "toggle", "当 SimMusic 主窗口打开播放页时自动关闭桌面歌词。"], ["dl.showTrans", "显示歌词翻译", "toggle"], ["dl.color", "字体颜色", "color"], ["dl.border", "启用边框", "toggle"], ["dl.borderColor", "边框颜色", "color"], ["dl.size", "字体大小", "slider", "", [20, 60]], ["dl.width", "歌词区域宽度", "slider", "", [500, 1920]], ["dl.center", "始终居中", "toggle", "无视用户左右拖拽操作，保持桌面歌词在屏幕中央。"]]],
    ["曲目下载", [["down.parallel", "并行下载数", "select", "下载在线歌曲时并行下载的任务数量。", [1, 2, 3, 4, 5].map(value => [String(value), `${value} 首曲目`])], ["down.nameFormat", "命名格式", "text", "可使用 [title] 表示歌曲名，[artist] 表示艺术家。SimMusic 会自动处理 Windows 无法写入的文件名。"], ["down.writeTitle", "写入曲目标题", "toggle"], ["down.writeArtist", "写入艺术家与专辑名", "toggle"], ["down.writeCover", "写入专辑封面", "toggle"], ["down.lyricMode", "歌词下载方式", "select", "", [["embed", "嵌入音频"], ["file", "独立 LRC"], ["none", "不下载歌词"]]], ["chooseDownloadDir", "下载目录", "button"]]]
];
const settingActions = { playPage: () => showPlayPages(), chooseTheme: () => run("chooseTheme"), chooseDownloadDir: async () => { await run("chooseDownloadDir"); await refreshCatalog(); showDownloadDir(); }, equalizer: () => showEqualizer(), sleepTimer: () => showSleepTimer(), clearIndex: async () => { if (await run("clearIndex") === null) return; lastCurrentKey = ""; updateIndexSize(); } };
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
// Each family previews itself: its entry in the list is drawn in that font. That loads every installed
// font, so it happens behind the page, a few entries at a time while the page is idle.
const previewFonts = row => {
    const options = [...row.querySelectorAll(".SimsvOptionsContainer > div")];
    let next = 0;
    const step = deadline => {
        if (!row.isConnected) return;
        for (let count = 0; next < options.length && count < 4 && (!count || deadline.timeRemaining() > 4); count++, next++)
            simCss(options[next]).fontFamily = `"${(options[next].dataset.value || "Microsoft YaHei").replace(/["\\]/g, "")}", "Microsoft YaHei"`;
        if (next < options.length) requestIdleCallback(step, { timeout: 500 });
    };
    requestIdleCallback(step, { timeout: 500 });
};
// Lists that take time to read never hold the settings page back. It is drawn at once with what the
// last visit read, or with the saved choice alone, and the row is exchanged when the list arrives.
const listedOptions = { "audio.device": () => outputDevices(), "general.font": () => fontOptions() };
const knownOptions = {};
const settingItem = ([key, label, type, desc = "", extra]) => {
    const saved = catalog.config[key] || "";
    const pending = [{ value: saved, label: key === "general.font" ? saved || "微软雅黑" : saved ? "" : "系统默认" }];
    return { key, label, type, desc: key === "chooseDownloadDir" ? catalog.downloadDir : desc, button: key === "clearIndex" ? "清理" : "", options: listedOptions[key] ? knownOptions[key] || pending : type === "select" ? extra.map(([value, label]) => ({ value, label })) : undefined, min: extra?.[0], max: extra?.[1] };
};
const renderSettings = async () => {
    const generation = pageGeneration;
    mainContent.innerHTML = pageHeader("软件设置", "", icons.settings) + `<div class="pageBody" id="settingsBody"></div>`;
    settingGroups.forEach(([title, items]) => appendSettingsSection(title, items.map(settingItem), catalog.config, settingsBody));
    // Rows that depend on a switch that is off are folded before the page is first drawn.
    updateSettingDependencies(false);
    const fontRow = () => settingsBody.querySelector('[data-setting-key="general.font"]');
    if (knownOptions["general.font"]) previewFonts(fontRow());
    Object.entries(listedOptions).forEach(async ([key, read]) => {
        const shown = JSON.stringify(knownOptions[key]);
        const options = await read();
        if (generation !== pageGeneration || !window.settingsBody || JSON.stringify(options) === shown) return;
        knownOptions[key] = options;
        const definition = settingGroups.flatMap(group => group[1]).find(item => item[0] === key);
        settingsBody.querySelector(`[data-setting-key="${key}"]`).replaceWith(makeSetting(settingItem(definition), catalog.config));
        if (key === "general.font") previewFonts(fontRow());
    });
    updateIndexSize();
    for (const plugin of catalog.plugins.filter(item => item.running)) {
        const data = await run("pluginSettings", { plugin: plugin.manifest.id });
        if (generation !== pageGeneration) return;
        const items = (data?.sections || []).flatMap(section => section.items || []);
        if (items.length) appendSettingsSection(`扩展：${plugin.manifest.name}`, items, data.values, settingsBody, plugin.manifest.id);
    }
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
    content.innerHTML = `<div class="timerPresets">${[5, 10, 15, 30, 60].map(minutes => `<button class="sub" data-minutes="${minutes}">${minutes} 分钟</button>`).join("")}</div><form class="timerCustom"><span>自定义时长</span><label class="input"><input name="minutes" inputmode="numeric" maxlength="4" value="30"><span>分钟</span></label></form><div class="buttons"><button class="sub cancel">取消</button><button>开启定时</button></div>`;
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
// the drag entered, so the drop only says which page it landed on. Each .simex opens the installer in turn, a folder
// becomes a local directory, and audio files join the playlist or local directory being viewed.
document.addEventListener("dragover", event => { if (event.dataTransfer.types.includes("Files")) event.preventDefault(); });
document.addEventListener("drop", async event => {
    if (!event.dataTransfer.types.includes("Files")) return;
    event.preventDefault();
    if (dialogOpen()) return;
    const target = page;
    const result = await run("dropFiles", { text: target.kind, plugin: target.plugin || "", id: target.id || "" });
    // A playlist follows the catalog by itself; a local directory is read again.
    if (result?.refresh) { await refreshCatalog(); if (page === target && page.kind === "menu") reloadMenuPage(); }
    if (result?.packages) installDropped(result.packages);
});
// Several packages dropped at once are offered one after another, each in its own installer.
// A new drop replaces the packages still waiting; the line that is running takes them on.
let droppedWaiting = 0;
let installingDropped = false;
const installDropped = async count => {
    droppedWaiting = count;
    if (installingDropped) return;
    installingDropped = true;
    try {
        while (droppedWaiting > 0) {
            droppedWaiting--;
            let manifest;
            try { manifest = await api("nextDroppedPackage"); }
            catch (error) { await showError("操作失败", error); continue; }
            if (!manifest) return;
            await confirmInstall(manifest);
        }
    } finally { installingDropped = false; }
};
// The page is drawn once; after that the cards follow the catalog in place, so the list keeps where it is
// scrolled. Cards that leave fold away, the others slide to their new places and new ones fade in.
const renderPlugins = () => {
    const fresh = !window.pluginCards;
    if (fresh) {
        mainContent.innerHTML = pageHeader("扩展程序", "", icons.plugins) + `<div class="pageBody pluginBody"><div class="pluginInstall"><div><span>安装扩展</span></div><button id="pluginFolderButton" class="iconButton" title="打开扩展目录">${icon("ED6A")}</button><button id="loadPluginFolderButton" class="sub">从文件夹加载</button><button id="installPluginButton">安装扩展</button><input id="pluginPackageInput" type="file" accept=".simex" multiple hidden></div><div id="pluginCards"></div></div>`;
        pluginFolderButton.onclick = () => run("pluginFolder");
        installPluginButton.onclick = () => pluginPackageInput.click();
        // Several packages chosen at once are offered one after another.
        pluginPackageInput.onchange = async () => {
            const files = [...pluginPackageInput.files];
            pluginPackageInput.value = "";
            for (const file of files) {
                try {
                    const response = await fetch("extensionPackage", { method: "POST", body: file });
                    const payload = await response.json();
                    if (!response.ok) throw new Error(payload.error);
                    await confirmInstall(payload.result);
                } catch (error) { await showAlert(error.message); }
            }
        };
        loadPluginFolderButton.onclick = async () => confirmInstall(await run("prepareInstall"), true);
    }
    const shownCards = () => [...pluginCards.querySelectorAll(".pluginCard:not(.pluginLeaving)")];
    const cards = new Map(shownCards().map(card => [card.dataset.plugin, card]));
    const places = new Map([...cards].map(([id, card]) => [id, card.getBoundingClientRect().top]));
    const ids = new Set(catalog.plugins.map(plugin => plugin.manifest.id));
    cards.forEach((card, id) => { if (!ids.has(id)) removePluginCard(card); });
    const ordered = catalog.plugins.map(plugin => {
        let card = cards.get(plugin.manifest.id);
        if (!card) {
            card = createPluginCard(plugin.manifest.id);
            if (!fresh) card.classList.add("pluginEntering");
        }
        fillPluginCard(card, plugin);
        return card;
    });
    const current = shownCards();
    if (ordered.some((card, index) => current[index] !== card)) pluginCards.append(...ordered);
    ordered.forEach(card => {
        const shift = places.has(card.dataset.plugin) ? places.get(card.dataset.plugin) - card.getBoundingClientRect().top : 0;
        if (!shift) return;
        const style = simCss(card);
        style.transform = `translateY(${shift}px)`;
        card.getBoundingClientRect();
        card.classList.add("pluginMoving");
        style.removeProperty("transform");
        setTimeout(() => card.classList.remove("pluginMoving"), 250);
    });
};
const removePluginCard = card => {
    const style = simCss(card);
    card.classList.add("pluginLeaving");
    style.height = `${card.offsetHeight}px`;
    card.getBoundingClientRect();
    card.classList.add("pluginCollapsing");
    style.height = "0px";
    setTimeout(() => card.remove(), 250);
};
const createPluginCard = id => {
    const card = document.createElement("div");
    card.className = "pluginCard";
    card.dataset.plugin = id;
    card.innerHTML = `<div class="pluginDetails"></div><div class="pluginTools"></div>`;
    const tools = card.lastElementChild;
    const handle = document.createElement("button");
    handle.className = "iconButton pluginDragHandle";
    handle.textContent = "⋮⋮";
    handle.title = "拖动调整扩展加载顺序";
    handle.onpointerdown = event => {
        if (event.button !== 0) return;
        event.preventDefault();
        const cards = [...pluginCards.querySelectorAll(".pluginCard:not(.pluginLeaving)")];
        startDragSort({
            event, element: card, index: cards.indexOf(card), count: cards.length, scroller: pluginCards.closest(".pageBody"), step: card.offsetHeight + 5, edges: [70, 80],
            items: () => cards.map((node, position) => [node, position]),
            commit: async (from, to) => {
                const order = catalog.plugins.map(item => item.manifest.id);
                moveInList(order, from, to);
                pluginCards.insertBefore(card, cards[to + (to > from)] || null);
                await run("pluginOrder", { order });
                await refreshCatalog();
                if (page.kind === "plugins") renderPlugins();
            }
        });
    };
    card.prepend(handle);
    tools.append(makeToggle("", false, async value => { await run("pluginEnabled", { plugin: id, value }); await refreshCatalog(); }));
    // Everything else about one extension is on a page of its own.
    const details = document.createElement("button");
    details.className = "iconButton";
    details.innerHTML = icon("EA6E");
    details.onclick = () => navigate({ kind: "plugin", plugin: id });
    tools.append(details);
    card.querySelector(".pluginDetails").onclick = () => navigate({ kind: "plugin", plugin: id });
    return card;
};
// A card is written again only when what it shows changed; the switch only follows a change made elsewhere.
const fillPluginCard = (card, plugin) => {
    const manifest = plugin.manifest;
    const state = pluginState(plugin);
    if (card.pluginState !== state) {
        card.pluginState = state;
        card.querySelector(".pluginDetails").innerHTML = `<h2>${escapeText(manifest.name)}${plugin.builtin ? `<span class="badge">内置</span>` : ""}${plugin.development ? `<span class="badge">开发</span>` : ""}${!plugin.running ? `<span class="badge">${plugin.enabled ? "加载失败" : "已停用"}</span>` : ""}</h2><div class="pluginMeta">${escapeText(manifest.id)} · ${escapeText(manifest.version)}${manifest.author ? ` · ${escapeText(manifest.author)}` : ""}</div><p>${escapeText(manifest.description)}</p>${plugin.error ? `<p class="pluginError">${escapeText(plugin.error)}</p>` : ""}`;
        card.querySelector(".pluginTools > .iconButton").title = `${manifest.name} · 详情`;
    }
    const toggle = card.querySelector(".toggleControl");
    const input = toggle.querySelector("input");
    if (input.checked !== plugin.enabled) { input.checked = plugin.enabled; toggle.classList.toggle("on", plugin.enabled); }
};
// One extension on one page, as a browser's extension page has it: whether it is enabled, what it is and
// how it fares, its permissions, its settings, its log, and reloading or removing it.
const pluginState = plugin => JSON.stringify([plugin.manifest, plugin.enabled, plugin.running, plugin.error, plugin.builtin, plugin.development]);
let pluginPageState = "";
let pluginLogTimer;
const renderPluginPage = async () => {
    const generation = pageGeneration;
    const plugin = catalog.plugins.find(item => item.manifest.id === page.plugin);
    if (!plugin) { navigate({ kind: "plugins" }); return; }
    const manifest = plugin.manifest;
    const scroll = window.pluginPageBody?.scrollTop || 0;
    pluginPageState = pluginState(plugin);
    mainContent.innerHTML = `<div class="pageHeader pluginPageHeader"><button id="pluginBackButton" class="iconButton" title="返回扩展列表">${icon("EA64")}</button><div><h1>${escapeText(manifest.name)}</h1></div></div><div class="pageBody" id="pluginPageBody"></div>`;
    pluginBackButton.onclick = () => navigate({ kind: "plugins" });
    const section = (title, ...nodes) => {
        const node = document.createElement("section");
        node.className = "settingsSection";
        const heading = document.createElement("h3");
        heading.textContent = title;
        node.append(heading, ...nodes);
        pluginPageBody.append(node);
        return node;
    };
    const card = () => { const node = document.createElement("div"); node.className = "settingsCard"; return node; };
    const row = (label, desc, control) => {
        const node = document.createElement("div");
        node.className = "settingRow";
        node.innerHTML = `<div class="settingText"><span>${escapeText(label)}</span>${desc ? `<p>${escapeText(desc)}</p>` : ""}</div><div class="settingControl"></div>`;
        if (control) node.lastElementChild.append(control);
        return node;
    };
    const button = (label, action, quiet = true) => { const node = document.createElement("button"); if (quiet) node.className = "sub"; node.textContent = label; node.onclick = action; return node; };

    // What it is, and whether it runs.
    const overview = card();
    const state = !plugin.enabled ? "已停用" : plugin.running ? "正在运行" : "加载失败";
    overview.append(row("启用扩展", state, makeToggle(`启用${manifest.name}`, plugin.enabled, async value => { await run("pluginEnabled", { plugin: manifest.id, value }); await refreshCatalog(); })));
    const about = row(manifest.name, manifest.description);
    about.querySelector(".settingText span").insertAdjacentHTML("afterend", `${plugin.builtin ? `<span class="badge">内置</span>` : ""}${plugin.development ? `<span class="badge">开发</span>` : ""}`);
    about.querySelector(".settingText").insertAdjacentHTML("beforeend", `<div class="pluginMeta">${escapeText([manifest.id, manifest.version, manifest.author].filter(Boolean).join(" · "))}</div>${plugin.error ? `<p class="pluginError">${escapeText(plugin.error)}</p>` : ""}`);
    overview.append(about);
    section("概览", overview);

    // Permissions: the ones the extension declares; entries of the song-menu permission follow it, indented.
    const permissions = await run("pluginPermissions", { plugin: manifest.id });
    if (generation !== pageGeneration) return;
    const list = document.createElement("div");
    list.className = "permissionList";
    const permissionRow = (label, desc, granted, action, key, child) => {
        const node = document.createElement("div");
        node.className = child ? "permissionRow childPermission" : "permissionRow";
        node.innerHTML = `<div><span>${escapeText(label)}</span><p>${escapeText(desc)}</p></div>`;
        node.append(makeToggle(label, granted, async value => { await run(action, { plugin: manifest.id, key, value }); await refreshCatalog(); }));
        list.append(node);
    };
    (permissions || []).forEach(permission => {
        const info = permission.info;
        permissionRow(info.Name, info.Desc, permission.granted, "pluginPermission", info.Key);
        (permission.menus || []).forEach(menu => permissionRow(menu.label, (menu.scenes || ["list", "play", "queue"]).map(scene => ({ list: "歌曲列表", play: "播放详情", queue: "播放队列" }[scene] || scene)).join("、"), menu.granted, "trackMenuPermission", menu.key, true));
    });
    if (!permissions?.length) list.innerHTML = `<div class="permissionRow"><div><p>此扩展未声明任何权限，仅能访问自身的数据目录。</p></div></div>`;
    section("权限", list);

    // Its settings, under the title the host gives every extension's settings.
    if (plugin.running) {
        const data = await run("pluginSettings", { plugin: manifest.id });
        if (generation !== pageGeneration) return;
        const items = (data?.sections || []).flatMap(part => part.items || []);
        if (items.length) appendSettingsSection(`扩展：${manifest.name}`, items, data.values, pluginPageBody, manifest.id);
    }

    // The log is read again while the page is shown; it stays at its end unless the user scrolled up.
    const log = document.createElement("pre");
    log.className = "logView";
    const readLog = async () => {
        const lines = await api("pluginLogs", { plugin: manifest.id }).catch(() => null);
        if (generation !== pageGeneration) { clearInterval(pluginLogTimer); return; }
        const text = (lines || []).join("\n") || "暂无日志";
        if (text === log.textContent) return;
        const atEnd = log.scrollTop + log.clientHeight >= log.scrollHeight - 4;
        log.textContent = text;
        if (atEnd) log.scrollTop = log.scrollHeight;
    };
    section("日志", log);
    clearInterval(pluginLogTimer);
    pluginLogTimer = setInterval(readLog, 2000);
    await readLog();
    if (generation !== pageGeneration) return;

    const actions = card();
    actions.append(row("重新加载", "", button("重新加载", async () => { await run("reloadPlugin", { plugin: manifest.id }); await refreshCatalog(); })));
    if (!plugin.builtin) actions.append(row("移除扩展", plugin.development ? "只解除对源文件夹的引用，不删除其中的文件。" : "", button("移除", async () => {
        if (!await su5.dialog.confirm(`移除「${manifest.name}」？`)) return;
        await run("removePlugin", { plugin: manifest.id });
        await refreshCatalog();
        if (page.kind === "plugin" && page.plugin === manifest.id) navigate({ kind: "plugins" });
    })));
    section("操作", actions);
    pluginPageBody.scrollTop = scroll;
};
// The catalog changed while the page of an extension is shown: it is drawn again when the extension itself
// changed (enabled, running, reloaded with another manifest), and left when the extension is gone. A
// permission switched on this page changes neither, so the page does not move under the pointer.
const syncPluginPage = () => {
    const plugin = catalog.plugins.find(item => item.manifest.id === page.plugin);
    if (!plugin) { navigate({ kind: "plugins" }); return; }
    if (pluginState(plugin) !== pluginPageState) renderPluginPage();
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
    content.innerHTML = `<div class="installHeader"><div><div class="installName">${escapeText(manifest.name || manifest.id)}</div><div class="installMeta">${escapeText([`版本 ${manifest.version || "未知"}`, manifest.author].filter(Boolean).join(" · "))}</div></div></div>${manifest.description ? `<p class="installDesc">${escapeText(manifest.description)}</p>` : ""}<button class="installSummary"><span>权限</span><small></small>${icon("EA6E")}</button><div class="permissionList installPermissions"></div><p class="installNote">${update ? `将替换已安装的 ${escapeText(manifest.installedVersion || "旧")} 版本。` : ""}${development ? "直接加载原文件夹，不复制源文件。" : ""}${manifest.requestedBy ? `由扩展「${escapeText(manifest.requestedBy)}」请求安装。` : ""}权限可在安装后随时更改。</p><div class="buttons"><button class="sub cancel">取消</button><button>${update ? "更新" : "安装"}</button></div>`;
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
// The pages on offer, the bundled one first. The choice applies at once.
const showPlayPages = async () => {
    await refreshCatalog();
    const { id, content } = su5.dialog.create("播放页选项");
    content.closest("dialog").classList.add("playPagesDialog");
    const list = document.createElement("div");
    list.className = "playPageList";
    catalog.playPages.forEach(item => {
        const row = document.createElement("button");
        row.className = "playPageRow clickable";
        row.classList.toggle("active", item.key === catalog.playPage?.key);
        row.innerHTML = `<div class="playPagePreview">${item.preview ? `<img src="playPreview?plugin=${encodeURIComponent(item.plugin)}&id=${encodeURIComponent(item.id)}">` : icon("F00B")}</div><div class="playPageText"><span>${escapeText(item.name)}</span><small>${escapeText(item.pluginName)}</small>${item.description ? `<p>${escapeText(item.description)}</p>` : ""}</div>${icon("EB7B")}`;
        row.onclick = async () => {
            list.querySelectorAll(".playPageRow").forEach(other => other.classList.toggle("active", other === row));
            await setConfig("play.page", item.key);
            refreshCatalog();
        };
        list.append(row);
    });
    if (!catalog.playPages.length) list.innerHTML = emptyState();
    content.append(list);
    dialogFooter(id, content);
    su5.dialog.show(id);
};

const setPlaySide = (showQueue, showLyrics) => {
    playSide = showQueue ? "queue" : showLyrics ? "lyrics" : "";
    if (externalShown) sendPlayView({ value: true, text: playSide });
};
// The play page covers the whole window. Menus, notices and dialogs are windows above it; all that is cut
// out of it are the window buttons, for a page that leaves them to the host.
const layoutPlayView = () => {
    if (!externalShown) return;
    requestAnimationFrame(layoutPlayView);
    const value = { left: 0, top: 0, right: 0, bottom: 0, visible: true, holes: catalog.playPage?.windowButtons ? [] : [...document.querySelectorAll(".titleActions, #connectionBanner")].map(elementBox).filter(rect => rect[2] > 0 && rect[3] > 0) };
    const signature = JSON.stringify(value);
    if (signature === playLayoutSignature) return;
    playLayoutSignature = signature;
    api("playLayout", { value }).catch(() => {});
};
// A page that leaves the window buttons to the host names the colour they are drawn on.
const applyPlayTitle = () => {
    const shown = externalShown && catalog.playPage;
    document.body.classList.toggle("playerExternal", !!shown);
    document.body.classList.toggle("playerHostButtons", !!shown && !catalog.playPage.windowButtons);
    document.body.classList.toggle("playerExternalDark", !!shown && !!catalog.playPage.dark);
    if (shown) simCss(document.body).setProperty("--playTitleColor", /^#[0-9a-f]{6}$/i.test(catalog.playPage.background) ? catalog.playPage.background : "#ffffff");
    collapseButton.hidden = !shown || !!catalog.playPage.windowButtons;
};
// What only this page can do for the play page: closing it, the song menus, the player's dialogs, the keys.
const handlePlayRequest = request => {
    if (request.kind === "trackMenu") { showTrackContext(request.selected || [], "list", { list: request.list || [], external: true }); return; }
    if (request.kind === "menu") { handleMenuEvent(request); return; }
    if (request.kind === "lyricsShown") { lyricsShown = !!request.value; return; }
    // The song menu asked for from outside the window (sim.player.showMenu): the play page need not be open.
    if (request.kind === "songMenu" && request.scene === "outside") { if (player.Current) showTrackContext([player.Current], "play"); return; }
    if (!externalShown) return;
    if (request.kind === "close") openPlayPage(false);
    if (request.kind === "songMenu") {
        const track = request.scene === "queue" ? player.Queue[request.index] : player.Current;
        showTrackContext(track ? [track] : [], request.scene === "queue" ? "queue" : "play");
    }
    if (request.kind === "dialog") ({ equalizer: showEqualizer, sleep: showSleepTimer, speed: showSpeedDialog })[request.name]?.();
    if (request.kind === "titleBar" && catalog.playPage) { Object.assign(catalog.playPage, { background: request.background, dark: request.dark }); applyPlayTitle(); }
    // A key pressed in the play page is handled here as if it had been pressed in this page.
    if (request.kind === "key" && typeof request.event?.key === "string") document.body.dispatchEvent(new KeyboardEvent("keydown", { key: request.event.key, ctrlKey: !!request.event.ctrlKey, altKey: !!request.event.altKey, metaKey: !!request.event.metaKey, shiftKey: !!request.event.shiftKey, repeat: !!request.event.repeat, bubbles: true, cancelable: true }));
};
const waitPlayRequests = async () => {
    if (playWaiting) return;
    playWaiting = true;
    for (;;) (await api("playWait").catch(() => new Promise(resolve => setTimeout(resolve, 1000))) || []).forEach(handlePlayRequest);
};
// The latest state wins; the answer says whether the page is still there.
const sendPlayView = queueValue(async value => { externalLive = await api("playView", value).catch(() => false); });
const setExternalShown = shown => {
    externalShown = shown;
    externalKey = shown ? catalog.playPage.key : "";
    playLayoutSignature = "";
    applyPlayTitle();
    if (shown) layoutPlayView();
};
const closeExternalPlay = () => { setExternalShown(false); sendPlayView({ value: false, text: "" }); run("playPage", { value: false }); };
const playIsOpen = () => externalShown;
const openPlayPage = async open => {
    if (open && !player.Current) return;
    if (externalShown === open) return;
    if (open && !catalog.playPage) { showAlert("没有可用的播放页，请在「扩展」中启用提供播放页的扩展。"); return; }
    setExternalShown(open);
    await sendPlayView({ value: open, text: playSide });
    // The page went away between two catalog reads: the next one names the page that serves now.
    if (open && externalShown && !externalLive) { setExternalShown(false); catalogSignature = ""; await refreshCatalog(); return; }
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
    if (!player.Current && externalShown) openPlayPage(false);
    // The catalog is read again at once when the extension page in use is not the one it names.
    if ((snapshot.playView || "") !== (catalog.playPage?.key || "")) catalogPollAt = 0;
    const key = player.Current ? `${trackKey(player.Current)}:${player.Current.infoRevision || 0}` : "";
    if (lastCurrentKey !== key) {
        const nextSongKey = player.Current ? trackKey(player.Current) : "";
        const changed = displayedSongKey && nextSongKey && displayedSongKey !== nextSongKey;
        displayedSongKey = nextSongKey;
        lastCurrentKey = key;
        updateTrackDisplay(player.Current, changed);
        updateBackdrop();
        updateRowStates();
    }
    const modes = ["列表循环", "单曲循环", "随机播放"];
    const modeIcon = icon(["F072", "F075", "F124"][player.Mode || 0]);
    if (modeButton.innerHTML !== modeIcon) modeButton.innerHTML = modeIcon;
    modeButton.title = modes[player.Mode || 0];
    const duration = Math.max(1, player.Dur);
    if (Number(seekBar.progress.max) !== duration) seekBar.progress.max = duration;
    seekBar.progress.value = player.Pos;
    volumeBar.progress.value = player.Volume;
    updateVolumeIcons();
    speed = player.Speed || 1;
    timeLabel.textContent = `${formatTime(player.Pos)} / ${formatTime(player.Dur)}`;
    desktopButton.classList.toggle("active", snapshot.deskVisible);
    const nextQueueSignature = JSON.stringify([player.Queue, player.Index]);
    if (queueSignature !== nextQueueSignature && !dragActive) {
        queueSignature = nextQueueSignature;
        updateRowStates();
    }
    if (page.kind === "downloads") {
        const nextSignature = JSON.stringify(snapshot.downloads);
        if (downloadSignature !== nextSignature) { downloadSignature = nextSignature; renderDownloads(); }
    }
    const sleep = sleepState = snapshot.sleep;
    const until = new Date(sleep.Until).getTime();
    const sleepActive = sleep.WaitTrackEnd || until > Date.now();
    document.body.classList.toggle("sleepMode", sleepActive);
    const sleepAction = sleepActive ? () => run("cancelSleep") : showSleepTimer;
    const timerSetting = mainContent.querySelector('[data-setting-key="sleepTimer"] .settingControl button');
    if (timerSetting) { timerSetting.textContent = sleepActive ? "取消定时" : "开启定时"; timerSetting.onclick = sleepAction; }
    (snapshot.alerts || []).forEach(showAlert);
    (snapshot.musicInvalidations || []).forEach(id => songInfo.invalidate(id));
    // An extension asked for one of its own sidebar entries to be shown.
    // The entry already shown as a list is only read again, so the list keeps its place.
    (snapshot.navigations || []).forEach(item => page.kind === "menu" && page.plugin === item.plugin && page.id === item.id && !shownPanel && mainContent.querySelector(".playlistHeader") ? reloadMenuPage() : navigate({ kind: "menu", plugin: item.plugin, id: item.id }));
    // A panel opened outside a sidebar click takes the content area; one the extension closed gives it back.
    // A snapshot may predate what a click has just changed, so a difference counts once it is seen twice.
    const livePanel = snapshot.panel?.id || "";
    if (menuOpening || livePanel === shownPanel) panelDrift = null;
    else if (panelDrift !== livePanel) panelDrift = livePanel;
    else { panelDrift = null; navigate(livePanel ? { kind: "panel", plugin: snapshot.panel.plugin, id: livePanel } : { kind: "home" }); }
    (snapshot.prompts || []).forEach(async prompt => {
        if (shownPrompts.size) return;
        shownPrompts.add(prompt.id);
        let text;
        if (prompt.kind === "install") {
            // An extension asks to install a package: the installer shows it and answers with the permissions switched off.
            const denied = await showInstaller(JSON.parse(prompt.value), false);
            text = denied ? JSON.stringify(denied) : "";
        } else if (prompt.kind === "form") {
            const answer = await su5.dialog.prompt({ ...prompt.form, buttonHtml: escapeText(prompt.form.submit || "确定") });
            text = answer ? JSON.stringify(answer) : "";
        } else text = prompt.kind === "confirm" ? await su5.dialog.confirm(prompt.title) ? "1" : "" : (await su5.dialog.prompt({ title: prompt.title, items: [{ type: "input", name: "value", placeholder: prompt.placeholder, value: prompt.value }] }))?.value || "";
        await run("promptResult", { id: prompt.id, text });
        shownPrompts.delete(prompt.id);
    });
};
minimizeButton.onclick = () => run("window", { text: "minimize" });
maximizeButton.onclick = () => run("window", { text: "maximize" });
closeButton.onclick = () => run("window", { text: "close" });
desktopButton.onclick = () => run("desktopLyrics");
previousButton.onclick = () => run("previous");
nextButton.onclick = () => run("next");
toggleButton.onclick = () => run("toggle");
modeButton.onclick = () => run("mode");
queueButton.onclick = () => { openPlayPage(true); setPlaySide(true, false); };
miniCoverButton.onclick = () => { setPlaySide(false, lyricsShown); openPlayPage(true); };
collapseButton.onclick = () => openPlayPage(false);
muteButton.onclick = () => { if (player.Volume > 0) { previousVolume = player.Volume; run("volume", { number: 0 }); } else run("volume", { number: previousVolume }); };
seekBar.progress.onchange = number => run("seek", { number });
const updateVolumeIcons = () => {
    const glyph = icon(player.Volume <= 0 ? "F29E" : "F2A2");
    if (muteButton.innerHTML !== glyph) muteButton.innerHTML = glyph;
    muteButton.title = player.Volume <= 0 ? "取消静音" : "静音";
};
let trackLeaveAt = 0;
const updateTrackDisplay = async (track, changed) => {
    const generation = ++trackDisplayGeneration;
    const elements = [playerBar.querySelector(".info")];
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
    const covers = [coverUrl(track)];
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
    await miniCover.decode().catch(() => {});
    if (generation !== trackDisplayGeneration) return;
    miniTitle.textContent = track?.title || "未播放";
    updateArtist(miniArtist, track?.artist);
    trackDisplayPending = false;
    elements.forEach(element => element.classList.remove("trackLoading"));
    if (track && changed) {
        void playerBar.offsetWidth;
        elements.forEach(element => element.classList.add("trackChanging"));
    }
};
const sendVolume = queueValue(number => run("volume", { number }));
const setVolume = number => {
    player.Volume = Math.max(0, Math.min(1, number));
    volumeBar.progress.value = player.Volume;
    updateVolumeIcons();
    return sendVolume(player.Volume);
};
volumeBar.progress.onchange = volumeBar.progress.ondrag = setVolume;
const volumeWheel = event => {
    if (!event.deltaY) return;
    event.preventDefault();
    setVolume(player.Volume + (event.deltaY < 0 ? .05 : -.05));
};
document.querySelector(".volBtnBottom").addEventListener("wheel", volumeWheel, { passive: false });


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
    if (event.target.closest("textarea,select,[contenteditable]") || dialogOpen()) return;
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
    if (event.key === "Escape") { setPlaySide(false, lyricsShown); openPlayPage(false); if (document.fullscreenElement) document.exitFullscreen(); }
    if (event.key === "F11" && playIsOpen()) { event.preventDefault(); if (document.fullscreenElement) document.exitFullscreen(); else document.documentElement.requestFullscreen(); }
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
    waitPlayRequests();
    poll();
};
window.simInitialReady = start().catch(() => { mainContent.innerHTML = emptyState(); connectionBanner.hidden = false; setTimeout(start, 1500); });



const pageToken = crypto.randomUUID();
const pageLoaded = document.readyState === "complete" ? Promise.resolve() : new Promise(resolve => window.addEventListener("load", resolve, { once: true }));
Promise.all([pageLoaded, document.fonts.ready, window.simInitialReady]).then(() => api("pageReady", { id: pageToken }));
// The window waits out a reload hidden. Only a page really being left says so: a navigation that merely starts
// (a dropped file the drop handler did not take) is refused by the host, and the page would never report ready again.
window.addEventListener("pagehide", () => navigator.sendBeacon("api", new Blob([JSON.stringify({ action: "pageLoading", id: pageToken })], { type: "application/json" })));
