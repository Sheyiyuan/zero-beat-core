package session

import (
	"context"
	"fmt"
	coresync "github.com/sheyiyuan/zero-beat-core/sync"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type Archive struct{ Name, Title string }

func (f *Features) Archives(ctx context.Context) ([]Archive, error) {
	return feature(f, ctx, func(ctx context.Context) ([]Archive, error) {
		root := f.syncer.Client().ArchiveDir()
		out := []Archive{}
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if d.IsDir() || d.Type()&os.ModeSymlink != 0 || filepath.Ext(path) != ".json" || filepath.Base(filepath.Dir(path)) != "deleted-playlists" {
				return nil
			}
			if len(out) >= 1000 {
				return fmt.Errorf("归档超过 1000 项，请先整理归档目录")
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			if info.Size() > 4<<20 {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			p, err := coresync.DecodePlaylistFile(raw)
			if err != nil {
				return nil
			}
			name, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			out = append(out, Archive{Name: filepath.ToSlash(name), Title: p.Title})
			return nil
		})
		return out, err
	})
}
func (f *Features) ArchiveText(ctx context.Context, name string) (string, error) {
	list, err := f.Archives(ctx)
	if err != nil {
		return "", err
	}
	valid := false
	for _, entry := range list {
		if entry.Name == name {
			valid = true
			break
		}
	}
	if !valid || strings.Contains(name, "..") {
		return "", ErrNotFound
	}
	return feature(f, ctx, func(context.Context) (string, error) {
		raw, err := os.ReadFile(filepath.Join(f.syncer.Client().ArchiveDir(), filepath.FromSlash(name)))
		return string(raw), err
	})
}
