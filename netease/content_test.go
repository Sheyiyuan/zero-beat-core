package netease

import (
	"context"
	"errors"
	"github.com/sheyiyuan/zero-beat-core/source"
	"testing"
)

func TestParseComments(t *testing.T) {
	p, err := parseComments([]byte(`{"code":200,"more":true,"comments":[{"commentId":123,"content":" 第一行\n第二行 ","likedCount":12,"time":1700000000123,"user":{"nickname":"读者","avatarUrl":"//avatar"},"beReplied":[{"content":"父评论"}],"replyCount":5}]}`), 2)
	if err != nil || p.Page != 2 || !p.HasMore || len(p.Comments) != 1 {
		t.Fatalf("%+v %v", p, err)
	}
	c := p.Comments[0]
	if c.ID != "123" || c.Author != "读者" || c.Avatar != "https://avatar" || c.Content != "第一行\n第二行" || c.Likes != 12 || c.CreatedAt.UnixMilli() != 1700000000123 || c.ReplyCount != 5 {
		t.Fatalf("mapping: %+v", c)
	}
	p, err = parseComments([]byte(`{"code":200,"more":true,"comments":[{"commentId":1,"content":"回复","beReplied":[{}]}]}`), 1)
	if err != nil || p.Comments[0].ReplyCount != 0 || !p.Comments[0].CreatedAt.IsZero() {
		t.Fatalf("parent comment is not reply count: %+v %v", p, err)
	}
	for _, body := range []string{`{"code":200,"more":true,"comments":[]}`, `{"code":200,"comments":null}`} {
		p, err := parseComments([]byte(body), 1)
		if err != nil || p.HasMore || len(p.Comments) != 0 {
			t.Fatalf("empty: %+v %v", p, err)
		}
	}
}

func TestParseRelated(t *testing.T) {
	items, err := parseRelated([]byte(`{"code":200,"songs":[{"id":123,"name":"曲目","artists":[{"name":"歌手"}],"album":{"name":"专辑","picUrl":"//cover"},"duration":203500,"fee":1},{"name":"缺少 ID"}]}`))
	if err != nil || len(items) != 1 {
		t.Fatalf("%+v %v", items, err)
	}
	c := items[0]
	if c.Source != source.NetEase || c.ExternalID != "123" || c.Page != 1 || c.Title != "曲目" || c.Artist != "歌手" || c.Album != "专辑" || c.Cover != "https://cover" || c.Duration != 204 || !c.VIP {
		t.Fatalf("mapping: %+v", c)
	}
	if items, err := parseRelated([]byte(`{"code":200,"songs":[]}`)); err != nil || len(items) != 0 {
		t.Fatalf("empty: %+v %v", items, err)
	}
}

func TestContentErrorsAndCapabilities(t *testing.T) {
	for _, body := range []string{`{"code":301}`, `{"code":500}`, `{}`, `broken`} {
		if _, err := parseComments([]byte(body), 1); err == nil {
			t.Fatalf("comments swallowed: %s", body)
		}
		if _, err := parseRelated([]byte(body)); err == nil {
			t.Fatalf("related swallowed: %s", body)
		}
	}
	if _, err := parseComments([]byte(`{"code":301}`), 1); !errors.Is(err, source.ErrSessionExpired) {
		t.Fatalf("auth classification: %v", err)
	}
	c := New()
	if !c.Caps().Has(source.CapComments) || !c.Caps().Has(source.CapRelated) {
		t.Fatal("missing capabilities")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ref := source.Ref{Source: source.NetEase, ExternalID: "123"}
	if _, err := c.Comments(ctx, ref, 1); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := c.Related(ctx, ref); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, id := range []string{"", "x", "-1", "0"} {
		ref.ExternalID = id
		if _, err := c.Comments(context.Background(), ref, 1); err == nil {
			t.Fatal("invalid comment ref")
		}
		if _, err := c.Related(context.Background(), ref); err == nil {
			t.Fatal("invalid related ref")
		}
	}
}
