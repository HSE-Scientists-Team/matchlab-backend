package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

func readOpenAPI(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "api", "http", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestOpenAPIEnumsAndReferences(t *testing.T) {
	doc := readOpenAPI(t)
	definitions := doc["components"].(map[string]any)["schemas"].(map[string]any)
	var catalog map[string]enumEntry
	if err := json.Unmarshal(enumCatalogJSON, &catalog); err != nil {
		t.Fatal(err)
	}
	for name, entry := range catalog {
		definition, ok := definitions[name].(map[string]any)
		if !ok {
			t.Fatalf("missing enum %s", name)
		}
		values := definition["enum"].([]any)
		if len(values) != len(entry.Values) {
			t.Fatalf("%s: stale enum", name)
		}
		seen := map[string]bool{}
		for i, item := range entry.Values {
			if item.Value == "" || item.Description == "" || seen[item.Value] {
				t.Fatalf("%s: invalid or duplicate enum entry", name)
			}
			seen[item.Value] = true
			if values[i] != item.Value || !strings.Contains(definition["description"].(string), item.Description) {
				t.Fatalf("%s: missing value or description for %s", name, item.Value)
			}
		}
	}
	var visit func(any)
	visit = func(node any) {
		switch v := node.(type) {
		case map[string]any:
			if ref, ok := v["$ref"].(string); ok {
				var target any = doc
				for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
					mapping, ok := target.(map[string]any)
					if !ok {
						t.Fatalf("unresolved reference %s", ref)
					}
					target, ok = mapping[part]
					if !ok {
						t.Fatalf("unresolved reference %s", ref)
					}
				}
			}
			for _, item := range v {
				visit(item)
			}
		case []any:
			for _, item := range v {
				visit(item)
			}
		}
	}
	visit(doc)
}

// Database constraints are independent of the API catalog. Compare complete
// sets so an added or removed persisted state cannot silently go undocumented.
func TestEnumsMatchSQLConstraints(t *testing.T) {
	var catalog map[string]enumEntry
	if err := json.Unmarshal(enumCatalogJSON, &catalog); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ file, pattern, name string }{
		{"00006_create_projects.sql", `CHECK\(status IN \(([^)]+)\)\)`, "ProjectStatus"},
		{"00006_create_projects.sql", `CHECK\(status IN \(([^)]+)\)`, "ProjectMemberStatus"},
		{"00006_create_projects.sql", `CHECK\(type IN \(([^)]+)\)`, "ProjectJoinType"},
		{"00006_create_projects.sql", `CHECK\(status IN \(([^)]+)\)`, "ProjectJoinStatus"},
		{"00005_create_profiles.sql", `CHECK\s*\(kind IN \(([^)]+)\)`, "AchievementKind"},
		{"00005_create_profiles.sql", `CHECK\s*\(status IN \(([^)]+)\)`, "AchievementStatus"},
	}
	// Select successive status CHECKs for the three distinct project tables.
	indexes := map[string]int{"ProjectMemberStatus": 1, "ProjectJoinStatus": 2}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("..", "..", "pkg", "migrations", c.file))
			if err != nil {
				t.Fatal(err)
			}
			matches := regexp.MustCompile(c.pattern).FindAllStringSubmatch(string(data), -1)
			index := indexes[c.name]
			if len(matches) <= index {
				t.Fatal("SQL constraint not found")
			}
			var persisted, documented []string
			for _, m := range regexp.MustCompile(`'([^']+)'`).FindAllStringSubmatch(matches[index][1], -1) {
				persisted = append(persisted, m[1])
			}
			for _, v := range catalog[c.name].Values {
				documented = append(documented, v.Value)
			}
			sort.Strings(persisted)
			sort.Strings(documented)
			if !reflect.DeepEqual(persisted, documented) {
				t.Fatalf("SQL %v != API %v", persisted, documented)
			}
		})
	}
}

func TestFilterAndMutationDomains(t *testing.T) {
	doc := readOpenAPI(t)
	definitions := doc["components"].(map[string]any)["schemas"].(map[string]any)
	has := func(name, value string) bool {
		for _, v := range definitions[name].(map[string]any)["enum"].([]any) {
			if v == value {
				return true
			}
		}
		return false
	}
	if has("TicketStatus", "closed") || !has("TicketStatusFilterOptionalFilter", "closed") || !has("TicketStatusFilterOptionalFilter", "") {
		t.Fatal("ticket filter and response domains must differ")
	}
	if has("AccountStatusChange", "deleted") || !has("AccountStatus", "deleted") {
		t.Fatal("deletion is not a block/unblock command")
	}
	paths := doc["paths"].(map[string]any)
	for path, expected := range map[string]string{
		"/api/v1/admin/tickets":      "TicketStatusFilterOptionalFilter",
		"/api/v1/admin/users":        "AccountStatusOptionalFilter",
		"/api/v1/admin/achievements": "AchievementStatusOptionalFilter",
		"/api/v1/projects":           "ProjectStatusOptionalFilter",
	} {
		operation := paths[path].(map[string]any)["get"].(map[string]any)
		found := false
		for _, item := range operation["parameters"].([]any) {
			p := item.(map[string]any)
			if p["name"] == "status" {
				found = p["schema"].(map[string]any)["$ref"] == "#/components/schemas/"+expected
			}
		}
		if !found {
			t.Errorf("%s: wrong status filter domain", path)
		}
	}
}

func TestEnumFieldBindings(t *testing.T) {
	registerEnums()
	for name, enum := range fieldEnums {
		descriptor, err := protoregistry.GlobalFiles.FindDescriptorByName(name)
		if err != nil {
			t.Fatalf("enum binding %s: %v", name, err)
		}
		field, ok := descriptor.(protoreflect.FieldDescriptor)
		if !ok || field.Kind() != protoreflect.StringKind {
			t.Fatalf("%s is not a string field", name)
		}
		if _, ok := schemas[enum]; !ok {
			t.Fatalf("%s references unknown enum %s", name, enum)
		}
	}
}
