let sleepCancelled = false;
const cancelSleepOperation = async () => {
    if (sleepCancelled) return;
    sleepCancelled = true;
    try { await fetch("api", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ action: "cancelPending" }) }); }
    catch { sleepCancelled = false; }
};
document.addEventListener("keydown", event => { event.preventDefault(); cancelSleepOperation(); });
document.addEventListener("pointerdown", cancelSleepOperation);
const hours = new Date().getHours();
sleepGoodNight.hidden = !(hours > 20 || hours < 6);
for (let index = 0; index < 100; index++) {
    const star = document.createElement("div");
    star.className = "sleepStar";
    sleepStars.append(star);
    const size = Math.random() * 3 + 2;
    const css = simCss(star);
    css.width = css.height = `${size}px`;
    css.left = `${Math.random() * 100}%`;
    css.top = `${Math.random() * 100}%`;
    css.animationDuration = `${Math.random() * 7 + 3}s`;
}
const updateSleepCountdown = async () => {
    if (sleepCancelled) return;
    try {
        const response = await fetch("api", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ action: "sleepState" }) });
        const { result } = await response.json();
        sleepOperation.textContent = result.Pending === "shutdown" ? "关机" : "休眠";
        sleepSeconds.textContent = Math.max(0, Math.ceil((new Date(result.PendingAt).getTime() - Date.now()) / 1000));
    } catch {}
    setTimeout(updateSleepCountdown, 250);
};
window.sleepInitialReady = updateSleepCountdown();

const sleepPageLoaded = document.readyState === "complete" ? Promise.resolve() : new Promise(resolve => window.addEventListener("load", resolve, { once: true }));
Promise.all([sleepPageLoaded, document.fonts.ready, window.sleepInitialReady]).then(() => fetch("api", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ action: "sleepPageReady" }) }));