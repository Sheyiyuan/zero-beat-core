package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sheyiyuan/zero-beat-core/app"
	"github.com/sheyiyuan/zero-beat-core/bili"
	"github.com/sheyiyuan/zero-beat-core/config"
	"github.com/sheyiyuan/zero-beat-core/instance"
	"github.com/sheyiyuan/zero-beat-core/lyrics"
	"github.com/sheyiyuan/zero-beat-core/media"
	"github.com/sheyiyuan/zero-beat-core/netease"
	"github.com/sheyiyuan/zero-beat-core/player"
	"github.com/sheyiyuan/zero-beat-core/source"
	bilisource "github.com/sheyiyuan/zero-beat-core/source/bilibili"
	localsource "github.com/sheyiyuan/zero-beat-core/source/local"
	"github.com/sheyiyuan/zero-beat-core/store"
)

const (
	defaultReadyTimeout = 20 * time.Second
	defaultSlowNotice   = 5 * time.Second
	slowNoticeText      = "网络较慢"
	tickEvery           = 250 * time.Millisecond
)

var pidInLockError = regexp.MustCompile(`PID\s+([0-9]+)`)

// Session 是无界面播放会话。控制器方法都在同一条串行循环上执行。
type Session struct {
	resolveWG       sync.WaitGroup
	prefetch        player.Prefetch
	prefetchSong    store.Song
	prefetchKey     string
	prefetchCancel  context.CancelFunc
	prefetchQuality source.Quality
	prefetchLocal   bool

	features        *Features
	clipPreviewSong string
	clipPreviewClip store.Clip
	clipTarget      string
	clipEpoch       uint64
	dir             string
	lock            *instance.Lock
	store           *store.Store
	svc             *app.Service
	ctrl            *player.Controller
	adapt           *engineAdapter
	engine          Engine
	events          <-chan EngineEvent

	root   context.Context
	cancel context.CancelFunc

	ops      chan func()
	stop     chan struct{}
	loopDone chan struct{}
	wake     chan struct{}
	ticker   *time.Ticker

	closed    atomic.Bool
	closeOnce sync.Once

	// playGen 在每次发起解析和断线时递增。过期的完成结果直接丢弃。
	playGen      atomic.Uint64
	autoHold     atomic.Bool
	resolvesDone atomic.Uint64
	eventsSeen   atomic.Uint64

	intent        PlayIntent
	playbackID    string
	playWhenReady bool
	lastSeq       uint64
	terminal      bool

	readyArmed bool
	slowSent   bool
	armedAt    time.Time
	readyWait  time.Duration
	slowWait   time.Duration

	resolveCancel context.CancelFunc

	notice      string
	noticeLevel string

	nowFn   func() time.Time
	offset  time.Duration
	virtual bool
	clockMu sync.Mutex

	mu   sync.Mutex
	snap Snapshot
	subs []chan struct{}
}

// Open 按「目录、实例锁、数据库、音源、控制器、恢复队列」的顺序启动会话。
func Open(ctx context.Context, opt OpenOptions) (*Session, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir, err := resolveDataDir(opt)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("session: 创建数据目录: %w", err)
	}

	var cleanup []func()
	fail := func(err error) (*Session, error) {
		for i := len(cleanup) - 1; i >= 0; i-- {
			cleanup[i]()
		}
		return nil, err
	}

	var lock *instance.Lock
	if wantLock(opt) {
		lock, err = instance.Acquire(dir)
		if err != nil {
			if errors.Is(err, instance.ErrAlreadyRunning) {
				return nil, &AlreadyRunningError{PID: pidFrom(err), Err: err}
			}
			return nil, err
		}
		cleanup = append(cleanup, lock.Release)
	}

	path := filepath.Join(dir, "zero-beat.db")
	var st *store.Store
	if opt.OpenStore != nil {
		st, err = opt.OpenStore(path)
	} else {
		st, err = store.Open(path)
	}
	if err != nil {
		return fail(err)
	}
	cleanup = append(cleanup, func() { _ = st.Close() })

	if opt.AfterOpen != nil {
		if err := opt.AfterOpen(st); err != nil {
			return fail(err)
		}
	}

	reg := opt.Sources
	if reg == nil {
		reg = builtinSources(st)
	}
	svc := app.New(reg, st, nil)
	downloadDir := ""
	if opt.Platform == "android" {
		downloadDir = filepath.Join(dir, "downloads")
	}
	svc.SetDirectories(dir, downloadDir)

	root, cancel := context.WithCancel(context.Background())
	s := &Session{
		dir:       dir,
		lock:      lock,
		store:     st,
		svc:       svc,
		engine:    opt.Engine,
		root:      root,
		cancel:    cancel,
		ops:       make(chan func(), 64),
		stop:      make(chan struct{}),
		loopDone:  make(chan struct{}),
		wake:      make(chan struct{}, 1),
		readyWait: opt.ReadyTimeout,
		slowWait:  opt.SlowNotice,
		nowFn:     opt.Now,
		virtual:   opt.Now != nil,
	}
	if s.readyWait <= 0 {
		s.readyWait = defaultReadyTimeout
	}
	if s.slowWait <= 0 {
		s.slowWait = defaultSlowNotice
	}
	if s.engine != nil {
		s.events = s.engine.Events()
	}
	s.adapt = newEngineAdapter(s)
	s.ctrl = player.New(s.adapt, svc)
	s.ctrl.SetClock(s.now)

	s.features = newFeatures(s)
	s.features.startJobs()
	s.applyPreferences()
	s.restoreQueue()
	s.publish()

	if !s.virtual {
		s.ticker = time.NewTicker(tickEvery)
	}
	go s.loop()
	if opt.Sources == nil {
		go s.features.maintainAccounts()
	}
	return s, nil
}

