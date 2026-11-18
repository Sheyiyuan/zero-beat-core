package sync

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sheyiyuan/zero-beat-core/store"
)

func newDefaultDevice(t *testing.T, backend Backend) *testDevice {
	t.Helper()
	d := newTestDevice(t, "default")
	// Exercise the actual missing-config default, not an explicit false setting.
	if err := os.Remove(d.client.opts.ConfigPath); err != nil {
		t.Fatal(err)
	}
	d.client.opts.Backend = backend
	return d
}

func assertDataOnlyResult(t *testing.T, d *testDevice, result Result, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	if result.KeyIssue != nil || len(result.Warnings) != 0 || result.Accounts != 0 || len(result.AccountsApplied) != 0 {
		t.Fatalf("disabled account sync leaked work/warnings: %+v", result)
	}
	status := d.client.Status()
	if status.SyncAccounts || status.KeyIssue != "" || len(status.Warnings) != 0 || !strings.Contains(status.Summary(), "成功") {
		t.Fatalf("unexpected data-only status: %+v", status)
	}
}

func TestSyncAccountsDefaultsAndStatus(t *testing.T) {
	d := newDefaultDevice(t, BackendGoGit)
	for _, config := range []string{"", "remote = \"/tmp/remote.git\"\nauto = true\n"} {
		if config != "" {
			if err := os.WriteFile(d.client.opts.ConfigPath, []byte(config), 0600); err != nil {
				t.Fatal(err)
			}
		}
		cfg, err := LoadConfig(d.client.opts.ConfigPath)
		if err != nil || cfg.SyncAccounts {
			t.Fatalf("must default off: %+v %v", cfg, err)
		}
	}
	issue := ErrKeyMissing.Error()
	if err := SaveState(d.client.statePath(), State{LastSyncAt: time.Now().Unix(), KeyIssue: issue, CredentialWarnings: []string{issue, "远端登录态不可解密：bilibili", "本地 keymap.toml 无效，已跳过同步"}}); err != nil {
		t.Fatal(err)
	}
	status := d.client.Status()
	if status.KeyIssue != "" || len(status.Warnings) != 1 || !strings.HasPrefix(status.Warnings[0], "本地 keymap") || !strings.Contains(status.Summary(), "成功") {
		t.Fatalf("stale warnings: %+v", status)
	}
	if err := d.client.SetSyncAccounts(true); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(d.client.opts.ConfigPath)
	if err != nil || !cfg.SyncAccounts || !cfg.Auto {
		t.Fatalf("enable did not persist: %+v %v", cfg, err)
	}
	if d.client.Status().KeyIssue == "" {
		t.Fatal("enabled mode must expose missing-key issue")
	}
	if err := d.client.SetSyncAccounts(false); err != nil {
		t.Fatal(err)
	}
	if _, err := d.client.SetPassphrase(context.Background(), testPassphrase()); err == nil {
		t.Fatal("rotation must require opt-in")
	}
}

