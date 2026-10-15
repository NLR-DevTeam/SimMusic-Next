// The player of the host, for the play pages of extensions: served to them as /.sim/player.js.
// A play page runs in a browser of its own; simHost is its only way out, and the host answers these
// messages itself. Needs the "playPage" and "player" permissions.
(() => {
    if (window.simPlayer || !window.simHost) return;
    const listeners = {};
    const pending = new Map();
    let serial = 0;
    // The position between the host's reports. A report says where the song was when it was taken, and
    // arrives a varying moment later; starting again from each one would make the position jump back and
    // forth. So the page keeps a clock of its own, started at a report and running at the playback speed.
    // A later report only steers it: the clock goes on from where it is, a little faster or slower, until
    // it agrees with the reports. It starts afresh when a report is far from it (a seek, another song) or
    // while nothing plays.
    const clock = { position: 0, at: performance.now(), rate: 0 };
    const clockNow = () => clock.position + (performance.now() - clock.at) / 1000 * clock.rate;
    const steerClock = () => {
        const running = state.playing && !state.loading;
        const now = clockNow(), off = state.position - now;
        if (!running || !clock.rate || Math.abs(off) > .3) Object.assign(clock, { position: state.position, at: performance.now(), rate: running ? state.speed : 0 });
        else Object.assign(clock, { position: now, at: performance.now(), rate: state.speed * (1 + Math.max(-.1, Math.min(.1, off * .5))) });
    };
    let announce;
    const state = { current: null, index: -1, queue: [], lyrics: { traditional: [], perWord: null }, playing: false, loading: false, position: 0, duration: 0, volume: 0, speed: 1, mode: 0, sleep: { active: false, until: 0, waitTrackEnd: false }, settings: {}, visible: false, side: "", windowMaximized: false, fullscreen: false };
    const emit = (type, detail) => (listeners[type] || []).slice().forEach(handler => { try { handler(detail); } catch (error) { console.error(error); } });
    const request = (action, ...args) => new Promise((resolve, reject) => {
        const id = ++serial;
        pending.set(id, { resolve, reject });
        simHost.postMessage({ sim: "player", id, action, args });
    });
    const player = {
        state,
        // Resolves with the state once the host has delivered it for the first time.
        ready: new Promise(resolve => announce = resolve),
        // Events: track, queue, lyrics, status, view, position (each with the state), change ({ state, changed }), message (data from the extension).
        on: (type, handler) => { (listeners[type] ||= []).push(handler); return player; },
        off: (type, handler) => { listeners[type] = (listeners[type] || []).filter(item => item !== handler); return player; },
        // The position now: the host reports it four times a second, the time since then is added here.
        position: () => state.playing && !state.loading && clock.rate ? Math.max(0, Math.min(state.duration || Infinity, clockNow())) : state.position,
        postMessage: data => simHost.postMessage(data),
        // Fullscreen is the host's to give: it takes the main window fullscreen for the play page shown.
        toggleFullscreen: () => request("setFullscreen", !state.fullscreen),
        shortcuts: true
    };
    ["toggle", "play", "pause", "previous", "next", "stop", "seek", "setVolume", "setSpeed", "cycleMode", "setMode", "playIndex", "removeFromQueue", "moveInQueue", "clearQueue", "setSleep", "cancelSleep", "showWindow",
        "setQueue", "append", "playNext",
        "close", "showMenu", "showDialog", "setTitleBar", "window", "setLyricsShown", "loadInfo", "spectrum", "setFullscreen"].forEach(name => player[name] = (...args) => request(name, ...args));
    simHost.addEventListener("message", event => {
        const data = event.data;
        if (!data || data.sim !== "player") return;
        if (data.reply !== undefined) {
            const waiting = pending.get(data.reply);
            pending.delete(data.reply);
            if (waiting) data.error === undefined ? waiting.resolve(data.result) : waiting.reject(new Error(data.error));
            return;
        }
        if (data.event === "message") { emit("message", data.data); return; }
        if (data.event !== "state") return;
        Object.assign(state, data.state);
        steerClock();
        data.changed.forEach(group => emit(group, state));
        emit("position", state);
        emit("change", { state, changed: data.changed });
        announce(state);
    });
    // A click here does not reach the host page: it is told, so that a menu it has open closes.
    addEventListener("pointerdown", () => simHost.postMessage({ sim: "player", action: "pointer", args: [] }), true);
    // The keys of the main window keep working while the page has the focus: they are handed to the host
    // page, which handles them as its own (space, the arrows, Esc and the rest). F11 is done here, where the
    // key was pressed. A page that handles a key itself calls preventDefault, or turns all of them off
    // with simPlayer.shortcuts = false.
    addEventListener("keydown", event => {
        if (!player.shortcuts || event.defaultPrevented) return;
        const target = event.target.closest ? event.target : document.body;
        const input = target.closest("input");
        if (input && input.type !== "range" || target.closest("textarea, select, [contenteditable]")) return;
        const key = event.key.toLowerCase();
        if (event.key === "F11") { event.preventDefault(); player.toggleFullscreen(); return; }
        if (![" ", "arrowup", "arrowdown", "arrowleft", "arrowright", "escape", "w", "a", "s", "d"].includes(key) && !(event.ctrlKey && ["f", "o"].includes(key))) return;
        event.preventDefault();
        if (event.key === "Escape" && state.fullscreen) request("setFullscreen", false).catch(() => {});
        simHost.postMessage({ sim: "player", action: "key", args: [{ key: event.key, ctrlKey: event.ctrlKey, altKey: event.altKey, metaKey: event.metaKey, shiftKey: event.shiftKey, repeat: event.repeat }] });
    });
    Object.defineProperty(window, "simPlayer", { value: player });
    request("hello").catch(error => console.error(error));
})();