func resolveDataDir(opt OpenOptions) (string, error) {
	if strings.EqualFold(opt.Platform, "android") {
		if strings.TrimSpace(opt.DataDir) == "" {
			return "", errors.New("session: android 平台必须提供 DataDir")
		}
		return opt.DataDir, nil
	}
	if strings.TrimSpace(opt.DataDir) == "" {
		return config.DataDir()
	}
	return opt.DataDir, nil
}

func wantLock(opt OpenOptions) bool {
	if opt.Lock != nil {
		return *opt.Lock
	}
	return !strings.EqualFold(opt.Platform, "android")
}

func pidFrom(err error) int {
	if err == nil {
		return 0
	}
	m := pidInLockError.FindStringSubmatch(err.Error())
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

func builtinSources(st *store.Store) *source.Registry {
	list := []source.Source{
		bilisource.New(bili.New()),
		netease.New(),
		localsource.New(st, lyrics.DefaultLocalConfig()),
	}
	for _, src := range list {
		loadSavedAccount(st, src)
	}
	return source.NewRegistry(list...)
}

func loadSavedAccount(st *store.Store, src source.Source) {
	if st == nil || src == nil {
		return
	}
	payload, err := st.LoadAccount(string(src.ID()))
	if err != nil {
		return
	}
	var saved map[string]string
	if json.Unmarshal(payload, &saved) == nil {
		src.SetSession(saved)
	}
}

func (s *Session) applyPreferences() {
	prefs := s.svc.Preferences()
	s.ctrl.SetVolume(prefs.Volume)
	s.ctrl.SetCompDB(prefs.CompDB)
	s.ctrl.SetQuality(prefs.Quality)
	s.ctrl.SetMode(player.Mode(prefs.Mode))
	if prefs.AutoGain && !s.ctrl.SetAutoGain(true) {
		s.notice = "当前引擎不支持 loudnorm，自动响度均衡已关闭"
		s.noticeLevel = "warn"
	}
}

func (s *Session) restoreQueue() {
	q := s.svc.RestoreQueue()
	if !q.OK || len(q.Songs) == 0 {
		return
	}
	idx := q.Index
	if idx < 0 || idx >= len(q.Songs) {
		idx = 0
	}
	song := q.Songs[idx]
	s.ctrl.RestoreSession(q.Songs, idx, &song, q.Position)
}

// Close 持久化队列、停止引擎、关闭数据库并释放实例锁。重复调用无效果。
func (s *Session) Close() error {
	if s == nil {
		return nil
	}
	var err error
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		s.playGen.Add(1)
		s.autoHold.Store(true)
		s.cancelResolve()
		s.cancel()
		close(s.stop)
		<-s.loopDone
		s.resolveWG.Wait()
		if s.ticker != nil {
			s.ticker.Stop()
		}
		if s.features != nil {
			s.features.close()
		}
		if s.store != nil {
			err = s.store.Close()
			s.store = nil
		}
		if s.lock != nil {
			s.lock.Release()
			s.lock = nil
		}
	})
	return err
}

// DataDir 返回本次会话使用的数据目录。
func (s *Session) DataDir() string {
	if s == nil {
		return ""
	}
	return s.dir
}

// SetEngine 替换音频输出。后续加载走新引擎，已分配的代次不会被改写。
func (s *Session) SetEngine(e Engine) {
	if s == nil {
		return
	}
	_ = s.call(context.Background(), func() error {
		s.engine = e
		if e == nil {
			s.events = nil
			return nil
		}
		s.events = e.Events()
		return nil
	})
}

