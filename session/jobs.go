package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sheyiyuan/zero-beat-core/app"
	"github.com/sheyiyuan/zero-beat-core/downloader"
	"github.com/sheyiyuan/zero-beat-core/source"
	"github.com/sheyiyuan/zero-beat-core/store"
	coresync "github.com/sheyiyuan/zero-beat-core/sync"
)

// Work is an in-memory runtime task. It survives control socket replacement.
// Credentials and raw upstream errors are deliberately never stored here.
type Work struct{ ID, Action, State, Message string }
type workItem struct {
	Work
	cancel    context.CancelFunc
	result    BatchResult
	scan      app.LibraryScan
	selection []store.Song
	target    string
	force     bool
}

func (f *Features) startJobs() {
	f.downloads = downloader.New(2, func(ctx context.Context, song store.Song, job downloader.Job, progress func(int64, int64)) (string, int64, error) {
		out := f.s.svc.DownloadSong(ctx, song, source.Quality(job.Quality), job.Force, progress)
		return out.Path, out.Size, out.Err
	})
	dir := f.s.dir
	f.syncer = coresync.NewManager(coresync.NewClient(coresync.Options{Store: f.s.store, Dir: filepath.Join(dir, "sync"), ConfigPath: filepath.Join(dir, "sync.toml"), KeyPath: filepath.Join(dir, "sync-key"), TokenPath: filepath.Join(dir, "sync-token"), KeymapPath: filepath.Join(dir, "keymap.toml"), Revision: app.LibraryRevision}), coresync.ManagerOptions{})
	f.work = map[string]*workItem{}
	f.syncer.Start()
	go func() {
		for {
			select {
			case <-f.s.root.Done():
				return
			case event := <-f.downloads.Events():
				f.changed("download.changed")
				if event.Finished {
					f.invalidate("library.changed")
				}
			case event := <-f.syncer.Events():
				f.changed("sync.changed")
				if event.Kind == coresync.EventApplied {
					f.applySyncedAccounts()
					f.invalidate("library.changed")
					f.invalidate("auth.changed")
				}
			}
		}
	}()
}
func (f *Features) DownloadTasks(ctx context.Context) ([]downloader.Task, error) {
	return feature(f, ctx, func(context.Context) ([]downloader.Task, error) { return f.downloads.Tasks(), nil })
}
func (f *Features) Downloads(ctx context.Context) ([]store.DownloadEntry, error) {
	return feature(f, ctx, func(context.Context) ([]store.DownloadEntry, error) { return f.s.svc.Downloads() })
}
func (f *Features) DownloadSelection(ctx context.Context, in Selection, force bool) (BatchResult, error) {
	return feature(f, ctx, func(ctx context.Context) (BatchResult, error) {
		out := BatchResult{Succeeded: []string{}, Skipped: []string{}, Failed: []BatchItem{}}
		songs, err := f.selection(ctx, in)
		if err != nil {
			return out, err
		}
		quality := f.s.svc.Preferences().Quality
		for _, song := range songs {
			if song.Source == "local" {
				out.Skipped = append(out.Skipped, song.ID)
				continue
			}
			if !force {
				if _, ok := f.s.svc.DownloadedPath(song.ID); ok {
					out.Skipped = append(out.Skipped, song.ID)
					continue
				}
			}
			_, added := f.downloads.Add(song, song.Title, downloader.Job{Quality: int(quality), Force: force})
			if added {
				out.Succeeded = append(out.Succeeded, song.ID)
			} else {
				out.Skipped = append(out.Skipped, song.ID)
			}
		}
		f.downloads.Start()
		f.changed("download.changed")
		return out, nil
	})
}
func (f *Features) DownloadAction(ctx context.Context, action string, id int, songID string) error {
	_, err := feature(f, ctx, func(context.Context) (bool, error) {
		switch action {
		case "cancel":
			if !f.downloads.Cancel(id) {
				return false, ErrNotFound
			}
		case "retry":
			found := false
			for _, t := range f.downloads.Tasks() {
				if t.ID == id && t.State == downloader.Failed {
					f.downloads.Remove(id)
					f.downloads.Add(t.Song, t.Title, t.Job)
					found = true
					break
				}
			}
			if !found {
				return false, ErrNotFound
			}
			f.downloads.Start()
		case "remove_task":
			f.downloads.Remove(id)
		case "cancel_all":
			f.downloads.CancelAll()
		case "remove_record", "delete_file":
			if f.downloads.Busy(songID) {
				return false, fmt.Errorf("请先取消此曲下载")
			}
			if action == "delete_file" {
				entry, err := f.s.store.Download(songID)
				if err != nil {
					return false, err
				}
				if err = os.Remove(entry.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
					return false, err
				}
			}
			if err := f.s.svc.DeleteDownload(songID); err != nil {
				return false, err
			}
		default:
			return false, ErrUnsupported
		}
		f.changed("download.changed")
		return true, nil
	})
	return err
}
func (f *Features) Work(ctx context.Context) ([]Work, error) {
	return feature(f, ctx, func(context.Context) ([]Work, error) {
		f.workMu.Lock()
		defer f.workMu.Unlock()
		out := []Work{}
		for _, w := range f.work {
			out = append(out, w.Work)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
		return out, nil
	})
}
func (f *Features) CancelWork(ctx context.Context, id string) error {
	_, err := feature(f, ctx, func(context.Context) (bool, error) {
		f.workMu.Lock()
		defer f.workMu.Unlock()
		if w := f.work[id]; w != nil {
			w.cancel()
			return true, nil
		}
		return false, ErrNotFound
	})
	return err
}
func (f *Features) startWork(ctx context.Context, action string, run func(context.Context) error) (Work, error) {
	return f.startWorkJob(ctx, action, func(ctx context.Context, _ string) error {
		_, err := feature(f, ctx, func(ctx context.Context) (bool, error) { return true, run(ctx) })
		return err
	})
}
func (f *Features) startWorkJob(ctx context.Context, action string, run func(context.Context, string) error) (Work, error) {
	return feature(f, ctx, func(context.Context) (Work, error) {
		f.workMu.Lock()
		defer f.workMu.Unlock()
		// A sync transaction is serial, but must not block an unrelated batch or
		// directory scan. Serialize each mutation family, bounding workers to three.
		for _, w := range f.work {
			if w.State == "running" && workGroup(w.Action) == workGroup(action) {
				return Work{}, fmt.Errorf("同类后台操作正在进行，请稍后重试")
			}
		}
		taskCtx, cancel := context.WithCancel(f.s.root)
		id := strconv.FormatInt(time.Now().UnixNano(), 36)
		w := &workItem{Work: Work{ID: id, Action: action, State: "running"}, cancel: cancel}
		// Completed result snapshots remain available after a reconnect.
		if len(f.work) >= 100 {
			oldest := ""
			for key, old := range f.work {
				if old.State != "running" && (oldest == "" || key < oldest) {
					oldest = key
				}
			}
			if oldest != "" {
				delete(f.work, oldest)
			}
		}
		f.work[id] = w
		f.workWG.Add(1)
		go func() {
			defer f.workWG.Done()
			defer cancel()
			err := run(taskCtx, id)
			if err == nil && (action == "now" || action == "resolve" || action == "init") {
				f.applySyncedAccounts()
			}
			f.workMu.Lock()
			w.State = "completed"
			if errors.Is(err, context.Canceled) {
				w.State = "cancelled"
			} else if err != nil {
				w.State = "failed"
				switch workGroup(action) {
				case "folder":
					w.Message = "扫描未完成，请检查目录授权、文件和可用空间后重试"
				case "batch":
					w.Message = "批量操作未完成，请查看失败项后重试"
				default:
					w.Message = "同步操作未完成，请检查连接和同步状态后重试"
				}
			}
			f.workMu.Unlock()
			f.changed("tasks.changed")
			f.changed("sync.changed")
			f.invalidate("library.changed")
		}()
		return w.Work, nil
	})
}

func workGroup(action string) string {
	if strings.HasPrefix(action, "batch.") {
		return "batch"
	}
	if strings.HasPrefix(action, "folder.") {
		return "folder"
	}
	return "sync"
}
func (f *Features) SyncStatus(ctx context.Context) (coresync.Status, error) {
	return feature(f, ctx, func(context.Context) (coresync.Status, error) { return f.syncer.Status(), nil })
}

type SyncInput struct {
	Remote, Branch, Passphrase, Token, TxID, Entity, Choice string
	Auto, Accounts                                          bool
}

func (f *Features) SyncAction(ctx context.Context, action string, in SyncInput) (Work, error) {
	return f.startWork(ctx, action, func(ctx context.Context) error {
		client := f.syncer.Client()
		switch action {
		case "configure":
			return client.Configure(in.Remote, in.Branch, in.Auto)
		case "init":
			_, err := client.Init(ctx, in.Remote, in.Branch, in.Auto, in.Passphrase)
			return err
		case "probe":
			_, err := client.Probe(ctx)
			return err
		case "now":
			_, err := client.Sync(ctx)
			return err
		case "auto":
			return client.SetAuto(in.Auto)
		case "accounts":
			return client.SetSyncAccounts(in.Accounts)
		case "token":
			return client.SetToken(in.Token)
		case "passphrase":
			_, err := client.SetPassphrase(ctx, in.Passphrase)
			return err
		case "forget":
			return client.ForgetKey()
		case "choice":
			return client.SaveChoice(in.TxID, in.Entity, coresync.Choice(in.Choice))
		case "resolve":
			_, err := client.ResolvePending(ctx, in.TxID)
			return err
		case "discard":
			return client.DiscardConflict(ctx, in.TxID)
		default:
			return ErrUnsupported
		}
	})
}
func (f *Features) SyncConflicts(ctx context.Context) (coresync.ConflictIndex, error) {
	return feature(f, ctx, func(context.Context) (coresync.ConflictIndex, error) { return f.syncer.Client().Conflicts() })
}
func (f *Features) ConflictVersion(ctx context.Context, txid string, index int, side string) (string, error) {
	return feature(f, ctx, func(context.Context) (string, error) {
		if side != "local" && side != "remote" && side != "base" {
			return "", ErrUnsupported
		}
		current, err := f.syncer.Client().Conflicts()
		if err != nil {
			return "", err
		}
		if current.TxID != txid {
			return "", ErrNotFound
		}
		for _, entity := range current.Entities {
			if entity.Index == index {
				data, _, err := coresync.ReadConflictVersion(filepath.Join(f.s.dir, "sync"), txid, entity, side)
				return string(data), err
			}
		}
		return "", ErrNotFound
	})
}

func (f *Features) applySyncedAccounts() {
	_, _ = feature(f, f.s.root, func(context.Context) (bool, error) {
		f.authMu.Lock()
		defer f.authMu.Unlock()
		f.s.svc.ApplyStoredSessions()
		return true, nil
	})
	f.invalidate("auth.changed")
}

// StartBatch snapshots targets in the runtime and keeps partial results even if
// the control socket disappears. Playback stays synchronous and is never replayed.
func (f *Features) StartBatch(ctx context.Context, action, target string, in Selection, force bool) (Work, error) {
	switch action {
	case "add", "remove", "download":
	default:
		return Work{}, ErrUnsupported
	}
	in.Songs = append([]store.Song(nil), in.Songs...)
	return f.startWorkJob(ctx, "batch."+action, func(ctx context.Context, id string) error {
		songs, err := feature(f, ctx, func(ctx context.Context) ([]store.Song, error) { return f.selection(ctx, in) })
		if err != nil {
			return err
		}
		f.workMu.Lock()
		w := f.work[id]
		w.selection = songs
		w.target, w.force = target, force
		f.workMu.Unlock()
		var result BatchResult
		if action == "download" {
			result, err = f.DownloadSelection(ctx, Selection{Songs: songs}, force)
		} else {
			result, err = f.Batch(ctx, action, target, Selection{Songs: songs})
		}
		// Cancelled/unprocessed items remain retryable without repeating successes.
		completed := map[string]bool{}
		for _, key := range result.Succeeded {
			completed[key] = true
		}
		for _, key := range result.Skipped {
			completed[key] = true
		}
		for _, failure := range result.Failed {
			completed[failure.SongID] = true
		}
		for _, song := range songs {
			if !completed[song.ID] {
				result.Failed = append(result.Failed, BatchItem{SongID: song.ID, Error: "未完成"})
			}
		}
		f.workMu.Lock()
		w.result = result
		f.workMu.Unlock()
		return err
	})
}
func (f *Features) BatchWork(ctx context.Context, id string) (struct {
	Task   Work
	Result BatchResult
}, error) {
	return feature(f, ctx, func(context.Context) (struct {
		Task   Work
		Result BatchResult
	}, error) {
		f.workMu.Lock()
		defer f.workMu.Unlock()
		out := struct {
			Task   Work
			Result BatchResult
		}{}
		w := f.work[id]
		if w == nil {
			return out, ErrNotFound
		}
		out.Task = w.Work
		out.Result = w.result
		out.Result.Succeeded = append([]string{}, w.result.Succeeded...)
		out.Result.Skipped = append([]string{}, w.result.Skipped...)
		out.Result.Failed = append([]BatchItem{}, w.result.Failed...)
		return out, nil
	})
}
func (f *Features) RetryBatch(ctx context.Context, id string) (Work, error) {
	f.workMu.Lock()
	w := f.work[id]
	if w == nil || w.State == "running" {
		f.workMu.Unlock()
		return Work{}, ErrNotFound
	}
	failed := map[string]bool{}
	for _, item := range w.result.Failed {
		failed[item.SongID] = true
	}
	songs := []store.Song{}
	for _, song := range w.selection {
		if failed[song.ID] {
			songs = append(songs, song)
		}
	}
	action, target, force := strings.TrimPrefix(w.Action, "batch."), w.target, w.force
	f.workMu.Unlock()
	if len(songs) == 0 {
		return Work{}, ErrNotFound
	}
	return f.StartBatch(ctx, action, target, Selection{Songs: songs}, force)
}
func (f *Features) ConflictChoices(ctx context.Context, txid string) (map[string]coresync.Choice, error) {
	return feature(f, ctx, func(context.Context) (map[string]coresync.Choice, error) {
		return f.syncer.Client().PendingChoices(txid)
	})
}

func (f *Features) StartFolder(ctx context.Context, action, id, path string) (Work, error) {
	return f.startWorkJob(ctx, "folder."+action, func(ctx context.Context, key string) error {
		scan, err := f.Folder(ctx, action, id, path)
		f.workMu.Lock()
		f.work[key].scan = scan
		f.workMu.Unlock()
		return err
	})
}
func (f *Features) FolderResult(ctx context.Context, id string) (app.LibraryScan, error) {
	return feature(f, ctx, func(context.Context) (app.LibraryScan, error) {
		f.workMu.Lock()
		defer f.workMu.Unlock()
		w := f.work[id]
		if w == nil || w.State != "completed" {
			return app.LibraryScan{}, ErrNotFound
		}
		return w.scan, nil
	})
}
