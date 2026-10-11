package webui

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"
)

type promptRequest struct {
	ID          string `json:"id"`
	Kind        string `json:"kind,omitempty"` // "confirm" asks yes or no; Title then holds the question
	Title       string `json:"title"`
	Placeholder string `json:"placeholder"`
	Value       string `json:"value"`
	result      chan string
}

func (u *UI) Prompt(title, placeholder, value string) (string, error) {
	return u.ask(&promptRequest{Title: title, Placeholder: placeholder, Value: value})
}

func (u *UI) Confirm(text string) (bool, error) {
	answer, err := u.ask(&promptRequest{Kind: "confirm", Title: text})
	return answer != "", err
}

func (u *UI) ask(request *promptRequest) (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	request.ID, request.result = hex.EncodeToString(token[:]), make(chan string, 1)
	u.mu.Lock()
	if u.prompts == nil {
		u.prompts = map[string]*promptRequest{}
	}
	u.prompts[request.ID] = request
	u.mu.Unlock()
	defer func() { u.mu.Lock(); delete(u.prompts, request.ID); u.mu.Unlock() }()
	select {
	case value := <-request.result:
		return value, nil
	case <-u.quit:
		return "", errors.New("应用已退出")
	case <-time.After(5 * time.Minute):
		return "", errors.New("输入已超时")
	}
}

func (u *UI) pendingPrompts() []*promptRequest {
	u.mu.Lock()
	defer u.mu.Unlock()
	var result []*promptRequest
	for _, request := range u.prompts {
		result = append(result, request)
	}
	return result
}

func (u *UI) resolvePrompt(id, value string) {
	u.mu.Lock()
	request := u.prompts[id]
	u.mu.Unlock()
	if request != nil {
		select {
		case request.result <- value:
		default:
		}
	}
}
