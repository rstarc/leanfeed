package filestore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// tmpPrefix marks files and directories that are being written. Startup
// deletes anything with this prefix.
const tmpPrefix = ".tmp-"

// writeFileAtomic replaces path with data: it writes .tmp-<name> in the same
// directory, syncs it, then renames it over path.
func writeFileAtomic(path string, data []byte) error {
	tmp := filepath.Join(filepath.Dir(path), tmpPrefix+filepath.Base(path))
	if err := writeFileSync(tmp, data); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// writeFileSync creates or truncates path, writes data and syncs it to disk.
func writeFileSync(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
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
	return f.Close()
}

func writeJSONAtomic(path string, v any) error {
	data, err := marshalJSON(v)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data)
}

func marshalJSON(v any) ([]byte, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// readDirClean lists dir after deleting leftover temp files and directories
// in it. A missing directory lists as empty.
func readDirClean(dir string) ([]os.DirEntry, error) {
	all, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var kept []os.DirEntry
	for _, e := range all {
		if strings.HasPrefix(e.Name(), tmpPrefix) {
			if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
				return nil, err
			}
			continue
		}
		kept = append(kept, e)
	}
	return kept, nil
}
