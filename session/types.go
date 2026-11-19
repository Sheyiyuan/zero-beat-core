package session

import (
	"errors"
	"time"

	"github.com/sheyiyuan/zero-beat-core/player"
	"github.com/sheyiyuan/zero-beat-core/source"
	"github.com/sheyiyuan/zero-beat-core/store"
)

const (
	// APIVersion 是本批 RPC 协商使用的协议版本。
	APIVersion = 1
	// CoreVersion 与尚未发布的 core 0.8.0 对齐，不代表已经打标签。
	CoreVersion = "0.8.0"
	// RepoVersion 是新同步仓库的 meta.v，握手只作兼容性提示。
	RepoVersion = 3
)

var (
	// ErrAlreadyRunning 表示同一数据目录的实例锁已被占用。
	ErrAlreadyRunning = errors.New("session: already running")
	// ErrClosed 表示会话已经关闭。
	ErrClosed = errors.New("session: closed")
	// ErrNotFound 表示歌单、曲目或队列位置不存在。
	ErrNotFound = errors.New("session: not found")
	// ErrUnsupported 表示当前批次没有实现该能力。
	ErrUnsupported = errors.New("session: unsupported")
)

// AlreadyRunningError 在拿不到锁时携带对方 PID。PID 为 0 表示锁文件里没有可读 PID。
type AlreadyRunningError struct {
	PID int
	Err error
}

func (e *AlreadyRunningError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return ErrAlreadyRunning.Error()
}

func (e *AlreadyRunningError) Unwrap() error {
	if e.Err != nil {
		return e.Err
	}
	return ErrAlreadyRunning
}

// OpenOptions 控制会话启动。测试必须传入 DataDir，避免碰用户目录。
//
// Lock 为 nil 时：platform 为 android 则不加锁，其余平台加锁。
// Android 必须提供 DataDir，且不调用 os.UserConfigDir。
// Sources 为 nil 时注册内置的哔哩哔哩、网易云和本地音源，并装入已保存的登录态。
// OpenStore 与 AfterOpen 只给测试注入；生产路径保持 nil。
type OpenOptions struct {
	DataDir   string
	Platform  string
	Lock      *bool
	Now       func() time.Time
	Engine    Engine
	Sources   *source.Registry
	OpenStore func(path string) (*store.Store, error)
	AfterOpen func(*store.Store) error
	// ReadyTimeout 为 0 时使用 20 秒起播期限。SlowNotice 为 0 时使用 5 秒。
	ReadyTimeout time.Duration
	SlowNotice   time.Duration
}

// SongPage 是歌单曲目的一页。
type SongPage struct {
	Songs  []store.Song
	Total  int
	Offset int
	Limit  int
}

// SearchHit 是搜索结果。Warning 非空表示部分音源失败，其余结果仍然有效。
type SearchHit struct {
	Candidates []source.Candidate
	Pages      int
	Warning    string
}

// LinkKind 区分链接解析结果。本批只把歌曲链接交给播放；浏览链接标记为 browse。
type LinkKind string

const (
	LinkTrack   LinkKind = "track"
	LinkBrowse  LinkKind = "browse"
	LinkUnknown LinkKind = "unknown"
)

// LinkResult 是用户粘贴内容的解析结果。未识别时 Matched 为 false。
type LinkResult struct {
	Matched bool
	Kind    LinkKind
	Ref     source.Ref
	Song    store.Song
}

// PlayInput 选择要播放的曲目。Song 非空时优先使用它（搜索结果尚未入库）。
// Queue 非空时用这份列表替换当前队列，再从 Song 或 QueueIndex 起播。
// QueueIndex >= 0 且没有新队列时，改为播放队列中的该位置。StartAt 是绝对起点。
// RandomStart 表示「播放全部」：随机模式下第一首也参与洗牌，和 TUI 从歌单开头播放一致。
// 显式点某一首时不要置位，那首歌必须留在当前索引。
type PlayInput struct {
	SongID      string
	Song        *store.Song
	Queue       []store.Song
	QueueIndex  int
	StartAt     time.Duration
	RandomStart bool
}

// Accepted 是会话已经接受的播放请求。RequestID 是控制器请求号，不是播放代次。
// Started 为 false 表示没有发起新的解析（例如队列尽头的下一首）。
type Accepted struct {
	RequestID int
	Started   bool
}

// Snapshot 是 UI 应消费的播放快照。PlaybackID 为空表示当前没有已加载的代次。
type Snapshot struct {
	Player        player.State
	PlaybackID    string
	PlayWhenReady bool
	Notice        string
	NoticeLevel   string
}

// SourceStatus 是「我的」页展示的音源摘要，不含 cookie 或 token。
type SourceStatus struct {
	ID           string
	Name         string
	LoggedIn     bool
	UserName     string
	Capabilities []string
	Notice       string
}
