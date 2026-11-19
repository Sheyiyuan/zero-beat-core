package session

import (
	"context"
	"errors"
	"time"

	"github.com/sheyiyuan/zero-beat-core/media"
	"github.com/sheyiyuan/zero-beat-core/player"
	"github.com/sheyiyuan/zero-beat-core/source"
	"github.com/sheyiyuan/zero-beat-core/store"
)

// Play 接受一次显式起播。解析在后台进行，返回时只表示请求已被登记。
func (s *Session) Play(ctx context.Context, in PlayInput) (Accepted, error) {
	var out Accepted
	err := s.call(ctx, func() error {
		s.autoHold.Store(false)
		if len(in.Queue) > 0 {
			req, err := s.playReplacingQueue(in)
			if err != nil {
				return err
			}
			out = Accepted{RequestID: req, Started: true}
			return nil
		}
		song, err := s.songForPlay(in)
		if err != nil {
			return err
		}
		req := s.startPlay(song, in.StartAt, 0, PlayIntent{PlayWhenReady: true})
		out = Accepted{RequestID: req, Started: true}
		return nil
	})
	return out, err
}

// Toggle 在暂停、继续和重新起播之间切换。需要起播时使用新的重试预算。
func (s *Session) Toggle(ctx context.Context) (Accepted, error) {
	var out Accepted
	err := s.call(ctx, func() error {
		if _, ok := s.ctrl.Current(); !ok {
			if song, ok := s.resumeTarget(); ok {
				s.autoHold.Store(false)
				out.RequestID = s.startPlay(song, 0, 0, PlayIntent{PlayWhenReady: true})
				out.Started = true
			}
			return nil
		}
		if s.ctrl.Awaiting() {
			if s.intent.PlayWhenReady {
				s.adapt.Pause()
				s.setPlaybackState(media.StatePaused)
			} else {
				s.adapt.Resume()
				s.setPlaybackState(media.StateBuffering)
			}
			s.publish()
			return nil
		}
		res := s.ctrl.Toggle()
		out.RequestID = s.ctrl.Export().Request
		switch res.Action {
		case player.ToggleNeedPlay:
			if res.Song.ID == "" && res.Song.ExternalID() == "" {
				s.publish()
				return nil
			}
			s.autoHold.Store(false)
			out.RequestID = s.startPlay(res.Song, res.StartAt, 0, PlayIntent{PlayWhenReady: true})
			out.Started = true
		case player.ToggleBusy:
			s.setNotice("正在缓冲，请稍候", "info")
		default:
			s.publish()
		}
		return nil
	})
	return out, err
}

// Pause 暂停当前代次。没有已加载的流时只把快照留在暂停。
func (s *Session) Pause(ctx context.Context) error {
	return s.call(ctx, func() error {
		if s.playbackID != "" && s.engine != nil {
			cctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := s.engine.Pause(cctx, s.playbackID); err != nil {
				return err
			}
		}
		s.setPlaybackState(media.StatePaused)
		s.playWhenReady = false
		s.intent.PlayWhenReady = false
		s.ctrl.Persist()
		s.publish()
		return nil
	})
}

// Resume 继续当前流。流已失效时按暂停前的位置重新解析，并使用新的重试预算。
func (s *Session) Resume(ctx context.Context) error {
	return s.call(ctx, func() error {
		if s.ctrl.Awaiting() {
			s.adapt.Resume()
			s.setPlaybackState(media.StateBuffering)
			s.publish()
			return nil
		}
		s.autoHold.Store(false)
		if s.canResumeCurrent() {
			cctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := s.engine.Resume(cctx, s.playbackID); err != nil {
				return err
			}
			s.setPlaybackState(media.StatePlaying)
			s.playWhenReady = true
			s.intent.PlayWhenReady = true
			s.ctrl.Persist()
			s.publish()
			return nil
		}
		song, ok := s.resumeTarget()
		if !ok {
			return ErrNotFound
		}
		at := s.ctrl.Position()
		if at <= 0 {
			at = s.ctrl.Export().ResumeAt
		}
		s.startPlay(song, at, 0, PlayIntent{PlayWhenReady: true})
		return nil
	})
}

