// Palette and interaction adapted from LinkiumEquationEditor's current color control.
const colorPalette = [
    "#ffffff", "#000000", "#e7e6e6", "#44546a", "#4472c4", "#ed7d31", "#ffc000", "#70ad47", "#36b9b0", "#e84c5b",
    "#f2f2f2", "#7f7f7f", "#d0cece", "#d6dce4", "#d9e2f3", "#fce4d6", "#fff2cc", "#e2f0d9", "#d0ecea", "#f8d7da",
    "#d9d9d9", "#595959", "#aeaaaa", "#adb9ca", "#b4c6e7", "#f8cbad", "#ffe699", "#c6e0b4", "#a2d9d5", "#f4a6ad",
    "#bfbfbf", "#404040", "#757171", "#8497b0", "#8eaadb", "#f4b183", "#ffd966", "#a9d18e", "#75c6c0", "#ef7a86",
    "#a6a6a6", "#262626", "#3b3838", "#323f4f", "#2f5597", "#c65911", "#bf9000", "#548235", "#258f88", "#c51b32",
    "#7f7f7f", "#0d0d0d", "#171717", "#222a35", "#203864", "#843c0c", "#7f6000", "#375623", "#195f5a", "#821220"
];
window.createColorPicker = (label, initial, change) => {
    const container = document.createElement("div");
    container.className = "colorPicker";
    container.innerHTML = `<button type="button" class="plainButton colorControl"><i>&#xEFC5;</i></button><div class="colorPalette dropdown"><div class="colorSwatches"></div><div class="colorHexField"><div class="input"><input type="text" maxlength="7" spellcheck="false"></div></div></div>`;
    const control = container.querySelector("button");

    control.title = label;
    const palette = container.querySelector(".colorPalette");
    const swatches = container.querySelector(".colorSwatches");
    const input = container.querySelector("input");
    let current = String(initial || "#000000").toLowerCase();
    const normalize = value => {
        const text = String(value).trim();
        const hex = text.startsWith("#") ? text : `#${text}`;
        return /^#[0-9a-f]{6}$/i.test(hex) ? hex.toLowerCase() : null;
    };
    const sync = color => {
        simCss(control).setProperty("--pickerColor", color);
        swatches.querySelectorAll("button").forEach(swatch => swatch.classList.toggle("selected", swatch.dataset.color === color));
    };
    const commit = () => {
        const hex = normalize(input.value);
        if (!hex) { input.value = current; input.select(); return; }
        current = input.value = hex;
        sync(hex);
        su5.dropDown.hide(palette);
        change(hex);
    };
    colorPalette.forEach(color => {
        const swatch = document.createElement("button");
        swatch.type = "button";
        swatch.className = "colorSwatch";
        swatch.dataset.color = color;
        swatch.title = color.toUpperCase();

        simCss(swatch).backgroundColor = color;
        swatch.onmousedown = event => event.preventDefault();
        swatch.onclick = () => { input.value = color; commit(); };
        swatches.append(swatch);
    });
    input.value = current;
    input.oninput = () => { const hex = normalize(input.value); if (hex) sync(hex); };
    input.onchange = commit;
    input.onkeydown = event => { if (event.key === "Enter") { event.preventDefault(); event.stopPropagation(); commit(); } };
    su5.form.init(container.querySelector(".input"));
    su5.dropDown.create(palette, control);
    sync(current);
    return container;
};
