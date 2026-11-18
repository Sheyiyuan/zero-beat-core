package netease

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	neaservice "github.com/go-musicfox/netease-music/service"
	"github.com/sheyiyuan/zero-beat-core/source"
)

var _ source.Browser = (*Client)(nil)

const browseLimit = 20
const programPrefix = "program:"

type neProfile struct {
	UserID    int64  `json:"userId"`
	Nickname  string `json:"nickname"`
	AvatarURL string `json:"avatarUrl"`
	Signature string `json:"signature"`
}
type neRadio struct {
	ID           int64     `json:"id"`
	Name         string    `json:"name"`
	Desc         string    `json:"desc"`
	PicURL       string    `json:"picUrl"`
	ProgramCount int       `json:"programCount"`
	DJ           neProfile `json:"dj"`
}
type neProgram struct {
	ID       int64     `json:"id"`
	Name     string    `json:"name"`
	CoverURL string    `json:"coverUrl"`
	Duration int64     `json:"duration"`
	MainSong neSong    `json:"mainSong"`
	DJ       neProfile `json:"dj"`
	Radio    neRadio   `json:"radio"`
}

func (p neProgram) track() source.Track {
	return source.Track{Source: source.NetEase, ExternalID: programPrefix + strconv.FormatInt(p.ID, 10), Page: 1, Title: p.Name, Artist: p.DJ.Nickname, Album: p.Radio.Name, Cover: normalizeURL(p.CoverURL), Duration: int(p.Duration / 1000)}
}

type browseResponse struct {
	Code    int  `json:"code"`
	More    bool `json:"more"`
	HasMore bool `json:"hasMore"`
	Count   int  `json:"count"`
	Artist  struct {
		ID        int64  `json:"id"`
		Name      string `json:"name"`
		PicURL    string `json:"picUrl"`
		BriefDesc string `json:"briefDesc"`
	} `json:"artist"`
	Profile  neProfile `json:"profile"`
	Songs    []neSong  `json:"songs"`
	Playlist struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		CoverImgURL string `json:"coverImgUrl"`
		TrackIDs    []struct {
			ID int64 `json:"id"`
		} `json:"trackIds"`
	} `json:"playlist"`
	DJRadio  neRadio     `json:"djRadio"`
	DJRadios []neRadio   `json:"djRadios"`
	Programs []neProgram `json:"programs"`
	Program  neProgram   `json:"program"`
}

