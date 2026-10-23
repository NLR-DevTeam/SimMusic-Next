package plugin

import (
	"errors"
	"regexp"
)

type DialogField struct {
	Name        string            `json:"name"`
	Type        string            `json:"type"`
	Placeholder string            `json:"placeholder"`
	Value       string            `json:"value"`
	AllowEmpty  bool              `json:"allowEmpty"`
	Options     map[string]string `json:"options,omitempty"`
}

type DialogForm struct {
	Title    string        `json:"title"`
	Subtitle string        `json:"subtitle,omitempty"`
	Submit   string        `json:"submit,omitempty"`
	Items    []DialogField `json:"items"`
}

var dialogFieldName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)

func (form DialogForm) Validate() error {
	if len(form.Items) == 0 || len(form.Items) > 20 || len(form.Title) > 512 || len(form.Subtitle) > 2048 || len(form.Submit) > 256 {
		return errors.New("无效的对话框表单")
	}
	seen := map[string]bool{}
	for _, field := range form.Items {
		if !dialogFieldName.MatchString(field.Name) || seen[field.Name] || len(field.Value) > 16384 || len(field.Placeholder) > 1024 {
			return errors.New("无效的对话框字段")
		}
		seen[field.Name] = true
		if field.Type != "input" && field.Type != "textarea" && field.Type != "select" {
			return errors.New("不支持的对话框字段类型")
		}
		if field.Type == "select" && (len(field.Options) == 0 || len(field.Options) > 100) {
			return errors.New("无效的对话框选项")
		}
		for key, value := range field.Options {
			if len(key) > 512 || len(value) > 1024 {
				return errors.New("对话框选项过长")
			}
		}
	}
	return nil
}
