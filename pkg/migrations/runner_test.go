package migrations

import (
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

func TestChecksumsRequireTransactionalMigrations(t *testing.T) {
	for _, sql := range []string{"-- +goose Up\nSELECT 1;", "-- +goose NO TRANSACTION\n-- +goose Up\nSELECT 1;\n-- +goose Down\nSELECT 1;"} {
		files := fstest.MapFS{"00001_test.sql": {Data: []byte(sql)}}
		hashes, err := migrationHashes(files)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := withChecksums(files, hashes); err == nil {
			t.Fatal("unsafe migration accepted")
		}
	}
}
func TestFingerprintTracksOriginalSQLAndChanges(t *testing.T) {
	sql := "-- +goose Up\nCREATE TABLE test(id int);\n-- +goose Down\nDROP TABLE test;"
	files := fstest.MapFS{"00001_test.sql": {Data: []byte(sql)}}
	hashes, err := migrationHashes(files)
	if err != nil {
		t.Fatal(err)
	}
	checked, err := withChecksums(files, hashes)
	if err != nil {
		t.Fatal(err)
	}
	data, err := fs.ReadFile(checked, "00001_test.sql")
	if err != nil {
		t.Fatal(err)
	}
	insert := strings.Index(string(data), "INSERT INTO users.migration_checksum")
	down := strings.Index(string(data), "-- +goose Down")
	if insert < 0 || insert > down || !strings.Contains(string(data), hashes[1]) {
		t.Fatal("fingerprint must be committed within Up")
	}
	files["00001_test.sql"].Data = []byte(sql + "\n-- changed")
	changed, err := migrationHashes(files)
	if err != nil {
		t.Fatal(err)
	}
	if hashes[1] == changed[1] {
		t.Fatal("SQL change did not change fingerprint")
	}
}