// resumeTarget preserves the selected queue position after an explicit stop.
func (s *Session) resumeTarget() (store.Song, bool) {
	if song, ok := s.ctrl.Current(); ok {
		return song, true
	}
	state := s.ctrl.Export()
	if state.Index >= 0 && state.Index < len(state.Queue) {
		return state.Queue[state.Index], true
	}
	return store.Song{}, false
}

func (s *Session) canResumeCurrent() bool {
	if s.playbackID == "" || s.engine == nil {
		return false
	}
	if s.engine.State() != media.StatePaused {
		return false
	}
	stream := s.engine.CurrentStream()
	if stream.URL == "" {
		return false
	}
	if !stream.ExpiresAt.IsZero() && !s.now().Before(stream.ExpiresAt) {
		return false
	}
	return true
}

// Next 按当前模式切到下一首。顺序模式在队尾不发起新的解析。
func (s *Session) Next(ctx context.Context) (Accepted, error) {
	return s.skip(ctx, true)
}

// Prev 切到上一首。顺序模式在队首不发起新的解析。
func (s *Session) Prev(ctx context.Context) (Accepted, error) {
	return s.skip(ctx, false)
}

func (s *Session) skip(ctx context.Context, next bool) (Accepted, error) {
	var out Accepted
	err := s.call(ctx, func() error {
		s.autoHold.Store(false)
		var (
			song store.Song
			ok   bool
		)
		if next {
			song, _, ok = s.ctrl.Next()
		} else {
			song, _, ok = s.ctrl.Prev()
		}
		if !ok {
			out = Accepted{RequestID: s.ctrl.Export().Request}
			s.publish()
			return nil
		}
		out = Accepted{
			RequestID: s.startPlay(song, 0, 0, PlayIntent{PlayWhenReady: true}),
			Started:   true,
		}
		return nil
	})
	return out, err
}

// SeekMS 跳到绝对毫秒。过期或无法跳转时按当前重试预算重新取流，不把预算清零。
func (s *Session) SeekMS(ctx context.Context, positionMS int64) error {
	return s.call(ctx, func() error {
		if positionMS < 0 {
			positionMS = 0
		}
		if _, ok := s.ctrl.Current(); !ok {
			return ErrNotFound
		}
		target := time.Duration(positionMS) * time.Millisecond
		paused := s.engine != nil && s.engine.State() == media.StatePaused
		if s.ctrl.Export().PlaybackState == media.StatePaused {
			paused = true
		}
		base := s.ctrl.Position()
		if s.engine != nil && s.engine.State() != media.StateIdle && s.engine.CurrentStream().URL != "" {
			base = s.engine.Position()
		}
		song, _ := s.ctrl.Current()
		res := s.ctrl.Seek(ctx, target-base, s.clipFor(song), false)
		if res.Action == player.ToggleBusy {
			return errors.New("session: 正在缓冲")
		}
		if res.Action == player.ToggleNeedPlay {
			s.autoHold.Store(false)
			intent := PlayIntent{PlayWhenReady: !paused}
			retry := s.ctrl.Export().Retry
			s.startPlay(res.Song, res.StartAt, retry, intent)
			return nil
		}
		s.publish()
		return res.Err
	})
}

// SetVolume 把音量夹到 0..1，应用到引擎并写入设置。
func (s *Session) SetVolume(ctx context.Context, volume float64) error {
	return s.call(ctx, func() error {
		if volume < 0 {
			volume = 0
		}
		if volume > 1 {
			volume = 1
		}
		s.ctrl.SetVolume(volume)
		s.svc.SaveVolume(volume)
		s.publish()
		return nil
	})
}

// SetMode 切换播放模式并持久化。
func (s *Session) SetMode(ctx context.Context, mode player.Mode) error {
	return s.call(ctx, func() error {
		s.ctrl.SetMode(mode)
		s.svc.SaveMode(int(mode))
		s.ctrl.Persist()
		s.publish()
		return nil
	})
}

// Enqueue 把歌曲追加到队列末尾并落盘。
func (s *Session) Enqueue(ctx context.Context, song store.Song) (int, error) {
	var idx int
	err := s.call(ctx, func() error {
		idx = s.ctrl.Enqueue(s.remember(song))
		s.ctrl.Persist()
		s.publish()
		return nil
	})
	return idx, err
}

