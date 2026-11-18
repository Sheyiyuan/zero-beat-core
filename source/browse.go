package source

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// BrowseKind 区分主页、集合和发现页；集合引用不能作为歌曲引用播放。
type BrowseKind string

const (
	BrowseArtist       BrowseKind = "artist"
	BrowseUser         BrowseKind = "user"
	BrowseUserPrograms BrowseKind = "user-programs"
	BrowseRadio        BrowseKind = "radio"
	BrowseRadios       BrowseKind = "radios"
	BrowsePlaylist     BrowseKind = "playlist"
)

type BrowseRef struct {
	Source ID
	Kind   BrowseKind
	ID     string
}

// BrowseEntry 是可进入的页面，或可播放的曲目；两种引用互斥。
type BrowseEntry struct {
	Title, Subtitle string
	Ref             BrowseRef
	Track           *Track
}
type BrowsePage struct {
	Title, Description, Cover, Notice string
	Entries                           []BrowseEntry
	Page                              int
	HasMore                           bool
}

// Browser 是可选的只读内容浏览能力，GUI 与 TUI 共用。
type Browser interface {
	BrowseRoots() []BrowseEntry
	TrackProfiles(context.Context, Ref) ([]BrowseEntry, error)
	Browse(context.Context, BrowseRef, int) (BrowsePage, error)
}

// ParseBrowseTarget 接受明确类型的引用或平台主页链接，不把歌手/电台 ID 当成歌曲。
func ParseBrowseTarget(input string) (BrowseRef, error) {
	raw := strings.TrimSpace(input)
	parts := strings.Split(raw, ":")
	var ref BrowseRef
	if len(parts) == 3 && (parts[0] == string(NetEase) || parts[0] == string(Bilibili)) {
		ref = BrowseRef{Source: ID(parts[0]), Kind: BrowseKind(parts[1]), ID: parts[2]}
	} else if u, err := url.Parse(raw); err == nil && (u.Scheme == "https" || u.Scheme == "http") {
		switch strings.ToLower(u.Hostname()) {
		case "space.bilibili.com":
			ref = BrowseRef{Source: Bilibili, Kind: BrowseUser, ID: strings.Split(strings.Trim(u.Path, "/"), "/")[0]}
		case "music.163.com", "y.music.163.com":
			if strings.HasPrefix(u.Fragment, "/") {
				var err error
				u, err = url.Parse(u.Fragment)
				if err != nil {
					return BrowseRef{}, fmt.Errorf("主页链接无效: %w", err)
				}
			}
			kind := BrowseKind(strings.Trim(u.Path, "/"))
			if kind == "djradio" {
				kind = BrowseRadio
			}
			if kind == "user/home" {
				kind = BrowseUser
			}
			ref = BrowseRef{Source: NetEase, Kind: kind, ID: u.Query().Get("id")}
		}
	}
	id, err := strconv.ParseInt(ref.ID, 10, 64)
	valid := ref.Source == Bilibili && ref.Kind == BrowseUser || ref.Source == NetEase && (ref.Kind == BrowseArtist || ref.Kind == BrowseUser || ref.Kind == BrowseRadio || ref.Kind == BrowsePlaylist)
	if !valid || err != nil || id <= 0 {
		return BrowseRef{}, fmt.Errorf("请输入主页/电台链接，或 netease:artist:6452、netease:user:ID、netease:radio:ID、bilibili:user:ID")
	}
	return ref, nil
}
