// 播放页本身在 player.html 中，经宿主的 simPlayer 直接读取状态、控制播放。
// 这里只提供它的外观设置：设置保存在扩展自己的 settings.json，页面加载时索取，更改时推送给页面。
const items = [
  { type: "toggle", key: "dark", label: "播放页深色模式", default: false },
  { type: "toggle", key: "dynamicBg", label: "背景动态混色", desc: "关闭后可减少播放页对硬件资源的占用。", default: true },
  { type: "toggle", key: "effect3D", label: "3D 特效", desc: "在播放页的歌曲信息、播放列表与歌词视图使用 3D 视觉效果。", default: false },
  { type: "toggle", key: "coverScale", label: "专辑封面缩放", desc: "在按下「播放」或「暂停」时对播放页专辑封面使用缩放动效。", default: true },
  { type: "toggle", key: "lyricsBlur", label: "层级虚化", desc: "若无需虚化效果或需要提升性能可关闭此功能。", default: true },
  { type: "select", key: "lyricsAlign", label: "文字对齐", desc: "此配置项切换曲目生效。", options: [{ value: "left", label: "左端对齐" }, { value: "center", label: "居中对齐" }], default: "left" },
  { type: "slider", key: "lyricsSize", label: "歌词字号", min: 1, max: 3, default: 1.5 },
  { type: "slider", key: "lyricsSpacing", label: "歌词间距", min: 0.2, max: 1, default: 0.5 },
];
const publish = () => sim.playPage.postMessage({ type: "settings", values: Object.fromEntries(items.map((item) => [item.key, sim.settings.get(item.key, item.default)])) });

module.exports = {
  getSettings: () => [{ items }],
  onSettingChange: publish,
  onPlayPageMessage: (id, data) => { if (data && data.type === "settings") publish(); },
};
