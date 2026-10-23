// SimMusic 的内置播放页：封面与歌曲信息、歌词、播放队列。它与其他扩展的播放页一样运行在自己的
// 浏览器里，只通过 simPlayer 读取状态、控制播放；菜单与对话框由主界面显示。
const state = simPlayer.state;
const icon = code => `<i>${String.fromCodePoint(parseInt(code, 16))}</i>`;
const escapeText = value => su5.utils.formatText(value ?? "");
const trackKey = track => `${track.source}\0${track.id}`;
const coverOf = (track, large) => track && track.infoReady ? large ? track.largeCover : track.cover : "/.sim/assets/placeholder.png";
const updateArtist = (element, value) => {
    const artist = value && value !== "未知艺术家" && value.toLowerCase() !== "unknown artist" ? value : "";
    element.textContent = artist;
    element.hidden = !artist;
};
const formatTime = seconds => {
    const value = Math.max(0, Math.floor(seconds || 0));
    return value >= 3600 ? `${Math.floor(value / 3600)}:${String(Math.floor(value / 60) % 60).padStart(2, "0")}:${String(value % 60).padStart(2, "0")}` : `${Math.floor(value / 60)}:${String(value % 60).padStart(2, "0")}`;
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
                await send(value).catch(() => {});
            }
        })().finally(() => sending = null);
        return sending;
    };
};
let lyricsShown = true;
let displayedSongKey = "";
let lastCurrentKey = "";
let trackDisplayGeneration = 0;
let trackDisplayPending = false;
let trackCoverImage;
let trackLeaveAt = 0;
let lyricsSignature = "";
let queueSignature = "";
let activeLyric = -1;
let lyricsManualTimer;
let dragActive = false;
let dragRefresh = null;
let sleepWaitSince = 0;
let wasVisible = false;

// ---------- 封面取色：SimMusic 2024 的采样、颜色合并与主色压暗 ----------
const getCoverColors = image => {
    const canvas = document.createElement("canvas");
    canvas.width = canvas.height = 64;
    const context = canvas.getContext("2d", { willReadFrequently: true });
    context.drawImage(image, 0, 0, canvas.width, canvas.height);
    const pixels = context.getImageData(0, 0, canvas.width, canvas.height).data;
    const count = pixels.length / 4;
    const step = Math.max(1, Math.round(count / 500));
    const colors = new Map();
    for (let index = 0; index < count; index += step) {
        const color = [...pixels.slice(index * 4, index * 4 + 3)];
        let merged = false;
        for (const [previous, occurrences] of colors) {
            if (Math.hypot(...color.map((value, channel) => value - previous[channel])) >= 80) continue;
            colors.delete(previous);
            colors.set(color.map((value, channel) => Math.floor((previous[channel] * occurrences + value) / (occurrences + 1))), occurrences + 1);
            merged = true;
            break;
        }
        if (!merged) colors.set(color, 1);
    }
    return [...colors].sort((a, b) => b[1] - a[1]).slice(0, 4).map(([color]) => color);
};
const updateCoverTheme = () => {
    let colors = [[204, 204, 204]];
    if (largeCover.complete && largeCover.naturalWidth) colors = getCoverColors(largeCover);
    const main = colors[0];
    const scale = Math.min(255 / (main.reduce((sum, value) => sum + value, 0) + 1), 1);
    simCss(playPage).setProperty("--SimAPTheme", `rgb(${main.map(value => value * scale).join(",")})`);
    simCss(coverAtmosphere).backgroundColor = `rgb(${main.join(",")})`;
    coverAtmosphere.querySelectorAll("div").forEach((element, index) => {
        const color = colors[index + 1] || [255, 255, 255];
        simCss(element).backgroundImage = `radial-gradient(ellipse closest-side, rgb(${color.join(",")}), transparent)`;
    });
};
largeCover.addEventListener("load", updateCoverTheme);
largeCover.addEventListener("error", updateCoverTheme);

