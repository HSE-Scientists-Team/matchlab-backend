// Generate an OpenAPI fragment from the Admin protobuf descriptors and explicit HTTP routes.
package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	pb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/admin/v1"
	"go.yaml.in/yaml/v3"
	"google.golang.org/protobuf/reflect/protoreflect"
	"os"
	"sort"
	"strings"
)

func name(d protoreflect.MessageDescriptor) string {
	return "Admin_" + strings.ReplaceAll(string(d.FullName()), ".", "_")
}

var schemas = map[string]any{}

//go:embed string-enums.json
var enumCatalogJSON []byte

type enumEntry struct {
	Description string `json:"description"`
	Values      []struct {
		Value       string `json:"value"`
		Description string `json:"description"`
	} `json:"values"`
}

func enumRef(name string) map[string]any {
	return map[string]any{"$ref": "#/components/schemas/" + name}
}

// String fields keep their existing JSON representation. Only closed business
// domains get enum schemas; generic audit actions and free text stay strings.
var fieldEnums = map[protoreflect.FullName]string{
	"matchlab.project.v1.Project.status":          "ProjectStatus",
	"matchlab.project.v1.ProjectInput.type":       "ProjectType",
	"matchlab.project.v1.Member.status":           "ProjectMemberStatus",
	"matchlab.project.v1.JoinRequest.type":        "ProjectJoinType",
	"matchlab.project.v1.JoinRequest.status":      "ProjectJoinStatus",
	"matchlab.user.v1.Achievement.status":         "AchievementStatus",
	"matchlab.user.v1.AchievementInput.kind":      "AchievementKind",
	"matchlab.user.v1.ProfileInput.project_types": "ProjectType",
	"matchlab.user.v1.ManagedUser.status":         "AccountStatus",
	"matchlab.user.v1.UserStatusChange.status":    "AccountStatusChange",
	"matchlab.support.v1.Ticket.status":           "TicketStatus",
	"matchlab.support.v1.TicketChange.action":     "TicketAction",
	"matchlab.admin.v1.StaffAccount.role":         "StaffRole",
	"matchlab.admin.v1.StaffInput.role":           "StaffRole",
	"matchlab.admin.v1.StaffUpdate.role":          "StaffRole",
	"matchlab.management.v1.Event.result":         "AuditResult",
}

func queryEnum(method, field string) string {
	if field == "status" {
		switch method {
		case "ListStaff", "ListDomains":
			return "ActiveStatusFilterOptionalFilter"
		case "ListUsers":
			return "AccountStatusOptionalFilter"
		case "ListAchievements":
			return "AchievementStatusOptionalFilter"
		case "ListTickets":
			return "TicketStatusFilterOptionalFilter"
		case "ListProjects":
			return "ProjectStatusOptionalFilter"
		}
	}
	if method == "ListAchievements" && field == "kind" {
		return "AchievementKindOptionalFilter"
	}
	if method == "ListAudit" && field == "result" {
		return "AuditResultOptionalFilter"
	}
	return ""
}

func registerEnums() {
	var catalog map[string]enumEntry
	if err := json.Unmarshal(enumCatalogJSON, &catalog); err != nil {
		panic(err)
	}
	filters := map[string]bool{}
	for _, name := range []string{"ProjectType", "ProjectStatus", "ProjectMineFilter", "ProjectDurationFilter", "ProjectJoinStatus", "AchievementKind", "ActiveStatusFilter", "AccountStatus", "AchievementStatus", "TicketStatusFilter", "AuditResult"} {
		filters[name] = true
	}
	for name, entry := range catalog {
		values := []string{}
		descriptions := []string{}
		description := entry.Description + "\n\n"
		for _, item := range entry.Values {
			values = append(values, item.Value)
			descriptions = append(descriptions, item.Description)
			description += "- `" + item.Value + "` — " + item.Description + "\n"
		}
		schemas[name] = map[string]any{"type": "string", "enum": values, "description": description, "x-enum-descriptions": descriptions}
		if filters[name] {
			schemas[name+"OptionalFilter"] = map[string]any{"type": "string", "enum": append([]string{""}, values...), "description": description + "\nПараметр можно не передавать. Пустая строка также отключает этот фильтр.", "x-enum-descriptions": append([]string{"Без фильтра."}, descriptions...)}
		}
	}
	schemas["CreatedComplaintStatus"] = map[string]any{"type": "string", "enum": []string{"open"}, "description": "Только что созданная жалоба открыта и ожидает обработки (`open`)."}
}

