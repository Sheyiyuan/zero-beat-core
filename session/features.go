package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/sheyiyuan/zero-beat-core/app"
	"github.com/sheyiyuan/zero-beat-core/downloader"
	"github.com/sheyiyuan/zero-beat-core/source"
	"github.com/sheyiyuan/zero-beat-core/store"
	coresync "github.com/sheyiyuan/zero-beat-core/sync"
)

// Features owns non-playback workflows. Its lifetime is the native session,
// never a page or socket. Operations are drained before the store is closed.
type Features struct {
	authMu        sync.Mutex
	loginMu       sync.Mutex
	logins        map[string]*loginAttempt
	resourceMu    sync.Mutex
	resourceScans map[string]*resourceScan
	downloads     *downloader.Manager
	syncer        *coresync.Manager
	workMu        sync.Mutex
	workWG        sync.WaitGroup
	work          map[string]*workItem

	s           *Session
	gate        sync.RWMutex
	mu          sync.Mutex
	subscribers map[chan string]struct{}
	closed      bool
}

func (s *Session) Features() *Features { return s.features }
func newFeatures(s *Session) *Features {
	return &Features{s: s, subscribers: make(map[chan string]struct{})}
}
func feature[T any](f *Features, ctx context.Context, fn func(context.Context) (T, error)) (T, error) {
	var zero T
	f.gate.RLock()
	defer f.gate.RUnlock()
	if err := f.s.ready(ctx); err != nil {
		return zero, err
	}
	local, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(f.s.root, cancel)
	defer stop()
	return fn(local)
}
func (f *Features) Subscribe() (<-chan string, func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch := make(chan string, 32)
	if f.closed {
		close(ch)
		return ch, func() {}
	}
	f.subscribers[ch] = struct{}{}
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			f.mu.Lock()
			defer f.mu.Unlock()
			if _, ok := f.subscribers[ch]; ok {
				delete(f.subscribers, ch)
				close(ch)
			}
		})
	}
}
func (f *Features) changed(kind string) {
	if f.syncer != nil && (kind == "library.changed" || kind == "auth.changed") {
		f.syncer.NotifyChange()
	}
	f.invalidate(kind)
}
func (f *Features) invalidate(kind string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for ch := range f.subscribers {
		select {
		case ch <- kind:
		default:
			// A full observer gets an invalidation for every domain, not a lost result.
		draining:
			for {
				select {
				case <-ch:
				default:
					break draining
				}
			}
			ch <- "all.changed"
		}
	}
}
func (f *Features) close() {
	if f.downloads != nil {
		f.downloads.Close()
	}
	if f.syncer != nil {
		f.syncer.Close()
	}
	f.gate.Lock()
	// Close has already cancelled the root and marked Session closed. Wait for
	// accepted operations to leave the gate, then let queued tasks observe that
	// closed state before draining them (never wait while holding the gate).
	f.gate.Unlock()
	f.workWG.Wait()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	for ch := range f.subscribers {
		close(ch)
		delete(f.subscribers, ch)
	}
}
func (f *Features) Accounts(ctx context.Context, refresh bool) ([]source.User, error) {
	return feature(f, ctx, func(ctx context.Context) ([]source.User, error) {
		if refresh {
			f.authMu.Lock()
			f.validateAccounts(ctx)
			f.authMu.Unlock()
			f.changed("auth.changed")
			f.changed("library.changed")
		}
		users := []source.User{}
		for _, src := range f.s.svc.Sources().AccountSources() {
			users = append(users, src.User())
		}
		return users, nil
	})
}
func (f *Features) SourceInfos(ctx context.Context) ([]source.Source, error) {
	return feature(f, ctx, func(context.Context) ([]source.Source, error) { return f.s.svc.Sources().All(), nil })
}

type loginAttempt struct {
	ctx    context.Context
	cancel context.CancelFunc
	key    string
}

