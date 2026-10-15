// SimMusic Next 内置插件：本地音乐
// 该插件本身就是一个普通 JS 插件，只使用公开的 sim.* API，可作为编写音源插件的参考。

// 音乐信息的索引缓存由主程序统一管理，扩展每次都直接读取文件。
const DEFAULT_EXTS = ".mp3 .wav .flac .ogg";

function folders() {
  return sim.settings.get("folders", []);
}

function exts() {
  return String(sim.settings.get("exts", DEFAULT_EXTS))
    .split(/[\s,;]+/)
    .filter(Boolean)
    .map((e) => (e.startsWith(".") ? e : "." + e).toLowerCase());
}

function toTrack(meta) {
  return {
    id: meta.path,
    title: meta.title || sim.path.basename(meta.path),
    artist: meta.artist || "未知艺术家",
    album: meta.album || sim.path.basename(sim.path.dirname(meta.path)),
    duration: meta.duration || 0,
  };
}

// 曲目引用：只含路径与临时标题，完整信息由主程序按需调用 getMusicInfo 获取
function toRef(path) {
  return { id: path, title: sim.path.basename(path), artist: "", album: sim.path.basename(sim.path.dirname(path)), duration: 0 };
}

const folderName = (f) => sim.path.basename(f) || f;

// 旧版的搜索分词会滤掉这些过于常见的词
const COMMON_WORDS = ["的", "大", "小", "了", "the", "a"];

// ---------- 编辑元数据 ----------
// 编辑窗口是扩展自己的 webview 窗口（editor.html），页面与这里通过消息通信：
// 读写标签由主脚本用 sim.media.readTags / writeTags 完成，页面只负责界面。
const editors = {};

async function editTags(path) {
  if (editors[path] && !editors[path].closed) {
    editors[path].focus();
    return;
  }
  let tags;
  try {
    tags = await sim.media.readTags(path, { cover: true });
  } catch (error) {
    console.error("读取元数据失败（" + path + "）：" + error.message);
    sim.ui.alert("很抱歉，歌曲元数据读取失败。");
    return;
  }
  const view = new sim.webview.window({
    url: "ext://editor.html", title: "编辑元数据",
    width: 920, height: 700, minWidth: 920, minHeight: 700,
    frame: false, top: true,
  });
  editors[path] = view;
  view.onclose = () => { if (editors[path] === view) delete editors[path]; };
  view.onmessage = async (event) => {
    const message = event.data || {};
    if (message.type === "ready") view.postMessage({ type: "load", tags });
    if (message.type === "close") view.close();
    if (message.type !== "save") return;
    try {
      await sim.media.writeTags(path, message.changes);
    } catch (error) {
      console.error("写入元数据失败（" + path + "）：" + error.message);
      view.postMessage({ type: "failed" });
      return;
    }
    // 文件已经改变：清掉这首歌的索引缓存，主程序会重新读取它的全部信息
    sim.index.remove([path]);
    sim.ui.toast("元数据已保存");
    view.close();
  };
}

// 同名的 .lrc 文件优先于内嵌歌词。
const readLyrics = (path) => {
  const base = path.slice(0, path.length - sim.path.extname(path).length);
  for (const p of [base + ".lrc", base + ".LRC"]) {
    if (sim.fs.exists(p)) return sim.fs.readText(p);
  }
  return sim.media.readLyrics(path);
};

