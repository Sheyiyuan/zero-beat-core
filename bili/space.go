package bili

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
)

type SpaceProfile struct {
	Name string `json:"name"`
	Face string `json:"face"`
	Sign string `json:"sign"`
}

// Space 读取 UP 主公开资料，保留平台风控错误供调用方显示。
func (c *Client) Space(ctx context.Context, mid string) (SpaceProfile, error) {
	var out SpaceProfile
	if n, err := strconv.ParseInt(mid, 10, 64); err != nil || n <= 0 {
		return out, fmt.Errorf("bilibili: 用户 ID 无效")
	}
	q, err := c.signedQuery(url.Values{"mid": {mid}})
	if err != nil {
		return out, err
	}
	err = c.getJSON(ctx, withQuery(apiHost+"/x/space/wbi/acc/info", q), "https://space.bilibili.com/"+mid, &out)
	return out, err
}
func (c *Client) SpaceVideos(ctx context.Context, mid string, page int) ([]SearchItem, bool, error) {
	if n, err := strconv.ParseInt(mid, 10, 64); err != nil || n <= 0 {
		return nil, false, fmt.Errorf("bilibili: 用户 ID 无效")
	}
	page = max(1, page)
	q, err := c.signedQuery(url.Values{"mid": {mid}, "pn": {strconv.Itoa(page)}, "ps": {"20"}, "order": {"pubdate"}, "platform": {"web"}})
	if err != nil {
		return nil, false, err
	}
	var d struct {
		List struct {
			VList []struct {
				BVID   string `json:"bvid"`
				Title  string `json:"title"`
				Author string `json:"author"`
				Pic    string `json:"pic"`
				Length string `json:"length"`
				Play   int64  `json:"play"`
			} `json:"vlist"`
		} `json:"list"`
		Page struct {
			Count int `json:"count"`
		} `json:"page"`
	}
	if err := c.getJSON(ctx, withQuery(apiHost+"/x/space/wbi/arc/search", q), "https://space.bilibili.com/"+mid, &d); err != nil {
		return nil, false, err
	}
	var out []SearchItem
	for _, v := range d.List.VList {
		if v.BVID != "" {
			out = append(out, SearchItem{BVID: v.BVID, Title: stripEm(v.Title), Author: v.Author, Cover: normalizeURL(v.Pic), Duration: parseClock(v.Length), Play: v.Play})
		}
	}
	return out, len(out) > 0 && page*20 < d.Page.Count, nil
}