func TestDataOnlyInitJoinAndEnableAccounts(t *testing.T) {
	for _, backend := range []Backend{BackendCLI, BackendGoGit} {
		t.Run(string(backend), func(t *testing.T) {
			ctx := context.Background()
			remote := embeddedRemote(t)
			a, b := newDefaultDevice(t, backend), newDefaultDevice(t, backend)
			pl, err := a.store.CreatePlaylist("default off")
			if err != nil {
				t.Fatal(err)
			}
			if err := a.store.SaveAccountAt("bilibili", []byte(`{"SESSDATA":"local-a"}`), 100); err != nil {
				t.Fatal(err)
			}
			out, err := a.client.Init(ctx, remote, "main", true, "")
			assertDataOnlyResult(t, a, out.Synced, err)
			if fileExists(a.client.opts.KeyPath) {
				t.Fatal("data-only init must not create a key")
			}
			meta, err := LoadMetaFile(a.client.repoPath())
			if err != nil || meta.V != 3 || meta.KeyVersion != 0 {
				t.Fatalf("invalid keyless meta: %+v %v", meta, err)
			}
			if fileExists(filepath.Join(a.client.repoPath(), AccountPath("bilibili"))) {
				t.Fatal("default init exported local credentials")
			}
			out, err = b.client.Init(ctx, remote, "main", true, "")
			assertDataOnlyResult(t, b, out.Synced, err)
			if !out.Joined {
				t.Fatal("expected join")
			}
			if _, err := b.store.Playlist(pl.ID); err != nil {
				t.Fatal(err)
			}
			res, err := b.client.Sync(ctx)
			assertDataOnlyResult(t, b, res, err)
			if res.Committed {
				t.Fatal("unchanged data-only sync should not commit")
			}
			// An unrelated/corrupt old cache must not block setting a first repository key.
			if err := os.WriteFile(a.client.opts.KeyPath, []byte("obsolete cache"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := a.client.SetSyncAccounts(true); err != nil {
				t.Fatal(err)
			}
			out, err = a.client.Init(ctx, remote, "main", true, testPassphrase())
			if err != nil || out.Synced.KeyIssue != nil || out.Synced.Accounts != 1 {
				t.Fatalf("enable first key: %+v %v", out, err)
			}
			// Device B remains opted out even after another device enables encryption.
			res, err = b.client.Sync(ctx)
			assertDataOnlyResult(t, b, res, err)
			if _, _, err := b.store.LoadAccountAt("bilibili"); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("disabled device imported a session: %v", err)
			}
			if err := b.client.SetSyncAccounts(true); err != nil {
				t.Fatal(err)
			}
			if _, err := b.client.Init(ctx, remote, "main", true, "wrong passphrase"); !errors.Is(err, ErrKeyMismatch) {
				t.Fatalf("wrong pass accepted: %v", err)
			}
			out, err = b.client.Init(ctx, remote, "main", true, testPassphrase())
			if err != nil || out.Synced.KeyIssue != nil {
				t.Fatalf("unlock failed: %+v %v", out, err)
			}
			payload, _, err := b.store.LoadAccountAt("bilibili")
			if err != nil || string(payload) != `{"SESSDATA":"local-a"}` {
				t.Fatalf("opt-in failed: %v", err)
			}
		})
	}
}

func TestDisabledAccountsPreserveCiphertextAndResolve(t *testing.T) {
	for _, backend := range []Backend{BackendCLI, BackendGoGit} {
		t.Run(string(backend), func(t *testing.T) {
			ctx := context.Background()
			remote := embeddedRemote(t)
			a, b := newTestDevice(t, "encrypted"), newDefaultDevice(t, backend)
			a.client.opts.Backend = backend
			pl, err := a.store.CreatePlaylist("shared")
			if err != nil {
				t.Fatal(err)
			}
			if err := a.store.SaveAccountAt("bilibili", []byte(`{"SESSDATA":"remote"}`), 100); err != nil {
				t.Fatal(err)
			}
			if _, err := a.client.Init(ctx, remote, "main", true, testPassphrase()); err != nil {
				t.Fatal(err)
			}
			cfgA, err := LoadConfig(a.client.opts.ConfigPath)
			if err != nil {
				t.Fatal(err)
			}
			gA, err := a.client.prepareGit(ctx, cfgA, "")
			if err != nil {
				t.Fatal(err)
			}
			meta, err := LoadMetaFile(a.client.repoPath())
			if err != nil {
				t.Fatal(err)
			}
			meta.V = 2
			metaData, err := meta.Encode()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(a.client.repoPath(), MetaFile), metaData, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := gA.CommitAll(ctx, "existing v2 repository"); err != nil {
				t.Fatal(err)
			}
			if err := gA.Push(ctx); err != nil {
				t.Fatal(err)
			}
			cipherPath := filepath.Join(a.client.repoPath(), AccountPath("bilibili"))
			cipher, err := os.ReadFile(cipherPath)
			if err != nil {
				t.Fatal(err)
			}
			// A cached, valid key must not opt B in. A corrupt key must not break sync either.
			key, err := os.ReadFile(a.client.opts.KeyPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(b.client.opts.KeyPath, key, 0600); err != nil {
				t.Fatal(err)
			}
			if err := b.store.SaveAccountAt("bilibili", []byte(`{"SESSDATA":"local-b"}`), 200); err != nil {
				t.Fatal(err)
			}
			out, err := b.client.Init(ctx, remote, "main", true, "")
			assertDataOnlyResult(t, b, out.Synced, err)
			// Replaying an interrupted first join must also leave the local session alone.
			cfg, err := LoadConfig(b.client.opts.ConfigPath)
			if err != nil {
				t.Fatal(err)
			}
			g, err := b.client.prepareGit(ctx, cfg, "")
			if err != nil {
				t.Fatal(err)
			}
			head, err := g.Head(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err := b.client.saveTransaction(transaction{Phase: "joining", Candidate: head, Backend: backend}); err != nil {
				t.Fatal(err)
			}
			if err := b.store.SaveAccountAt("bilibili", []byte(`{"SESSDATA":"older-local"}`), 50); err != nil {
				t.Fatal(err)
			}
			res, err := b.client.Sync(ctx)
			assertDataOnlyResult(t, b, res, err)
			payload, _, err := b.store.LoadAccountAt("bilibili")
			if err != nil || string(payload) != `{"SESSDATA":"older-local"}` {
				t.Fatal("recovery imported credentials while disabled")
			}
			if err := os.WriteFile(b.client.opts.KeyPath, []byte("corrupt key"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := a.store.RenamePlaylist(pl.ID, "remote edit"); err != nil {
				t.Fatal(err)
			}
			if _, err := a.client.Sync(ctx); err != nil {
				t.Fatal(err)
			}
			if err := b.store.RenamePlaylist(pl.ID, "local edit"); err != nil {
				t.Fatal(err)
			}
			if _, err := b.client.Sync(ctx); !errors.Is(err, ErrConflict) {
				t.Fatalf("expected playlist conflict: %v", err)
			}
			index, err := b.client.Conflicts()
			if err != nil {
				t.Fatal(err)
			}
			choices := map[string]Choice{}
			for _, entity := range index.Entities {
				choices[entity.Entity] = KeepRemote
			}
			res, err = b.client.Resolve(ctx, index.TxID, choices)
			assertDataOnlyResult(t, b, res, err)
			if _, err := a.client.Sync(ctx); err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(cipherPath)
			if err != nil || !bytes.Equal(cipher, after) {
				t.Fatal("disabled sync replaced existing ciphertext")
			}
			metaAfter, err := os.ReadFile(filepath.Join(a.client.repoPath(), MetaFile))
			if err != nil || !bytes.Equal(metaData, metaAfter) {
				t.Fatal("switch changed existing v2 encryption metadata")
			}
			payload, _, err = b.store.LoadAccountAt("bilibili")
			if err != nil || string(payload) != `{"SESSDATA":"older-local"}` {
				t.Fatal("resolve imported credentials while disabled")
			}
			// Reusing a managed clone for an empty remote must preserve the metadata
			// belonging to its existing ciphertext rather than publish invalid keyless data.
			out, err = b.client.Init(ctx, embeddedRemote(t), "main", true, "")
			assertDataOnlyResult(t, b, out.Synced, err)
			metaAfter, err = os.ReadFile(filepath.Join(b.client.repoPath(), MetaFile))
			if err != nil || !bytes.Equal(metaData, metaAfter) {
				t.Fatal("empty remote lost encryption metadata")
			}
			res, err = b.client.Sync(ctx)
			assertDataOnlyResult(t, b, res, err)
		})
	}
}

func TestKeylessMetaValidation(t *testing.T) {
	valid := Meta{V: 3, CreatedAt: time.Now().Unix()}
	data, _ := valid.Encode()
	if _, err := DecodeMeta(data); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Meta){func(m *Meta) { m.V = 2 }, func(m *Meta) { m.KeyVersion = -1 }, func(m *Meta) { m.Salt = "unexpected" }, func(m *Meta) { m.KDF = DefaultKDF() }, func(m *Meta) { m.CreatedAt = 0 }} {
		bad := valid
		mutate(&bad)
		data, _ := bad.Encode()
		if _, err := DecodeMeta(data); err == nil {
			t.Fatalf("accepted malformed meta: %+v", bad)
		}
	}
}
