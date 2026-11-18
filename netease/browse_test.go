package netease

import (
	"context"
	"crypto/aes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/sheyiyuan/zero-beat-core/app"
	"github.com/sheyiyuan/zero-beat-core/source"
)

type browseTransport func(*http.Request) (*http.Response, error)

func (f browseTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func mockBrowseHTTP(t *testing.T, f func(*http.Request) string) {
	t.Helper()
	old := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = old })
	http.DefaultTransport = browseTransport(func(r *http.Request) (*http.Response, error) {
		body := f(r)
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
}
func linuxParams(t *testing.T, r *http.Request) map[string]string {
	t.Helper()
	if err := r.ParseForm(); err != nil {
		t.Fatal(err)
	}
	b, err := hex.DecodeString(r.Form.Get("eparams"))
	if err != nil || len(b) == 0 || len(b)%aes.BlockSize != 0 {
		t.Fatal("invalid LinuxAPI request")
	}
	cipher, _ := aes.NewCipher([]byte("rFgB&h#%2?^eDg:Q"))
	for i := 0; i < len(b); i += aes.BlockSize {
		cipher.Decrypt(b[i:i+aes.BlockSize], b[i:i+aes.BlockSize])
	}
	b = b[:len(b)-int(b[len(b)-1])]
	var d struct {
		Params map[string]string `json:"params"`
	}
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	return d.Params
}

const programFixture = `{"code":200,"program":{"id":77,"name":"节目标题","duration":123000,"coverUrl":"//cover","mainSong":{"id":999},"dj":{"userId":12,"nickname":"主播"},"radio":{"id":8,"name":"电台"}}}`

func TestProgramIdentityPlaybackCommentsAndStore(t *testing.T) {
	c := New()
	calls := 0
	mockBrowseHTTP(t, func(r *http.Request) string {
		calls++
		switch r.URL.Path {
		case "/weapi/dj/program/detail":
			return programFixture
		case "/api/linux/forward":
			if p := linuxParams(t, r); p["ids"] != "[999]" {
				t.Fatalf("program ID used as audio ID: %v", p)
			}
			return `{"code":200,"data":[{"id":999,"url":"https://audio.example/a.mp3","type":"mp3","br":128000,"code":200}]}`
		case "/weapi/v1/resource/comments/A_DJ_1_77":
			return `{"code":200,"more":false,"comments":[{"commentId":1,"content":"节目评论","user":{"userId":12,"nickname":"主播"}}]}`
		default:
			t.Fatalf("unexpected request %s", r.URL)
			return ""
		}
	})
	ref := source.Ref{Source: source.NetEase, ExternalID: "program:77", Page: 1}
	resolved, err := c.Resolve(context.Background(), ref, source.QualityLossless)
	if err != nil || resolved.Track.ExternalID != "program:77" || resolved.Track.Title != "节目标题" || resolved.Stream.URL == "" {
		t.Fatalf("%+v %v", resolved, err)
	}
	song := app.SongFromTrack(resolved.Track)
	if app.RefFromSong(song) != ref || song.ID != "netease:program:77:1" {
		t.Fatalf("identity lost in persistence: %+v", song)
	}
	comments, err := c.Comments(context.Background(), ref, 1)
	if err != nil || len(comments.Comments) != 1 || comments.Comments[0].AuthorRef.ID != "12" {
		t.Fatalf("%+v %v", comments, err)
	}
	links, err := c.TrackProfiles(context.Background(), ref)
	if err != nil || len(links) != 2 || links[0].Ref.Kind != source.BrowseRadio || links[1].Ref.Kind != source.BrowseUser {
		t.Fatalf("%+v %v", links, err)
	}
	before := calls
	if _, _, err := c.Lyrics(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	if err := c.AddToPlaylist(context.Background(), "1", ref); err == nil {
		t.Fatal("program accepted into cloud song playlist")
	}
	if calls != before {
		t.Fatal("unnecessary request")
	}
}
func TestBrowseProfilesAndCollections(t *testing.T) {
	c := New()
	mockBrowseHTTP(t, func(r *http.Request) string {
		switch r.URL.Path {
		case "/weapi/v3/song/detail":
			return `{"code":200,"songs":[{"id":1,"name":"作品","ar":[{"id":3,"name":"甲"},{"id":4,"name":"乙"},{"id":3,"name":"甲"}]}]}`
		case "/weapi/v1/artist/3":
			return `{"code":200,"artist":{"id":3,"name":"甲","briefDesc":"简介","picUrl":"//avatar"}}`
		case "/weapi/v1/artist/songs":
			return `{"code":200,"more":true,"songs":[{"id":2,"name":"第二页作品","ar":[{"name":"甲"}]}]}`
		case "/weapi/v1/user/detail/12":
			return `{"code":200,"profile":{"userId":12,"nickname":"用户","signature":"介绍"}}`
		case "/weapi/user/playlist":
			return `{"code":200,"more":true,"playlist":[{"id":9,"name":"公开歌单","trackCount":21}]}`
		case "/weapi/djradio/hot/v1":
			return `{"code":200,"hasMore":true,"djRadios":[{"id":8,"name":"电台","programCount":21,"dj":{"nickname":"主播"}}]}`
		case "/weapi/djradio/get":
			return `{"code":200,"djRadio":{"id":8,"name":"电台","desc":"电台简介","dj":{"userId":12,"nickname":"主播"}}}`
		case "/weapi/dj/program/byradio":
			return `{"code":200,"more":true,"count":21,"programs":[{"id":77,"name":"节目","mainSong":{"id":999}},{"id":78,"mainSong":{"id":0}}]}`
		case "/weapi/dj/program/12":
			return `{"code":200,"more":false,"programs":[]}`
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
			return ""
		}
	})
	ctx := context.Background()
	links, err := c.TrackProfiles(ctx, source.Ref{Source: source.NetEase, ExternalID: "1"})
	if err != nil || len(links) != 2 {
		t.Fatalf("%+v %v", links, err)
	}
	p, err := c.Browse(ctx, links[0].Ref, 2)
	if err != nil || p.Title != "甲" || p.Page != 2 || !p.HasMore || p.Entries[0].Track.ExternalID != "2" {
		t.Fatalf("%+v %v", p, err)
	}
	p, err = c.Browse(ctx, source.BrowseRef{Source: source.NetEase, Kind: source.BrowseUser, ID: "12"}, 1)
	if err != nil || len(p.Entries) != 2 || p.Entries[1].Ref.Kind != source.BrowsePlaylist {
		t.Fatalf("%+v %v", p, err)
	}
	p, err = c.Browse(ctx, source.BrowseRef{Source: source.NetEase, Kind: source.BrowseRadios}, 1)
	if err != nil || len(p.Entries) != 1 || !p.HasMore {
		t.Fatalf("%+v %v", p, err)
	}
	p, err = c.Browse(ctx, p.Entries[0].Ref, 1)
	if err != nil || len(p.Entries) != 2 || p.Entries[1].Track.ExternalID != "program:77" {
		t.Fatalf("%+v %v", p, err)
	}
	p, err = c.Browse(ctx, source.BrowseRef{Source: source.NetEase, Kind: source.BrowseUserPrograms, ID: "12"}, 1)
	if err != nil || p.HasMore || len(p.Entries) > 0 {
		t.Fatalf("empty programs: %+v %v", p, err)
	}
}
func TestBrowseCancellationAndProtocolErrors(t *testing.T) {
	c := New()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	mockBrowseHTTP(t, func(*http.Request) string { t.Fatal("canceled request reached transport"); return "" })
	if _, err := c.Browse(ctx, source.BrowseRef{Source: source.NetEase, Kind: source.BrowseRadios}, 1); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, body := range []string{`{`, `{"code":301}`, `{"code":404}`} {
		var out browseResponse
		if err := callBrowse(context.Background(), "test", func() (float64, []byte) { return 200, []byte(body) }, &out); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
}
func TestProgramAndProfileLinksAreDistinct(t *testing.T) {
	c := New()
	for _, raw := range []string{"program:77", "https://music.163.com/program?id=77", "https://music.163.com/#/program?id=77"} {
		r, ok, err := c.ParseTarget(raw, 0)
		if err != nil || !ok || r.ExternalID != "program:77" {
			t.Fatalf("%s: %+v %v", raw, r, err)
		}
	}
	for _, raw := range []string{"https://music.163.com/artist?id=3", "https://music.163.com/#/djradio?id=8", "https://evil-music.163.com/song?id=1"} {
		if _, ok, _ := c.ParseTarget(raw, 0); ok {
			t.Fatalf("non-song link accepted: %s", raw)
		}
	}
}
