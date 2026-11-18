package session

import (
	"context"
	"errors"
	"strings"

	"github.com/sheyiyuan/zero-beat-core/source"
	"github.com/sheyiyuan/zero-beat-core/store"
)

// Playlists 返回歌单列表，以及不阻断结果的告警文本。
// 网络读取不进入串行循环，避免挡住暂停和切歌。
func (s *Session) Playlists(ctx context.Context) ([]store.Playlist, string, error) {
	if err := s.ready(ctx); err != nil {
		return nil, "", err
	}
	got, w, err := s.svc.Playlists(ctx)
	var warn string
	if w != nil {
		warn = w.Error()
	}
	return got, warn, err
}

// Songs 在内存中分页返回歌单曲目。limit <= 0 时按 50，最大 200。
func (s *Session) Songs(ctx context.Context, playlistID string, offset, limit int) (SongPage, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	if err := s.ready(ctx); err != nil {
		return SongPage{}, err
	}
	if s.store == nil {
		return SongPage{}, ErrNotFound
	}
	playlist, err := s.store.Playlist(playlistID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return SongPage{}, ErrNotFound
		}
		return SongPage{}, err
	}
	items, _, err := s.svc.PlaylistSongs(ctx, playlist, false)
	if err != nil {
		return SongPage{}, err
	}
	total := len(items)
	start := offset
	if start > total {
		start = total
	}
	end := start + limit
	if end > total {
		end = total
	}
	return SongPage{
		Songs:  append([]store.Song(nil), items[start:end]...),
		Total:  total,
		Offset: start,
		Limit:  limit,
	}, nil
}

// Search 合并音源搜索。部分失败写入 Warning，全部失败才返回错误。
// page < 1 时按第 1 页。
func (s *Session) Search(ctx context.Context, keyword string, page int, sourceID string) (SearchHit, error) {
	if err := s.ready(ctx); err != nil {
		return SearchHit{}, err
	}
	if page < 1 {
		page = 1
	}
	res, err := s.svc.Search(ctx, keyword, page, source.ID(strings.TrimSpace(sourceID)))
	if err != nil {
		return SearchHit{}, err
	}
	hit := SearchHit{Candidates: res.Candidates, Pages: res.Pages}
	if res.Warn != nil {
		hit.Warning = res.Warn.Error()
	}
	return hit, nil
}

// OpenLink 只使用注册表的 ParseTarget。未识别不是错误。
func (s *Session) OpenLink(ctx context.Context, text string) (LinkResult, error) {
	if err := s.ready(ctx); err != nil {
		return LinkResult{}, err
	}
	ref, ok, err := s.svc.Sources().ParseTarget(text, 0)
	if err != nil {
		return LinkResult{}, err
	}
	if !ok {
		return LinkResult{Matched: false, Kind: LinkUnknown}, nil
	}
	song := store.NormalizeSong(store.Song{
		Source:   string(ref.Source),
		RemoteID: ref.ExternalID,
		Page:     ref.Page,
		CID:      ref.CID,
	})
	return LinkResult{Matched: true, Kind: LinkTrack, Ref: ref, Song: song}, nil
}

// Sources 返回音源摘要。登录 cookie 不会出现在结果里。
func (s *Session) Sources(ctx context.Context) ([]SourceStatus, error) {
	if err := s.ready(ctx); err != nil {
		return nil, err
	}
	var out []SourceStatus
	err := s.call(ctx, func() error {
		if s.svc.Sources() == nil {
			return nil
		}
		for _, src := range s.svc.Sources().All() {
			user := src.User()
			item := SourceStatus{
				ID:           string(src.ID()),
				Name:         src.Name(),
				LoggedIn:     src.LoggedIn(),
				UserName:     user.Name,
				Capabilities: capabilityNames(src.Caps()),
			}
			if src.Caps().Has(source.CapAccount) && !src.LoggedIn() {
				item.Notice = "未登录，匿名访问部分内容受限"
			}
			out = append(out, item)
		}
		return nil
	})
	return out, err
}

func (s *Session) ready(ctx context.Context) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if s == nil || s.closed.Load() {
		return ErrClosed
	}
	return nil
}

func capabilityNames(caps source.Capability) []string {
	known := []struct {
		bit  source.Capability
		name string
	}{
		{source.CapSearch, "search"},
		{source.CapMultiPart, "multi_part"},
		{source.CapRemotePlaylists, "remote_playlists"},
		{source.CapOfficialLyrics, "official_lyrics"},
		{source.CapQualitySelect, "quality_select"},
		{source.CapLossless, "lossless"},
		{source.CapDailyRecommendations, "daily_recommendations"},
		{source.CapPlaylistWrite, "playlist_write"},
		{source.CapAccount, "account"},
		{source.CapComments, "comments"},
		{source.CapRelated, "related"},
		{source.CapBrowse, "browse"},
	}
	var names []string
	for _, item := range known {
		if caps.Has(item.bit) {
			names = append(names, item.name)
		}
	}
	return names
}
