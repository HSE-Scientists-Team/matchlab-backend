package http

import (
	"strings"
	"testing"

	upb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/user/v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestRecommendationEnvelopePreservesCardsAndAddsProvenance(t *testing.T) {
	value := &upb.PeopleList{Items: []*upb.Person{{UserId: "person", DisplayName: "Имя"}}, Total: 9007199254740993}
	message, err := recommendationEnvelope(value, metadata.Pairs("matchlab-recommendations-bin",
		`{"schema_version":1,"model":"baseline-v1","items":[{"id":"person","score":0.7,"factors":["shared_skills:python"],"fallback":false}]}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := protojson.Marshal(message)
	if err != nil || !strings.Contains(string(raw), `"9007199254740993"`) || !strings.Contains(string(raw), "shared_skills:python") || !strings.Contains(string(raw), "Имя") {
		t.Fatalf("changed protobuf payload: %s %v", raw, err)
	}
	legacy, err := recommendationEnvelope(value, nil)
	if err != nil || legacy != value {
		t.Fatal("broke compatibility with older recommendation server")
	}
	if _, err := recommendationEnvelope(value, metadata.Pairs("matchlab-recommendations-bin", "not-json")); err == nil {
		t.Fatal("malformed provenance silently accepted")
	}
}