func (f *Features) CancelLogin(ctx context.Context, id, key string) error {
	_, err := feature(f, ctx, func(context.Context) (bool, error) {
		f.loginMu.Lock()
		defer f.loginMu.Unlock()
		if attempt := f.logins[id]; attempt != nil && (key == "" || attempt.key == key) {
			attempt.cancel()
			delete(f.logins, id)
		}
		return true, nil
	})
	return err
}
func (f *Features) StartLogin(ctx context.Context, id string) (source.QRCode, error) {
	return feature(f, ctx, func(ctx context.Context) (source.QRCode, error) {
		src := f.s.svc.Sources().GetString(id)
		if src == nil || !src.Caps().Has(source.CapAccount) {
			return source.QRCode{}, ErrUnsupported
		}
		f.loginMu.Lock()
		if f.logins == nil {
			f.logins = map[string]*loginAttempt{}
		}
		if old := f.logins[id]; old != nil {
			old.cancel()
		}
		local, cancel := context.WithCancel(f.s.root)
		attempt := &loginAttempt{ctx: local, cancel: cancel}
		f.logins[id] = attempt
		f.loginMu.Unlock()
		stop := context.AfterFunc(ctx, cancel)
		qr, err := f.s.svc.StartLogin(local, src.ID())
		stop()
		f.loginMu.Lock()
		defer f.loginMu.Unlock()
		if f.logins[id] != attempt || local.Err() != nil {
			return source.QRCode{}, context.Canceled
		}
		if err != nil {
			cancel()
			delete(f.logins, id)
			return qr, err
		}
		attempt.key = qr.Key
		return qr, nil
	})
}
func (f *Features) PollLogin(ctx context.Context, id, key string) (source.LoginStatus, error) {
	return feature(f, ctx, func(ctx context.Context) (source.LoginStatus, error) {
		src := f.s.svc.Sources().GetString(id)
		if src == nil || !src.Caps().Has(source.CapAccount) {
			return source.LoginStatus{}, ErrUnsupported
		}
		f.authMu.Lock()
		defer f.authMu.Unlock()
		f.loginMu.Lock()
		attempt := f.logins[id]
		valid := attempt != nil && attempt.key == key && key != ""
		f.loginMu.Unlock()
		if !valid {
			return source.LoginStatus{}, fmt.Errorf("二维码已失效，请刷新")
		}
		local, cancel := context.WithCancel(ctx)
		defer cancel()
		stop := context.AfterFunc(attempt.ctx, cancel)
		defer stop()
		previous := src.Session()
		status, err := f.s.svc.PollLogin(local, src, key)
		f.loginMu.Lock()
		defer f.loginMu.Unlock()
		if f.logins[id] != attempt || local.Err() != nil {
			// Providers may update cookies before returning; cancelled polls cannot commit them.
			src.SetSession(previous)
			return source.LoginStatus{}, context.Canceled
		}
		if err == nil && status.LoggedIn {
			err = f.s.svc.SaveAccount(src.ID(), src.Session())
			f.changed("auth.changed")
			f.changed("library.changed")
			attempt.cancel()
			delete(f.logins, id)
		}
		return status, err
	})
}
func (f *Features) Logout(ctx context.Context, id string) error {
	_, err := feature(f, ctx, func(context.Context) (bool, error) {
		src := f.s.svc.Sources().GetString(id)
		if src == nil || !src.Caps().Has(source.CapAccount) {
			return false, ErrUnsupported
		}
		f.loginMu.Lock()
		if pending := f.logins[id]; pending != nil {
			pending.cancel()
			delete(f.logins, id)
		}
		f.loginMu.Unlock()
		f.authMu.Lock()
		f.s.svc.Logout(src.ID())
		f.authMu.Unlock()
		f.changed("auth.changed")
		f.changed("library.changed")
		return true, nil
	})
	return err
}
func (f *Features) Comments(ctx context.Context, ref source.Ref, page int) (source.CommentPage, error) {
	return feature(f, ctx, func(ctx context.Context) (source.CommentPage, error) {
		src := f.s.svc.Sources().Get(ref.Source)
		r, ok := src.(source.CommentReader)
		if !ok || !src.Caps().Has(source.CapComments) {
			return source.CommentPage{}, ErrUnsupported
		}
		return r.Comments(ctx, ref, max(1, page))
	})
}
func (f *Features) Related(ctx context.Context, ref source.Ref) ([]source.Candidate, error) {
	return feature(f, ctx, func(ctx context.Context) ([]source.Candidate, error) {
		src := f.s.svc.Sources().Get(ref.Source)
		r, ok := src.(source.RelatedRecommender)
		if !ok || !src.Caps().Has(source.CapRelated) {
			return nil, ErrUnsupported
		}
		return r.Related(ctx, ref)
	})
}
func (f *Features) Browse(ctx context.Context, ref source.BrowseRef, page int) (source.BrowsePage, error) {
	return feature(f, ctx, func(ctx context.Context) (source.BrowsePage, error) {
		src := f.s.svc.Sources().Get(ref.Source)
		r, ok := src.(source.Browser)
		if !ok || !src.Caps().Has(source.CapBrowse) {
			return source.BrowsePage{}, ErrUnsupported
		}
		return r.Browse(ctx, ref, max(1, page))
	})
}
func (f *Features) Roots(ctx context.Context) ([]source.BrowseEntry, error) {
	return feature(f, ctx, func(context.Context) ([]source.BrowseEntry, error) {
		out := []source.BrowseEntry{}
		for _, src := range f.s.svc.Sources().All() {
			if b, ok := src.(source.Browser); ok && src.Caps().Has(source.CapBrowse) {
				out = append(out, b.BrowseRoots()...)
			}
		}
		return out, nil
	})
}
func (f *Features) Profiles(ctx context.Context, ref source.Ref, authorsOnly bool) ([]source.BrowseEntry, error) {
	return feature(f, ctx, func(ctx context.Context) ([]source.BrowseEntry, error) {
		src := f.s.svc.Sources().Get(ref.Source)
		b, ok := src.(source.Browser)
		if !ok || !src.Caps().Has(source.CapBrowse) {
			return nil, ErrUnsupported
		}
		all, err := b.TrackProfiles(ctx, ref)
		if err != nil {
			return nil, err
		}
		out := []source.BrowseEntry{}
		seen := map[source.BrowseRef]bool{}
		for _, v := range all {
			if authorsOnly && v.Ref.Kind != source.BrowseArtist && v.Ref.Kind != source.BrowseUser {
				continue
			}
			if !seen[v.Ref] {
				seen[v.Ref] = true
				out = append(out, v)
			}
		}
		return out, nil
	})
}

