package webui

import (
	"context"
	"net/http"
	"simmusic/internal/core"
	"strings"
	"testing"
)

// Real HTTP boundary tests: other origins, guessed paths and unsupported methods
// cannot invoke native operations, even though the interface binds to loopback.
func TestLocalBridgeBoundary(t *testing.T) {
	u := New(&core.App{}, nil)
	url, err := u.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer u.Quit()
	defer u.server.Shutdown(context.Background())
	tests := []struct {
		name, method, path, origin, contentType, site string
		want                                          int
	}{
		{"page", "GET", url, "", "", "", 200},
		{"unknown capability", "GET", u.origin + "/wrong/", "", "", "", 404},
		{"API rejects GET", "GET", url + "api", "", "", "", 405},
		{"missing origin", "POST", url + "api", "", "application/json", "", 403},
		{"other origin", "POST", url + "api", "https://example.com", "application/json", "", 403},
		{"simple form", "POST", url + "api", u.origin, "text/plain", "", 403},
		{"unknown action", "POST", url + "api", u.origin, "application/json", "", 400},
		// Chromium omits Origin on same-origin requests once web security is relaxed for webviews.
		{"same-origin fetch metadata", "POST", url + "api", "", "application/json", "same-origin", 400},
		{"cross-site fetch metadata", "POST", url + "api", "", "application/json", "cross-site", 403},
		{"other origin with metadata", "POST", url + "api", "https://example.com", "application/json", "same-origin", 403},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req, _ := http.NewRequest(test.method, test.path, strings.NewReader(`{"action":"unknown"}`))
			if test.origin != "" {
				req.Header.Set("Origin", test.origin)
			}
			if test.site != "" {
				req.Header.Set("Sec-Fetch-Site", test.site)
			}
			req.Header.Set("Content-Type", test.contentType)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != test.want {
				t.Fatalf("status %d, want %d", resp.StatusCode, test.want)
			}
			if resp.Header.Get("Content-Security-Policy") == "" && test.want != 404 {
				t.Fatal("missing content security policy")
			}
		})
	}
}
