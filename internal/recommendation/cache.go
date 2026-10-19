package recommendation

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

const cachePrefix = "matchlab:recommendations:v1"

// Match contains IDs and public matching factors, never cached profile cards.
type Match struct {
	ID       string   `json:"id"`
	Score    float64  `json:"score"`
	Factors  []string `json:"factors"`
	RoleID   string   `json:"role_id"`
	Fallback bool     `json:"fallback"`
	Model    string   `json:"model"`
}

type Ranking struct {
	SchemaVersion int     `json:"schema_version"`
	RunID         string  `json:"run_id"`
	Model         string  `json:"model"`
	GeneratedAt   string  `json:"generated_at"`
	Items         []Match `json:"items"`
}

type Cache interface {
	Load(context.Context, string, string) (*Ranking, error)
}

type RedisCache struct{ Client *redis.Client }

func (c *RedisCache) Load(ctx context.Context, kind, viewer string) (*Ranking, error) {
	call, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	run, err := c.Client.Get(call, cachePrefix+":active").Result()
	if err != nil {
		return nil, err
	}
	if len(run) == 0 || len(run) > 100 {
		return nil, fmt.Errorf("invalid recommendation generation")
	}
	raw, err := c.Client.Get(call, cachePrefix+":"+run+":"+kind+":"+viewer).Bytes()
	if err != nil {
		return nil, err
	}
	return decodeRanking(raw, run, time.Now())
}

func decodeRanking(raw []byte, run string, now time.Time) (*Ranking, error) {
	if len(raw) > 1024*1024 {
		return nil, fmt.Errorf("recommendation cache entry too large")
	}
	var result Ranking
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	generated, err := time.Parse(time.RFC3339Nano, result.GeneratedAt)
	if err != nil || now.Sub(generated) > 36*time.Hour || generated.After(now.Add(10*time.Minute)) ||
		result.SchemaVersion != 1 || result.RunID != run || len(result.Items) > 500 {
		return nil, fmt.Errorf("invalid or stale recommendation generation")
	}
	seen := map[string]bool{}
	for _, item := range result.Items {
		id, err := uuid.Parse(item.ID)
		if err != nil || id == uuid.Nil || id.String() != item.ID || seen[item.ID] ||
			math.IsNaN(item.Score) || math.IsInf(item.Score, 0) || item.Score < 0 || item.Score > 1 || len(item.Factors) > 20 {
			return nil, fmt.Errorf("invalid recommendation cache item")
		}
		seen[item.ID] = true
		for _, factor := range item.Factors {
			if len(factor) > 2048 {
				return nil, fmt.Errorf("recommendation explanation too large")
			}
		}
	}
	return &result, nil
}
