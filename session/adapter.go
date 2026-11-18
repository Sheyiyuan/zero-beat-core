package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/sheyiyuan/zero-beat-core/media"
)

// engineAdapter 把 session.Engine 装成 player.Controller 唯一可见的 player.Engine。
// 播放代次只在 PlayAt 里分配；在此之前 BeginPlay 已经使旧代次失效。
type engineAdapter struct {
	s      *Session
	events chan media.Event
}

func newEngineAdapter(s *Session) *engineAdapter {
	return &engineAdapter{s: s, events: make(chan media.Event)}
}

func (a *engineAdapter) Events() <-chan media.Event { return a.events }

func (a *engineAdapter) State() media.State {
	if a.s.engine == nil {
		return media.StateIdle
	}
	return a.s.engine.State()
}

func (a *engineAdapter) Position() time.Duration {
	if a.s.engine == nil {
		return 0
	}
	return a.s.engine.Position()
}

func (a *engineAdapter) Duration() time.Duration {
	if a.s.engine == nil {
		return 0
	}
	return a.s.engine.Duration()
}

func (a *engineAdapter) CurrentStream() media.Stream {
	if a.s.engine == nil {
		return media.Stream{}
	}
	return a.s.engine.CurrentStream()
}

func (a *engineAdapter) AutoGainSupported() bool {
	if a.s.engine == nil {
		return false
	}
	return a.s.engine.AutoGainSupported()
}

func (a *engineAdapter) SetVolume(v float64) {
	a.forward(func(ctx context.Context, id string) error {
		return a.s.engine.SetVolume(ctx, id, v)
	})
}

func (a *engineAdapter) SetCompensationDB(db float64) {
	a.forward(func(ctx context.Context, id string) error {
		return a.s.engine.SetCompensationDB(ctx, id, db)
	})
}

func (a *engineAdapter) SetAutoGain(on bool) {
	a.forward(func(ctx context.Context, id string) error {
		return a.s.engine.SetAutoGain(ctx, id, on)
	})
}

func (a *engineAdapter) Pause() {
	a.s.intent.PlayWhenReady = false
	a.s.playWhenReady = false
	if a.s.playbackID == "" {
		return
	}
	a.forward(func(ctx context.Context, id string) error {
		return a.s.engine.Pause(ctx, id)
	})
}

func (a *engineAdapter) Resume() {
	a.s.intent.PlayWhenReady = true
	a.s.playWhenReady = true
	if a.s.playbackID == "" {
		return
	}
	a.forward(func(ctx context.Context, id string) error {
		return a.s.engine.Resume(ctx, id)
	})
}

func (a *engineAdapter) Stop() {
	a.s.intent.PlayWhenReady = false
	a.s.playWhenReady = false
	if a.s.playbackID == "" {
		return
	}
	a.forward(func(ctx context.Context, id string) error {
		return a.s.engine.Stop(ctx, id)
	})
}

func (a *engineAdapter) Seek(ctx context.Context, pos time.Duration) error {
	if a.s.engine == nil {
		return errors.New("session: 没有引擎")
	}
	if a.s.playbackID == "" {
		return ErrStalePlayback
	}
	return a.s.engine.Seek(ctx, a.s.playbackID, pos)
}

func (a *engineAdapter) PlayAt(ctx context.Context, stream media.Stream, at time.Duration) error {
	if a.s.engine == nil {
		return errors.New("session: 没有引擎")
	}
	id := newPlaybackID()
	a.s.playbackID = id
	a.s.lastSeq = 0
	a.s.terminal = false
	a.s.playWhenReady = a.s.intent.PlayWhenReady
	// 接受确认与出声分开。这里只限制等待 ack 的时间，不把慢缓冲当成 RPC 超时。
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return a.s.engine.Load(ctx, id, stream, at, a.s.intent)
}

func (a *engineAdapter) forward(fn func(context.Context, string) error) {
	if a.s.engine == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = fn(ctx, a.s.playbackID)
}

func newPlaybackID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		n := time.Now().UnixNano()
		return hex.EncodeToString([]byte{
			byte(n), byte(n >> 8), byte(n >> 16), byte(n >> 24),
			byte(n >> 32), byte(n >> 40), byte(n >> 48), byte(n >> 56),
		})
	}
	return hex.EncodeToString(buf[:])
}
