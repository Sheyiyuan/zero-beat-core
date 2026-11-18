package bilibili

import (
	"context"
	"errors"
	"fmt"
	"github.com/sheyiyuan/zero-beat-core/bili"
	"github.com/sheyiyuan/zero-beat-core/source"
	"strconv"
)

var _ source.Browser = (*Source)(nil)

func (s *Source) BrowseRoots() []source.BrowseEntry { return nil }
func (s *Source) TrackProfiles(ctx context.Context, ref source.Ref) ([]source.BrowseEntry, error) {
	if ref.Source != source.Bilibili {
		return nil, fmt.Errorf("bilibili: 引用来源错误")
	}
	v, err := s.client.VideoInfo(ctx, ref.ExternalID)
	if err != nil {
		return nil, err
	}
	if v.OwnerID <= 0 {
		return nil, fmt.Errorf("bilibili: 视频缺少 UP 主信息")
	}
	return []source.BrowseEntry{{Title: v.Owner, Subtitle: "UP 主主页", Ref: source.BrowseRef{Source: source.Bilibili, Kind: source.BrowseUser, ID: strconv.FormatInt(v.OwnerID, 10)}}}, nil
}
func (s *Source) Browse(ctx context.Context, ref source.BrowseRef, page int) (source.BrowsePage, error) {
	out := source.BrowsePage{Page: max(1, page)}
	if ref.Source != source.Bilibili || ref.Kind != source.BrowseUser {
		return out, fmt.Errorf("bilibili: 不支持的页面")
	}
	p, err := s.client.Space(ctx, ref.ID)
	if err != nil {
		return out, spaceError(err)
	}
	out.Title, out.Description, out.Cover = p.Name, p.Sign, p.Face
	items, more, err := s.client.SpaceVideos(ctx, ref.ID, out.Page)
	if err != nil {
		return out, spaceError(err)
	}
	out.HasMore = more
	for _, v := range items {
		t := source.Track{Source: source.Bilibili, ExternalID: v.BVID, Page: 1, Title: v.Title, Artist: v.Author, Cover: v.Cover, Duration: v.Duration}
		out.Entries = append(out.Entries, source.BrowseEntry{Title: t.Title, Subtitle: t.Artist, Track: &t})
	}
	return out, nil
}

func spaceError(err error) error {
	var api *bili.APIError
	if errors.As(err, &api) {
		return fmt.Errorf("B 站主页 code=%d：%s；请稍后重试（%w）", api.Code, api.Message, err)
	}
	return err
}
