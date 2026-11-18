package session

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/sheyiyuan/zero-beat-core/app"
	"github.com/sheyiyuan/zero-beat-core/library"
	"github.com/sheyiyuan/zero-beat-core/lyrics"
	"github.com/sheyiyuan/zero-beat-core/resource"
	localsource "github.com/sheyiyuan/zero-beat-core/source/local"
	"github.com/sheyiyuan/zero-beat-core/store"
	"io"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

type resourceScan struct {
	ID, Root, Title string
	Files           []library.File
	Lyrics          map[string]string
	Sidecars        map[string]string
	SidecarBytes    int
	Subdirs         int
	AudioSkipped    int
	Warnings        []string
}

func (f *Features) ResourceBegin(ctx context.Context, id, root, title string, subdirs ...int) (string, error) {
	return feature(f, ctx, func(context.Context) (string, error) {
		if !resource.IsURI(root) {
			return "", fmt.Errorf("目录授权无效")
		}
		f.resourceMu.Lock()
		defer f.resourceMu.Unlock()
		if f.resourceScans == nil {
			f.resourceScans = map[string]*resourceScan{}
		}
		// Abandoned scans have no database effects; keep only a bounded recent set.
		if len(f.resourceScans) > 4 {
			clear(f.resourceScans)
		}
		for key, scan := range f.resourceScans {
			if (id != "" && scan.ID == id) || (id == "" && scan.Root == root) {
				delete(f.resourceScans, key)
			}
		}
		token := strconv.FormatInt(time.Now().UnixNano(), 36)
		f.resourceScans[token] = &resourceScan{ID: id, Root: root, Title: title, Lyrics: map[string]string{}, Sidecars: map[string]string{}}
		if len(subdirs) > 0 {
			f.resourceScans[token].Subdirs = max(0, subdirs[0])
		}
		return token, nil
	})
}
func (f *Features) ResourceInspect(ctx context.Context, token, uri, name, lrc string, fd, duration int) error {
	_, err := feature(f, ctx, func(context.Context) (bool, error) {
		if !resource.IsURI(uri) || fd < 0 {
			return false, fmt.Errorf("文件授权无效")
		}
		f.resourceMu.Lock()
		defer f.resourceMu.Unlock()
		scan := f.resourceScans[token]
		if scan == nil {
			return false, ErrNotFound
		}
		file, err := library.InspectHandle(fd, name, uri, duration)
		if err != nil {
			return false, err
		}
		lines := localsource.ResourceLyrics(name, store.Song{Title: file.Title, Artist: file.Artist}, scan.Sidecars, lyrics.DefaultLocalConfig())
		if len(lines) == 0 && len(lrc) <= 4<<20 {
			lines = lyrics.MergeSameTimestamp(lyrics.ParseLocalLRC(lrc))
		}
		if len(lines) == 0 {
			embedded, _ := library.EmbeddedLyrics(fmt.Sprintf("/proc/self/fd/%d", fd))
			lines = lyrics.MergeSameTimestamp(lyrics.ParseLocalLRC(embedded))
		}
		raw, err := json.Marshal(lines)
		if err != nil {
			return false, err
		}
		lrc = "zero-lyrics-v1:" + string(raw)
		scan.Files = append(scan.Files, file)
		scan.Lyrics[uri] = lrc
		return true, nil
	})
	return err
}
func (f *Features) ResourceCommit(ctx context.Context, token string) (app.LibraryScan, error) {
	return feature(f, ctx, func(context.Context) (app.LibraryScan, error) {
		f.resourceMu.Lock()
		defer f.resourceMu.Unlock()
		scan := f.resourceScans[token]
		if scan == nil {
			return app.LibraryScan{}, ErrNotFound
		}
		delete(f.resourceScans, token)
		if len(scan.Files) == 0 && scan.AudioSkipped > 0 {
			return app.LibraryScan{}, fmt.Errorf("所有音频均不可读；已保留原索引，请检查目录授权后重扫")
		}
		library.SortFiles(scan.Files)
		out, err := f.s.svc.ApplyResourceScan(scan.ID, scan.Root, scan.Title, scan.Files, scan.Lyrics)
		out.Subdirs = scan.Subdirs
		out.Warnings = append(out.Warnings, scan.Warnings...)
		f.invalidate("library.changed")
		return out, err
	})
}
func (f *Features) ResourceUnavailable(ctx context.Context, id string) error {
	_, err := feature(f, ctx, func(context.Context) (bool, error) {
		err := f.s.store.UpdatePlaylistScan(id, "missing", "目录授权已失效，请重新授权")
		f.invalidate("library.changed")
		return true, err
	})
	return err
}

func (f *Features) ResourceCancel(ctx context.Context, token string) error {
	_, err := feature(f, ctx, func(context.Context) (bool, error) {
		f.resourceMu.Lock()
		defer f.resourceMu.Unlock()
		delete(f.resourceScans, token)
		return true, nil
	})
	return err
}
func (f *Features) ResourceSkip(ctx context.Context, token, name string) error {
	_, err := feature(f, ctx, func(context.Context) (bool, error) {
		f.resourceMu.Lock()
		defer f.resourceMu.Unlock()
		scan := f.resourceScans[token]
		if scan == nil {
			return false, ErrNotFound
		}
		if library.IsAudioFile(name) {
			scan.AudioSkipped++
		}
		scan.Warnings = append(scan.Warnings, name+"：未能读取，已跳过，可重新扫描")
		return true, nil
	})
	return err
}

// ResourceLyrics reads a borrowed descriptor while the caller retains ownership.
// Decode and matching stay in Go, sharing GBK/UTF and translation rules with TUI.
func (f *Features) ResourceLyrics(ctx context.Context, token, name string, fd int) error {
	_, err := feature(f, ctx, func(context.Context) (bool, error) {
		if fd < 0 || path.IsAbs(name) || strings.Contains(name, "..") {
			return false, fmt.Errorf("歌词路径无效")
		}
		f.resourceMu.Lock()
		defer f.resourceMu.Unlock()
		scan := f.resourceScans[token]
		if scan == nil {
			return false, ErrNotFound
		}
		file, err := os.Open(fmt.Sprintf("/proc/self/fd/%d", fd))
		if err != nil {
			return false, err
		}
		defer file.Close()
		raw, err := io.ReadAll(io.LimitReader(file, (4<<20)+1))
		if err != nil {
			return false, err
		}
		if len(raw) > 4<<20 || scan.SidecarBytes+len(raw) > 32<<20 {
			return false, fmt.Errorf("歌词文件超过扫描大小限制")
		}
		text, err := lyrics.DecodeText(raw)
		if err != nil {
			return false, err
		}
		scan.SidecarBytes += len(raw)
		scan.Sidecars[name] = text
		return true, nil
	})
	return err
}