// InsertNext 把歌曲插到当前曲目之后并落盘。
func (s *Session) InsertNext(ctx context.Context, song store.Song) (int, error) {
	var idx int
	err := s.call(ctx, func() error {
		idx = s.ctrl.InsertNext(s.remember(song))
		s.ctrl.Persist()
		s.publish()
		return nil
	})
	return idx, err
}

// RemoveAt 删除队列中的一项。删掉正在播放的曲目时停止出声，不自动改播下一首。
func (s *Session) RemoveAt(ctx context.Context, index int) error {
	return s.call(ctx, func() error {
		before := len(s.ctrl.Queue())
		res, _ := s.ctrl.RemoveItem(index)
		if len(s.ctrl.Queue()) == before {
			return ErrNotFound
		}
		if res.RemovingCurrent {
			s.invalidatePlayback(true)
			if res.Empty {
				s.ctrl.Stop()
			} else {
				st := s.ctrl.Export()
				st.Current = nil
				st.Awaiting = false
				st.PlaybackState = media.StateIdle
				st.Position = 0
				st.ResumeAt = 0
				st.Retry = 0
				s.ctrl.Load(st)
			}
		}
		s.ctrl.Persist()
		s.publish()
		return nil
	})
}

// MoveAt 按 delta 移动队列项并落盘。
func (s *Session) MoveAt(ctx context.Context, index, delta int) error {
	return s.call(ctx, func() error {
		queue := s.ctrl.Queue()
		if index < 0 || index >= len(queue) {
			return ErrNotFound
		}
		s.ctrl.MoveItem(index, delta)
		s.ctrl.Persist()
		s.publish()
		return nil
	})
}

// ClearUpcoming 清空待播队列，保留当前曲目。
func (s *Session) ClearUpcoming(ctx context.Context) error {
	return s.call(ctx, func() error {
		s.ctrl.Clear()
		s.ctrl.Persist()
		s.publish()
		return nil
	})
}

func (s *Session) playReplacingQueue(in PlayInput) (int, error) {
	songs := make([]store.Song, 0, len(in.Queue))
	for _, song := range in.Queue {
		songs = append(songs, s.remember(song))
	}
	idx := 0
	if in.Song == nil && in.QueueIndex >= 0 && in.QueueIndex < len(songs) {
		idx = in.QueueIndex
	}
	if in.Song != nil {
		want := s.remember(*in.Song)
		found := -1
		for i, song := range songs {
			if song.ID == want.ID {
				songs[i] = want
				found = i
				break
			}
		}
		if found < 0 {
			songs = append(songs, want)
			found = len(songs) - 1
		}
		idx = found
	}
	if len(songs) == 0 {
		return 0, ErrNotFound
	}
	if in.RandomStart && in.Song == nil && s.ctrl.Mode() == player.ModeShuffle {
		if !s.ctrl.LoadQueueRandom(songs) {
			return 0, ErrNotFound
		}
	} else {
		s.ctrl.LoadQueue(songs, idx)
	}
	queue := s.ctrl.Queue()
	if len(queue) == 0 {
		return 0, ErrNotFound
	}
	current := queue[s.ctrl.Index()]
	req := s.startPlay(current, in.StartAt, 0, PlayIntent{PlayWhenReady: true})
	s.ctrl.Persist()
	return req, nil
}

func (s *Session) songForPlay(in PlayInput) (store.Song, error) {
	if in.Song != nil {
		song := s.remember(*in.Song)
		s.focusSong(song)
		return song, nil
	}
	if in.QueueIndex >= 0 {
		queue := s.ctrl.Queue()
		if in.QueueIndex >= len(queue) {
			return store.Song{}, ErrNotFound
		}
		s.ctrl.SetIndex(in.QueueIndex)
		return queue[in.QueueIndex], nil
	}
	if in.SongID != "" {
		for i, song := range s.ctrl.Queue() {
			if song.ID == in.SongID {
				s.ctrl.SetIndex(i)
				return song, nil
			}
		}
		if s.store == nil {
			return store.Song{}, ErrNotFound
		}
		song, err := s.store.Song(in.SongID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return store.Song{}, ErrNotFound
			}
			return store.Song{}, err
		}
		song = s.remember(song)
		s.focusSong(song)
		return song, nil
	}
	return store.Song{}, ErrNotFound
}

