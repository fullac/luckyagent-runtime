package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yurika0211/luckyagent-runtime/utils"
)

func TestSessionIndexMatchesSidecarAndSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	mgr, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	sess := mgr.New()
	sess.SetTitle("indexed")
	sess.SetPinned(true)
	sess.SetProject("catalog")
	sess.AddMessage("user", "first")
	sess.AddMessage("assistant", "second")
	if err := sess.Save(); err != nil {
		t.Fatal(err)
	}

	rows, err := mgr.index.List()
	if err != nil {
		t.Fatal(err)
	}
	row, ok := rows[sess.ID]
	if !ok || row.Meta.Title != "indexed" || !row.Meta.Pinned || row.Meta.Project != "catalog" || row.Meta.MessageCount != 2 {
		t.Fatalf("index row = %+v", row.Meta)
	}
	if row.TailOffset < 0 || row.FileSize <= 0 || row.TailOffset >= row.FileSize {
		t.Fatalf("tail offset=%d file=%d", row.TailOffset, row.FileSize)
	}
	info, err := os.Stat(sessionMetadataPath(dir, sess.ID))
	if err != nil || row.SourceSize != info.Size() || !row.SourceTime.Equal(info.ModTime()) {
		t.Fatalf("fingerprint size=%d time=%v info=%v err=%v", row.SourceSize, row.SourceTime, info, err)
	}

	reloaded, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	loaded, ok := reloaded.Get(sess.ID)
	if !ok || loaded.messagesLoaded || loaded.Title != "indexed" || loaded.tailOffset != row.TailOffset {
		t.Fatalf("reload title=%q loaded=%v offset=%d", loaded.Title, loaded.messagesLoaded, loaded.tailOffset)
	}
	page, total, more, err := loaded.GetMessagesPage(1, 0)
	if err != nil || total != 2 || !more || len(page) != 1 || page[0].Content != "second" {
		t.Fatalf("tail page=%v total=%d more=%v err=%v", page, total, more, err)
	}
	if loaded.pageIndex != nil {
		t.Fatal("latest page rebuilt a full offset index")
	}
	page, total, _, err = loaded.GetMessagesPage(1, 1)
	if err != nil || total != 2 || len(page) != 1 || page[0].Content != "first" {
		t.Fatalf("older page=%v total=%d err=%v", page, total, err)
	}
}

func TestSessionIndexCorrectsExternalMetadataAndDelete(t *testing.T) {
	dir := t.TempDir()
	mgr, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	sess := mgr.NewWithTitle("before")
	sess.AddMessage("user", "kept")
	if err := sess.Save(); err != nil {
		t.Fatal(err)
	}
	metaPath := sessionMetadataPath(dir, sess.ID)
	meta, ok := readSessionMetadataLoose(dir, sess.ID)
	if !ok {
		t.Fatal("missing sidecar")
	}
	meta.Title = "after external edit"
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := utils.WriteFileAtomic(metaPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(metaPath, later, later); err != nil {
		t.Fatal(err)
	}

	reloaded, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	loaded, ok := reloaded.Get(sess.ID)
	if !ok || loaded.Title != "after external edit" {
		t.Fatalf("stale index title=%q", loaded.Title)
	}
	rows, err := reloaded.index.List()
	if err != nil || rows[sess.ID].Meta.Title != "after external edit" {
		t.Fatalf("index was not corrected: %+v err=%v", rows[sess.ID].Meta, err)
	}

	legacy := NewSession("legacy-only", dir)
	legacy.format = FormatLegacyMD
	legacy.Title = "old file"
	legacy.AddMessage("user", "legacy body")
	if err := legacy.saveLegacyMarkdownWithProfile(dir, legacy.ID, legacy.Title, "", "", "", false, legacy.CreatedAt, legacy.UpdatedAt, ShellContext{}, legacy.Messages); err != nil {
		t.Fatal(err)
	}
	withLegacy, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := withLegacy.Get(legacy.ID); !ok || got.Title != "old file" || got.Format() != FormatLegacyMD {
		t.Fatalf("legacy session missing: %+v", got)
	}
	rows, err = withLegacy.index.List()
	if err != nil || rows[legacy.ID].Meta.Format != FormatLegacyMD {
		t.Fatalf("legacy row=%+v err=%v", rows[legacy.ID].Meta, err)
	}

	if err := withLegacy.Delete(sess.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := withLegacy.Get(sess.ID); ok {
		t.Fatal("deleted session remained listed")
	}
	rows, err = withLegacy.index.List()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := rows[sess.ID]; ok {
		t.Fatal("deleted session remained in the index")
	}
	if _, err := os.Stat(filepath.Join(dir, sessionIndexName)); err != nil {
		t.Fatal(err)
	}
}

func TestSessionIndexFallsBackWhenDatabaseIsUnreadable(t *testing.T) {
	dir := t.TempDir()
	mgr, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	sess := mgr.NewWithTitle("survives")
	sess.AddMessage("user", "body")
	if err := sess.Save(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sessionIndexName), []byte("not a database"), 0600); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	loaded, ok := reloaded.Get(sess.ID)
	if !ok || loaded.Title != "survives" {
		t.Fatalf("corrupt index hid the session: %+v", loaded)
	}
	page, total, _, err := loaded.GetMessagesPage(10, 0)
	if err != nil || total != 1 || page[0].Content != "body" {
		t.Fatalf("page=%v total=%d err=%v", page, total, err)
	}
}