// ---------- 设置：外观是本扩展自己的设置，由扩展脚本送来；字体与翻译字号沿用主界面的设置 ----------
const pageSettings = { dark: false, dynamicBg: true, effect3D: false, coverScale: true, lyricsBlur: true, lyricsAlign: "left", lyricsSize: 1.5, lyricsSpacing: .5 };
let titleDark;
const applySettings = () => {
    const config = state.settings;
    const classes = { darkPlayer: "dark", albumScale: "coverScale", threeEffect: "effect3D", blurLyrics: "lyricsBlur" };
    Object.entries(classes).forEach(([className, key]) => document.body.classList.toggle(className, !!pageSettings[key]));
    document.body.classList.toggle("staticBackdrop", !pageSettings.dynamicBg);
    // 主界面在本页面下方垫的底色随深浅模式变化。
    if (titleDark !== !!pageSettings.dark) { titleDark = !!pageSettings.dark; simPlayer.setTitleBar({ color: titleDark ? "#000000" : "#ffffff", dark: titleDark }); }
    const appFont = String(config["general.font"] || "").replace(/["\\]/g, "");
    simCss(document.documentElement).fontFamily = `"${appFont || "Microsoft YaHei"}", "Microsoft YaHei", sans-serif`;
    simCss(lyricsView).setProperty("--lyricSize", `${(pageSettings.lyricsSize || 1.5) * 24}px`);
    simCss(lyricsView).setProperty("--lineSpace", `${pageSettings.lyricsSpacing ?? .5}em`);
    simCss(lyricsView).setProperty("--lrcTranslation", `${config["lrc.transSize"] ?? .8}em`);
    simCss(lyricsView).setProperty("--align", pageSettings.lyricsAlign === "center" ? "center" : "left");
};
simPlayer.on("message", data => { if (data?.type === "settings") { Object.assign(pageSettings, data.values); applySettings(); } });
simPlayer.postMessage({ type: "settings" });

// ---------- 拖动排序：按指针位置计算，浮动副本跟随指针，途经的行让位 ----------
const startDragSort = ({ event, element, index, count, scroller, items, step, commit }) => {
    const grabbed = element.getBoundingClientRect();
    const grip = event.clientY - grabbed.top;
    const origin = grabbed.top + scroller.scrollTop - index * step;
    let pointerY = event.clientY;
    let target = index;
    let frame = 0;
    const ghost = element.cloneNode(true);
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
        const top = bounds.top + 40, bottom = bounds.bottom - 40;
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
        // The release must not also play the row underneath.
        addEventListener("click", swallow, true);
        setTimeout(() => removeEventListener("click", swallow, true));
        ghost.remove();
        scroller.classList.remove("dragSorting");
        items().forEach(([node]) => node.classList.remove("dragSource", "dragUp", "dragDown"));
        dragActive = false;
        dragRefresh = null;
        if (!cancelled && target !== index) commit(index, target);
        else syncQueue();
    };
    const drop = () => finish(false);
    const cancel = () => finish(true);
    const escape = pressed => { if (pressed.key === "Escape") { pressed.stopPropagation(); pressed.preventDefault(); cancel(); } };
    addEventListener("pointermove", move);
    addEventListener("pointerup", drop);
    addEventListener("pointercancel", cancel);
    addEventListener("blur", cancel);
    addEventListener("keydown", escape, true);
    dragActive = true;
    dragRefresh = update;
    update();
    requestAnimationFrame(() => { if (dragActive) scroller.classList.add("dragSorting"); });
    frame = requestAnimationFrame(scroll);
};
// Rows keep their click: a drag begins once the pressed pointer has moved.
const dragOnMove = (event, begin) => {
    if (event.button !== 0 || event.target.closest("button")) return;
    const stop = () => { removeEventListener("pointermove", move); removeEventListener("pointerup", stop); };
    const move = moved => { if (Math.abs(moved.clientY - event.clientY) < 6) return; stop(); begin(); };
    addEventListener("pointermove", move);
    addEventListener("pointerup", stop);
};

// ---------- 播放队列 ----------
let infoTimer;
// 队列中可见的歌曲若尚未读取信息，请宿主读取；读到后随状态送达并更新对应的行。
const loadVisibleInfo = () => {
    clearTimeout(infoTimer);
    infoTimer = setTimeout(() => {
        if (queuePanel.hidden) return;
        const bounds = queueTracks.getBoundingClientRect();
        const indexes = [...queueTracks.querySelectorAll(".queueRow")].filter(row => { const rect = row.getBoundingClientRect(); return rect.bottom > bounds.top - 400 && rect.top < bounds.bottom + 400; }).map(row => Number(row.dataset.index)).filter(index => state.queue[index] && !state.queue[index].infoReady);
        if (indexes.length) simPlayer.loadInfo(indexes).catch(() => {});
    }, 90);
};
const renderQueue = () => {
    const tracks = state.queue;
    queueTracks.replaceChildren();
    if (!tracks.length) { queueTracks.innerHTML = `<div class="emptyState">${icon("EF83")}</div>`; return; }
    tracks.forEach((track, index) => {
        const row = document.createElement("div");
        row.className = "queueRow";
        row.dataset.index = index;
        row.classList.toggle("current", index === state.index);
        row.innerHTML = `<img loading="lazy" src="${coverOf(track)}"><div><p>${escapeText(track.title)}</p><small>${escapeText(track.artist)}</small></div><button class="iconButton" title="移出队列">×</button>`;
        updateArtist(row.querySelector("small"), track.artist);
        row.onclick = event => { if (!event.target.closest("button")) simPlayer.playIndex(index); };
        row.onpointerdown = event => dragOnMove(event, () => startDragSort({
            event, element: row, index, count: tracks.length, scroller: queueTracks, step: 80,
            items: () => [...queueTracks.querySelectorAll(".queueRow")].map(node => [node, Number(node.dataset.index)]),
            commit: (from, to) => {
                // Shown at once in the new order; the host's state follows.
                state.queue.splice(to, 0, state.queue.splice(from, 1)[0]);
                state.index = state.index === from ? to : state.index > from && state.index <= to ? state.index - 1 : state.index < from && state.index >= to ? state.index + 1 : state.index;
                queueSignature = queueKeys();
                renderQueue();
                simPlayer.moveInQueue(from, to);
            }
        }));
        row.querySelector("button").onclick = () => simPlayer.removeFromQueue(index);
        row.oncontextmenu = event => { event.preventDefault(); simPlayer.showMenu({ scene: "queue", index }); };
        queueTracks.append(row);
    });
    loadVisibleInfo();
};
const queueKeys = () => JSON.stringify([state.queue.map(trackKey), state.index]);
// The same songs in the same order only had their information read: the rows are updated where they are.
const syncQueue = () => {
    if (dragActive) return;
    const keys = queueKeys();
    if (keys !== queueSignature) {
        queueSignature = keys;
        if (!queuePanel.hidden) renderQueue();
        return;
    }
    queueTracks.querySelectorAll(".queueRow").forEach(row => {
        const track = state.queue[Number(row.dataset.index)];
        if (!track) return;
        row.querySelector("p").textContent = track.title;
        updateArtist(row.querySelector("small"), track.artist);
        const image = row.querySelector("img");
        if (image.getAttribute("src") !== coverOf(track)) image.src = coverOf(track);
    });
};
queueTracks.onscroll = loadVisibleInfo;
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
        queueSignature = queueKeys();
        renderQueue();
        queueTracks.querySelector(".current")?.scrollIntoView({ block: "center" });
    }
    if (showLyrics) updateLyrics(true);
};