type PlaylistEdit struct {
	ID, Title, Kind, Path string
	LocalOnly             bool
	Refs                  []string
}

func (f *Features) CreatePlaylist(ctx context.Context, in PlaylistEdit) (store.Playlist, error) {
	return feature(f, ctx, func(context.Context) (store.Playlist, error) {
		if strings.TrimSpace(in.Title) == "" {
			return store.Playlist{}, fmt.Errorf("歌单名不能为空")
		}
		var p store.Playlist
		var err error
		switch in.Kind {
		case "mixin":
			p, err = f.s.svc.CreateMixin(in.Title, in.Refs)
		case "local", "":
			if in.LocalOnly {
				p, err = f.s.svc.CreateLocalOnlyPlaylist(in.Title)
			} else {
				p, err = f.s.svc.CreatePlaylist(in.Title)
			}
		default:
			return p, ErrUnsupported
		}
		if err == nil {
			f.changed("library.changed")
		}
		return p, err
	})
}
func (f *Features) EditPlaylist(ctx context.Context, action string, in PlaylistEdit) (store.Playlist, error) {
	return feature(f, ctx, func(ctx context.Context) (store.Playlist, error) {
		var p store.Playlist
		var err error
		switch action {
		case "rename":
			if strings.TrimSpace(in.Title) == "" {
				return p, fmt.Errorf("歌单名不能为空")
			}
			err = f.s.svc.RenamePlaylist(in.ID, in.Title)
		case "delete":
			err = f.s.svc.DeletePlaylistCompletely(in.ID)
		case "unlock":
			p, err = f.s.svc.UnlockPlaylist(in.ID)
		case "refs":
			err = f.s.svc.SetMixinRefs(in.ID, in.Refs)
		case "refresh":
			p, err = f.s.store.Playlist(in.ID)
			if err == nil {
				_, _, err = f.s.svc.PlaylistSongs(ctx, p, true)
			}
		default:
			return p, ErrUnsupported
		}
		if err == nil {
			f.changed("library.changed")
		}
		return p, err
	})
}
func (f *Features) MixinRefs(ctx context.Context, id string) ([]store.Playlist, error) {
	return feature(f, ctx, func(context.Context) ([]store.Playlist, error) { return f.s.svc.MixinRefs(id) })
}
func (f *Features) RemotePlaylists(ctx context.Context, id string, write bool) ([]source.Playlist, error) {
	return feature(f, ctx, func(ctx context.Context) ([]source.Playlist, error) {
		if write {
			return f.s.svc.CloudPlaylists(ctx, source.ID(id))
		}
		return f.s.svc.RemoteFolders(ctx, source.ID(id))
	})
}
func (f *Features) ImportPlaylist(ctx context.Context, p source.Playlist) (store.Playlist, error) {
	return feature(f, ctx, func(ctx context.Context) (store.Playlist, error) {
		p2, err := f.s.svc.ImportRemotePlaylist(p)
		if err == nil {
			_, _, err = f.s.svc.PlaylistSongs(ctx, p2, true)
			f.changed("library.changed")
		}
		return p2, err
	})
}
func (f *Features) Folder(ctx context.Context, action, id, path string) (app.LibraryScan, error) {
	return feature(f, ctx, func(ctx context.Context) (app.LibraryScan, error) {
		var out app.LibraryScan
		var err error
		switch action {
		case "add":
			_, out, err = f.s.svc.AddLocalFolderContext(ctx, path)
		case "rescan":
			out, err = f.s.svc.RescanLibraryContext(ctx, id)
		case "replace":
			out, err = f.s.svc.SetLibraryRootContext(ctx, id, path)
		default:
			return out, ErrUnsupported
		}
		f.changed("library.changed")
		return out, err
	})
}
func (f *Features) LocalSearch(ctx context.Context, query, playlistID string) ([]store.Song, error) {
	return feature(f, ctx, func(ctx context.Context) ([]store.Song, error) {
		var songs []store.Song
		var err error
		if playlistID == "" {
			songs, err = f.s.svc.SearchSongs(query, 10000)
		} else {
			p, e := f.s.store.Playlist(playlistID)
			if e != nil {
				return nil, e
			}
			songs, _, err = f.s.svc.PlaylistSongs(ctx, p, false)
		}
		if err != nil {
			return nil, err
		}
		q := strings.ToLower(strings.TrimSpace(query))
		out := []store.Song{}
		for _, song := range songs {
			if q == "" || strings.Contains(strings.ToLower(song.Title+" "+song.Artist+" "+song.Album), q) {
				out = append(out, song)
			}
		}
		return out, nil
	})
}

