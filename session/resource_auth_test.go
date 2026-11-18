package session

import (
	"context"
	"errors"
	"github.com/sheyiyuan/zero-beat-core/source"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

type lateLoginSource struct {
	*fakeSource
	loginLock        sync.Mutex
	cookies          map[string]string
	started, release chan struct{}
}

func (s *lateLoginSource) StartQRLogin(context.Context) (source.QRCode, error) {
	return source.QRCode{Key: "qr-key", URL: "https://example.invalid/login"}, nil
}
func (s *lateLoginSource) PollQRLogin(context.Context, string) (source.LoginStatus, error) {
	close(s.started)
	<-s.release // Model a provider whose upstream request cannot be cancelled.
	s.SetSession(map[string]string{"cookie": "late-login"})
	return source.LoginStatus{LoggedIn: true}, nil
}
func (s *lateLoginSource) Session() map[string]string {
	s.loginLock.Lock()
	defer s.loginLock.Unlock()
	return maps.Clone(s.cookies)
}
func (s *lateLoginSource) SetSession(value map[string]string) {
	s.loginLock.Lock()
	defer s.loginLock.Unlock()
	s.cookies = maps.Clone(value)
}
func (s *lateLoginSource) Logout() { s.SetSession(nil) }
func TestCancelledQRPollCannotCommitLateCookies(t *testing.T) {
	src := &lateLoginSource{fakeSource: newFakeSource(), cookies: map[string]string{"cookie": "before"}, started: make(chan struct{}), release: make(chan struct{})}
	s := openTestSession(t, newFakeEngine(), src)
	f := s.Features()
	ctx := context.Background()
	qr, err := f.StartLogin(ctx, "fake")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := f.PollLogin(ctx, "fake", qr.Key); done <- err }()
	<-src.started
	if err = f.CancelLogin(ctx, "fake", qr.Key); err != nil {
		t.Fatal(err)
	}
	close(src.release)
	if err = <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("late result: %v", err)
	}
	if src.Session()["cookie"] != "before" {
		t.Fatal("cancelled QR replaced existing cookies")
	}
	if _, err = f.PollLogin(ctx, "fake", qr.Key); err == nil {
		t.Fatal("cancelled QR remains usable")
	}
}
func TestResourceScanPreservesIdentityAndMissingIndex(t *testing.T) {
	s := openTestSession(t, newFakeEngine(), newFakeSource())
	f := s.Features()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sample.mp3")
	if err := os.WriteFile(path, []byte("sample-audio-content"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	root := "content://provider/tree/music%3Amine"
	scan, err := f.ResourceBegin(ctx, "", root, "手机音乐")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.ResourceInspect(ctx, scan, root+"/document/one", "第一首.mp3", "[00:01]歌词", int(file.Fd()), 60); err != nil {
		t.Fatal(err)
	}
	first, err := f.ResourceCommit(ctx, scan)
	if err != nil {
		t.Fatal(err)
	}
	playlist, err := s.store.Playlist(first.Playlist.ID)
	if err != nil {
		t.Fatal(err)
	}
	songs, _, err := s.svc.PlaylistSongs(ctx, playlist, false)
	if err != nil || len(songs) != 1 {
		t.Fatalf("songs: %v %v", songs, err)
	}
	songID := songs[0].ID
	scan, err = f.ResourceBegin(ctx, playlist.ID, root, "手机音乐")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.ResourceInspect(ctx, scan, root+"/document/moved", "改名.mp3", "[00:01]歌词", int(file.Fd()), 60); err != nil {
		t.Fatal(err)
	}
	if _, err = f.ResourceCommit(ctx, scan); err != nil {
		t.Fatal(err)
	}
	songs, _, err = s.svc.PlaylistSongs(ctx, playlist, false)
	if err != nil || len(songs) != 1 || songs[0].ID != songID {
		t.Fatalf("identity after move: %+v %v", songs, err)
	}
	if err = f.ResourceUnavailable(ctx, playlist.ID); err != nil {
		t.Fatal(err)
	}
	songs, _, err = s.svc.PlaylistSongs(ctx, playlist, false)
	if err != nil || len(songs) != 1 {
		t.Fatalf("revocation lost index: %+v %v", songs, err)
	}
	if _, err = file.Stat(); err != nil {
		t.Fatalf("core closed borrowed descriptor: %v", err)
	}
}

func TestUnreadableResourceScanDoesNotReplaceExistingIndex(t *testing.T) {
	s := openTestSession(t, newFakeEngine(), newFakeSource())
	f, ctx := s.Features(), context.Background()
	root := "content://provider/tree/old"
	p, err := s.store.CreateFolderPlaylist("existing", root)
	if err != nil {
		t.Fatal(err)
	}
	scan, err := f.ResourceBegin(ctx, p.ID, "content://provider/tree/revoked", "replacement")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.ResourceSkip(ctx, scan, "unreadable.mp3"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.ResourceCommit(ctx, scan); err == nil {
		t.Fatal("unreadable scan was committed")
	}
	got, err := s.store.Playlist(p.ID)
	if err != nil || got.RootPath != root {
		t.Fatalf("root changed: %+v %v", got, err)
	}
}