module.exports = {
  // ---------- 侧边栏 ----------
  // 分区标题、各条目的名字、点击与右键菜单都由扩展自己维护，主程序只负责显示。
  getSidebar() {
    return { title: "本地", canAdd: true, items: folders().map((f) => ({ id: f, name: folderName(f) })) };
  },

  // 点击分区标题旁的「+」
  async onSidebarAdd() {
    const dir = await sim.dialog.pickFolder("选择要添加的音乐文件夹");
    if (!dir) return;
    const list = folders();
    if (list.includes(dir)) {
      sim.ui.toast("目录已存在");
      return;
    }
    list.push(dir);
    sim.settings.set("folders", list);
    sim.sidebar.refresh();
    sim.sidebar.open(dir);
    sim.ui.toast("已添加文件夹");
  },

  // 点击条目：返回要显示的歌曲列表
  async onSidebarOpen(folder) {
    if (!sim.fs.exists(folder)) throw new Error("文件夹不存在：" + folder);
    const files = await sim.fs.walk(folder, exts());
    return { title: folderName(folder), details: [{ icon: "ED6A", text: folder }], tracks: files.map(toRef) };
  },

  // 条目的右键菜单
  getSidebarMenu(folder) {
    const list = folders();
    const index = list.indexOf(folder);
    const items = [];
    if (index > 0) items.push({ key: "up", label: "上移", icon: "EA78" });
    if (index >= 0 && index < list.length - 1) items.push({ key: "down", label: "下移", icon: "EA4E" });
    items.push(
      { key: "refresh", label: "刷新", icon: "F064" },
      { key: "reveal", label: "在资源管理器中显示", icon: "ED8A" },
      { separator: true },
      { key: "remove", label: "移除入口", icon: "ED74" }
    );
    return items;
  },

  async onSidebarMenu(folder, key) {
    const list = folders();
    const index = list.indexOf(folder);
    if (key === "refresh") sim.sidebar.open(folder);
    if (key === "reveal") sim.shell.openPath(folder);
    if (index < 0) return;
    if (key === "up" || key === "down") {
      const target = index + (key === "up" ? -1 : 1);
      if (target < 0 || target >= list.length) return;
      list.splice(target, 0, list.splice(index, 1)[0]);
      sim.settings.set("folders", list);
      sim.sidebar.refresh();
    }
    if (key === "remove") {
      if (!(await sim.dialog.confirm("目录「" + folderName(folder) + "」将从 SimMusic 目录列表中移除，但不会从文件系统中删除。是否继续？"))) return;
      sim.settings.set("folders", folders().filter((f) => f !== folder));
      sim.sidebar.refresh();
    }
  },

  // 曲目右键菜单的额外项（仅单选时显示）
  getTrackActions(tracks) {
    return tracks.length === 1 ? [{ key: "reveal", label: "在资源管理器显示", icon: "ED8A" }, { key: "editTags", label: "编辑元数据", icon: "EC86" }] : [];
  },

  onTrackAction(key, tracks) {
    if (key === "reveal") sim.shell.showItemInFolder(tracks[0].id);
    if (key === "editTags") return editTags(tracks[0].id);
  },

  // ---------- 曲目 ----------
  async getMusicInfo(ids, scene) {
    const metas = await sim.media.readMetadata(ids);
    return metas.filter(meta => meta.mtime).map(meta => {
      const info = toTrack(meta);
      info.cover = sim.media.coverFile(meta.path);
      if (scene !== "list") {
        info.playback = sim.fs.file(meta.path);
        info.lyrics = readLyrics(meta.path);
      }
      return info;
    });
  },

  // 搜索：主程序传入用户的原搜索词 keyword 与分好的词块 words。
  // 与旧版一致，按词块匹配文件名、标题、专辑与艺术家，命中词块越多越靠前。
  // 只读取标签，不解码音频。
  async search(keyword, words) {
    const parts = (words && words.length ? words : [keyword])
      .map((w) => String(w).trim().toLowerCase())
      .filter((w) => w && !COMMON_WORDS.includes(w));
    if (!parts.length) return [];
    const seen = {};
    const out = [];
    for (const folder of folders()) {
      if (!sim.fs.exists(folder)) continue;
      const files = (await sim.fs.walk(folder, exts())).filter((path) => !seen[path]);
      const metas = await sim.media.readMetadata(files, { duration: false });
      for (const m of metas) {
        seen[m.path] = true;
        const name = sim.path.basename(m.path);
        const hay = (name.slice(0, name.length - sim.path.extname(name).length) + (m.title || "") + (m.album || "") + (m.artist || "")).toLowerCase();
        const count = parts.filter((w) => hay.includes(w)).length;
        if (count) out.push({ count, track: toTrack(m) });
      }
    }
    return out.sort((a, b) => b.count - a.count).map((item) => item.track);
  },

  // 通过「打开方式」/ 右键菜单打开的文件
  async openFiles(paths) {
    const allow = exts();
    return paths.filter((p) => allow.includes(sim.path.extname(p).toLowerCase()) && sim.fs.exists(p)).map(toRef);
  },

  // ---------- 设置 ----------
  getSettings() {
    return [
      {
        type: "text", key: "exts", label: "本地音频格式", default: DEFAULT_EXTS,
        desc: "扫描本地音乐与导入本地文件时识别的音频文件扩展名，以空格分隔。",
      },
      {
        type: "toggle", key: "fileMenu", label: "注册系统菜单", tag: "实验性", default: false,
        desc: "开启后，您可以在音频文件右键的「打开方式」菜单中选择 SimMusic 进行播放。在移动 SimMusic 程序目录或移除 SimMusic 前，您需要先关闭此选项。",
      },
    ];
  },

  onSettingChange(key, value) {
    if (key === "fileMenu") {
      const ok = sim.shell.registerFileMenu(exts(), !!value, "使用 SimMusic 播放");
      sim.ui.toast(ok ? (value ? "已注册系统菜单" : "已移除系统菜单") : "注册系统菜单失败");
    }
    if (key === "exts" && sim.settings.get("fileMenu", false)) {
      sim.shell.registerFileMenu(exts(), true, "使用 SimMusic 播放");
    }
  },
};
