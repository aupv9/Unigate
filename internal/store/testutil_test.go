package store

import (
	"context"
	"net"
	"os/exec"
	"testing"
	"time"

	"github.com/aupv9/unigate/internal/config"
)

// startTestRedis launches a throwaway redis-server on an ephemeral port
// for the duration of the test. It skips the test if redis-server isn't
// installed, rather than failing CI environments that lack it.
func startTestRedis(t *testing.T) *Store {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}
	addr := lis.Addr().String()
	lis.Close()

	if _, err := exec.LookPath("redis-server"); err != nil {
		t.Skip("redis-server not found in PATH, skipping store integration tests")
	}

	cmd := exec.Command("redis-server", "--port", addr[len("127.0.0.1:"):], "--bind", "127.0.0.1", "--save", "", "--appendonly", "no")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start redis-server: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	s, err := New(config.RedisConfig{
		Addrs:        []string{addr},
		DialTimeout:  config.Duration(2 * time.Second),
		ReadTimeout:  config.Duration(2 * time.Second),
		WriteTimeout: config.Duration(2 * time.Second),
	})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		err := s.Ping(ctx)
		cancel()
		if err == nil {
			return s
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("redis-server did not become ready in time")
	return nil
}
