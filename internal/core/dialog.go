package core

import (
	"errors"
	"simmusic/internal/plugin"
)

func (a *App) DialogForm(form plugin.DialogForm) (map[string]string, error) {
	if err := form.Validate(); err != nil {
		return nil, err
	}
	if a.OnDialogForm == nil {
		return nil, errors.New("当前界面不支持表单对话框")
	}
	return a.OnDialogForm(form)
}
