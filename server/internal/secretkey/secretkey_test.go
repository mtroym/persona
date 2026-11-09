package secretkey

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// A key file is made once; another Box on it (a restart, the other process) opens what the first sealed.
func TestSealOpenAcrossBoxes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.key")
	sealed, err := New(path).Seal("sk-test")
	if err != nil || sealed == "" || sealed == "sk-test" {
		t.Fatalf("sealed %q %v", sealed, err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 || fi.Size() != 32 {
		t.Fatalf("key file %v %v", fi, err)
	}
	if plain, err := New(path).Open(sealed); err != nil || plain != "sk-test" {
		t.Fatalf("opened %q %v", plain, err)
	}
	if plain, err := New("").Open(sealed); err == nil {
		t.Fatalf("another key opened it: %q", plain)
	}
	if err := os.WriteFile(path, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(path).Seal("x"); err == nil {
		t.Fatal("a short key file was used")
	}
}

// A key in a directory not made yet (a manager's own, before anything else wrote there) is made with it.
func TestKeyInANewDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "managers", "assistant", "secret.key")
	if _, err := New(path).Seal("x"); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Dir(path)); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("directory %v %v", fi, err)
	}
}

// A Box with the fallback opens what the kept old keys sealed while their file is there, never seals with them, and
// stops opening it the moment the file goes; a Box without it never reads the file.
func TestFallbackToTheKeptKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.key")
	cur, old1, old2 := mustFresh(t), mustFresh(t), mustFresh(t)
	if err := WriteKeys(path, cur); err != nil {
		t.Fatal(err)
	}
	sealedOld, err := SealWith(old2, "sk-old")
	if err != nil {
		t.Fatal(err)
	}
	b := NewWithFallback(path)
	if _, err := b.Open(sealedOld); err == nil {
		t.Fatal("opened without the old key")
	}
	if err := WriteKeys(PrevPath(path), old1, old2); err != nil {
		t.Fatal(err)
	}
	if plain, err := b.Open(sealedOld); err != nil || plain != "sk-old" {
		t.Fatalf("fallback: %q %v", plain, err)
	}
	if _, err := New(path).Open(sealedOld); err == nil {
		t.Fatal("a Box without the fallback opened it")
	}
	sealedNew, err := b.Seal("sk-new")
	if err != nil {
		t.Fatal(err)
	}
	if p, err := OpenWith(cur, sealedNew); err != nil || p != "sk-new" {
		t.Fatalf("sealed with another key: %q %v", p, err)
	}
	if err := os.Remove(PrevPath(path)); err != nil {
		t.Fatal(err)
	}
	// this process keeps what it read; one started after the file went never has them
	if plain, err := b.Open(sealedOld); err != nil || plain != "sk-old" {
		t.Fatalf("this process forgot the old keys: %q %v", plain, err)
	}
	if _, err := NewWithFallback(path).Open(sealedOld); err == nil {
		t.Fatal("a new process opened it after the old keys went")
	}
	// a broken old-keys file is no fallback, and the key's own error is the one returned
	if err := os.WriteFile(PrevPath(path), []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewWithFallback(path).Open(sealedOld); err == nil {
		t.Fatal("opened with a broken old-keys file")
	}
	// the old keys are read with the key, before any open needs them
	if err := WriteKeys(PrevPath(path), old2); err != nil {
		t.Fatal(err)
	}
	early := NewWithFallback(path)
	if _, err := early.Key(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(PrevPath(path)); err != nil {
		t.Fatal(err)
	}
	if plain, err := early.Open(sealedOld); err != nil || plain != "sk-old" {
		t.Fatalf("not read with the key: %q %v", plain, err)
	}
}

func TestKeyFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.key.prev")
	a, b := mustFresh(t), mustFresh(t)
	if err := WriteKeys(path, a, b); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 || fi.Size() != 2*Size {
		t.Fatalf("file %v %v", fi, err)
	}
	keys, err := ReadKeys(path)
	if err != nil || len(keys) != 2 || !Has(keys, a) || !Has(keys, b) || Has(keys, mustFresh(t)) {
		t.Fatalf("keys %d %v", len(keys), err)
	}
	if _, err := ReadKey(path); err == nil {
		t.Fatal("ReadKey took two keys")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*.tmp-*")); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
	if err := WriteKeys(path, []byte("short")); err == nil {
		t.Fatal("wrote a short key")
	}
	if err := WriteKeys(path); err == nil {
		t.Fatal("wrote no key")
	}
	if keys, err := ReadKeys(path); err != nil || len(keys) != 2 {
		t.Fatalf("a failed write changed the file: %d %v", len(keys), err)
	}
	if Fingerprint(a) == Fingerprint(b) || Fingerprint(a) != Fingerprint(append([]byte{}, a...)) || len(Fingerprint(a)) != 32 {
		t.Fatal("fingerprints")
	}
}

func mustFresh(t *testing.T) []byte {
	t.Helper()
	k, err := Fresh()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// A derived key is the same for the same purpose, differs between purposes and from the box's own key, and survives
// a new Box on the same file.
func TestDerive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.key")
	b := New(path)
	a1, err := b.Derive("persona extension image v1", 32)
	if err != nil {
		t.Fatal(err)
	}
	a2, _ := New(path).Derive("persona extension image v1", 32)
	other, _ := b.Derive("something else", 32)
	key, _ := b.Key()
	if !bytes.Equal(a1, a2) || bytes.Equal(a1, other) || bytes.Equal(a1, key) || len(a1) != 32 {
		t.Fatalf("derived %x %x %x", a1, a2, other)
	}
	if _, err := b.Derive("", 32); err == nil {
		t.Fatal("no purpose taken")
	}
}