func (s *Session) focusSong(song store.Song) {
	for i, item := range s.ctrl.Queue() {
		if item.ID == song.ID {
			s.ctrl.SetIndex(i)
			return
		}
	}
	s.ctrl.SetIndex(s.ctrl.Enqueue(song))
}

func (s *Session) remember(song store.Song) store.Song {
	if s.svc == nil {
		return store.NormalizeSong(song)
	}
	return s.svc.UpsertSong(song)
}

func (s *Session) clipFor(song store.Song) store.Clip {
	if s.clipPreviewSong == song.ID {
		return s.clipPreviewClip
	}
	if s.svc == nil || song.ID == "" {
		return store.Clip{}
	}
	clip, err := s.svc.Clip(song.ID)
	if err != nil {
		return store.Clip{}
	}
	return clip
}

func (s *Session) startPlay(song store.Song, at time.Duration, retry int, intent PlayIntent) int {
	if s.clipTarget != song.ID {
		s.clipTarget = song.ID
		s.clipEpoch++
	}
	if s.clipPreviewSong != song.ID {
		s.clipPreviewSong = ""
		s.clipPreviewClip = store.Clip{}
	}
	s.intent = intent
	s.playWhenReady = intent.PlayWhenReady
	req, song := s.begin(song, at, retry)
	s.launchResolve(req, song, at, retry)
	return req
}

func (s *Session) begin(song store.Song, at time.Duration, retry int) (int, store.Song) {
	s.cancelResolve()
	s.readyArmed = false
	s.invalidatePlayback(true)
	req, song := s.ctrl.BeginPlay(song, at, retry)
	s.publish()
	return req, song
}

func (s *Session) invalidatePlayback(stop bool) {
	id := s.playbackID
	s.playbackID = ""
	s.terminal = true
	if !stop || id == "" || s.engine == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = s.engine.Stop(ctx, id)
}

func (s *Session) launchResolve(req int, song store.Song, at time.Duration, retry int) {
	gen := s.playGen.Add(1)
	ctx, cancel := context.WithCancel(s.root)
	s.mu.Lock()
	s.resolveCancel = cancel
	s.mu.Unlock()
	quality := s.ctrl.Quality()
	cached, hasCached := s.prefetch.Take(song.ID, s.now())
	prefetched := s.prefetchSong
	hasCached = hasCached && s.prefetchQuality == quality && s.prefetchLocal == s.svc.LocalFirstEnabled() && retry == 0
	s.resolveWG.Add(1)
	go func() {
		defer s.resolveWG.Done()
		resolved, stream, err := song, cached, error(nil)
		if hasCached {
			resolved = prefetched
		} else {
			resolved, stream, err = s.svc.Resolve(ctx, song, quality)
		}
		if err != nil {
			resolved = song
		}
		s.post(func() {
			s.finishResolve(gen, req, resolved, stream, at, retry, err)
		})
	}()
}

func (s *Session) finishResolve(gen uint64, req int, song store.Song, stream source.Stream, at time.Duration, retry int, err error) {
	defer s.resolvesDone.Add(1)
	if s.closed.Load() || s.autoHold.Load() || gen != s.playGen.Load() {
		return
	}
	if song.ID != "" {
		s.ctrl.SetSongCompDB(s.svc.SongCompDB(song.ID))
	}
	res := s.ctrl.CompletePlay(s.root, req, song, stream, at, retry, s.clipFor(song), false, err)
	if s.closed.Load() || s.autoHold.Load() || gen != s.playGen.Load() {
		s.abortFreshLoad()
		return
	}
	switch {
	case res.Stale:
		return
	case res.Retry:
		s.setNotice("正在重试…", "warn")
		s.startPlay(res.Song, res.StartAt, res.RetryCount, s.intent)
	case res.Err != nil:
		s.finishLoadError(res.Err)
	case res.Started:
		s.ctrl.SetBuffering(res.StartAt)
		s.armReady()
		s.publish()
	default:
		s.publish()
	}
}

func (s *Session) abortFreshLoad() {
	if s.playbackID == "" {
		return
	}
	s.invalidatePlayback(true)
	st := s.ctrl.Export()
	st.Awaiting = false
	if st.PlaybackState == media.StatePlaying || st.PlaybackState == media.StateBuffering {
		st.PlaybackState = media.StatePaused
	}
	s.ctrl.Load(st)
	s.playWhenReady = false
	s.publish()
}

