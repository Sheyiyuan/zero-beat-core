package session

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/sheyiyuan/zero-beat-core/instance"
	"github.com/sheyiyuan/zero-beat-core/lyrics"
	"github.com/sheyiyuan/zero-beat-core/media"
	"github.com/sheyiyuan/zero-beat-core/player"
	"github.com/sheyiyuan/zero-beat-core/source"
	"github.com/sheyiyuan/zero-beat-core/store"
)

func TestAfterOpenReleasesLock(t *testing.T) {
	dir := t.TempDir()
	var calls int
	var mu sync.Mutex
	openStore := func(path string) (*store.Store, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return store.Open(path)
	}
	lock := true
	_, err := Open(context.Background(), OpenOptions{
		DataDir:   dir,
		Lock:      &lock,
		OpenStore: openStore,
		AfterOpen: func(*store.Store) error { return errors.New("after open") },
		Sources:   source.NewRegistry(),
		Engine:    newFakeEngine(),
	})
	if err == nil {
		t.Fatal("AfterOpen 失败时应返回错误")
	}
	mu.Lock()
	if calls != 1 {
		t.Fatalf("第一次应打开数据库，calls=%d", calls)
	}
	mu.Unlock()

	eng := newFakeEngine()
	s, err := Open(context.Background(), OpenOptions{
		DataDir:   dir,
		Lock:      &lock,
		OpenStore: openStore,
		Sources:   source.NewRegistry(),
		Engine:    eng,
	})
	if err != nil {
		t.Fatalf("锁和数据库释放后应能再次打开: %v", err)
	}
	defer s.Close()

	_, err = Open(context.Background(), OpenOptions{
		DataDir:   dir,
		Lock:      &lock,
		OpenStore: openStore,
		Sources:   source.NewRegistry(),
		Engine:    newFakeEngine(),
	})
	var running *AlreadyRunningError
	if !errors.As(err, &running) {
		t.Fatalf("持锁时应返回 AlreadyRunningError，实际 %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Fatalf("锁被占用时不应再打开数据库，calls=%d", calls)
	}
}

func TestLockBeforeOpenStore(t *testing.T) {
	dir := t.TempDir()
	held, err := instance.Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()

	var calls int
	lock := true
	_, err = Open(context.Background(), OpenOptions{
		DataDir: dir,
		Lock:    &lock,
		OpenStore: func(path string) (*store.Store, error) {
			calls++
			return store.Open(path)
		},
		Sources: source.NewRegistry(),
		Engine:  newFakeEngine(),
	})
	var running *AlreadyRunningError
	if !errors.As(err, &running) {
		t.Fatalf("应返回 AlreadyRunningError，实际 %v", err)
	}
	if calls != 0 {
		t.Fatalf("拿锁失败时 OpenStore 不应被调用，calls=%d", calls)
	}
	if running.PID == 0 {
		t.Fatal("锁文件里的 PID 应被解析出来")
	}
}

func TestRapidPlayIgnoresSlowResolve(t *testing.T) {
	src := newFakeSource()
	gate := make(chan struct{})
	src.gates["a"] = gate
	eng := newFakeEngine()
	s := openTestSession(t, eng, src)
	a := testSong("a", "A")
	b := testSong("b", "B")
	if _, err := s.Play(context.Background(), PlayInput{Song: &a, QueueIndex: -1}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Play(context.Background(), PlayInput{Song: &b, QueueIndex: -1}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, time.Second, func() bool {
		loads := eng.snapshotLoads()
		return len(loads) == 1 && loads[0].url == "mem://b"
	})
	close(gate)
	waitFor(t, time.Second, func() bool { return s.resolvesDone.Load() >= 2 })
	loads := eng.snapshotLoads()
	if len(loads) != 1 || loads[0].url != "mem://b" {
		t.Fatalf("迟到的 A 不应覆盖 B，loads=%+v", loads)
	}
	if id := s.Snapshot().PlaybackID; id == "" || id != loads[0].id {
		t.Fatalf("当前代次应是 B 的加载，id=%s loads=%+v", id, loads)
	}
}

func TestStopCancelsLoadAndRejectsLateEventsWithoutClearingQueue(t *testing.T) {
	src := newFakeSource()
	eng := newFakeEngine()
	s := openTestSession(t, eng, src)
	a, b := testSong("a", "A"), testSong("b", "B")
	if _, err := s.Play(context.Background(), PlayInput{Song: &a, Queue: []store.Song{a, b}, QueueIndex: 0}); err != nil {
		t.Fatal(err)
	}
	load := waitLoad(t, eng, 1)
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if eng.State() != media.StateIdle || eng.CurrentStream().URL != "" {
		t.Fatal("stop must release the active engine stream")
	}
	seen := s.eventsSeen.Load()
	eng.emit(EngineEvent{PlaybackID: load.id, Seq: 1, Type: media.EventStateChanged, State: media.StatePlaying})
	eng.emit(EngineEvent{PlaybackID: load.id, Seq: 2, Type: media.EventEnded, State: media.StateEnded})
	waitFor(t, time.Second, func() bool { return s.eventsSeen.Load() >= seen+2 })
	snap := s.Snapshot()
	if snap.PlayWhenReady || snap.PlaybackID != "" || snap.Player.PlaybackState != media.StateIdle {
		t.Fatalf("stop must remain idle after stale events: %+v", snap)
	}
	if len(snap.Player.Queue) != 2 || len(eng.snapshotLoads()) != 1 {
		t.Fatalf("stop must preserve queue and release engine: %+v", snap.Player)
	}
}

func TestStoppedQueueRestartsOnlyOnExplicitPlay(t *testing.T) {
	for _, action := range []string{"resume", "toggle"} {
		t.Run(action, func(t *testing.T) {
			src := newFakeSource()
			eng := newFakeEngine()
			s := openTestSession(t, eng, src)
			a, b := testSong("a", "A"), testSong("b", "B")
			if _, err := s.Play(context.Background(), PlayInput{Song: &b, Queue: []store.Song{a, b}, QueueIndex: 1}); err != nil {
				t.Fatal(err)
			}
			first := waitLoad(t, eng, 1)
			if err := s.Stop(context.Background()); err != nil {
				t.Fatal(err)
			}
			if s.Snapshot().Player.Index != 1 || s.Snapshot().PlayWhenReady {
				t.Fatal("stop must preserve selection without restarting")
			}
			if action == "resume" {
				if err := s.Resume(context.Background()); err != nil {
					t.Fatal(err)
				}
			} else {
				accepted, err := s.Toggle(context.Background())
				if err != nil || !accepted.Started {
					t.Fatalf("toggle: %+v %v", accepted, err)
				}
			}
			second := waitLoad(t, eng, 2)
			if second.url != "mem://b" || second.id == first.id || !second.ready {
				t.Fatalf("explicit play must reload selected song with a fresh generation: %+v", second)
			}
		})
	}
}

func TestPlayContextThenNext(t *testing.T) {
	src := newFakeSource()
	eng := newFakeEngine()
	s := openTestSession(t, eng, src)
	a := testSong("a", "A")
	b := testSong("b", "B")
	if _, err := s.Play(context.Background(), PlayInput{
		Song:       &a,
		Queue:      []store.Song{a, b},
		QueueIndex: -1,
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, time.Second, func() bool {
		loads := eng.snapshotLoads()
		return len(loads) == 1 && loads[0].url == "mem://a"
	})
	accepted, err := s.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !accepted.Started {
		t.Fatal("队列里的下一首应该开始解析")
	}
	waitFor(t, time.Second, func() bool {
		loads := eng.snapshotLoads()
		return len(loads) == 2 && loads[1].url == "mem://b"
	})
}

func TestDuplicateEndedAdvancesOnce(t *testing.T) {
	src := newFakeSource()
	eng := newFakeEngine()
	s := openTestSession(t, eng, src)
	if err := s.SetMode(context.Background(), player.ModeOrdered); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b", "c"} {
		if _, err := s.Enqueue(context.Background(), testSong(id, id)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Play(context.Background(), PlayInput{QueueIndex: 0, StartAt: 0}); err != nil {
		t.Fatal(err)
	}
	first := waitLoad(t, eng, 1)
	eng.emit(EngineEvent{PlaybackID: first.id, Seq: 1, Type: media.EventEnded, State: media.StateEnded})
	eng.emit(EngineEvent{PlaybackID: first.id, Seq: 2, Type: media.EventEnded, State: media.StateEnded})
	second := waitLoad(t, eng, 2)
	if second.url != "mem://b" {
		t.Fatalf("重复 ended 应只推进到 B，实际 %+v", eng.snapshotLoads())
	}
	seen := s.eventsSeen.Load()
	eng.emit(EngineEvent{PlaybackID: first.id, Seq: 3, Type: media.EventEnded, State: media.StateEnded})
	eng.emit(EngineEvent{
		PlaybackID: first.id, Seq: 4, Type: media.EventStateChanged,
		State: media.StatePlaying, Position: 50 * time.Second,
	})
	waitFor(t, time.Second, func() bool { return s.eventsSeen.Load() >= seen+2 })
	snap := s.Snapshot()
	if snap.Player.Index != 1 || snap.Player.Current == nil || snap.Player.Current.ExternalID() != "b" {
		t.Fatalf("A 的迟到事件不应改变 B: %+v", snap.Player)
	}
	if snap.Player.Position >= 50*time.Second {
		t.Fatalf("A 的迟到位置不应写入 B: %s", snap.Player.Position)
	}

	eng.emit(EngineEvent{PlaybackID: second.id, Seq: 1, Type: media.EventStateChanged, State: media.StatePlaying})
	waitState(t, s, media.StatePlaying)
	eng.emit(EngineEvent{PlaybackID: second.id, Seq: 2, Type: media.EventEnded, State: media.StateEnded})
	eng.emit(EngineEvent{PlaybackID: second.id, Seq: 3, Type: media.EventEnded, State: media.StateEnded})
	third := waitLoad(t, eng, 3)
	if third.url != "mem://c" {
		t.Fatalf("B 的重复 ended 应只推进到 C，实际 %+v", eng.snapshotLoads())
	}
	waitFor(t, time.Second, func() bool { return s.resolvesDone.Load() >= 3 })
	if got := s.Snapshot().Player.Index; got != 2 {
		t.Fatalf("索引应停在 C，实际 %d", got)
	}
	if calls := src.snapshotCalls(); len(calls) != 3 {
		t.Fatalf("三首歌应各解析一次，实际 %v", calls)
	}
}

func TestResolveRetryBudget(t *testing.T) {
	t.Run("two failures stop", func(t *testing.T) {
		src := newFakeSource()
		src.failN = 2
		eng := newFakeEngine()
		s := openTestSession(t, eng, src)
		song := testSong("a", "A")
		if _, err := s.Play(context.Background(), PlayInput{Song: &song, QueueIndex: -1}); err != nil {
			t.Fatal(err)
		}
		waitState(t, s, media.StateFailed)
		waitFor(t, time.Second, func() bool { return s.resolvesDone.Load() >= 2 })
		if calls := src.snapshotCalls(); len(calls) != 2 {
			t.Fatalf("失败应只重试一次，calls=%v", calls)
		}
		if loads := eng.snapshotLoads(); len(loads) != 0 {
			t.Fatalf("两次解析失败不应加载，loads=%+v", loads)
		}
	})

	t.Run("success then later failure stops", func(t *testing.T) {
		src := newFakeSource()
		src.failN = 1
		eng := newFakeEngine()
		s := openTestSession(t, eng, src)
		song := testSong("a", "A")
		if _, err := s.Play(context.Background(), PlayInput{Song: &song, QueueIndex: -1}); err != nil {
			t.Fatal(err)
		}
		load := waitLoad(t, eng, 1)
		if calls := src.snapshotCalls(); len(calls) != 2 {
			t.Fatalf("应失败一次后再成功，calls=%v", calls)
		}
		eng.emit(EngineEvent{PlaybackID: load.id, Seq: 1, Type: media.EventStateChanged, State: media.StatePlaying})
		waitState(t, s, media.StatePlaying)
		eng.emit(EngineEvent{PlaybackID: load.id, Seq: 2, Type: media.EventFailed, State: media.StateFailed, Err: errors.New("中断")})
		waitState(t, s, media.StateFailed)
		waitFor(t, time.Second, func() bool { return s.eventsSeen.Load() >= 2 })
		if calls := src.snapshotCalls(); len(calls) != 2 {
			t.Fatalf("重试成功后再次失败不应开启第三次解析，calls=%v", calls)
		}
	})
}

func TestLoadErrorSharesRetryBudget(t *testing.T) {
	src := newFakeSource()
	eng := newFakeEngine()
	eng.loadErr = errors.New("load failed")
	s := openTestSession(t, eng, src)
	song := testSong("a", "A")
	if _, err := s.Play(context.Background(), PlayInput{Song: &song, QueueIndex: -1}); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, media.StateFailed)
	waitFor(t, time.Second, func() bool { return s.resolvesDone.Load() >= 2 })
	if calls := src.snapshotCalls(); len(calls) != 2 {
		t.Fatalf("同步 Load 失败应占用同一次重试，calls=%v", calls)
	}
	if loads := eng.snapshotLoads(); len(loads) != 2 {
		t.Fatalf("应加载两次后停止，loads=%+v", loads)
	}
}

func TestPausedSeekDoesNotResume(t *testing.T) {
	src := newFakeSource()
	src.failN = 1
	src.expires = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	eng := newFakeEngine()
	base := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	s := openTestSession(t, eng, src, func(opt *OpenOptions) {
		opt.Now = func() time.Time { return base }
	})
	song := testSong("a", "A")
	if _, err := s.Play(context.Background(), PlayInput{Song: &song, QueueIndex: -1}); err != nil {
		t.Fatal(err)
	}
	first := waitLoad(t, eng, 1)
	eng.emit(EngineEvent{PlaybackID: first.id, Seq: 1, Type: media.EventStateChanged, State: media.StatePlaying})
	waitState(t, s, media.StatePlaying)
	if err := s.Pause(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, media.StatePaused)
	if err := s.SeekMS(context.Background(), 10_000); err != nil {
		t.Fatal(err)
	}
	second := waitLoad(t, eng, 2)
	if second.ready {
		t.Fatalf("暂停中的重新取流不应 PlayWhenReady，load=%+v", second)
	}
	snap := s.Snapshot()
	if snap.Player.PlaybackState == media.StatePlaying || snap.PlayWhenReady {
		t.Fatalf("暂停 seek 不应恢复出声: state=%s ready=%v", snap.Player.PlaybackState, snap.PlayWhenReady)
	}
	if snap.Player.Retry != 1 {
		t.Fatalf("seek 不应重置重试预算，retry=%d", snap.Player.Retry)
	}
	eng.emit(EngineEvent{PlaybackID: second.id, Seq: 1, Type: media.EventFailed, State: media.StateFailed, Err: errors.New("再次失败")})
	waitState(t, s, media.StateFailed)
	waitFor(t, time.Second, func() bool { return s.eventsSeen.Load() >= 2 })
	if calls := src.snapshotCalls(); len(calls) != 3 {
		t.Fatalf("预算已用过一次时，seek 后的失败不应再解析，calls=%v", calls)
	}
}

func TestRestoreQueueWithoutLoad(t *testing.T) {
	dir := t.TempDir()
	src := newFakeSource()
	eng := newFakeEngine()
	lock := false
	s, err := Open(context.Background(), OpenOptions{
		DataDir: dir,
		Lock:    &lock,
		Engine:  eng,
		Sources: source.NewRegistry(src),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enqueue(context.Background(), testSong("a", "A")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enqueue(context.Background(), testSong("b", "B")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Play(context.Background(), PlayInput{QueueIndex: 0, StartAt: 12 * time.Second}); err != nil {
		t.Fatal(err)
	}
	waitLoad(t, eng, 1)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	eng2 := newFakeEngine()
	s2, err := Open(context.Background(), OpenOptions{
		DataDir: dir,
		Lock:    &lock,
		Engine:  eng2,
		Sources: source.NewRegistry(newFakeSource()),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if loads := eng2.snapshotLoads(); len(loads) != 0 {
		t.Fatalf("恢复会话不应调用 Load，loads=%+v", loads)
	}
	snap := s2.Snapshot()
	if len(snap.Player.Queue) != 2 || snap.Player.Index != 0 {
		t.Fatalf("队列未恢复: index=%d queue=%v", snap.Player.Index, idsOf(snap.Player.Queue))
	}
	if snap.Player.Queue[0].ExternalID() != "a" || snap.Player.Queue[1].ExternalID() != "b" {
		t.Fatalf("队列内容不符: %+v", snap.Player.Queue)
	}
	if snap.Player.Position != 12*time.Second {
		t.Fatalf("进度应为 12s，实际 %s", snap.Player.Position)
	}
	if snap.Player.PlaybackState == media.StatePlaying {
		t.Fatal("恢复后不应处于播放中")
	}
}

func TestReadyTimeoutUsesInjectedClock(t *testing.T) {
	src := newFakeSource()
	eng := newFakeEngine()
	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	s := openTestSession(t, eng, src, func(opt *OpenOptions) {
		opt.Now = func() time.Time { return base }
		opt.SlowNotice = 5 * time.Second
		opt.ReadyTimeout = 20 * time.Second
	})
	song := testSong("a", "A")
	if _, err := s.Play(context.Background(), PlayInput{Song: &song, QueueIndex: -1}); err != nil {
		t.Fatal(err)
	}
	waitLoad(t, eng, 1)
	s.advance(5 * time.Second)
	waitFor(t, time.Second, func() bool {
		return s.Snapshot().Notice == slowNoticeText
	})
	if s.Snapshot().Player.PlaybackState == media.StateFailed {
		t.Fatal("慢提示不应判失败")
	}
	s.advance(15 * time.Second)
	waitFor(t, time.Second, func() bool {
		return len(src.snapshotCalls()) >= 2 || s.Snapshot().Player.PlaybackState == media.StateFailed
	})
	if calls := src.snapshotCalls(); len(calls) != 2 {
		t.Fatalf("起播超时应只自动重试一次，calls=%v", calls)
	}
}

func TestOpenLinkSearchAndAndroidDir(t *testing.T) {
	_, err := Open(context.Background(), OpenOptions{Platform: "android", Sources: source.NewRegistry()})
	if err == nil {
		t.Fatal("android 缺少 DataDir 应失败")
	}

	src := &linkSource{fakeSource: newFakeSource()}
	bad := newFakeSource()
	bad.id = "bad"
	bad.bad = true
	eng := newFakeEngine()
	s := openTestSession(t, eng, src, func(opt *OpenOptions) {
		opt.Sources = source.NewRegistry(src, bad)
	})
	hit, err := s.OpenLink(context.Background(), "https://www.bilibili.com/video/BV1GJ411x7h7?p=2")
	if err != nil {
		t.Fatal(err)
	}
	if !hit.Matched || hit.Kind != LinkTrack || hit.Song.ID != "bilibili:BV1GJ411x7h7:2" || hit.Song.Title != "" {
		t.Fatalf("歌曲链接解析不符: %+v", hit)
	}
	miss, err := s.OpenLink(context.Background(), "not a link")
	if err != nil || miss.Matched || miss.Kind != LinkUnknown {
		t.Fatalf("未识别应返回 unknown，err=%v result=%+v", err, miss)
	}

	ok, err := s.Search(context.Background(), "x", 0, "")
	if err != nil || ok.Warning == "" || len(ok.Candidates) != 1 {
		t.Fatalf("部分失败应保留结果: %+v err=%v", ok, err)
	}
	if _, err := s.Search(context.Background(), "x", 1, "missing"); err == nil {
		t.Fatal("全部失败应返回错误")
	}
}

type loadCall struct {
	id    string
	url   string
	ready bool
}

type fakeEngine struct {
	mu       sync.Mutex
	state    media.State
	stream   media.Stream
	position time.Duration
	autoOK   bool
	loadErr  error
	loads    []loadCall
	events   chan EngineEvent
}

func newFakeEngine() *fakeEngine {
	return &fakeEngine{
		state:  media.StateIdle,
		autoOK: true,
		events: make(chan EngineEvent, 32),
	}
}

func (f *fakeEngine) Load(_ context.Context, id string, stream media.Stream, at time.Duration, intent PlayIntent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loads = append(f.loads, loadCall{id: id, url: stream.URL, ready: intent.PlayWhenReady})
	f.stream = stream
	f.position = at
	f.state = media.StateBuffering
	return f.loadErr
}

func (f *fakeEngine) Pause(context.Context, string) error {
	f.mu.Lock()
	f.state = media.StatePaused
	f.mu.Unlock()
	return nil
}
func (f *fakeEngine) Resume(context.Context, string) error {
	f.mu.Lock()
	f.state = media.StatePlaying
	f.mu.Unlock()
	return nil
}
func (f *fakeEngine) Stop(context.Context, string) error {
	f.mu.Lock()
	f.state = media.StateIdle
	f.stream = media.Stream{}
	f.mu.Unlock()
	return nil
}
func (f *fakeEngine) Seek(_ context.Context, _ string, at time.Duration) error {
	f.mu.Lock()
	f.position = at
	f.mu.Unlock()
	return nil
}
func (f *fakeEngine) SetVolume(context.Context, string, float64) error         { return nil }
func (f *fakeEngine) SetCompensationDB(context.Context, string, float64) error { return nil }
func (f *fakeEngine) SetAutoGain(context.Context, string, bool) error          { return nil }
func (f *fakeEngine) AutoGainSupported() bool                                  { return f.autoOK }
func (f *fakeEngine) Position() time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.position
}
func (f *fakeEngine) Duration() time.Duration { return f.streamDuration() }
func (f *fakeEngine) streamDuration() time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return time.Duration(f.stream.DurationMS) * time.Millisecond
}
func (f *fakeEngine) State() media.State {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state
}
func (f *fakeEngine) CurrentStream() media.Stream {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stream
}
func (f *fakeEngine) Events() <-chan EngineEvent { return f.events }

func (f *fakeEngine) emit(ev EngineEvent) {
	f.mu.Lock()
	// 迟到事件必须留在产生它的代次上，不能改写当前流的缓存。
	if len(f.loads) > 0 && f.loads[len(f.loads)-1].id == ev.PlaybackID {
		switch ev.Type {
		case media.EventStateChanged:
			f.state = ev.State
			if ev.Position > 0 {
				f.position = ev.Position
			}
		case media.EventEnded:
			f.state = media.StateEnded
		case media.EventFailed:
			f.state = media.StateFailed
		}
	}
	f.mu.Unlock()
	f.events <- ev
}

func (f *fakeEngine) snapshotLoads() []loadCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]loadCall(nil), f.loads...)
}

type fakeSource struct {
	id      source.ID
	failN   int
	expires time.Time
	gates   map[string]chan struct{}
	mu      sync.Mutex
	calls   []string
	bad     bool
}

func newFakeSource() *fakeSource {
	return &fakeSource{id: "fake", gates: map[string]chan struct{}{}}
}

func (f *fakeSource) ID() source.ID { return f.id }
func (f *fakeSource) Name() string  { return "Fake" }
func (f *fakeSource) Mark() string  { return "F" }
func (f *fakeSource) Caps() source.Capability {
	return source.CapSearch | source.CapAccount
}
func (f *fakeSource) LoggedIn() bool { return false }
func (f *fakeSource) User() source.User {
	return source.User{Name: "测试用户"}
}
func (f *fakeSource) Account(context.Context) (source.User, error) { return f.User(), nil }
func (f *fakeSource) StartQRLogin(context.Context) (source.QRCode, error) {
	return source.QRCode{}, nil
}
func (f *fakeSource) PollQRLogin(context.Context, string) (source.LoginStatus, error) {
	return source.LoginStatus{}, nil
}
func (f *fakeSource) Search(context.Context, string, int) (source.SearchPage, error) {
	if f.bad {
		return source.SearchPage{}, errors.New("搜索失败")
	}
	return source.SearchPage{
		Page: 1, Pages: 1,
		Candidates: []source.Candidate{{Track: source.Track{Source: f.id, ExternalID: "1", Title: "命中"}}},
	}, nil
}
func (f *fakeSource) Resolve(_ context.Context, ref source.Ref, _ source.Quality) (source.Resolved, error) {
	if ch := f.gate(ref.ExternalID); ch != nil {
		<-ch
	}
	f.mu.Lock()
	f.calls = append(f.calls, ref.ExternalID)
	n := len(f.calls)
	failN := f.failN
	expires := f.expires
	f.mu.Unlock()
	if n <= failN {
		return source.Resolved{}, errors.New("解析失败")
	}
	return source.Resolved{
		Track: source.Track{
			Source: ref.Source, ExternalID: ref.ExternalID, Page: ref.Page,
			Title: ref.ExternalID, Duration: 100,
		},
		Stream: source.Stream{URL: "mem://" + ref.ExternalID, Duration: 100 * time.Second, ExpiresAt: expires},
	}, nil
}
func (f *fakeSource) Lyrics(context.Context, source.Ref) ([]lyrics.Line, string, error) {
	return nil, "", nil
}
func (f *fakeSource) RemotePlaylists(context.Context) ([]source.Playlist, error) { return nil, nil }
func (f *fakeSource) PlaylistTracks(context.Context, string) ([]source.Track, error) {
	return nil, nil
}
func (f *fakeSource) Session() map[string]string {
	return map[string]string{"cookie": "secret"}
}
func (f *fakeSource) SetSession(map[string]string) {}
func (f *fakeSource) Logout()                      {}

func (f *fakeSource) gate(id string) <-chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gates[id]
}

func (f *fakeSource) snapshotCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

type linkSource struct {
	*fakeSource
	badSearch bool
}

func (s *linkSource) ParseTarget(input string, pageOverride int) (source.Ref, bool, error) {
	const prefix = "https://www.bilibili.com/video/"
	if len(input) < len(prefix) || input[:len(prefix)] != prefix {
		return source.Ref{}, false, nil
	}
	rest := input[len(prefix):]
	bvid := rest
	page := 1
	if i := indexByte(rest, '?'); i >= 0 {
		bvid = rest[:i]
		if pageOverride > 0 {
			page = pageOverride
		} else if contains(rest[i:], "p=2") {
			page = 2
		}
	}
	return source.Ref{Source: source.Bilibili, ExternalID: bvid, Page: page}, true, nil
}

func (s *linkSource) Search(ctx context.Context, keyword string, page int) (source.SearchPage, error) {
	if s.badSearch {
		return source.SearchPage{}, errors.New("坏音源")
	}
	return s.fakeSource.Search(ctx, keyword, page)
}

func openTestSession(t *testing.T, eng Engine, src source.Source, tweak ...func(*OpenOptions)) *Session {
	t.Helper()
	lock := false
	opt := OpenOptions{
		DataDir: t.TempDir(),
		Lock:    &lock,
		Engine:  eng,
		Sources: source.NewRegistry(src),
	}
	for _, fn := range tweak {
		fn(&opt)
	}
	s, err := Open(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func testSong(id, title string) store.Song {
	return store.Song{Source: "fake", RemoteID: id, Page: 1, Title: title, Duration: 100}
}

func waitLoad(t *testing.T, eng *fakeEngine, n int) loadCall {
	t.Helper()
	var got loadCall
	waitFor(t, 2*time.Second, func() bool {
		loads := eng.snapshotLoads()
		if len(loads) < n {
			return false
		}
		got = loads[n-1]
		return true
	})
	return got
}

func waitState(t *testing.T, s *Session, want media.State) {
	t.Helper()
	waitFor(t, 2*time.Second, func() bool {
		return s.Snapshot().Player.PlaybackState == want
	})
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("等待条件超时")
}

func idsOf(songs []store.Song) []string {
	out := make([]string, len(songs))
	for i, song := range songs {
		out[i] = song.ID
	}
	return out
}

func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && (s == sub || len(s) > 0 && (indexOf(s, sub) >= 0)))
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
