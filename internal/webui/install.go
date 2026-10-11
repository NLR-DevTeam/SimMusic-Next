package webui

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"simmusic/internal/plugin"
)

func (u *UI) clearInstall() {
	u.mu.Lock()
	dir, development := u.pendingInstall, u.pendingDevelopment
	u.pendingInstall = ""
	u.pendingInstallToken = ""
	u.mu.Unlock()
	if dir != "" && !development {
		os.RemoveAll(dir)
	}
}

func (u *UI) setInstall(dir string, manifest plugin.Manifest, development bool) (map[string]any, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		if !development {
			os.RemoveAll(dir)
		}
		return nil, err
	}
	u.clearInstall()
	u.mu.Lock()
	u.pendingInstall, u.pendingManifest, u.pendingDevelopment = dir, manifest, development
	u.pendingInstallToken = hex.EncodeToString(token[:])
	id := u.pendingInstallToken
	u.mu.Unlock()
	data, _ := json.Marshal(manifest)
	var result map[string]any
	json.Unmarshal(data, &result)
	result["installToken"] = id
	// What the installer shows: each declared permission, and the version being replaced.
	permissions := []plugin.PermInfo{}
	for _, key := range manifest.Declared() {
		permissions = append(permissions, plugin.Info(key))
	}
	result["permissionList"] = permissions
	if old := u.app.Plugins.Get(manifest.ID); old != nil {
		result["installedVersion"] = old.Manifest.Version
	}
	return result, nil
}

func (u *UI) preparePackage(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !u.sameOrigin(r) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	dir, manifest, err := plugin.PreparePackage(http.MaxBytesReader(w, r.Body, 64<<20))
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err != nil {
		w.WriteHeader(400)
		json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
		return
	}
	result, err := u.setInstall(dir, manifest, false)
	if err != nil {
		w.WriteHeader(400)
		json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"result": result})
}
