package sync

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/sheyiyuan/zero-beat-core/store"
)

func TestSyncSongAnnotationsAcrossBackends(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("v2 annotation filenames contain colons, which Windows cannot materialize")
	}
	for _, backend := range []Backend{BackendCLI, BackendGoGit} {
		t.Run(string(backend), func(t *testing.T) {
			if backend == BackendGoGit {
				t.Setenv("PATH", t.TempDir())
			}
			ctx := context.Background()
			remote := embeddedRemote(t)
			a, b := newTestDevice(t, "a"), newTestDevice(t, "b")
			a.client.opts.Backend, b.client.opts.Backend = backend, backend
			playlist, err := a.store.CreatePlaylist("annotations")
			if err != nil {
				t.Fatal(err)
			}
			for _, song := range []store.Song{
				{Source: "bilibili", RemoteID: "BV18GVQ6AExD", BVID: "BV18GVQ6AExD", Page: 1, Title: "A"},
				{Source: "netease", RemoteID: "42", Page: 1, Title: "B"},
			} {
				if err := a.store.AddSongToPlaylist(playlist.ID, song); err != nil {
					t.Fatal(err)
				}
				annotation := store.SongAnnotation{
					SongID:        store.SongID(song.Source, song.RemoteID, song.Page),
					LyricOffsetMS: -500, CompDB: -1.5, ClipStartMS: 1000, ClipEndMS: 2000,
				}
				if err := a.store.SaveAnnotation(annotation); err != nil {
					t.Fatal(err)
				}
			}
			const passphrase = "a sufficiently long passphrase"
			if _, err := a.client.Init(ctx, remote, "main", true, passphrase); err != nil {
				t.Fatalf("initialize annotations: %v", err)
			}
			if _, err := b.client.Init(ctx, remote, "main", true, passphrase); err != nil {
				t.Fatalf("join repository containing annotations: %v", err)
			}
			want, err := a.store.ListAnnotations()
			if err != nil {
				t.Fatal(err)
			}
			got, err := b.store.ListAnnotations()
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("annotations changed on import: got %+v, want %+v, err %v", got, want, err)
			}
			songID := "bilibili:BV18GVQ6AExD:1"
			if err := a.store.SetSongCompDB(songID, 3); err != nil {
				t.Fatal(err)
			}
			if err := b.store.SetSongCompDB(songID, -3); err != nil {
				t.Fatal(err)
			}
			if _, err := a.client.Sync(ctx); err != nil {
				t.Fatalf("push updated annotation: %v", err)
			}
			if _, err := b.client.Sync(ctx); !errors.Is(err, ErrConflict) {
				t.Fatalf("want annotation conflict, got %v", err)
			}
			index, err := b.client.Conflicts()
			if err != nil || len(index.Entities) != 1 {
				t.Fatalf("want one annotation conflict, got %+v, err %v", index, err)
			}
			entity := index.Entities[0]
			if entity.Kind != KindAnnotation || entity.Path != AnnotationPath(songID) {
				t.Fatalf("wrong annotation conflict: %+v", entity)
			}
			if _, err := b.client.Resolve(ctx, index.TxID, map[string]Choice{entity.Entity: KeepRemote}); err != nil {
				t.Fatalf("resolve annotation conflict: %v", err)
			}
			if got, err := b.store.SongCompDB(songID); err != nil || got != 3 {
				t.Fatalf("resolved compensation = %v, err %v", got, err)
			}
			if result, err := b.client.Sync(ctx); err != nil || result.Committed {
				t.Fatalf("repeat sync must be unchanged: %+v, err %v", result, err)
			}
		})
	}
}

func TestCleanRepoPathAnnotationCompatibility(t *testing.T) {
	annotation := "annotations/bilibili:BV18GVQ6AExD:1.json"
	if AnnotationPath("bilibili:BV18GVQ6AExD:1") != annotation {
		t.Fatal("existing v2 annotation filenames must remain stable")
	}
	for _, path := range []string{annotation, "annotations/netease:42:1.json", "annotations/bilibili%3ABV1%3A1.json"} {
		got, err := cleanRepoPath(path)
		if !filepath.IsLocal(filepath.FromSlash(path)) {
			if err == nil {
				t.Fatalf("accepted path unsupported on this platform: %q", path)
			}
			continue
		}
		if err != nil || got != path {
			t.Errorf("valid annotation path %q rejected or changed: %q, %v", path, got, err)
		}
	}
	for _, path := range []string{"", "/etc/passwd", "../secret", "annotations/../secret", `annotations\..\secret`, "C:/secret", "C:secret", ".git/config", ".GIT/config", "annotations/a\x00.json"} {
		if _, err := cleanRepoPath(path); !errors.Is(err, ErrCorruptRepo) {
			t.Errorf("unsafe path %q accepted: %v", path, err)
		}
	}
}