// callBrowse 必须持有 opMu；在每个网络调用前后检查取消，避免旧页面继续串行请求。
func callBrowse(ctx context.Context, path string, call func() (float64, []byte), out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	code, body := call()
	if err := ctx.Err(); err != nil {
		return err
	}
	if code != 200 {
		return apiError(int(code), "内容不可用", path)
	}
	var status struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(body, &status); err != nil {
		return fmt.Errorf("netease: 解析 %s: %w", path, err)
	}
	if status.Code != 200 {
		return apiError(status.Code, "内容不可用", path)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("netease: 解析 %s: %w", path, err)
	}
	return nil
}
func neLink(kind source.BrowseKind, id int64, title, subtitle string) source.BrowseEntry {
	return source.BrowseEntry{Title: title, Subtitle: subtitle, Ref: source.BrowseRef{Source: source.NetEase, Kind: kind, ID: strconv.FormatInt(id, 10)}}
}
func appendSongs(out *source.BrowsePage, songs []neSong) {
	for _, s := range songs {
		if s.ID > 0 {
			t := s.track()
			out.Entries = append(out.Entries, source.BrowseEntry{Title: t.Title, Subtitle: t.Artist, Track: &t})
		}
	}
}
func (c *Client) BrowseRoots() []source.BrowseEntry {
	return []source.BrowseEntry{{Title: "网易云播客 / 电台", Ref: source.BrowseRef{Source: source.NetEase, Kind: source.BrowseRadios}}}
}
func (c *Client) TrackProfiles(ctx context.Context, ref source.Ref) ([]source.BrowseEntry, error) {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	if ref.Source != source.NetEase {
		return nil, fmt.Errorf("netease: 引用来源错误")
	}
	if strings.HasPrefix(ref.ExternalID, programPrefix) {
		p, err := c.programLocked(ctx, ref.ExternalID)
		if err != nil {
			return nil, err
		}
		var out []source.BrowseEntry
		if p.Radio.ID > 0 {
			out = append(out, neLink(source.BrowseRadio, p.Radio.ID, p.Radio.Name, "所属电台"))
		}
		if p.DJ.UserID > 0 {
			out = append(out, neLink(source.BrowseUser, p.DJ.UserID, p.DJ.Nickname, "主播主页"))
		}
		return out, nil
	}
	if err := validateContentID(ref.ExternalID); err != nil {
		return nil, err
	}
	var d browseResponse
	svc := &neaservice.SongDetailService{Ids: ref.ExternalID}
	if err := callBrowse(ctx, "song/detail", svc.SongDetail, &d); err != nil {
		return nil, err
	}
	var out []source.BrowseEntry
	seen := map[int64]bool{}
	for _, song := range d.Songs {
		artists := song.Ar
		if len(artists) == 0 {
			artists = song.Artists
		}
		for _, a := range artists {
			if a.ID > 0 && !seen[a.ID] {
				out = append(out, neLink(source.BrowseArtist, a.ID, a.Name, "歌手主页"))
				seen[a.ID] = true
			}
		}
	}
	return out, nil
}
func (c *Client) Browse(ctx context.Context, ref source.BrowseRef, page int) (source.BrowsePage, error) {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	out := source.BrowsePage{Page: max(1, page)}
	page = out.Page
	if ref.Source != source.NetEase {
		return out, fmt.Errorf("netease: 页面来源错误")
	}
	if ref.Kind != source.BrowseRadios {
		if err := validateContentID(ref.ID); err != nil {
			return out, err
		}
	}
	limit, offset := strconv.Itoa(browseLimit), strconv.Itoa((page-1)*browseLimit)
	var d browseResponse
	switch ref.Kind {
	case source.BrowseArtist:
		svc := &neaservice.ArtistsService{ID: ref.ID}
		if err := callBrowse(ctx, "artist", svc.Artists, &d); err != nil {
			return out, err
		}
		out.Title, out.Description, out.Cover = d.Artist.Name, d.Artist.BriefDesc, normalizeURL(d.Artist.PicURL)
		songs := &neaservice.ArtistSongsService{ID: ref.ID, Limit: limit, Offset: offset, Order: "hot"}
		if err := callBrowse(ctx, "artist/songs", songs.ArtistSongs, &d); err != nil {
			return out, err
		}
		appendSongs(&out, d.Songs)
		out.HasMore = d.More
	case source.BrowseUser:
		svc := &neaservice.UserDetailService{Uid: ref.ID}
		if err := callBrowse(ctx, "user/detail", svc.UserDetail, &d); err != nil {
			return out, err
		}
		out.Title, out.Description, out.Cover = d.Profile.Nickname, d.Profile.Signature, normalizeURL(d.Profile.AvatarURL)
		if page == 1 {
			out.Entries = append(out.Entries, source.BrowseEntry{Title: "TA 的播客节目", Ref: source.BrowseRef{Source: source.NetEase, Kind: source.BrowseUserPrograms, ID: ref.ID}})
		}
		// playlist 在用户列表中为数组，在歌单详情中为对象，分开解码。
		var list struct {
			More      bool `json:"more"`
			Playlists []struct {
				ID         int64  `json:"id"`
				Name       string `json:"name"`
				TrackCount int    `json:"trackCount"`
			} `json:"playlist"`
		}
		lists := &neaservice.UserPlaylistService{Uid: ref.ID, Limit: limit, Offset: offset}
		if err := callBrowse(ctx, "user/playlist", lists.UserPlaylist, &list); err != nil {
			return out, err
		}
		for _, p := range list.Playlists {
			if p.ID > 0 {
				out.Entries = append(out.Entries, neLink(source.BrowsePlaylist, p.ID, p.Name, fmt.Sprintf("%d 首", p.TrackCount)))
			}
		}
		out.HasMore = list.More
	case source.BrowseRadios:
		svc := &neaservice.DjHotService{Limit: limit, Offset: offset}
		if err := callBrowse(ctx, "djradio/hot", svc.DjHot, &d); err != nil {
			return out, err
		}
		out.Title = "网易云播客 / 电台"
		for _, r := range d.DJRadios {
			if r.ID > 0 {
				out.Entries = append(out.Entries, neLink(source.BrowseRadio, r.ID, r.Name, fmt.Sprintf("%s · %d 期", r.DJ.Nickname, r.ProgramCount)))
			}
		}
		out.HasMore = d.HasMore
	case source.BrowseRadio, source.BrowseUserPrograms:
		if ref.Kind == source.BrowseRadio {
			svc := &neaservice.DjDetailService{ID: ref.ID}
			if err := callBrowse(ctx, "djradio/get", svc.DjDetail, &d); err != nil {
				return out, err
			}
			r := d.DJRadio
			out.Title, out.Description, out.Cover = r.Name, r.Desc, normalizeURL(r.PicURL)
			if page == 1 && r.DJ.UserID > 0 {
				out.Entries = append(out.Entries, neLink(source.BrowseUser, r.DJ.UserID, r.DJ.Nickname, "主播主页"))
			}
			programs := &neaservice.DjProgramService{RID: ref.ID, Limit: limit, Offset: offset, Asc: "false"}
			if err := callBrowse(ctx, "dj/program/byradio", programs.DjProgram, &d); err != nil {
				return out, err
			}
		} else {
			out.Title = "TA 的播客节目"
			svc := &neaservice.UserDjService{Uid: ref.ID, Limit: limit, Offset: offset}
			if err := callBrowse(ctx, "dj/program/user", svc.UserDj, &d); err != nil {
				return out, err
			}
		}
		for _, p := range d.Programs {
			if p.ID > 0 && p.MainSong.ID > 0 {
				t := p.track()
				out.Entries = append(out.Entries, source.BrowseEntry{Title: t.Title, Subtitle: t.Artist, Track: &t})
			}
		}
		out.HasMore = d.More || d.Count > page*browseLimit
	case source.BrowsePlaylist:
		svc := &neaservice.PlaylistDetailService{Id: ref.ID}
		if err := callBrowse(ctx, "playlist/detail", svc.PlaylistDetail, &d); err != nil {
			return out, err
		}
		p := d.Playlist
		out.Title, out.Description, out.Cover = p.Name, p.Description, normalizeURL(p.CoverImgURL)
		start := min((page-1)*browseLimit, len(p.TrackIDs))
		end := min(start+browseLimit, len(p.TrackIDs))
		out.HasMore = end < len(p.TrackIDs)
		var ids []string
		for _, id := range p.TrackIDs[start:end] {
			ids = append(ids, strconv.FormatInt(id.ID, 10))
		}
		if len(ids) > 0 {
			svc := &neaservice.SongDetailService{Ids: strings.Join(ids, ",")}
			if err := callBrowse(ctx, "song/detail", svc.SongDetail, &d); err != nil {
				return out, err
			}
			appendSongs(&out, d.Songs)
		}
	default:
		return out, fmt.Errorf("netease: 不支持的页面类型 %s", ref.Kind)
	}
	if len(out.Entries) == 0 {
		out.HasMore = false
	}
	return out, nil
}

// programLocked 把节目 ID 转换成 mainSong，同时保持上层节目身份稳定。
func (c *Client) programLocked(ctx context.Context, raw string) (neProgram, error) {
	id := strings.TrimPrefix(raw, programPrefix)
	if err := validateContentID(id); err != nil {
		return neProgram{}, err
	}
	svc := &neaservice.DjProgramDetailService{ID: id}
	var d browseResponse
	if err := callBrowse(ctx, "dj/program/detail", svc.DjProgramDetail, &d); err != nil {
		return neProgram{}, err
	}
	if d.Program.ID <= 0 || d.Program.MainSong.ID <= 0 {
		return neProgram{}, fmt.Errorf("netease: 节目不存在或无可用音频")
	}
	return d.Program, nil
}
