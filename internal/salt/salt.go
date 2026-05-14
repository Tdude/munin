package salt

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/Tdude/muntra/internal/store"
	"github.com/redis/go-redis/v9"
)

const (
	redisKeyPrefix = "muntra:daily_salt:"
	rotationTTL    = 25 * time.Hour
	checkInterval  = 1 * time.Minute
)

type Service struct {
	redis *store.Redis
	mu    sync.RWMutex
	value string
	day   string
}

func New(r *store.Redis) *Service {
	return &Service{redis: r}
}

func (s *Service) Run(ctx context.Context) {
	if err := s.refresh(ctx); err != nil {
		slog.Error("salt: initial refresh failed", "err", err)
	}

	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			today := time.Now().UTC().Format("2006-01-02")
			s.mu.RLock()
			stale := s.day != today
			s.mu.RUnlock()
			if stale {
				if err := s.refresh(ctx); err != nil {
					slog.Error("salt: refresh failed", "err", err)
				}
			}
		}
	}
}

// Current returns the salt value valid for today (UTC) and the day string.
// Returns ("", "") before the first successful refresh.
func (s *Service) Current() (value string, day string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.value, s.day
}

func (s *Service) refresh(ctx context.Context) error {
	today := time.Now().UTC().Format("2006-01-02")
	key := redisKeyPrefix + today

	val, err := s.redis.Client().Get(ctx, key).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return err
	}
	if val == "" {
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			return err
		}
		fresh := hex.EncodeToString(buf)
		ok, err := s.redis.Client().SetNX(ctx, key, fresh, rotationTTL).Result()
		if err != nil {
			return err
		}
		if ok {
			val = fresh
		} else {
			// Lost the race against another instance; read the winner's value.
			val, err = s.redis.Client().Get(ctx, key).Result()
			if err != nil {
				return err
			}
		}
	}

	s.mu.Lock()
	s.value = val
	s.day = today
	s.mu.Unlock()
	slog.Info("salt: rotated", "day", today)
	return nil
}
