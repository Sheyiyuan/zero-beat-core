package netease

import (
	"context"
	"encoding/json"
	"fmt"
	neaservice "github.com/go-musicfox/netease-music/service"
	"github.com/sheyiyuan/zero-beat-core/source"
	"strconv"
	"strings"
	"time"
)

// Comments 实现 source.CommentReader，读取网易云歌曲评论。
func (c *Client) Comments(ctx context.Context, ref source.Ref, page int) (source.CommentPage, error) {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	if ref.Source != source.NetEase {
		return source.CommentPage{}, fmt.Errorf("netease: 评论引用来源不是网易云")
	}
	if err := validateContentID(strings.TrimPrefix(ref.ExternalID, programPrefix)); err != nil {
		return source.CommentPage{}, err
	}
	if page <= 0 {
		page = 1
	}
	if err := ctx.Err(); err != nil {
		return source.CommentPage{}, err
	}
	const limit = 20
	svc := &neaservice.CommentMusicService{ID: ref.ExternalID, Limit: strconv.Itoa(limit), Offset: strconv.Itoa((page - 1) * limit)}
	var code float64
	var body []byte
	if strings.HasPrefix(ref.ExternalID, programPrefix) {
		dj := &neaservice.CommentDjService{ID: strings.TrimPrefix(ref.ExternalID, programPrefix), Limit: svc.Limit, Offset: svc.Offset}
		code, body = dj.CommentDj()
	} else {
		code, body = svc.CommentMusic()
	}
	if err := ctx.Err(); err != nil {
		return source.CommentPage{}, err
	}
	if code != 200 {
		return source.CommentPage{}, apiError(int(code), "评论不可用", "resource/comments")
	}
	return parseComments(body, page)
}

// Related 实现 source.RelatedRecommender，使用网易云「相似歌曲」接口。
func (c *Client) Related(ctx context.Context, ref source.Ref) ([]source.Candidate, error) {
	if strings.HasPrefix(ref.ExternalID, programPrefix) {
		return nil, fmt.Errorf("netease: 播客暂无相似歌曲，请在主页入口浏览所属电台")
	}
	c.opMu.Lock()
	defer c.opMu.Unlock()
	if ref.Source != source.NetEase {
		return nil, fmt.Errorf("netease: 推荐引用来源不是网易云")
	}
	if err := validateContentID(ref.ExternalID); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	svc := &neaservice.SimiSongService{ID: ref.ExternalID, Limit: "30"}
	code, body := svc.SimiSong()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if code != 200 {
		return nil, apiError(int(code), "相关推荐不可用", "discovery/simiSong")
	}
	return parseRelated(body)
}

func parseComments(body []byte, page int) (source.CommentPage, error) {
	var data struct {
		Code     int  `json:"code"`
		More     bool `json:"more"`
		Comments []struct {
			CommentID int64  `json:"commentId"`
			Content   string `json:"content"`
			Liked     int64  `json:"likedCount"`
			Time      int64  `json:"time"`
			User      struct {
				UserID   int64  `json:"userId"`
				Nickname string `json:"nickname"`
				Avatar   string `json:"avatarUrl"`
			} `json:"user"`
			ReplyCount int `json:"replyCount"`
		} `json:"comments"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return source.CommentPage{}, fmt.Errorf("netease: 解析评论响应: %w", err)
	}
	if data.Code != 200 {
		return source.CommentPage{}, apiError(data.Code, "评论不可用", "resource/comments")
	}
	out := source.CommentPage{Page: page, HasMore: data.More}
	for _, item := range data.Comments {
		if item.CommentID == 0 && strings.TrimSpace(item.Content) == "" {
			continue
		}
		var created time.Time
		if item.Time > 0 {
			created = time.UnixMilli(item.Time)
		}
		var authorRef source.BrowseRef
		if item.User.UserID > 0 {
			authorRef = source.BrowseRef{Source: source.NetEase, Kind: source.BrowseUser, ID: strconv.FormatInt(item.User.UserID, 10)}
		}
		out.Comments = append(out.Comments, source.Comment{ID: strconv.FormatInt(item.CommentID, 10),
			Author: item.User.Nickname, AuthorRef: authorRef, Avatar: normalizeURL(item.User.Avatar), Content: strings.TrimSpace(item.Content),
			Likes: item.Liked, CreatedAt: created, ReplyCount: item.ReplyCount})
	}
	out.HasMore = out.HasMore && len(out.Comments) > 0
	return out, nil
}

func parseRelated(body []byte) ([]source.Candidate, error) {
	var data struct {
		Code         int      `json:"code"`
		Songs        []neSong `json:"songs"`
		SimilarSongs []neSong `json:"similarSongs"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("netease: 解析相关推荐: %w", err)
	}
	if data.Code != 200 {
		return nil, apiError(data.Code, "相关推荐不可用", "discovery/simiSong")
	}
	if len(data.Songs) == 0 {
		data.Songs = data.SimilarSongs
	}
	out := make([]source.Candidate, 0, len(data.Songs))
	for _, item := range data.Songs {
		if item.ID == 0 {
			continue
		}
		out = append(out, source.Candidate{Track: item.track(), VIP: item.Fee == 1 || item.Fee == 4})
	}
	return out, nil
}

func validateContentID(raw string) error {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return fmt.Errorf("netease: 歌曲 ID 无效: %s", raw)
	}
	return nil
}
