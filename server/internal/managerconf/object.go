package managerconf

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// Object is a JSON object that keeps its keys in order and every value as it was: an edit of manager.json changes the
// keys it means to and nothing else (fields this build does not know, written by a newer one or by hand, stay). The
// hub edits hub.json with it too (hub/admin_config.go).
type Object struct {
	keys []string
	vals map[string]json.RawMessage
}

// Keys are the object's keys, in order.
func (o *Object) Keys() []string { return append([]string(nil), o.keys...) }

// ParseObject reads a JSON object (empty input is an empty object).
func ParseObject(b []byte) (*Object, error) {
	o := &Object{vals: map[string]json.RawMessage{}}
	if len(bytes.TrimSpace(b)) == 0 {
		return o, nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errors.New("not a JSON object")
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, errors.New("bad object key")
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		if _, seen := o.vals[key]; !seen {
			o.keys = append(o.keys, key)
		}
		o.vals[key] = raw
	}
	if _, err := dec.Token(); err != nil { // the closing brace
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("trailing data after the JSON object")
	}
	return o, nil
}

// Get is key's value as it is in the file.
func (o *Object) Get(key string) (json.RawMessage, bool) {
	v, ok := o.vals[key]
	return v, ok
}

// SetRaw sets key to raw (added at the end when new).
func (o *Object) SetRaw(key string, raw json.RawMessage) {
	if _, ok := o.vals[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = raw
}

// Set sets key to v, marshalled without HTML escapes.
func (o *Object) Set(key string, v any) error {
	raw, err := MarshalNoEscape(v)
	if err != nil {
		return err
	}
	o.SetRaw(key, raw)
	return nil
}

// Del removes key.
func (o *Object) Del(key string) {
	if _, ok := o.vals[key]; !ok {
		return
	}
	delete(o.vals, key)
	for i, k := range o.keys {
		if k == key {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

// Object is the nested object under key (empty when missing or null); SetObject puts it back (an empty one removes
// key).
func (o *Object) Object(key string) (*Object, error) {
	raw, ok := o.vals[key]
	if !ok || string(bytes.TrimSpace(raw)) == "null" {
		return &Object{vals: map[string]json.RawMessage{}}, nil
	}
	nested, err := ParseObject(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", key, err)
	}
	return nested, nil
}

func (o *Object) SetObject(key string, nested *Object) {
	if len(nested.keys) == 0 {
		o.Del(key)
		return
	}
	o.SetRaw(key, nested.Marshal())
}

// Marshal is the object compact, keys in order.
func (o *Object) Marshal() []byte {
	var b bytes.Buffer
	b.WriteString("{")
	for i, k := range o.keys {
		if i > 0 {
			b.WriteString(",")
		}
		key, _ := MarshalNoEscape(k)
		b.Write(key)
		b.WriteString(":")
		b.Write(o.vals[k])
	}
	b.WriteString("}")
	return b.Bytes()
}

// Indented is the file's text: two-space indent, one trailing newline.
func (o *Object) Indented() ([]byte, error) {
	var compact, out bytes.Buffer
	if err := json.Compact(&compact, o.Marshal()); err != nil {
		return nil, err
	}
	if err := json.Indent(&out, compact.Bytes(), "", "  "); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

// MarshalNoEscape is json.Marshal without HTML escapes.
func MarshalNoEscape(v any) (json.RawMessage, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// fileMu serializes this process's edits of a configuration file; a lock on its directory does the same between
// processes. Not on the file itself: the rename replaces that file.
var fileMu sync.Mutex

// Edit reads the JSON object in path (missing: an empty object), lets edit change it and writes it back atomically: a
// temporary file in the same directory, fsync, rename over the old one, mode 0600; the directory is made 0700 when
// missing. A file that is not a JSON object is left alone (error).
func Edit(path string, edit func(o *Object) error) error {
	return editIf(path, func(o *Object) (bool, error) { return true, edit(o) })
}

// editIf is Edit that writes only when edit says the object changed. A path that is a link (to a file on a volume)
// is written where it leads: the link stays.
func editIf(path string, edit func(o *Object) (bool, error)) error {
	fileMu.Lock()
	defer fileMu.Unlock()
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	dir := filepath.Dir(path)
	if err := danglingLink(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	unlock, err := LockDir(dir)
	if err != nil {
		return err
	}
	defer unlock()
	b, err := readConfig(path)
	if err != nil {
		return err
	}
	o, err := ParseObject(b)
	if err != nil {
		return fmt.Errorf("%s: %w (not changed)", path, err)
	}
	changed, err := edit(o)
	if err != nil || !changed {
		return err
	}
	out, err := o.Indented()
	if err != nil {
		return err
	}
	return WriteFileAtomic(path, out, 0o600)
}

// ReadFile is the configuration file at path (manager.json, or the hub.json a manager reads), nil when there is none.
// A link to nothing (a volume not mounted yet), as the file or as a directory above it, is an error, not a missing
// file: read as none, a manager would start without its sections, and a write would replace the link.
func ReadFile(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, danglingLink(path)
	}
	return b, err
}

// readConfig is the manager.json at path as ReadFile reads it; one that is there but empty (cut short, emptied by a
// failed save) is an error too, not an empty configuration.
func readConfig(path string) ([]byte, error) {
	b, err := ReadFile(path)
	if err == nil && b != nil && len(bytes.TrimSpace(b)) == 0 {
		return nil, fmt.Errorf("%s is empty", path)
	}
	return b, err
}

// danglingLink says whether the nearest of path and the directories above it that is there is a link to nothing.
func danglingLink(path string) error {
	for p := filepath.Clean(path); ; p = filepath.Dir(p) {
		if st, err := os.Lstat(p); err == nil {
			if st.Mode()&os.ModeSymlink != 0 {
				if _, err := os.Stat(p); err != nil {
					return fmt.Errorf("%s is a link to nothing (%w)", p, err)
				}
			}
			return nil
		}
		if p == filepath.Dir(p) {
			return nil
		}
	}
}

// tempInfix is in the names of WriteFileAtomic's temporary files (path + tempInfix + a random part).
const tempInfix = ".tmp-"

// WriteFileAtomic writes path in one step (temporary file, fsync, rename), with perm.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	// Named after the file (not hidden): the node's path guard refuses the temporary copy of a secret by its name too.
	f, err := os.CreateTemp(dir, filepath.Base(path)+tempInfix+"*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
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
	ok = true
	// The rename itself must survive a power cut too.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}
