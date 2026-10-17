package report

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestBoundedWriteCountsEncodedBytesAndPreservesPriorFile(t *testing.T) {
	dir := t.TempDir()
	reference := filepath.Join(dir, "reference.json")
	value := map[string]any{"escaped": "<>&\n", "nested": []string{"first", "second"}}
	if err := Write(reference, value); err != nil {
		t.Fatal(err)
	}
	encoded, err := os.ReadFile(reference)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "bounded.json")
	prior := []byte("prior private state\n")
	if err := os.WriteFile(path, prior, 0600); err != nil {
		t.Fatal(err)
	}
	if err := WriteBounded(path, value, int64(len(encoded)-1)); !errors.Is(err, ErrSizeLimit) {
		t.Fatalf("expected size rejection, got %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, prior) {
		t.Fatal("rejection changed prior state")
	}
	if err := WriteBounded(path, value, int64(len(encoded))); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(path)
	if err != nil || !bytes.Equal(got, encoded) {
		t.Fatal("exact-limit write differs")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("write not private", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 2 {
		t.Fatal("temporary file leaked", err)
	}
}
