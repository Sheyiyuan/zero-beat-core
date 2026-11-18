package bili

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestSpaceSignedRequests(t *testing.T) {
	c := contentClient(`{"code":0,"data":{"name":"UP","face":"https://avatar","sign":"简介","list":{"vlist":[{"bvid":"BV1","title":"标题","author":"UP","pic":"//cover","length":"03:04"}]},"page":{"count":41}}}`, func(r *http.Request) {
		q := r.URL.Query()
		if q.Get("mid") != "12" || q.Get("w_rid") == "" || q.Get("wts") == "" {
			t.Fatalf("unsigned %s", r.URL)
		}
		if r.URL.Path == "/x/space/wbi/arc/search" && (q.Get("pn") != "2" || q.Get("ps") != "20") {
			t.Fatal(r.URL)
		}
	})
	c.wbiKey = "test-key"
	c.wbiTime = time.Now()
	p, err := c.Space(context.Background(), "12")
	if err != nil || p.Name != "UP" {
		t.Fatalf("%+v %v", p, err)
	}
	v, more, err := c.SpaceVideos(context.Background(), "12", 2)
	if err != nil || len(v) != 1 || !more || v[0].Duration != 184 || v[0].Cover != "https://cover" {
		t.Fatalf("%+v %t %v", v, more, err)
	}
	c = contentClient(`{"code":-352,"message":"风控校验失败"}`, nil)
	c.wbiKey = "test-key"
	c.wbiTime = time.Now()
	if _, err := c.Space(context.Background(), "12"); err == nil {
		t.Fatal("risk response swallowed")
	}
}