func (s *Session) finishLoadError(err error) {
	s.readyArmed = false
	s.terminal = true
	ev := s.ctrl.HandleEvent(media.Event{Type: media.EventFailed, Err: err})
	s.applyEventResult(ev)
}

func (s *Session) onEngineEvent(ev EngineEvent) {
	defer s.eventsSeen.Add(1)
	if s.autoHold.Load() {
		return
	}
	if ev.PlaybackID == "" || ev.PlaybackID != s.playbackID {
		return
	}
	if ev.Seq <= s.lastSeq {
		return
	}
	if s.terminal {
		return
	}
	s.lastSeq = ev.Seq
	switch ev.Type {
	case media.EventEnded, media.EventFailed:
		s.terminal = true
		s.readyArmed = false
		res := s.ctrl.HandleEvent(media.Event{Type: ev.Type, State: ev.State, Err: ev.Err})
		s.applyEventResult(res)
	default:
		s.noteEngineState(ev.State)
	}
}

func (s *Session) applyEventResult(res player.EventResult) {
	switch {
	case res.Retry:
		if s.autoHold.Load() {
			s.setNotice("播放失败", "error")
			return
		}
		s.setNotice("正在重试…", "warn")
		s.startPlay(res.Song, res.StartAt, s.ctrl.Export().Retry, s.intent)
	case res.Ended:
		// A clip preview ends at its B point without advancing the queue. The
		// editor must stay pinned to the same song so it can save or cancel.
		if s.clipPreviewSong != "" {
			s.playWhenReady = false
			s.intent.PlayWhenReady = false
			s.setPlaybackState(media.StatePaused)
			s.publish()
			return
		}
		s.advanceFromEnd()
	case res.Err != nil:
		s.setNotice("播放失败："+res.Err.Error(), "error")
	default:
		s.publish()
	}
}

func (s *Session) advanceFromEnd() {
	if s.autoHold.Load() {
		s.publish()
		return
	}
	song, _, ok := s.ctrl.Next()
	if !ok {
		s.publish()
		return
	}
	s.startPlay(song, 0, 0, PlayIntent{PlayWhenReady: true})
}

func (s *Session) noteEngineState(st media.State) {
	switch st {
	case media.StateBuffering, media.StatePlaying, media.StatePaused:
		s.setPlaybackState(st)
	}
	if s.reachedReady(st) {
		s.readyArmed = false
		if s.notice == slowNoticeText {
			s.notice = ""
			s.noticeLevel = ""
		}
	}
	s.publish()
}

func (s *Session) reachedReady(st media.State) bool {
	if st == media.StatePlaying {
		return true
	}
	return st == media.StatePaused && !s.playWhenReady
}

func (s *Session) setPlaybackState(st media.State) {
	cur := s.ctrl.Export()
	if cur.PlaybackState == st {
		return
	}
	cur.PlaybackState = st
	s.ctrl.Load(cur)
}

func (s *Session) armReady() {
	s.readyArmed = true
	s.slowSent = false
	s.armedAt = s.now()
}

func (s *Session) checkDeadlines() {
	if !s.readyArmed || s.playbackID == "" {
		return
	}
	elapsed := s.now().Sub(s.armedAt)
	if !s.slowSent && elapsed >= s.slowWait {
		s.slowSent = true
		s.setNotice(slowNoticeText, "warn")
	}
	if elapsed < s.readyWait {
		return
	}
	s.readyArmed = false
	id := s.playbackID
	if s.engine != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = s.engine.Stop(ctx, id)
		cancel()
	}
	s.onEngineEvent(EngineEvent{
		PlaybackID: id,
		Seq:        s.lastSeq + 1,
		Type:       media.EventFailed,
		State:      media.StateFailed,
		Err:        errors.New("起播超时"),
	})
}

// Stop cancels pending resolution and preserves the queue for an explicit resume.
func (s *Session) Stop(ctx context.Context) error {
	return s.call(ctx, func() error {
		s.intent.PlayWhenReady = false
		s.playWhenReady = false
		s.cancelResolve()
		s.readyArmed = false
		s.invalidatePlayback(true)
		s.ctrl.Stop()
		s.publish()
		return nil
	})
}