// ---------- 歌词 ----------
const resetLyricsScroll = () => {
    clearTimeout(lyricsManualTimer);
    lyricsView.classList.remove("manualScroll");
};
// 显示的歌词行：宿主提供了逐字歌词时用它（每行由带起止时间的片段组成），否则是逐行歌词。
let shownLines = [];
let activeWords = [];
let wordFrame = 0;
let wordProgress = "";
const renderLyrics = () => {
    const signature = JSON.stringify(state.lyrics);
    if (signature !== lyricsSignature) {
        const manual = lyricsView.classList.contains("manualScroll"), scrollTop = lyricsView.scrollTop;
        lyricsSignature = signature;
        shownLines = state.lyrics.perWord ? state.lyrics.perWord.map(line => ({ time: line.start, text: line.text, translation: line.translation, words: line.words })) : state.lyrics.traditional;
        if (!manual) resetLyricsScroll();
        lyricsView.innerHTML = shownLines.length ? shownLines.map((line, index) => `<div class="lyricLine" data-lyric="${index}">${line.words ? line.words.map(word => `<b class="lyricWord">${escapeText(word.text)}</b>`).join("") : escapeText(line.text)}${line.translation ? `<span>${escapeText(line.translation)}</span>` : ""}</div>`).join("") : `<div class="emptyLyrics">暂无歌词</div>`;
        const lines = shownLines;
        lyricsView.querySelectorAll(".lyricLine").forEach((node, index) => node.onclick = async () => { resetLyricsScroll(); await simPlayer.seek(lines[index].time + .1); updateLyrics(true); });
        if (manual) lyricsView.scrollTop = scrollTop;
        activeLyric = -1;
        activeWords = [];
    }
    if (state.visible) updateLyrics();
    scheduleWords();
};
const updateLyrics = (forceScroll = false, position = state.position) => {
    if (trackDisplayPending) return;
    // Re-layout and state refresh may force a recalculation, but must not end
    // the user's three-second browsing interval.
    let index = -1;
    shownLines.forEach((line, i) => { if (line.time <= position) index = i; });
    if (index === activeLyric && !forceScroll) return;
    activeLyric = index;
    const lines = lyricsView.querySelectorAll(".lyricLine");
    lines.forEach((line, i) => {
        line.classList.toggle("active", i === index);
        const distance = Math.abs(i - index);
        line.dataset.blur = index < 0 || !distance ? 0 : Math.min(distance + 1, 5);
    });
    activeWords = index >= 0 ? [...lines[index].querySelectorAll(".lyricWord")] : [];
    if (index >= 0 && !lyricsView.classList.contains("manualScroll") && state.visible && !document.body.classList.contains("hideLyrics")) {
        const line = lines[index];
        lyricsView.scrollTo({ top: line.offsetTop - lyricsView.offsetTop - lyricsView.clientHeight / 2 + line.clientHeight / 2, behavior: document.body.classList.contains("playerOpening") ? "auto" : "smooth" });
    }
};
// 逐字歌词：当前行里唱过的片段完全显示，未唱到的半透明，正在唱的片段随时间从左到右显现。
// 位置由宿主每 250ms 报告一次，其间按经过的时间推算，逐帧绘制；暂停或页面隐藏时停止。
const paintWords = () => {
    wordFrame = 0;
    if (!state.visible || !state.lyrics.perWord || trackDisplayPending) return;
    const position = simPlayer.position();
    updateLyrics(false, position);
    const words = shownLines[activeLyric]?.words;
    if (words && activeWords.length === words.length) {
        let progress = "";
        words.forEach((word, index) => {
            const sung = position >= word.end, singing = !sung && position >= word.start;
            activeWords[index].classList.toggle("sung", sung);
            activeWords[index].classList.toggle("singing", singing);
            if (singing) progress = `${(100 * (position - word.start) / Math.max(.001, word.end - word.start)).toFixed(1)}%`;
        });
        if (progress && progress !== wordProgress) simCss(lyricsView).setProperty("--wordProgress", wordProgress = progress);
    }
    if (state.playing && !state.loading) wordFrame = requestAnimationFrame(paintWords);
};
const scheduleWords = () => { if (!wordFrame) wordFrame = requestAnimationFrame(paintWords); };
lyricsView.addEventListener("wheel", event => {
    if (!event.deltaY || !largeVolumeBar.hidden || !shownLines.length) return;
    clearTimeout(lyricsManualTimer);
    // Stop an in-flight follow animation before the browser applies the user's wheel delta.
    if (!lyricsView.classList.contains("manualScroll")) lyricsView.scrollTo({ top: lyricsView.scrollTop, behavior: "auto" });
    lyricsView.classList.add("manualScroll");
    lyricsManualTimer = setTimeout(() => { resetLyricsScroll(); updateLyrics(true); }, 3000);
}, { passive: true });
addEventListener("resize", () => { if (state.visible) updateLyrics(true); });

