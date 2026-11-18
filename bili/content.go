package bili

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Related 返回 B 站视频的相关推荐。
func (c *Client) Related(ctx context.Context, bvid string) ([]RelatedItem, error) {
	if strings.TrimSpace(bvid) == "" {
		return nil, fmt.Errorf("bilibili: BV 号不能为空")
	}
	var data []struct {
		BVID  string `json:"bvid"`
		Title string `json:"title"`
		Pic   string `json:"pic"`
		Owner struct {
			Name string `json:"name"`
		} `json:"owner"`
		Duration json.RawMessage `json:"duration"`
		Stat     struct {
			View int64 `json:"view"`
		} `json:"stat"`
		Play int64 `json:"play"`
	}
	if err := c.getJSON(ctx, apiHost+"/x/web-interface/archive/related?bvid="+url.QueryEscape(bvid), videoReferer(bvid), &data); err != nil {
		return nil, err
	}
	out := make([]RelatedItem, 0, len(data))
	for _, item := range data {
		if item.BVID == "" {
			continue
		}
		play := item.Play
		if play == 0 {
			play = item.Stat.View
		}
		out = append(out, RelatedItem{
			BVID: item.BVID, Title: stripEm(item.Title), Author: stripEm(item.Owner.Name),
			Cover: normalizeURL(item.Pic), Duration: parseDuration(item.Duration), Play: play,
		})
	}
	return out, nil
}

// Comments 返回 B 站视频评论的第 page 页。
func (c *Client) Comments(ctx context.Context, aid int64, page int) (CommentPage, error) {
	if aid <= 0 {
		return CommentPage{}, fmt.Errorf("bilibili: aid 无效")
	}
	if page <= 0 {
		page = 1
	}
	params := url.Values{
		"type": {"1"}, "oid": {strconv.FormatInt(aid, 10)},
		"pn": {strconv.Itoa(page)}, "ps": {"20"}, "sort": {"2"},
	}
	var data struct {
		Replies []struct {
			RPID   int64 `json:"rpid"`
			Member struct {
				Uname  string `json:"uname"`
				Mid    string `json:"mid"`
				Avatar string `json:"avatar"`
			} `json:"member"`
			Content struct {
				Message string `json:"message"`
			} `json:"content"`
			Like   int64 `json:"like"`
			CTime  int64 `json:"ctime"`
			RCount int   `json:"rcount"`
		} `json:"replies"`
		Page struct {
			Num   int `json:"num"`
			Size  int `json:"size"`
			Count int `json:"count"`
		} `json:"page"`
	}
	if err := c.getJSON(ctx, withQuery(apiHost+"/x/v2/reply", params), videoReferer("av"+strconv.FormatInt(aid, 10)), &data); err != nil {
		return CommentPage{}, err
	}
	out := CommentPage{Page: page}
	if data.Page.Num > 0 {
		out.Page = data.Page.Num
	}
	for _, reply := range data.Replies {
		if reply.RPID == 0 && strings.TrimSpace(reply.Content.Message) == "" {
			continue
		}
		out.Comments = append(out.Comments, Comment{
			ID: strconv.FormatInt(reply.RPID, 10), Author: reply.Member.Uname, AuthorID: reply.Member.Mid,
			Avatar: normalizeURL(reply.Member.Avatar), Content: html.UnescapeString(strings.TrimSpace(reply.Content.Message)),
			Likes: reply.Like, CreatedAt: time.Unix(reply.CTime, 0), ReplyCount: reply.RCount,
		})
	}
	if data.Page.Size <= 0 {
		data.Page.Size = 20
	}
	out.HasMore = len(out.Comments) > 0 && out.Page*data.Page.Size < data.Page.Count
	// 未登录接口只返回少量预览，下一页为空。不要把总数误当成可翻页结果。
	if !c.LoggedIn() && len(data.Replies) < data.Page.Size && data.Page.Count > len(data.Replies) {
		out.HasMore = false
		out.Notice = "登录 B 站后可查看完整评论"
	}
	return out, nil
}

func parseDuration(raw json.RawMessage) int {
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return parseClock(text)
	}
	var seconds int
	if json.Unmarshal(raw, &seconds) == nil {
		return seconds
	}
	return 0
}
