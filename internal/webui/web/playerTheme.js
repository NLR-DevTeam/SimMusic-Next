// SimMusic 2024's cover sampling, color merging and theme darkening.
const getCoverColors = image => {
    const canvas = document.createElement("canvas");
    // Fixed sampling size, as in the Go cover cache, also works while the page is hidden.
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
    const theme = `rgb(${main.map(value => value * scale).join(",")})`;
    simCss(playPage).setProperty("--SimAPTheme", theme);
    simCss(coverAtmosphere).backgroundColor = `rgb(${main.join(",")})`;
    coverAtmosphere.querySelectorAll("div").forEach((element, index) => {
        const color = colors[index + 1] || [255, 255, 255];
        simCss(element).backgroundImage = `radial-gradient(ellipse closest-side, rgb(${color.join(",")}), transparent)`;
    });
};
largeCover.addEventListener("load", updateCoverTheme);
largeCover.addEventListener("error", updateCoverTheme);
if (largeCover.complete) updateCoverTheme();
