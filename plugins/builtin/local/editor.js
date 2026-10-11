// 元数据编辑窗口的页面脚本。页面运行在扩展自己的 webview 里，没有 sim.* 接口，
// 只通过 simHost 与扩展主脚本（main.js）互发消息：
//   页面 → 扩展：{ type: "ready" } / { type: "save", changes } / { type: "close" }
//   扩展 → 页面：{ type: "load", tags } / { type: "failed" }
const fields = ["title", "artist", "album", "year", "track", "disc", "genre", "albumArtist", "composer", "lyricist", "comment", "lyrics"];
const inputOf = name => document.getElementById(`tag${name[0].toUpperCase()}${name.slice(1)}`);
const valueOf = name => inputOf(name).value.replace(/\r\n?/g, "\n").trim();
let original = {};
// undefined：不改动封面；null：移除；字符串：新图片的 data URL
let coverChange;

const formatBytes = bytes => bytes >= 1048576 ? `${(bytes / 1048576).toFixed(2)}MB` : `${(bytes / 1024).toFixed(2)}KB`;
const showCover = cover => {
    coverBox.classList.toggle("filled", !!cover);
    coverImage.hidden = !cover;
    if (cover) coverImage.src = cover.url;
    coverMime.textContent = cover?.mime || "";
    coverSize.textContent = cover?.width ? `${cover.width}x${cover.height}` : "";
    coverBytes.textContent = cover ? formatBytes(cover.size) : "";
    coverRemove.disabled = !cover;
};

simHost.onmessage = event => {
    const message = event.data || {};
    if (message.type === "load") {
        const tags = message.tags;
        fields.forEach(name => {
            inputOf(name).value = tags[name] || "";
            inputOf(name).disabled = !tags.writable;
            original[name] = valueOf(name);
        });
        fileName.textContent = tags.writable ? tags.path : "此格式暂不支持写入元数据";
        fileName.title = tags.path;
        showCover(tags.cover && { ...tags.cover, url: `data:${tags.cover.mime};base64,${tags.cover.base64}` });
        coverPick.disabled = !tags.writable;
        coverRemove.disabled = !tags.writable || !tags.cover;
        saveButton.disabled = !tags.writable;
    }
    if (message.type === "failed") {
        saveButton.disabled = false;
        su5.dialog.alert("很抱歉，元数据写入失败。");
    }
};

coverPick.onclick = () => coverFile.click();
coverFile.onchange = () => {
    const file = coverFile.files[0];
    coverFile.value = "";
    if (!file) return;
    const reader = new FileReader();
    reader.onload = () => {
        const image = new Image();
        image.onload = () => {
            coverChange = reader.result;
            showCover({ url: reader.result, mime: file.type, width: image.naturalWidth, height: image.naturalHeight, size: file.size });
        };
        image.onerror = () => su5.dialog.alert("所选文件不是有效的图片。");
        image.src = reader.result;
    };
    reader.readAsDataURL(file);
};
coverRemove.onclick = () => {
    coverChange = null;
    showCover(null);
};

const save = () => {
    if (saveButton.disabled) return;
    const changes = {};
    fields.forEach(name => { if (valueOf(name) !== original[name]) changes[name] = valueOf(name); });
    if (coverChange !== undefined) changes.cover = coverChange === null ? null : { base64: coverChange };
    if (!Object.keys(changes).length) return simHost.postMessage({ type: "close" });
    saveButton.disabled = true;
    simHost.postMessage({ type: "save", changes });
};
saveButton.onclick = save;
cancelButton.onclick = () => simHost.postMessage({ type: "close" });
addEventListener("keydown", event => {
    if (event.ctrlKey && event.key.toLowerCase() === "s") { event.preventDefault(); save(); }
});

simHost.postMessage({ type: "ready" });
