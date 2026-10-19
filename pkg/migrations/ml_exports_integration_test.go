//go:build integration

package migrations

import (
	"encoding/json"
	"testing"
)

func TestMLExportsAreMinimizedAndReaderHasNoBusinessTableAccess(t *testing.T) {
	ctx, db := newMigrationDatabase(t)
	applyMigrations(t, ctx, db)
	_, err := db.ExecContext(ctx, `
INSERT INTO users.user_account(id,email,password_hash) VALUES
('00000000-0000-4000-8000-000000000001','private@hse.ru','secret-hash');
INSERT INTO users.user_email(user_id,email) VALUES
('00000000-0000-4000-8000-000000000001','private@hse.ru');
UPDATE users.user_profile SET data='{"display_name":"Private name","skills":["python"],"open_to_collaboration":true,"contact_email":"secret@example.org","telegram":"private_handle"}';
CREATE ROLE ml_export_reader LOGIN;
GRANT USAGE ON SCHEMA users,projects TO ml_export_reader;
GRANT SELECT ON users.ml_profiles_v1,users.ml_blocks_v1,projects.ml_projects_v1,
 projects.ml_roles_v1,projects.ml_members_v1,projects.ml_requests_v1 TO ml_export_reader;`)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "SET ROLE ml_export_reader"); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(ctx, "RESET ROLE")
	var raw []byte
	if err := conn.QueryRowContext(ctx, "SELECT to_jsonb(v) FROM users.ml_profiles_v1 v").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"email", "password_hash", "display_name", "contact_email", "telegram"} {
		if _, exists := fields[forbidden]; exists {
			t.Fatalf("private column exported: %s", forbidden)
		}
	}
	if fields["confirmed"] != true || fields["has_name"] != true {
		t.Fatalf("invalid derived flags: %v", fields)
	}
	var allowed bool
	if err := conn.QueryRowContext(ctx, "SELECT has_table_privilege(current_user,'users.user_account','SELECT')").Scan(&allowed); err != nil || allowed {
		t.Fatalf("reader can access accounts: %t %v", allowed, err)
	}
}
