package core

import (
	"time"

	"simmusic/internal/plugin"
)

// SettingOption 是 select 类型设置项的选项。
type SettingOption struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// SettingItem 描述插件声明的一个设置项。
// type: toggle | text | select | slider | button | color
type SettingItem struct {
	Type        string          `json:"type"`
	Key         string          `json:"key"`
	Label       string          `json:"label"`
	Desc        string          `json:"desc"`
	Tag         string          `json:"tag"`
	Badge       string          `json:"badge"`
	Button      string          `json:"button"`
	Placeholder string          `json:"placeholder"`
	Default     any             `json:"default"`
	Options     []SettingOption `json:"options"`
	Min         float64         `json:"min"`
	Max         float64         `json:"max"`
}

type SettingSection struct {
	Title string        `json:"title"`
	Items []SettingItem `json:"items"`
}

// PluginSettings 获取插件声明的设置界面，并为缺失值填入默认值。
func (a *App) PluginSettings(p *plugin.Plugin) []SettingSection {
	var secs []SettingSection
	if err := p.CallInto(10*time.Second, &secs, "getSettings"); err != nil {
		return nil
	}
	var items []SettingItem
	for _, s := range secs {
		for _, it := range s.Items {
			if it.Key != "" && it.Default != nil && p.Settings.Get(it.Key) == nil {
				p.Settings.SetDefault(it.Key, it.Default)
			}
			items = append(items, it)
		}
	}
	if len(items) == 0 {
		return nil
	}
	return []SettingSection{{Title: "扩展：" + p.Manifest.Name, Items: items}}
}

// SetPluginSetting 写入插件设置并通知插件。
func (a *App) SetPluginSetting(p *plugin.Plugin, key string, v any) {
	p.Settings.Set(key, v)
	go p.Call(30*time.Second, "onSettingChange", key, v)
}

// PluginAction 触发插件设置中的按钮。
func (a *App) PluginAction(p *plugin.Plugin, key string, done func()) {
	go func() {
		p.Call(time.Minute, "onAction", key)
		if done != nil {
			done()
		}
		a.redraw()
	}()
}