// ---------- 当前曲目：旧内容淡出，新封面解码完成后再替换并淡入 ----------
const updateTrackDisplay = async (track, changed) => {
    const generation = ++trackDisplayGeneration;
    const elements = [largeCover, largeTitle.parentElement, lyricsView];
    const leaving = elements.some(element => element.classList.contains("trackLoading"));
    changed = !!changed || leaving;
    if (changed && !leaving) trackLeaveAt = performance.now();
    trackDisplayPending = !!track;
    if (trackCoverImage) trackCoverImage.src = "";
    trackCoverImage = null;
    if (changed) elements.forEach(element => {
        element.classList.remove("trackChanging");
        element.classList.toggle("trackLoading", !!track);
    });
    let cover = coverOf(track, true);
    if (track) {
        const image = trackCoverImage = new Image();
        await Promise.all([
            (async () => {
                image.src = cover;
                try { await image.decode(); }
                catch {
                    if (generation !== trackDisplayGeneration) return;
                    cover = "/.sim/assets/placeholder.png";
                    image.src = cover;
                    await image.decode().catch(() => {});
                }
            })(),
            changed ? new Promise(resolve => setTimeout(resolve, Math.max(0, 130 - (performance.now() - trackLeaveAt)))) : Promise.resolve()
        ]);
        if (generation !== trackDisplayGeneration) return;
    }
    largeCover.src = cover;
    await largeCover.decode().catch(() => {});
    if (generation !== trackDisplayGeneration) return;
    largeTitle.textContent = track?.title || "未播放";
    updateArtist(largeArtist, track?.artist);
    updateCoverTheme();
    trackDisplayPending = false;
    renderLyrics();
    elements.forEach(element => element.classList.remove("trackLoading"));
    if (track && changed && state.visible && !document.body.classList.contains("playerOpening")) {
        void playPage.offsetWidth;
        elements.forEach(element => element.classList.add("trackChanging"));
    }
};
const syncTrack = () => {
    const key = state.current ? `${trackKey(state.current)}:${state.current.infoRevision || 0}:${state.current.infoReady}` : "";
    if (lastCurrentKey === key) return;
    const nextSongKey = state.current ? trackKey(state.current) : "";
    const changed = displayedSongKey && nextSongKey && displayedSongKey !== nextSongKey;
    if (displayedSongKey !== nextSongKey) resetLyricsScroll();
    displayedSongKey = nextSongKey;
    lastCurrentKey = key;
    activeLyric = -1;
    updateTrackDisplay(state.current, changed);
};

