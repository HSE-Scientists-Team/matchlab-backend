package migrations

import (
	"bytes"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var noTransaction = regexp.MustCompile(`(?m)^\s*--\s*\+goose\s+NO TRANSACTION\s*$`)
var downDirective = regexp.MustCompile(`(?m)^[ \t]*--[ \t]*\+goose[ \t]+Down[ \t]*\r?$`)

// Добавляем отпечаток к Up в той же транзакции, что SQL и история Goose.
// Даже аварийное завершение между миграциями не теряет историю отпечатков.
func withChecksums(source fs.FS, hashes map[int64]string) (fs.FS, error) {
	names, err := fs.Glob(source, "*.sql")
	if err != nil {
		return nil, err
	}
	overlay := &checksumFS{FS: source, data: make(map[string][]byte, len(names))}
	for _, name := range names {
		data, err := fs.ReadFile(source, name)
		if err != nil {
			return nil, err
		}
		sql := string(data)
		if noTransaction.MatchString(sql) {
			return nil, fmt.Errorf("миграция %s должна выполняться в транзакции", name)
		}
		down := downDirective.FindStringIndex(sql)
		if down == nil {
			return nil, fmt.Errorf("миграция %s должна иметь явный Down", name)
		}
		split := down[0]
		prefix, _, _ := strings.Cut(name, "_")
		version, _ := strconv.ParseInt(prefix, 10, 64)
		insert := fmt.Sprintf("\nINSERT INTO users.migration_checksum (version,sha256) VALUES (%d,'%s') ON CONFLICT (version) DO NOTHING;\n\n", version, hashes[version])
		overlay.data[name] = []byte(sql[:split] + insert + sql[split:])
	}
	return overlay, nil
}

type checksumFS struct {
	fs.FS
	data map[string][]byte
}

func (f *checksumFS) Open(name string) (fs.File, error) {
	if data, ok := f.data[name]; ok {
		return &memoryFile{Reader: bytes.NewReader(data), name: path.Base(name), size: int64(len(data))}, nil
	}
	return f.FS.Open(name)
}

type memoryFile struct {
	*bytes.Reader
	name string
	size int64
}

func (f *memoryFile) Close() error               { return nil }
func (f *memoryFile) Stat() (fs.FileInfo, error) { return fileInfo{f.name, f.size}, nil }

type fileInfo struct {
	name string
	size int64
}

func (f fileInfo) Name() string       { return f.name }
func (f fileInfo) Size() int64        { return f.size }
func (f fileInfo) Mode() fs.FileMode  { return 0444 }
func (f fileInfo) ModTime() time.Time { return time.Time{} }
func (f fileInfo) IsDir() bool        { return false }
func (f fileInfo) Sys() any           { return nil }
