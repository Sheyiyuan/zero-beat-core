package bili

import "time"

// User 是登录用户信息。
type User struct {
	Mid      int64
	Name     string
	Face     string
	LoggedIn bool
}

// VideoPage 是分 P 信息。
type VideoPage struct {
	CID      int64
	Page     int
	Part     string
	Duration int64 // 秒
}

// Video 是视频元数据。
type Video struct {
	Aid      int64
	BVID     string
	Title    string
	Cover    string
	OwnerID  int64
	Owner    string
	Duration int64 // 秒
	Pages    []VideoPage
}

// SearchItem 是搜索结果的视频条目。
type SearchItem struct {
	BVID     string
	Title    string
	Author   string
	Cover    string
	Duration int // 秒
	Play     int64
}

// RelatedItem 是视频相关推荐条目。
type RelatedItem struct {
	BVID     string
	Title    string
	Author   string
	Cover    string
	Duration int
	Play     int64
}

// Comment 是 B 站评论的统一字段。
type Comment struct {
	ID         string
	Author     string
	AuthorID   string
	Avatar     string
	Content    string
	Likes      int64
	CreatedAt  time.Time
	ReplyCount int
}

// CommentPage 是 B 站评论分页结果。
type CommentPage struct {
	Comments []Comment
	Page     int
	HasMore  bool
	Notice   string
}

// AudioTrack 是一条 DASH 音频轨。
type AudioTrack struct {
	ID        int
	URL       string
	BackupURL []string
	Bandwidth int64
	Codecs    string
	MimeType  string
	Quality   string // 展示用档位名，如「192k」/「FLAC」/「Dolby」
}

// Stream 是解析后可播放的音频流。
type Stream struct {
	URL        string
	Headers    map[string]string
	Codec      string
	DurationMS int64
	ExpiresAt  time.Time
	Quality    string
	BVID       string
	CID        int64
}

// Folder 是 B 站收藏夹。
type Folder struct {
	MediaID int64
	Title   string
	Count   int
}

// FavItem 是收藏夹里的一条内容。
type FavItem struct {
	BVID     string
	Title    string
	Cover    string
	Artist   string
	Duration int
}

// Quality 是音质偏好。
type Quality int

const (
	// QualityAuto 在可用轨道里取最好的。
	QualityAuto Quality = iota
	// QualityHigh 优先 192k 的 AAC。
	QualityHigh
	// QualityLossless 优先 FLAC（需要大会员）。
	QualityLossless
)
