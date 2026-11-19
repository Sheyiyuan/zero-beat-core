package session

import (
	"context"
	"fmt"
	"github.com/sheyiyuan/zero-beat-core/media"
	"github.com/sheyiyuan/zero-beat-core/player"
	"github.com/sheyiyuan/zero-beat-core/store"
	"testing"
	"time"
)

func TestBatchPlaylistIncludesBeyondVisiblePage(t *testing.T) {
	s := openTestSession(t, newFakeEngine(), newFakeSource())
	f := s.Features()
	ctx := context.Background()
	p, err := f.CreatePlaylist(ctx, PlaylistEdit{Title: "完整歌单"})
	if err != nil {
		t.Fatal(err)
	}
	songs := []store.Song{}
	for i := 0; i < 251; i++ {
		songs = append(songs, testSong(fmt.Sprint(i), fmt.Sprint(i)))
	}
	result, err := f.Batch(ctx, "add", p.ID, Selection{Songs: songs})
	if err != nil || len(result.Succeeded) != 251 {
		t.Fatalf("add: %+v %v", result, err)
	}
	result, err = f.Batch(ctx, "play", store.NormalizeSong(songs[230]).ID, Selection{PlaylistID: p.ID})
	if err != nil || len(result.Succeeded) != 251 {
		t.Fatalf("play: %+v %v", result, err)
	}
	snapshot := s.Snapshot().Player
	if len(snapshot.Queue) != 251 || snapshot.Index != 230 {
		t.Fatalf("visible-page truncation: %d/%d", snapshot.Index, len(snapshot.Queue))
	}
}

