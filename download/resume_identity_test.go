package download

import (
	"context"
	"github.com/sheyiyuan/zero-beat-core/source"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type resumeTransport func(*http.Request) (*http.Response, error)

func (fn resumeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }
func TestResumeChangedResourceCannotAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audio.part")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".resume.json", []byte(`{"validator":"\"old\"","total":6}`), 0600); err != nil {
		t.Fatal(err)
	}
	previous := http.DefaultClient
	defer func() { http.DefaultClient = previous }()
	http.DefaultClient = &http.Client{Transport: resumeTransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("If-Range") != `"old"` {
			t.Fatalf("missing validator: %v", r.Header)
		}
		return &http.Response{StatusCode: 206, Header: http.Header{"Etag": {`"new"`}, "Content-Range": {"bytes 3-5/6"}}, Body: io.NopCloser(strings.NewReader("new")), ContentLength: 3}, nil
	})}
	if err := downloadToPart(context.Background(), path, source.Stream{URL: "https://example.invalid/audio"}, nil); err == nil {
		t.Fatal("resource switch accepted")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "old" {
		t.Fatalf("partial corrupted: %q", data)
	}
	if _, err := os.Stat(path + ".resume.json"); !os.IsNotExist(err) {
		t.Fatal("stale identity retained")
	}
}
