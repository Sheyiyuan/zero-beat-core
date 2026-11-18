package session

import (
	"context"
	"errors"
	"time"

	"github.com/sheyiyuan/zero-beat-core/media"
)

// ErrStalePlayback 表示命令或事件属于已被取代的播放代次。
var ErrStalePlayback = errors.New("session: stale playback")

// PlayIntent 是一次加载的出声意图。暂停中的 seek 或恢复必须为 false，
// 避免先出声再补一次暂停。
type PlayIntent struct {
	PlayWhenReady bool
}

// EngineEvent 是带播放代次和序号的引擎事件。
// 同一代次的 seq 单调递增。Ended 与 Failed 是终态。
type EngineEvent struct {
	PlaybackID string
	Seq        uint64
	Type       media.EventType
	State      media.State
	Position   time.Duration
	Duration   time.Duration
	Err        error
}

// Engine 是会话面对的音频输出。实现必须把回调绑定到产生它的播放代次，
// 不能把旧播放器的迟到事件改写成当前代次。
//
// Load 在命令被接受后返回，不表示已经出声。拒绝接受时返回错误。
// 过期的 playbackID 返回 ErrStalePlayback，且不得作用于新曲目。
// 尚无代次时，音量、补偿和自动增益只更新实现自己的缓存。
type Engine interface {
	Load(ctx context.Context, playbackID string, stream media.Stream, at time.Duration, intent PlayIntent) error
	Pause(ctx context.Context, playbackID string) error
	Resume(ctx context.Context, playbackID string) error
	Stop(ctx context.Context, playbackID string) error
	Seek(ctx context.Context, playbackID string, at time.Duration) error
	SetVolume(ctx context.Context, playbackID string, volume float64) error
	SetCompensationDB(ctx context.Context, playbackID string, db float64) error
	SetAutoGain(ctx context.Context, playbackID string, on bool) error
	AutoGainSupported() bool
	Position() time.Duration
	Duration() time.Duration
	State() media.State
	CurrentStream() media.Stream
	Events() <-chan EngineEvent
}