func TestPlayAllShufflesFirstSong(t *testing.T) {
	s := openTestSession(t, newFakeEngine(), newFakeSource())
	f := s.Features()
	ctx := context.Background()
	p, err := f.CreatePlaylist(ctx, PlaylistEdit{Title: "完整歌单"})
	if err != nil {
		t.Fatal(err)
	}
	songs := []store.Song{testSong("a", "A"), testSong("b", "B"), testSong("c", "C")}
	if _, err := f.Batch(ctx, "add", p.ID, Selection{Songs: songs}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMode(ctx, player.ModeShuffle); err != nil {
		t.Fatal(err)
	}
	s.ctrl.SetShuffleFunc(func(queue []store.Song) {
		for i, j := 0, len(queue)-1; i < j; i, j = i+1, j-1 {
			queue[i], queue[j] = queue[j], queue[i]
		}
	})
	if _, err := f.Batch(ctx, "play", p.ID, Selection{PlaylistID: p.ID}); err != nil {
		t.Fatal(err)
	}
	current := s.Snapshot().Player.Current
	if current == nil || current.Title != "C" {
		t.Fatalf("随机播放全部应从洗牌后的第一首开始: %+v", current)
	}
	want := store.NormalizeSong(songs[0]).ID
	if _, err := f.Batch(ctx, "play", want, Selection{PlaylistID: p.ID}); err != nil {
		t.Fatal(err)
	}
	current = s.Snapshot().Player.Current
	if current == nil || current.ID != want {
		t.Fatalf("点歌应保留选中曲目: %+v", current)
	}
}
func TestLoadingPauseResumeUsesLatestIntent(t *testing.T) {
	src := newFakeSource()
	gate := make(chan struct{})
	src.gates["a"] = gate
	engine := newFakeEngine()
	s := openTestSession(t, engine, src)
	ctx := context.Background()
	song := testSong("a", "a")
	if _, err := s.Play(ctx, PlayInput{Song: &song}); err != nil {
		t.Fatal(err)
	}
	if err := s.Pause(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	close(gate)
	load := waitLoad(t, engine, 1)
	if !load.ready {
		t.Fatal("resume during resolution was lost")
	}
	engine.emit(EngineEvent{PlaybackID: load.id, Seq: 1, Type: media.EventStateChanged, State: media.StatePlaying})
	waitFor(t, time.Second, func() bool { return s.Snapshot().Player.PlaybackState == media.StatePlaying })
	if _, err := s.Toggle(ctx); err != nil {
		t.Fatal(err)
	}
	if s.Snapshot().PlayWhenReady {
		t.Fatal("toggle pause left a stale playing intention")
	}
}
func TestInvalidationOverflowDoesNotBlockWithConcurrentReader(t *testing.T) {
	s := openTestSession(t, newFakeEngine(), newFakeSource())
	f := s.Features()
	events, cancel := f.Subscribe()
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 5000; i++ {
			f.invalidate("download.changed")
		}
	}()
	timeout := time.After(time.Second)
	for {
		select {
		case <-events:
		case <-done:
			return
		case <-timeout:
			t.Fatal("event overflow drain blocked")
		}
	}
}
func TestGUISettingsDoNotOverwriteTUIKeymap(t *testing.T) {
	s := openTestSession(t, newFakeEngine(), newFakeSource())
	ctx := context.Background()
	f := s.Features()
	if err := s.store.SetSetting("keymap", "tui"); err != nil {
		t.Fatal(err)
	}
	settings, err := f.GUISettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.Theme = "dark"
	settings.Shortcuts = map[string]string{"authors": "u"}
	if err = f.SaveGUISettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if s.store.Setting("keymap", "") != "tui" {
		t.Fatal("GUI changed TUI keymap")
	}
	status, err := f.SyncStatus(ctx)
	if err != nil || status.SyncAccounts {
		t.Fatalf("account sync must default off: %+v %v", status, err)
	}
}

func TestGUISettingsHapticsDefaultsOnAndPersists(t *testing.T) {
	s := openTestSession(t, newFakeEngine(), newFakeSource())
	ctx := context.Background()
	f := s.Features()
	settings, err := f.GUISettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !settings.Haptics {
		t.Fatal("haptic feedback must default on")
	}
	settings.Theme = "dark"
	settings.Haptics = false
	if err = f.SaveGUISettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	settings, err = f.GUISettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Haptics {
		t.Fatal("disabled haptic feedback was not persisted")
	}
	// Preferences written before this field existed keep the default.
	if err = s.store.SetSetting("gui.settings", `{"theme":"dark","palette":"zero-red","close_action":"background","shortcuts":{},"window":""}`); err != nil {
		t.Fatal(err)
	}
	legacy, err := f.GUISettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !legacy.Haptics {
		t.Fatal("existing settings without the field must keep haptics on")
	}
}

func TestBatchTaskSurvivesRequestCancellation(t *testing.T) {
	s := openTestSession(t, newFakeEngine(), newFakeSource())
	f := s.Features()
	ctx, cancel := context.WithCancel(context.Background())
	playlist, err := f.CreatePlaylist(ctx, PlaylistEdit{Title: "任务目标"})
	if err != nil {
		t.Fatal(err)
	}
	songs := []store.Song{}
	for i := 0; i < 251; i++ {
		songs = append(songs, testSong(fmt.Sprint(i), fmt.Sprint(i)))
	}
	task, err := f.StartBatch(ctx, "add", playlist.ID, Selection{Songs: songs}, false)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	waitFor(t, 3*time.Second, func() bool {
		result, e := f.BatchWork(context.Background(), task.ID)
		return e == nil && result.Task.State != "running"
	})
	result, err := f.BatchWork(context.Background(), task.ID)
	if err != nil || result.Task.State != "completed" || len(result.Result.Succeeded) != 251 {
		t.Fatalf("lost task after request cancellation: %+v %v", result, err)
	}
	if _, err = f.RetryBatch(context.Background(), task.ID); err == nil {
		t.Fatal("retry replayed successful targets")
	}
}
func TestResourceNewScanInvalidatesOldCommit(t *testing.T) {
	s := openTestSession(t, newFakeEngine(), newFakeSource())
	f := s.Features()
	ctx := context.Background()
	old, err := f.ResourceBegin(ctx, "", "content://provider/tree/folder", "before")
	if err != nil {
		t.Fatal(err)
	}
	current, err := f.ResourceBegin(ctx, "", "content://provider/tree/folder", "after")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.ResourceCommit(ctx, old); err == nil {
		t.Fatal("old scan was allowed to commit")
	}
	if err = f.ResourceCancel(ctx, current); err != nil {
		t.Fatal(err)
	}
	if _, err = f.ResourceCommit(ctx, current); err == nil {
		t.Fatal("cancelled scan was allowed to commit")
	}
}

func TestWorkFamiliesDoNotBlockPlaybackOrEachOther(t *testing.T) {
	s := openTestSession(t, newFakeEngine(), newFakeSource())
	f := s.Features()
	ctx := context.Background()
	started := make(chan struct{})
	finished := make(chan struct{})
	_, err := f.startWorkJob(ctx, "probe", func(ctx context.Context, _ string) error {
		close(started)
		defer close(finished)
		<-ctx.Done()
		return ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err := f.startWorkJob(ctx, "now", func(context.Context, string) error { return nil }); err == nil {
		t.Fatal("overlapping sync transaction was accepted")
	}
	p, err := f.CreatePlaylist(ctx, PlaylistEdit{Title: "parallel"})
	if err != nil {
		t.Fatal(err)
	}
	task, err := f.StartBatch(ctx, "add", p.ID, Selection{Songs: []store.Song{testSong("a", "a")}}, false)
	if err != nil {
		t.Fatal("sync blocked independent batch:", err)
	}
	waitFor(t, time.Second, func() bool {
		result, e := f.BatchWork(ctx, task.ID)
		return e == nil && result.Task.State == "completed"
	})
	if err := s.SetVolume(ctx, 0.2); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	default:
		t.Fatal("close returned before accepted work drained")
	}
}

func TestClipPreviewUsesRequestedRange(t *testing.T) {
	engine := newFakeEngine()
	s := openTestSession(t, engine, newFakeSource())
	f, ctx := s.Features(), context.Background()
	song := testSong("a", "a")
	if _, err := s.Play(ctx, PlayInput{Song: &song}); err != nil {
		t.Fatal(err)
	}
	waitLoad(t, engine, 1)
	id := s.Snapshot().Player.Current.ID
	token, _, err := f.BeginClip(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	clip := store.Clip{StartMS: 10000, EndMS: 20000}
	if err := f.EditClip(ctx, id, "preview", clip, 10000, token); err != nil {
		t.Fatal(err)
	}
	load := waitLoad(t, engine, 2)
	engine.mu.Lock()
	stream := engine.stream
	at := engine.position
	engine.mu.Unlock()
	if stream.ClipStartMS != clip.StartMS || stream.ClipEndMS != clip.EndMS {
		t.Fatalf("preview stream clip = %d..%d, want %d..%d", stream.ClipStartMS, stream.ClipEndMS, clip.StartMS, clip.EndMS)
	}
	if at != 10*time.Second {
		t.Fatalf("preview start = %v, want 10s", at)
	}
	engine.emit(EngineEvent{PlaybackID: load.id, Seq: 1, Type: media.EventEnded, State: media.StateEnded})
	waitFor(t, 2*time.Second, func() bool {
		snap := s.Snapshot().Player
		return snap.Current != nil && snap.Current.ID == id && snap.PlaybackState == media.StatePaused
	})
}

func TestClipCancelRestoresStoredClipAndLatestPauseIntent(t *testing.T) {
	engine := newFakeEngine()
	s := openTestSession(t, engine, newFakeSource())
	f, ctx := s.Features(), context.Background()
	song := testSong("a", "a")
	if _, err := s.Play(ctx, PlayInput{Song: &song}); err != nil {
		t.Fatal(err)
	}
	waitLoad(t, engine, 1)
	id := s.Snapshot().Player.Current.ID
	original := store.Clip{StartMS: 1000, EndMS: 8000}
	if err := s.svc.SaveClip(id, original); err != nil {
		t.Fatal(err)
	}
	token, _, err := f.BeginClip(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.EditClip(ctx, id, "preview", store.Clip{}, 500, token); err != nil {
		t.Fatal(err)
	}
	waitLoad(t, engine, 2)
	if err = s.Pause(ctx); err != nil {
		t.Fatal(err)
	}
	if err = f.EditClip(ctx, id, "cancel", store.Clip{}, 0, token); err != nil {
		t.Fatal(err)
	}
	load := waitLoad(t, engine, 3)
	if load.ready || s.Snapshot().PlayWhenReady {
		t.Fatal("cancel resumed paused playback")
	}
	clip, err := f.Clip(ctx, id)
	if err != nil || clip != original {
		t.Fatalf("stored clip changed: %+v %v", clip, err)
	}
	var restored store.Clip
	s.call(ctx, func() error { restored = s.clipFor(*s.Snapshot().Player.Current); return nil })
	if restored != original {
		t.Fatalf("preview was not cleared: %+v", restored)
	}
	other := testSong("b", "b")
	s.Play(ctx, PlayInput{Song: &other})
	s.Play(ctx, PlayInput{Song: &song})
	if err = f.EditClip(ctx, id, "save", store.Clip{StartMS: 3000}, 0, token); err != ErrStalePlayback {
		t.Fatalf("old editor revived after A/B/A: %v", err)
	}
}

func TestPrefetchIsConsumedWithoutResolvingNextSongTwice(t *testing.T) {
	engine, src := newFakeEngine(), newFakeSource()
	s := openTestSession(t, engine, src)
	ctx := context.Background()
	songs := []store.Song{testSong("a", "a"), testSong("b", "b")}
	if _, err := s.Play(ctx, PlayInput{Queue: songs, QueueIndex: 0}); err != nil {
		t.Fatal(err)
	}
	load := waitLoad(t, engine, 1)
	engine.emit(EngineEvent{PlaybackID: load.id, Seq: 1, Type: media.EventStateChanged, State: media.StatePlaying})
	waitFor(t, time.Second, func() bool { return s.Snapshot().Player.PlaybackState == media.StatePlaying })
	s.call(ctx, func() error { s.prefetchNext(); return nil })
	waitFor(t, time.Second, func() bool {
		var ready bool
		s.call(ctx, func() error { ready = s.prefetch.Matches(store.NormalizeSong(songs[1]).ID, s.now()); return nil })
		return ready
	})
	if _, err := s.Next(ctx); err != nil {
		t.Fatal(err)
	}
	waitLoad(t, engine, 2)
	count := 0
	for _, id := range src.snapshotCalls() {
		if id == "b" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("next song resolved %d times", count)
	}
}

func TestSleepDuringResolutionKeepsLateLoadPaused(t *testing.T) {
	src := newFakeSource()
	gate := make(chan struct{})
	src.gates["a"] = gate
	engine := newFakeEngine()
	s := openTestSession(t, engine, src)
	ctx := context.Background()
	song := testSong("a", "a")
	s.Play(ctx, PlayInput{Song: &song})
	s.call(ctx, func() error {
		s.ctrl.SetSleep(time.Second)
		expired := s.ctrl.SleepUntil().Add(time.Millisecond)
		s.clockMu.Lock()
		s.nowFn = func() time.Time { return expired }
		s.clockMu.Unlock()
		s.onTick()
		return nil
	})
	close(gate)
	if load := waitLoad(t, engine, 1); load.ready {
		t.Fatal("sleep timeout lost during resolution")
	}
	if s.Snapshot().PlayWhenReady {
		t.Fatal("expired timer kept playing intention")
	}
}