func schema(d protoreflect.MessageDescriptor) map[string]any {
	n := name(d)
	if _, ok := schemas[n]; ok {
		return map[string]any{"$ref": "#/components/schemas/" + n}
	}
	properties := map[string]any{}
	schemas[n] = map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	for i := 0; i < d.Fields().Len(); i++ {
		f := d.Fields().Get(i)
		v := value(f)
		if f.IsList() {
			v = map[string]any{"type": "array", "items": v}
		}
		properties[string(f.Name())] = v
	}
	return map[string]any{"$ref": "#/components/schemas/" + n}
}
func value(f protoreflect.FieldDescriptor) map[string]any {
	// Shared protobuf messages serve several resources. Their field domains
	// depend on the route, so document that rather than inventing a global enum.
	descriptions := map[protoreflect.FullName]string{
		"matchlab.user.v1.AchievementInput.url":        "Необязательная ссылка HTTP/HTTPS на публикацию, сертификат или страницу проекта; пустая строка означает отсутствие ссылки. Маршруты /verify-email и /api/v1/auth/email/confirm не допускаются: они подтверждают почту, а не достижение. Для отправки на проверку нужна допустимая ссылка или приватный подтверждающий файл.",
		"matchlab.management.v1.Query.status":          "Значения зависят от метода; см. enum query-параметра status конкретного HTTP-маршрута.",
		"matchlab.management.v1.Query.kind":            "Для очереди достижений: education, certificate, scientific, project; см. enum query-параметра kind.",
		"matchlab.management.v1.Event.action":          "Имя операции в истории конкретного ресурса или общем аудите. Набор отличается от команд TicketAction; например, domain.save или session.login.",
		"matchlab.management.v1.Event.status":          "Состояние после операции; формат зависит от ресурса. История доменов может содержать домен и флаг активности; общий аудит может оставлять поле пустым.",
		"matchlab.management.v1.Event.previous_status": "Состояние до операции в том же формате, что status; может быть пустым, например при создании ресурса.",
	}
	if description := descriptions[f.FullName()]; description != "" {
		return map[string]any{"type": "string", "description": description}
	}
	if n := fieldEnums[f.FullName()]; n != "" {
		return enumRef(n)
	}
	switch f.Kind() {
	case protoreflect.MessageKind:
		return schema(f.Message())
	case protoreflect.BoolKind:
		return map[string]any{"type": "boolean"}
	case protoreflect.Int32Kind, protoreflect.Uint32Kind:
		return map[string]any{"type": "integer", "format": "int32"}
	case protoreflect.Int64Kind, protoreflect.Uint64Kind:
		return map[string]any{"type": "string", "pattern": "^-?[0-9]+$", "description": "64-bit integer encoded as a string by ProtoJSON."}
	case protoreflect.DoubleKind, protoreflect.FloatKind:
		return map[string]any{"type": "number"}
	case protoreflect.EnumKind:
		values := []string{}
		for i := 0; i < f.Enum().Values().Len(); i++ {
			values = append(values, string(f.Enum().Values().Get(i).Name()))
		}
		return map[string]any{"type": "string", "enum": values}
	default:
		return map[string]any{"type": "string"}
	}
}
func main() {
	write := flag.Bool("write", false, "update generated OpenAPI sections")
	check := flag.Bool("check", false, "verify generated sections")
	flag.Parse()
	registerEnums()
	raw, e := os.ReadFile("scripts/api/admin-routes.json")
	if e != nil {
		panic(e)
	}
	var routes [][]json.RawMessage
	if e = json.Unmarshal(raw, &routes); e != nil {
		panic(e)
	}
	methods := pb.File_api_proto_admin_v1_admin_proto.Services().ByName("AdminService").Methods()
	paths := map[string]any{}
	for _, r := range routes {
		var verb, path, method string
		var body bool
		var code int
		var bindings map[string]string
		json.Unmarshal(r[0], &verb)
		json.Unmarshal(r[1], &path)
		json.Unmarshal(r[3], &method)
		json.Unmarshal(r[4], &body)
		json.Unmarshal(r[5], &code)
		json.Unmarshal(r[6], &bindings)
		descriptor := methods.ByName(protoreflect.Name(method))
		if descriptor == nil {
			panic(method)
		}
		input, output := descriptor.Input(), descriptor.Output()
		schema(input)
		schema(output)
		allowedQuery := map[string]bool{}
		if len(r) > 7 {
			var names []string
			json.Unmarshal(r[7], &names)
			for _, n := range names {
				allowedQuery[n] = true
			}
		}
		parameters := []any{}
		pathFields := make([]string, 0, len(bindings))
		for key := range bindings {
			pathFields = append(pathFields, key)
		}
		sort.Strings(pathFields)
		for _, pathField := range pathFields {
			parameters = append(parameters, map[string]any{"name": pathField, "in": "path", "required": true, "schema": map[string]any{"type": "string", "format": "uuid"}})
		}
		if !body {
			for i := 0; i < input.Fields().Len(); i++ {
				f := input.Fields().Get(i)
				if _, exists := bindings[string(f.Name())]; exists {
					continue
				}
				bound := false
				for _, v := range bindings {
					if v == string(f.Name()) {
						bound = true
					}
				}
				if bound {
					continue
				}
				if f.Name() == "page" {
					for _, p := range []string{"limit", "offset"} {
						if !allowedQuery[p] {
							continue
						}
						parameters = append(parameters, map[string]any{"name": p, "in": "query", "schema": map[string]any{"type": "integer", "minimum": 0}, "description": "limit: 1–100 (default 20); offset: 0–10000. Stable ordering, total returned separately."})
					}
					continue
				}
				if f.Kind() == protoreflect.MessageKind || !allowedQuery[string(f.Name())] {
					continue
				}
				v := value(f)
				if n := queryEnum(method, string(f.Name())); n != "" {
					v = enumRef(n)
				}
				if f.Kind() == protoreflect.Int64Kind {
					v = map[string]any{"type": "integer", "format": "int64", "minimum": 0, "description": "Unix seconds in UTC; upper bound is exclusive."}
				}
				parameters = append(parameters, map[string]any{"name": string(f.Name()), "in": "query", "schema": v})
			}
		}
		responses := map[string]any{}
		if code == 204 {
			responses[fmt.Sprint(code)] = map[string]any{"description": "Success; password changes invalidate existing staff sessions."}
		} else {
			responses[fmt.Sprint(code)] = map[string]any{"description": "Success", "content": map[string]any{"application/json": map[string]any{"schema": schema(output)}}}
		}
		for _, code := range []string{"400", "401", "403", "404", "409", "429", "500", "503", "504"} {
			responses[code] = map[string]any{"description": map[string]string{"400": "Invalid input, pagination or filter", "401": "Missing/expired staff session or invalid login", "403": "Insufficient staff permissions", "404": "Resource not found", "409": "Stale revision or invalid state transition", "429": "Login attempt limit reached", "500": "Internal failure", "503": "Dependency unavailable; inspect state before retrying mutations", "504": "Request timed out; inspect state before retrying mutations"}[code], "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/Error"}}}}
		}
		operation := map[string]any{"tags": []string{"Администрирование"}, "summary": method, "operationId": "admin" + method + strings.ReplaceAll(verb, " ", ""), "parameters": parameters, "responses": responses, "security": []any{map[string]any{"AdminSession": []string{}}}}
		if method == "SaveDomain" {
			operation["operationId"] = "adminSaveDomain" + verb
		}
		if method == "Login" {
			operation["security"] = []any{}
			operation["description"] = "Separate staff login, no public registration. 12–72 byte passwords; five failures per normalized email per 15 minutes. Staff tokens expire after eight hours and are incompatible with user sessions."
		}
		if body {
			props := map[string]any{}
			inputProperties := schemas[name(input)].(map[string]any)["properties"].(map[string]any)
			for k, v := range inputProperties {
				exclude := false
				for _, field := range bindings {
					if field == k {
						exclude = true
					}
				}
				if !exclude {
					props[k] = v
				}
			}
			operation["requestBody"] = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "object", "additionalProperties": false, "properties": props}}}}
		}

		if body {
			required := map[string][]string{
				"LoginInput": {"email", "password"}, "PasswordChange": {"current_password", "new_password"},
				"StaffInput": {"email", "display_name", "role", "password"}, "StaffUpdate": {"display_name", "role", "active", "expected_revision", "reason"},
				"PasswordReset": {"password", "reason", "expected_revision"}, "TicketChange": {"action", "expected_revision"}, "TicketNote": {"comment", "expected_revision"},
				"UserStatusChange": {"status", "reason", "expected_revision"}, "DomainInput": {"domain", "organization_name", "is_active", "reason"},
				"VerificationDecision": {"approve", "expected_revision"}, "ModerationInput": {"hide", "reason", "expected_revision"},
			}[string(input.Name())]
			if method == "SaveDomain" && verb == "PUT" {
				required = append(required, "expected_revision")
			}
			operation["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)["required"] = required
		}
		if method == "CreateStaff" || method == "UpdateStaff" || method == "ListStaff" || method == "GetStaff" || method == "ResetStaffPassword" || method == "RevokeStaffSessions" || method == "SaveDomain" {
			operation["description"] = "Requires administrator role; other moderation operations also accept moderator."
		}
		if method == "UpdateTicket" {
			operation["description"] = "Actions: assign/unassign/resolve/decline/reopen. Closing and reopening require a non-empty comment. expected_revision protects concurrent changes (409)."
		}
		if method == "ReviewAchievement" {
			operation["description"] = "Approve pending evidence or return it for additional proof (needs_evidence). Rejection requires a comment. A staff email cannot review its own public achievement."
		}
		if method == "GetStatistics" {
			operation["description"] = "Current distributions and UTC daily activity for [from_unix,to_unix). Default last 30 days; maximum 366 days. Missing sources are errors, never zero counts."
		}

		key := "/api/v1/admin" + path
		entry, ok := paths[key].(map[string]any)
		if !ok {
			entry = map[string]any{}
			paths[key] = entry
		}
		entry[strings.ToLower(verb)] = operation
	}

	if *write || *check {
		file := "api/http/openapi.yaml"
		data, e := os.ReadFile(file)
		if e != nil {
			panic(e)
		}
		source := string(data)
		for _, section := range []struct {
			label  string
			indent int
			value  any
		}{{"PATHS", 2, paths}, {"SCHEMAS", 4, schemas}} {
			marker := strings.Repeat(" ", section.indent) + "# BEGIN GENERATED ADMIN " + section.label + "\n"
			endMarker := strings.Repeat(" ", section.indent) + "# END GENERATED ADMIN " + section.label + "\n"
			start := strings.Index(source, marker)
			if start < 0 {
				panic("missing marker " + section.label)
			}
			end := strings.Index(source[start:], endMarker)
			if end < 0 {
				panic("missing end marker")
			}
			end += start + len(endMarker)
			var buffer bytes.Buffer
			enc := yaml.NewEncoder(&buffer)
			enc.SetIndent(2)
			if e = enc.Encode(section.value); e != nil {
				panic(e)
			}
			enc.Close()
			var replacement strings.Builder
			replacement.WriteString(marker)
			for _, line := range strings.Split(strings.TrimSuffix(buffer.String(), "\n"), "\n") {
				if line == "" {
					replacement.WriteString("\n")
				} else {
					replacement.WriteString(strings.Repeat(" ", section.indent) + line + "\n")
				}
			}
			replacement.WriteString(endMarker)
			source = source[:start] + replacement.String() + source[end:]
		}
		if *check {
			if source != string(data) {
				panic("Admin OpenAPI is stale; run go run ./scripts/api/admin-openapi.go --write")
			}
		} else if e = os.WriteFile(file, []byte(source), 0644); e != nil {
			panic(e)
		}
		return
	}

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if e = encoder.Encode(map[string]any{"paths": paths, "schemas": schemas}); e != nil {
		panic(e)
	}
}
