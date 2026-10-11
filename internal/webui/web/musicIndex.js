const songInfo = (() => {
    const records = new Map();
    const pending = new Map();
    const epochs = new Map();
    let tableTimer;
    let queueTimer;
    const apply = fresh => {
        fresh.forEach(track => {
            const key = trackKey(track);
            const old = records.get(key);
            if (old && old.infoRevision > track.infoRevision) return;
            records.set(key, track);
            [viewTracks, tableTracks, player.Queue].forEach(items => items?.forEach(item => { if (trackKey(item) === key) Object.assign(item, track); }));
            if (player.Current && trackKey(player.Current) === key) Object.assign(player.Current, track);
        });
        window.trackList?.querySelectorAll(".trackRow").forEach(row => {
            const track = tableTracks[Number(row.dataset.row)];
            if (!track) return;
            const cells = row.children;
            const url = coverUrl(track);
            if (cells[0].firstElementChild.getAttribute("src") !== url) cells[0].firstElementChild.src = url;
            cells[1].textContent = cells[1].title = track.title;
            updateArtist(cells[2], track.artist, true);
            cells[3].textContent = track.album;
            cells[4].textContent = formatTime(track.duration);
            highlightRow(row, track);
        });
        queueTracks.querySelectorAll(".queueRow").forEach(row => {
            const track = player.Queue[Number(row.dataset.index)];
            if (!track) return;
            row.querySelector("p").textContent = track.title;
            updateArtist(row.querySelector("small"), track.artist);
            const image = row.querySelector("img");
            if (image.getAttribute("src") !== coverUrl(track)) image.src = coverUrl(track);
        });
        const header = mainContent.querySelector(".playlistHeader > img");
        if (header && viewTracks[0] && header.getAttribute("src") !== coverUrl(viewTracks[0])) header.src = coverUrl(viewTracks[0]);
        const summary = mainContent.querySelector(".listSummary");
        if (summary) summary.innerHTML = musicListSummary(viewTracks);
    };
    const fetchTracks = async tracks => {
        const unique = [...new Map(tracks.map(track => [trackKey(track), track])).values()];
        unique.forEach(track => { if (track.infoReady && !records.has(trackKey(track))) records.set(trackKey(track), track); });
        const cached = unique.map(track => records.get(trackKey(track))).filter(Boolean);
        if (cached.length) apply(cached);
        const missing = unique.filter(track => !records.has(trackKey(track)) && !pending.has(trackKey(track)));
        if (!missing.length) return;
        const requestEpochs = new Map(missing.map(track => [trackKey(track), epochs.get(trackKey(track)) || 0]));
        const request = api("musicInfo", { tracks: missing.map(track => ({ source: track.source, id: track.id, title: track.title, artist: track.artist, album: track.album, duration: track.duration, cover: track.cover || "" })), scene: "list" });
        missing.forEach(track => pending.set(trackKey(track), request));
        try {
            const fresh = await request;
            apply(fresh.filter(track => requestEpochs.get(trackKey(track)) === (epochs.get(trackKey(track)) || 0)));
        } catch (error) { showAlert(error.message); }
        finally { missing.forEach(track => { if (pending.get(trackKey(track)) === request) pending.delete(trackKey(track)); }); }
    };
    const queue = () => {
        clearTimeout(queueTimer);
        queueTimer = setTimeout(() => {
            if (queuePanel.hidden) return;
            const bounds = queueTracks.getBoundingClientRect();
            const visible = [...queueTracks.querySelectorAll(".queueRow")].filter(row => { const r = row.getBoundingClientRect(); return r.bottom > bounds.top - 400 && r.top < bounds.bottom + 400; }).map(row => player.Queue[Number(row.dataset.index)]);
            fetchTracks(visible);
        }, 90);
    };
    // Songs whose records were dropped are read again where they are shown: the rows of the list
    // on screen and of the play queue, without leaving the page.
    const refreshShown = () => {
        const rows = window.trackList ? [...trackList.querySelectorAll(".trackRow")].map(row => tableTracks[Number(row.dataset.row)]).filter(Boolean) : [];
        if (rows.length) { clearTimeout(tableTimer); tableTimer = setTimeout(() => fetchTracks(rows), 90); }
        queue();
    };
    return {
        hydrate: tracks => tracks.forEach(track => { const cached = records.get(trackKey(track)); if (cached && (cached.infoRevision || 0) >= (track.infoRevision || 0)) Object.assign(track, cached); }),
        table: tracks => { clearTimeout(tableTimer); tableTimer = setTimeout(() => fetchTracks(tracks), 90); },
        queue,
        clear: async (tracks, scene) => {
            tracks.forEach(track => { const key = trackKey(track); records.delete(key); pending.delete(key); epochs.set(key, (epochs.get(key) || 0) + 1); });
            const fresh = await run("clearMusicInfo", { tracks, scene });
            if (fresh) { apply(fresh); lastCurrentKey = ""; su5.toast("索引缓存已刷新"); }
        },
        invalidate: source => {
            const sources = source ? Object.keys(catalog.schemes || {}).filter(key => catalog.schemes[key].plugin === source || catalog.schemes[key].providers?.includes(source)) : null;
            const matches = key => !source || key.startsWith(`${source}\0`) || sources.some(value => key.startsWith(`${value}\0`));
            new Set([...records.keys(), ...pending.keys()]).forEach(key => { if (matches(key)) { records.delete(key); pending.delete(key); epochs.set(key, (epochs.get(key) || 0) + 1); } });
            [viewTracks, tableTracks, player.Queue].forEach(items => items?.forEach(track => { if (matches(trackKey(track))) track.infoReady = false; }));
            refreshShown();
        }
    };
})();
const clearSongInfo = songInfo.clear;
