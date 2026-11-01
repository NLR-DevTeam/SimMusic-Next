package audio

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestHTTPReaderReadsFromItsFileAndRemovesIt(t *testing.T) {
	song := bytes.Repeat([]byte("0123456789abcdef"), 64<<10)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(song) }))
	defer server.Close()
	h, err := OpenHTTP(server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	path := h.file.Name()
	got, err := io.ReadAll(h)
	if err != nil || !bytes.Equal(got, song) {
		t.Fatalf("read %d of %d bytes: %v", len(got), len(song), err)
	}
	if _, err = h.Seek(-16, io.SeekEnd); err != nil {
		t.Fatal(err)
	}
	tail := make([]byte, 32)
	if n, err := h.Read(tail); n != 16 || err != nil || !bytes.Equal(tail[:n], song[len(song)-16:]) {
		t.Fatalf("read past a seek: %d %v", n, err)
	}
	h.Close()
	for until := time.Now().Add(2 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, err = os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return
		}
		if time.Now().After(until) {
			t.Fatal("the song's file outlived its reader")
		}
	}
}
