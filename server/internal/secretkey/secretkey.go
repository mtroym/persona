// Package secretkey seals secrets kept in Postgres with a part's own secret.key (32 bytes, AES-256-GCM, base64 of
// nonce + ciphertext). The hub seals its own with the one in its data directory (peers' tokens, the computers'
// connector credentials); a manager with the one in its own directory (model providers' keys, extensions', connectors'
// and bots' secrets, mail passwords). The built-in manager's started as a copy of the data directory's; it rotates to
// a key of its own once its data is in its own database (internal/assistant keyrotate.go, docs/assistant/managers.md "Its own
// secret.key").
package secretkey

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
)

// Size is a key's length.
const Size = 32

// Box seals and opens with the key in one file, read (or made) on first use.
type Box struct {
	path string
	// fallback: Open also tries the keys in PrevPath(path) (NewWithFallback), and the ones it read from there before.
	fallback bool
	mu       sync.Mutex
	key      []byte
	old      [][]byte // the old keys read from PrevPath(path), kept for this process
}

// New is a Box on the key file at path; "" is a key made for this process only (tests).
func New(path string) *Box { return &Box{path: path} }

// NewWithFallback is New, and Open also opens what the keys in PrevPath(path) sealed: a rotation whose old key is
// still kept (keyrotate.go). It never seals with them. It reads them with the key, and again at each open the key
// itself does not open, and keeps what it read for this process even once the rotation deleted the file: a value this
// process read before and writes back unchanged still opens (and the rotation's watcher seals it again). A process
// started after the deletion never has them.
func NewWithFallback(path string) *Box { return &Box{path: path, fallback: true} }

// NextPath is the file beside a key file that holds a new key while a rotation re-seals the secrets with it, named
// after its fingerprint (each attempt has its own: one never overwrites another's); PrevPath the one that holds the
// keys it replaced (one or more, each Size bytes), kept only for reading until nothing sealed with them can be left.
func NextPath(path, fingerprint string) string {
	if len(fingerprint) > 16 {
		fingerprint = fingerprint[:16]
	}
	return path + ".next-" + fingerprint
}

// NextGlob matches every NextPath of path (and their temporary files, which have ".tmp-" in their name).
func NextGlob(path string) string { return path + ".next-*" }

// PrevPath: see NextPath.
func PrevPath(path string) string { return path + ".prev" }

// Key is the AES key: the file's, made when there is none (only one process gets to write it: the hub and the manager
// may make it at the same moment, and both then read the winner's).
func (b *Box) Key() ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.key != nil {
		return b.key, nil
	}
	fresh := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, fresh); err != nil {
		return nil, err
	}
	if b.path == "" {
		b.key = fresh
		return fresh, nil
	}
	key, err := os.ReadFile(b.path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(b.path), 0o700); err != nil { // a manager's own directory, made at first use
			return nil, err
		}
		f, err := os.OpenFile(b.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		switch {
		case err == nil:
			_, err = f.Write(fresh)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return nil, err
			}
			key = fresh
		case errors.Is(err, os.ErrExist):
			if key, err = os.ReadFile(b.path); err != nil {
				return nil, err
			}
		default:
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("%s must hold 32 bytes", b.path)
	}
	b.key = key
	if b.fallback {
		b.keepOld()
	}
	return key, nil
}

// keepOld adds the keys in PrevPath to the ones kept (b.mu held).
func (b *Box) keepOld() {
	keys, err := ReadKeys(PrevPath(b.path))
	if err != nil {
		return
	}
	for _, k := range keys {
		if !Has(b.old, k) {
			b.old = append(b.old, k)
		}
	}
}

// Seal is plain sealed.
func (b *Box) Seal(plain string) (string, error) {
	key, err := b.Key()
	if err != nil {
		return "", err
	}
	return SealWith(key, plain)
}

// Open is what Seal sealed; "" opens to "". With a fallback (NewWithFallback), what the key does not open is tried
// with the kept old keys; the key's own error is the one returned.
func (b *Box) Open(sealed string) (string, error) {
	key, err := b.Key()
	if err != nil {
		return "", err
	}
	plain, err := OpenWith(key, sealed)
	if err == nil || !b.fallback || b.path == "" {
		return plain, err
	}
	b.mu.Lock()
	b.keepOld()
	old := slices.Clone(b.old)
	b.mu.Unlock()
	for _, k := range old {
		if p, oerr := OpenWith(k, sealed); oerr == nil {
			return p, nil
		}
	}
	return "", err
}

// SealWith seals plain with key: base64 of a random nonce and the AES-256-GCM ciphertext.
func SealWith(key []byte, plain string) (string, error) {
	gcm, err := gcmOf(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte(plain), nil)), nil
}

// OpenWith opens what SealWith sealed with key; "" opens to "".
func OpenWith(key []byte, sealed string) (string, error) {
	if sealed == "" {
		return "", nil
	}
	gcm, err := gcmOf(key)
	if err != nil {
		return "", err
	}
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("sealed key too short")
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	return string(plain), err
}

func gcmOf(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Fingerprint names a key without telling it: HMAC-SHA256 of a fixed label keyed with it, the first 16 bytes in hex.
// The rotation records it in the database (sealing_key) to know which key the secrets there are sealed with.
func Fingerprint(key []byte) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("persona secret.key fingerprint v1"))
	return hex.EncodeToString(m.Sum(nil)[:16])
}

// Fresh is a new random key.
func Fresh() ([]byte, error) {
	k := make([]byte, Size)
	if _, err := io.ReadFull(rand.Reader, k); err != nil {
		return nil, err
	}
	return k, nil
}

// ReadKey reads a key file: exactly Size bytes.
func ReadKey(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(b) != Size {
		return nil, fmt.Errorf("%s must hold %d bytes", path, Size)
	}
	return b, nil
}

// ReadKeys reads a file of one or more keys (PrevPath): a whole number of Size bytes, at least one.
func ReadKeys(path string) ([][]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(b) == 0 || len(b)%Size != 0 {
		return nil, fmt.Errorf("%s must hold one or more keys of %d bytes", path, Size)
	}
	var out [][]byte
	for i := 0; i < len(b); i += Size {
		out = append(out, b[i:i+Size])
	}
	return out, nil
}

// Has is whether keys holds key.
func Has(keys [][]byte, key []byte) bool {
	for _, k := range keys {
		if bytes.Equal(k, key) {
			return true
		}
	}
	return false
}

// WriteKeys puts keys (each Size bytes) in path whole or not at all: a temporary file beside it (0600, synced),
// renamed over it, the directory synced.
func WriteKeys(path string, keys ...[]byte) error {
	var b []byte
	for _, k := range keys {
		if len(k) != Size {
			return fmt.Errorf("a key of %d bytes", len(k))
		}
		b = append(b, k...)
	}
	if len(b) == 0 {
		return errors.New("no key to write")
	}
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // gone after the rename; left by a failure
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return SyncDir(dir)
}

// SyncDir makes a rename or removal in dir durable (Windows has no directory sync: nothing to do there).
func SyncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// Derive is a key of n bytes for one purpose (info, such as "persona extension image v1"), derived from the box's key
// with HKDF-SHA256: nothing more is stored, it is the same across restarts and redeploys as long as the key file is,
// and it says nothing about the box's key or another purpose's.
func (b *Box) Derive(info string, n int) ([]byte, error) {
	if info == "" {
		return nil, errors.New("derive: say what the key is for")
	}
	key, err := b.Key()
	if err != nil {
		return nil, err
	}
	return hkdf.Key(sha256.New, key, nil, info, n)
}
