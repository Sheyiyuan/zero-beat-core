package store

import "testing"

func TestResourceScanFailureRollsBackRootIndexAndLyrics(t *testing.T) {
	s := openTestStore(t)
	old := NormalizeSong(Song{Source: "local", RemoteID: "old", Title: "before"})
	root := "content://provider/tree/old"
	id, err := s.CommitResourceScan("", root, "folder", []LibraryItem{{Song: old, Path: root + "/old.mp3"}}, map[string]string{old.ID: "old lyrics"})
	if err != nil {
		t.Fatal(err)
	}
	// Emulate a storage failure at the last part of the transaction, after songs
	// and root were changed. The entire scan must still roll back.
	_, err = s.db.Exec(`CREATE TRIGGER fail_scan BEFORE INSERT ON settings WHEN NEW.key LIKE 'local.resource.lyrics:%' BEGIN SELECT RAISE(ABORT, 'disk full'); END`)
	if err != nil {
		t.Fatal(err)
	}
	next := NormalizeSong(Song{Source: "local", RemoteID: "new", Title: "after"})
	_, err = s.CommitResourceScan(id, "content://provider/tree/new", "new", []LibraryItem{{Song: next, Path: "content://provider/tree/new/new.mp3"}}, map[string]string{next.ID: "new lyrics"})
	if err == nil {
		t.Fatal("expected write failure")
	}
	p, err := s.Playlist(id)
	if err != nil || p.RootPath != root {
		t.Fatalf("root changed: %+v %v", p, err)
	}
	files, err := s.LibraryFiles(id)
	if err != nil || len(files) != 1 || files[0].SongID != old.ID || !files[0].Present {
		t.Fatalf("index changed: %+v %v", files, err)
	}
	if s.Setting("local.resource.lyrics:"+old.ID, "") != "old lyrics" {
		t.Fatal("lyrics lost")
	}
	var count int
	s.db.QueryRow(`SELECT COUNT(*) FROM playlists`).Scan(&count)
	_, err = s.CommitResourceScan("", "content://provider/tree/new", "new", []LibraryItem{{Song: next, Path: "content://provider/new.mp3"}}, map[string]string{next.ID: "new lyrics"})
	if err == nil {
		t.Fatal("expected create failure")
	}
	var after int
	s.db.QueryRow(`SELECT COUNT(*) FROM playlists`).Scan(&after)
	if count != after {
		t.Fatal("failed create left an empty playlist")
	}
}
