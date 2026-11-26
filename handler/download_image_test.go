package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDownloadImageFallsBackToRemoteWhenLocalFileIsMissing(t *testing.T) {
	want := []byte("remote-image")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/images/remote.png" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(want)
	}))
	defer server.Close()

	oldBaseURL := ImageUrlBase
	oldBaseDirectory := LocalBaseDirectory
	ImageUrlBase = server.URL + "/images"
	LocalBaseDirectory = t.TempDir()
	t.Cleanup(func() {
		ImageUrlBase = oldBaseURL
		LocalBaseDirectory = oldBaseDirectory
	})

	got, err := DownloadImage(server.URL + "/images/remote.png")
	if err != nil {
		t.Fatalf("DownloadImage() error = %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("DownloadImage() = %q, want %q", got, want)
	}
}