// Snapshot 返回最近一次发布的快照，可从串行循环之外调用。
func (s *Session) Snapshot() Snapshot {
	if s == nil {
		return Snapshot{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneSnapshot(s.snap)
}

// Subscribe 返回容量为 1 的合并信号。快照需要另行 Snapshot 拉取。
func (s *Session) Subscribe() (<-chan struct{}, func()) {
	if s == nil {
		ch := make(chan struct{})
		return ch, func() {}
	}
	ch := make(chan struct{}, 1)
	s.mu.Lock()
	s.subs = append(s.subs, ch)
	s.mu.Unlock()
	return ch, func() { s.unsubscribe(ch) }
}

func (s *Session) unsubscribe(ch chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.subs[:0]
	for _, sub := range s.subs {
		if sub != ch {
			out = append(out, sub)
		}
	}
	s.subs = out
}

// OnEngineDisconnected 使当前播放代次失效，记下位置，并停止自动重试。
// 它不会重新起播。
func (s *Session) OnEngineDisconnected() {
	if s == nil || s.closed.Load() {
		return
	}
	token := s.playGen.Add(1)
	s.autoHold.Store(true)
	s.cancelResolve()
	_ = s.call(context.Background(), func() error {
		if s.playGen.Load() != token {
			return nil
		}
		s.applyDisconnect()
		return nil
	})
}

func (s *Session) applyDisconnect() {
	s.autoHold.Store(true)
	s.readyArmed = false
	s.cancelResolve()
	if s.engine != nil {
		if pos := s.engine.Position(); pos > 0 {
			s.ctrl.SetPosition(pos)
		}
	}
	s.ctrl.Persist()
	id := s.playbackID
	s.playbackID = ""
	s.terminal = true
	s.playWhenReady = false
	if id != "" && s.engine != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = s.engine.Stop(ctx, id)
		cancel()
	}
	st := s.ctrl.Export()
	st.Awaiting = false
	st.ResumeAt = st.Position
	if st.Current != nil {
		st.PlaybackState = media.StatePaused
	} else {
		st.PlaybackState = media.StateIdle
	}
	s.ctrl.Load(st)
	s.publish()
}

func (s *Session) loop() {
	defer close(s.loopDone)
	for {
		var tick <-chan time.Time
		if s.ticker != nil {
			tick = s.ticker.C
		}
		var wake <-chan struct{}
		if s.virtual {
			wake = s.wake
		}
		events := s.events
		select {
		case <-s.stop:
			s.shutdownPlayback()
			return
		case fn := <-s.ops:
			fn()
		case ev, ok := <-events:
			if !ok {
				if s.events == events {
					s.events = nil
				}
				continue
			}
			s.onEngineEvent(ev)
		case <-tick:
			s.onTick()
		case <-wake:
			s.onTick()
		}
		if s.closed.Load() {
			select {
			case <-s.stop:
				s.shutdownPlayback()
				return
			default:
			}
		}
		s.checkDeadlines()
	}
}

func (s *Session) shutdownPlayback() {
	s.readyArmed = false
	s.cancelResolve()
	if s.ctrl == nil {
		return
	}
	if s.engine != nil && s.playbackID != "" {
		if pos := s.engine.Position(); pos > 0 {
			s.ctrl.SetPosition(pos)
		}
	}
	s.ctrl.Persist()
	if s.engine != nil && s.playbackID != "" {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = s.engine.Stop(ctx, s.playbackID)
		cancel()
		s.playbackID = ""
	}
}

func (s *Session) onTick() {
	if s.ctrl == nil {
		return
	}
	expired := !s.ctrl.SleepUntil().IsZero() && !s.now().Before(s.ctrl.SleepUntil())
	s.ctrl.Tick(s.now())
	if expired {
		s.adapt.Pause()
		s.setPlaybackState(media.StatePaused)
	}
	s.prefetchNext()
	s.publish()
}

func (s *Session) call(ctx context.Context, fn func() error) error {
	if s == nil || s.closed.Load() {
		return ErrClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	errCh := make(chan error, 1)
	op := func() { errCh <- fn() }
	select {
	case <-s.loopDone:
		return ErrClosed
	case <-s.stop:
		return ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	case s.ops <- op:
	}
	select {
	case err := <-errCh:
		return err
	case <-s.loopDone:
		return ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Session) post(fn func()) {
	select {
	case <-s.loopDone:
	case s.ops <- fn:
	}
}

func (s *Session) now() time.Time {
	s.clockMu.Lock()
	defer s.clockMu.Unlock()
	if s.nowFn != nil {
		return s.nowFn().Add(s.offset)
	}
	return time.Now()
}

// advance 只给测试用：把注入的时钟往前拨，并唤醒串行循环。
func (s *Session) advance(d time.Duration) {
	if s == nil || !s.virtual || d == 0 {
		return
	}
	s.clockMu.Lock()
	s.offset += d
	s.clockMu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Session) cancelResolve() {
	s.mu.Lock()
	cancel := s.resolveCancel
	s.resolveCancel = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *Session) setNotice(text, level string) {
	s.notice = text
	s.noticeLevel = level
	s.publish()
}

func (s *Session) publish() {
	if s == nil || s.ctrl == nil {
		return
	}
	next := Snapshot{
		Player:        cloneState(s.ctrl.Export()),
		PlaybackID:    s.playbackID,
		PlayWhenReady: s.playWhenReady,
		Notice:        s.notice,
		NoticeLevel:   s.noticeLevel,
	}
	s.mu.Lock()
	if reflect.DeepEqual(s.snap, next) {
		s.mu.Unlock()
		return
	}
	s.snap = next
	subs := append([]chan struct{}(nil), s.subs...)
	s.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func cloneSnapshot(in Snapshot) Snapshot {
	in.Player = cloneState(in.Player)
	return in
}

func cloneState(in player.State) player.State {
	in.Queue = append([]store.Song(nil), in.Queue...)
	in.ShuffleBase = append([]store.Song(nil), in.ShuffleBase...)
	if in.Current != nil {
		cur := *in.Current
		in.Current = &cur
	}
	return in
}