// ---------- 播放状态 ----------
const updateVolumeIcon = () => {
    const glyph = icon(state.volume <= 0 ? "F29E" : "F2A2");
    if (largeVolumeButton.innerHTML !== glyph) largeVolumeButton.innerHTML = glyph;
};
const syncSleep = () => {
    const sleep = state.sleep;
    const active = sleep.waitTrackEnd || sleep.until > Date.now();
    // 2024: while a timer runs, the menu button shows the countdown, then "+time" once it waits for the track to end.
    sleepWaitSince = sleep.waitTrackEnd ? sleepWaitSince || Date.now() : 0;
    document.body.classList.toggle("sleepMode", active);
    const label = !active ? icon("EF77") : sleep.waitTrackEnd ? `+${formatTime((Date.now() - sleepWaitSince) / 1000)}` : formatTime((sleep.until - Date.now()) / 1000);
    if (trackMoreButton.innerHTML !== label) trackMoreButton.innerHTML = label;
};
setInterval(() => { if (state.visible) syncSleep(); }, 250);
const syncStatus = () => {
    document.body.classList.toggle("playing", state.playing);
    document.body.classList.toggle("musicLoading", state.loading);
    const modeIcon = icon(["F072", "F075", "F124"][state.mode || 0]);
    if (largeModeButton.innerHTML !== modeIcon) largeModeButton.innerHTML = modeIcon;
    largeModeButton.title = ["列表循环", "单曲循环", "随机播放"][state.mode || 0];
    const duration = Math.max(1, state.duration);
    if (Number(largeSeekBar.progress.max) !== duration) largeSeekBar.progress.max = duration;
    largeVolumeBar.progress.value = state.volume;
    updateVolumeIcon();
    durationLabel.textContent = formatTime(state.duration);
    maximizeButton.textContent = String.fromCodePoint(state.windowMaximized ? 0xEABB : 0xEAB9);
    maximizeButton.title = state.windowMaximized ? "还原" : "最大化";
    applySettings();
    syncSleep();
};
const syncPosition = () => {
    largeSeekBar.progress.value = state.position;
    positionLabel.textContent = formatTime(state.position);
    if (!trackDisplayPending && state.visible) updateLyrics();
    scheduleWords();
};
// 展开时页面随窗口滑入：其间自身的过渡与动画静止，歌词与队列直接位于当前位置。
let openingTimer;
const syncView = () => {
    document.body.classList.toggle("playerShown", state.visible);
    if (state.visible) setPlaySide(state.side === "queue", state.side === "lyrics");
    if (state.side !== "queue") lyricsShown = state.side === "lyrics";
    if (state.visible && !wasVisible) {
        resetLyricsScroll();
        clearTimeout(openingTimer);
        document.body.classList.add("playerOpening");
        [largeCover, largeTitle.parentElement, lyricsView].forEach(element => element.classList.remove("trackChanging"));
        updateLyrics(true);
        queueTracks.querySelector(".current")?.scrollIntoView({ block: "center" });
        openingTimer = setTimeout(() => document.body.classList.remove("playerOpening"), 300);
    }
    if (!state.visible) showVolume(false);
    wasVisible = state.visible;
};
simPlayer.on("track", syncTrack);
simPlayer.on("queue", () => { syncTrack(); syncQueue(); });
simPlayer.on("lyrics", () => { if (!trackDisplayPending) renderLyrics(); });
simPlayer.on("status", () => { syncStatus(); scheduleWords(); });
simPlayer.on("position", syncPosition);
simPlayer.on("view", () => { syncView(); scheduleWords(); });