// Selection is resolved once before a batch. PlaylistID means the entire expanded
// playlist, irrespective of the UI's loaded page count.
type Selection struct {
	PlaylistID string
	Songs      []store.Song
}
type BatchItem struct{ SongID, Error string }
type BatchResult struct {
	Succeeded, Skipped []string
	Failed             []BatchItem
}

func (f *Features) selection(ctx context.Context, in Selection) ([]store.Song, error) {
	if in.PlaylistID != "" {
		p, err := f.s.store.Playlist(in.PlaylistID)
		if err != nil {
			return nil, err
		}
		songs, _, err := f.s.svc.PlaylistSongs(ctx, p, false)
		return append([]store.Song(nil), songs...), err
	}
	out := make([]store.Song, 0, len(in.Songs))
	seen := map[string]bool{}
	for _, raw := range in.Songs {
		song := store.NormalizeSong(raw)
		if song.ID == "" {
			return nil, fmt.Errorf("曲目无效")
		}
		if !seen[song.ID] {
			out = append(out, song)
			seen[song.ID] = true
		}
	}
	return out, nil
}
func (f *Features) Batch(ctx context.Context, action, target string, in Selection) (BatchResult, error) {
	return feature(f, ctx, func(ctx context.Context) (BatchResult, error) {
		out := BatchResult{Succeeded: []string{}, Skipped: []string{}, Failed: []BatchItem{}}
		songs, err := f.selection(ctx, in)
		if err != nil {
			return out, err
		}
		if action == "play" {
			if len(songs) == 0 {
				return out, ErrNotFound
			}
			index := 0
			if target != "" {
				found := false
				for i, song := range songs {
					if song.ID == target {
						index, found = i, true
						break
					}
				}
				// A playlist ID is accepted for whole-selection play.
				if !found && target != in.PlaylistID {
					return out, ErrNotFound
				}
			}
			_, err = f.s.Play(ctx, PlayInput{Queue: songs, QueueIndex: index})
			if err == nil {
				for _, song := range songs {
					out.Succeeded = append(out.Succeeded, song.ID)
				}
			}
			return out, err
		}
		// Insert-next runs backwards to retain the user's selection order.
		if action == "next" {
			for i, j := 0, len(songs)-1; i < j; i, j = i+1, j-1 {
				songs[i], songs[j] = songs[j], songs[i]
			}
		}
		for _, song := range songs {
			if err := ctx.Err(); err != nil {
				return out, err
			}
			var e error
			switch action {
			case "add":
				if strings.HasPrefix(target, "cloud:") {
					parts := strings.SplitN(strings.TrimPrefix(target, "cloud:"), ":", 2)
					if len(parts) != 2 || parts[0] != song.Source {
						e = fmt.Errorf("只能添加同来源曲目")
					} else if strings.HasPrefix(song.ExternalID(), "program:") {
						e = fmt.Errorf("节目不能加入云端歌曲歌单")
					} else {
						e = f.s.svc.AddToCloudPlaylist(ctx, song, parts[1])
					}
					break
				}
				p, pe := f.s.store.Playlist(target)
				if pe != nil {
					e = pe
				} else if p.Kind == "remote" {
					if p.Source != song.Source {
						e = fmt.Errorf("只能添加同来源曲目")
					} else {
						e = f.s.svc.AddToCloudPlaylist(ctx, song, p.RemoteID)
					}
				} else {
					e = f.s.svc.AddSongToPlaylist(target, song)
				}
			case "remove":
				e = f.s.svc.RemoveSongFromPlaylist(target, song.ID)
			case "enqueue":
				_, e = f.s.Enqueue(ctx, song)
			case "next":
				_, e = f.s.InsertNext(ctx, song)
			default:
				return out, ErrUnsupported
			}
			if e != nil {
				out.Failed = append(out.Failed, BatchItem{song.ID, e.Error()})
			} else {
				out.Succeeded = append(out.Succeeded, song.ID)
			}
		}
		f.changed("library.changed")
		return out, nil
	})
}
func (f *Features) Lyrics(ctx context.Context, song store.Song) (app.LyricsResult, error) {
	return feature(f, ctx, func(ctx context.Context) (app.LyricsResult, error) {
		return f.s.svc.Lyrics(ctx, store.NormalizeSong(song)), nil
	})
}
func (f *Features) LyricCandidates(ctx context.Context, song store.Song, query, filter string) ([]app.LyricCandidate, error) {
	return feature(f, ctx, func(ctx context.Context) ([]app.LyricCandidate, error) {
		return f.s.svc.SearchLyricCandidates(ctx, song, query, filter)
	})
}
func (f *Features) PreviewLyrics(ctx context.Context, song store.Song, c app.LyricCandidate) (app.LyricPreview, error) {
	return feature(f, ctx, func(ctx context.Context) (app.LyricPreview, error) { return f.s.svc.PreviewLyricBinding(ctx, song, c) })
}
func (f *Features) BindLyrics(ctx context.Context, song store.Song, c app.LyricCandidate) error {
	_, err := feature(f, ctx, func(ctx context.Context) (bool, error) {
		_, _, err := f.s.svc.BindLyrics(ctx, song, c)
		if err == nil {
			f.changed("lyrics.changed")
		}
		return true, err
	})
	return err
}
func (f *Features) UnbindLyrics(ctx context.Context, id string) error {
	_, err := feature(f, ctx, func(context.Context) (bool, error) {
		err := f.s.svc.ClearLyricBinding(id)
		if err == nil {
			f.changed("lyrics.changed")
		}
		return true, err
	})
	return err
}
func (f *Features) LyricOffset(ctx context.Context, id string, scope store.BindingScope, ms int) error {
	_, err := feature(f, ctx, func(context.Context) (bool, error) {
		if scope != store.BindingAuto && scope != store.BindingLocal && scope != store.BindingPortable {
			return false, errors.New("歌词来源无效")
		}
		f.s.svc.SaveLyricOffset(id, scope, ms)
		f.changed("lyrics.changed")
		return true, nil
	})
	return err
}

func (f *Features) maintainAccounts() {
	refresh := time.NewTicker(12 * time.Hour)
	check := time.NewTicker(15 * time.Minute)
	defer refresh.Stop()
	defer check.Stop()
	renew := func() {
		_, _ = feature(f, f.s.root, func(ctx context.Context) (bool, error) {
			f.authMu.Lock()
			f.s.svc.RefreshSessions(ctx)
			f.validateAccounts(ctx)
			f.authMu.Unlock()
			f.changed("auth.changed")
			f.changed("library.changed")
			return true, nil
		})
	}
	renew()
	for {
		select {
		case <-f.s.root.Done():
			return
		case <-refresh.C:
			renew()
		case <-check.C:
			_, _ = f.Accounts(f.s.root, true)
		}
	}
}

func (f *Features) validateAccounts(ctx context.Context) {
	_, expired := f.s.svc.Users(ctx)
	for _, id := range expired {
		f.s.svc.Logout(id)
	}
}
