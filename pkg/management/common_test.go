package management

import (
	pb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/management/v1"
	"testing"
)

func TestValidation(t *testing.T) {
	for _, p := range []*pb.Page{{Limit: 101}, {Limit: -1}, {Offset: -1}, {Offset: 10001}} {
		if _, _, e := Page(p); e == nil {
			t.Fatal("unsafe page", p)
		}
	}
	if l, o, e := Page(nil); e != nil || l != 20 || o != 0 {
		t.Fatal(l, o, e)
	}
	for _, q := range []*pb.Query{{CreatedFromUnix: 20, CreatedToUnix: 10}, {CreatedToUnix: 9223372036854775807}, {UserId: "invalid"}, {TargetUserId: "invalid"}} {
		if _, _, e := Query(q); e == nil {
			t.Fatal("bad filter", q)
		}
	}
	if e := AllowedQuery(&pb.Query{Status: "active", Kind: "ignored"}, "status"); e == nil {
		t.Fatal("silently ignored filter")
	}
	for _, q := range []*pb.StatisticsQuery{{FromUnix: 20, ToUnix: 10}, {ToUnix: 9223372036854775807}, {FromUnix: 1, ToUnix: 40000000}} {
		if _, _, e := Range(q); e == nil {
			t.Fatal("bad statistics interval", q)
		}
	}
}