// ---------- 控件 ----------
collapseButton.onclick = () => simPlayer.close();
minimizeButton.onclick = () => simPlayer.window("minimize");
maximizeButton.onclick = () => simPlayer.window("maximize");
closeButton.onclick = () => simPlayer.window("close");
largePreviousButton.onclick = () => simPlayer.previous();
largeNextButton.onclick = () => simPlayer.next();
largeToggleButton.onclick = () => simPlayer.toggle();
largeModeButton.onclick = () => simPlayer.cycleMode();
largeQueueButton.onclick = () => setPlaySide(document.body.classList.contains("hideList"), false);
lyricsButton.onclick = () => { lyricsShown = document.body.classList.contains("hideLyrics"); setPlaySide(false, lyricsShown); simPlayer.setLyricsShown(lyricsShown); };
trackMoreButton.onclick = event => simPlayer.showMenu();
largeCover.oncontextmenu = event => { event.preventDefault(); simPlayer.showMenu(); };
document.addEventListener("contextmenu", event => event.preventDefault());
largeSeekBar.progress.onchange = number => simPlayer.seek(number);
const sendVolume = queueValue(number => simPlayer.setVolume(number));
const setVolume = number => {
    state.volume = Math.max(0, Math.min(1, number));
    largeVolumeBar.progress.value = state.volume;
    updateVolumeIcon();
    return sendVolume(state.volume);
};
largeVolumeBar.progress.onchange = largeVolumeBar.progress.ondrag = setVolume;
const showVolume = open => { largeVolumeBar.hidden = !open; };
largeVolumeButton.onclick = () => showVolume(largeVolumeBar.hidden);
document.addEventListener("wheel", event => {
    if (event.target.closest(".largeVolumeControl")) showVolume(true);
    if (largeVolumeBar.hidden || !event.deltaY) return;
    event.preventDefault();
    setVolume(state.volume + (event.deltaY < 0 ? .05 : -.05));
}, { passive: false });
document.addEventListener("pointerdown", event => { if (!event.target.closest(".largeVolumeControl")) showVolume(false); });
addEventListener("blur", () => showVolume(false));
// Esc 由宿主收起播放页；这里只收起自己的浮层。
addEventListener("keydown", event => { if (event.key === "Escape") showVolume(false); }, true);

// The initial state can arrive before this page has registered its listeners.
simPlayer.ready.then(() => {
    syncTrack();
    syncQueue();
    syncStatus();
    renderLyrics();
    syncPosition();
    syncView();
});
