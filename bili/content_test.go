package bili

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type contentTransport func(*http.Request) (*http.Response, error)

func (f contentTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func contentClient(body string, check func(*http.Request)) *Client {
	c := New()
	c.http.Transport = contentTransport(func(r *http.Request) (*http.Response, error) {
		if check != nil {
			check(r)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	return c
}

func TestRelatedRequestAndMapping(t *testing.T) {
	c := contentClient(`{"code":0,"data":[{"bvid":"BVtest","title":"<em>测试</em> &amp; 音乐","owner":{"name":"UP"},"pic":"//cover","duration":123,"stat":{"view":456}},{"bvid":"BVtwo","duration":"03:04","play":789},{"title":"失效"}]}`, func(r *http.Request) {
		if r.URL.Path != "/x/web-interface/archive/related" || r.URL.Query().Get("bvid") != "BVinput" {
			t.Fatalf("wrong related request: %s", r.URL)
		}
		if r.Header.Get("Referer") != "https://www.bilibili.com/video/BVinput" {
			t.Fatal("missing video referer")
		}
	})
	items, err := c.Related(context.Background(), "BVinput")
	if err != nil || len(items) != 2 {
		t.Fatalf("%+v %v", items, err)
	}
	if items[0].Title != "测试 & 音乐" || items[0].Author != "UP" || items[0].Cover != "https://cover" || items[0].Duration != 123 || items[0].Play != 456 || items[1].Duration != 184 || items[1].Play != 789 {
		t.Fatalf("mapping: %+v", items)
	}
}

func TestCommentsPaginationAndMapping(t *testing.T) {
	body := `{"code":0,"data":{"page":{"num":2,"size":20,"count":41},"replies":[{"rpid":123,"member":{"uname":"读者","avatar":"//avatar"},"content":{"message":"一行 &amp; 二行\n保留 <3>"},"like":4,"ctime":1700000000,"rcount":6}]}}`
	c := contentClient(body, func(r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/x/v2/reply" || q.Get("oid") != "170001" || q.Get("pn") != "2" || q.Get("ps") != "20" || q.Get("type") != "1" || q.Get("sort") != "2" {
			t.Fatalf("wrong comments request: %s", r.URL)
		}
		if r.Header.Get("Cookie") != "SESSDATA=test-session" {
			t.Fatal("session not sent")
		}
	})
	c.SetCookies(map[string]string{"SESSDATA": "test-session"})
	p, err := c.Comments(context.Background(), 170001, 2)
	if err != nil || p.Page != 2 || !p.HasMore || len(p.Comments) != 1 {
		t.Fatalf("%+v %v", p, err)
	}
	item := p.Comments[0]
	if item.ID != "123" || item.Content != "一行 & 二行\n保留 <3>" || item.Author != "读者" || item.Avatar != "https://avatar" || item.Likes != 4 || item.CreatedAt.Unix() != 1700000000 || item.ReplyCount != 6 {
		t.Fatalf("mapping: %+v", item)
	}
	for _, body := range []string{
		`{"code":0,"data":{"page":{"num":1,"size":20,"count":1},"replies":[{"rpid":1,"content":{"message":"末页"}}]}}`,
		`{"code":0,"data":{"page":{"num":1,"size":20,"count":999},"replies":null}}`,
		`{"code":0,"data":{"replies":[]}}`,
	} {
		p, err := contentClient(body, nil).Comments(context.Background(), 170001, 0)
		if err != nil || p.HasMore || p.Page != 1 {
			t.Fatalf("empty/final page: %+v %v", p, err)
		}
	}
	preview, err := contentClient(body, nil).Comments(context.Background(), 170001, 2)
	if err != nil || preview.HasMore || preview.Notice == "" {
		t.Fatalf("anonymous preview: %+v %v", preview, err)
	}
}

func TestContentAPIErrors(t *testing.T) {
	for _, body := range []string{`{"code":-101,"message":"需要登录"}`, `{"code":12002,"message":"评论区关闭"}`, `not-json`} {
		c := contentClient(body, nil)
		if _, err := c.Comments(context.Background(), 1, 1); err == nil {
			t.Fatalf("comments swallowed %s", body)
		}
		if _, err := c.Related(context.Background(), "BV1"); err == nil {
			t.Fatalf("related swallowed %s", body)
		}
	}
	c := contentClient(`{"code":0,"data":[]}`, nil)
	if items, err := c.Related(context.Background(), "BV1"); err != nil || len(items) != 0 {
		t.Fatalf("empty related: %+v %v", items, err)
	}
	if _, err := c.Comments(context.Background(), 0, 1); err == nil {
		t.Fatal("invalid aid")
	}
	if _, err := c.Related(context.Background(), ""); err == nil {
		t.Fatal("empty bvid")
	}
	c.http.Transport = contentTransport(func(r *http.Request) (*http.Response, error) { return nil, r.Context().Err() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Related(ctx, "BV1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: %v", err)
	}
}
