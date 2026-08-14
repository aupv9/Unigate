package store

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aupv9/unigate/internal/config"
)

// startRealRedisCluster spins up a real 3-master Redis Cluster
// (redis-server --cluster-enabled yes on 3 ports, wired together via
// `redis-cli --cluster create`) so cluster-mode tests exercise actual
// slot sharding and MOVED/CROSSSLOT behavior - not just a single node
// with cluster_mode:true set, which wouldn't catch a design that
// happens to send multi-key Lua calls across slots (NFR3, NFR4).
//
// Skips the test if redis-server/redis-cli aren't installed, or if a
// free port block can't be found (Cluster requires port and port+10000
// for the cluster bus, so a plain net.Listen probe on port alone can
// still race with the bus port; failures here are surfaced as a
// skip/fatal rather than a flaky pass).
func startRealRedisCluster(t *testing.T, numNodes int) []string {
	t.Helper()

	if _, err := exec.LookPath("redis-server"); err != nil {
		t.Skip("redis-server not found in PATH, skipping real cluster tests")
	}
	if _, err := exec.LookPath("redis-cli"); err != nil {
		t.Skip("redis-cli not found in PATH, skipping real cluster tests")
	}

	ports := make([]int, numNodes)
	addrs := make([]string, numNodes)
	for i := 0; i < numNodes; i++ {
		port := findFreePort(t)
		ports[i] = port
		addrs[i] = fmt.Sprintf("127.0.0.1:%d", port)
	}

	for i, port := range ports {
		dir := t.TempDir()
		cmd := exec.Command("redis-server",
			"--port", strconv.Itoa(port),
			"--bind", "127.0.0.1",
			"--cluster-enabled", "yes",
			"--cluster-config-file", "nodes.conf",
			"--cluster-node-timeout", "2000",
			"--dir", dir,
			"--save", "",
			"--appendonly", "no",
			"--daemonize", "no",
		)
		if err := cmd.Start(); err != nil {
			t.Fatalf("start redis-server node %d: %v", i, err)
		}
		t.Cleanup(func() {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		})
	}

	for _, addr := range addrs {
		waitForRedisUp(t, addr)
	}

	createArgs := append([]string{"--cluster", "create"}, addrs...)
	createArgs = append(createArgs, "--cluster-yes")
	createCmd := exec.Command("redis-cli", createArgs...)
	out, err := createCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("redis-cli --cluster create failed: %v\n%s", err, out)
	}

	for _, addr := range addrs {
		waitForClusterOK(t, addr)
	}

	return addrs
}

// findFreePort returns a free TCP port, retrying until port+10000 also
// fits under 65535 - Redis Cluster needs that second port free too,
// for its internal cluster-bus protocol, and the OS's ephemeral port
// range (often 32768-60999) can otherwise hand out a port too high for
// +10000 to stay in range.
func findFreePort(t *testing.T) int {
	t.Helper()
	for i := 0; i < 50; i++ {
		lis, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("find free port: %v", err)
		}
		port := lis.Addr().(*net.TCPAddr).Port
		lis.Close()
		if port+10000 <= 65535 {
			return port
		}
	}
	t.Fatalf("could not find a free port leaving room for the cluster-bus port")
	return 0
}

func waitForRedisUp(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		cmd := exec.Command("redis-cli", "-h", strings.Split(addr, ":")[0], "-p", strings.Split(addr, ":")[1], "ping")
		if out, err := cmd.CombinedOutput(); err == nil && strings.TrimSpace(string(out)) == "PONG" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("redis node %s did not come up in time", addr)
}

func waitForClusterOK(t *testing.T, addr string) {
	t.Helper()
	host, port, _ := strings.Cut(addr, ":")
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		cmd := exec.Command("redis-cli", "-h", host, "-p", port, "cluster", "info")
		out, err := cmd.CombinedOutput()
		if err == nil && strings.Contains(string(out), "cluster_state:ok") {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("cluster node %s did not reach cluster_state:ok in time", addr)
}

func newClusterStore(t *testing.T, addrs []string) *Store {
	t.Helper()
	s, err := New(config.RedisConfig{
		Addrs:        addrs,
		ClusterMode:  true,
		DialTimeout:  config.Duration(2 * time.Second),
		ReadTimeout:  config.Duration(2 * time.Second),
		WriteTimeout: config.Duration(2 * time.Second),
	})
	if err != nil {
		t.Fatalf("new cluster store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// TestRealCluster_SlidingWindowMultiWindow is the most important test
// in this file: the sliding-window Lua script takes one KEYS entry
// per window and EVALs them together. Under real Redis Cluster, a
// multi-key Lua call whose keys don't all hash to the same slot fails
// with a CROSSSLOT error - this proves the {namespace:ruleID:identity}
// hash-tag design (internal/store/client.go's hashTagKey) actually
// avoids that in practice, not just in theory.
func TestRealCluster_SlidingWindowMultiWindow(t *testing.T) {
	addrs := startRealRedisCluster(t, 3)
	s := newClusterStore(t, addrs)

	ctx := context.Background()
	base := time.Unix(1_700_000_000, 0)
	windows := []WindowSpec{
		{Period: time.Minute, Limit: 2},
		{Period: time.Hour, Limit: 100},
	}

	for i := 0; i < 2; i++ {
		res, err := s.CheckSlidingWindow(ctx, "ns", "cluster-rule", "user-a", 1, windows, base)
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		if !res.Allowed {
			t.Fatalf("request %d expected allowed, got blocked", i)
		}
	}

	res, err := s.CheckSlidingWindow(ctx, "ns", "cluster-rule", "user-a", 1, windows, base)
	if err != nil {
		t.Fatalf("3rd request: %v", err)
	}
	if res.Allowed {
		t.Fatalf("3rd request should be blocked by the 1-minute window")
	}
}

// TestRealCluster_DifferentIdentitiesShardAcrossNodes proves distinct
// identities really do land on different cluster nodes/slots (i.e.
// we're not accidentally forcing everything onto one node), while
// each identity's own multi-key operations still stay single-slot.
func TestRealCluster_DifferentIdentitiesShardAcrossNodes(t *testing.T) {
	addrs := startRealRedisCluster(t, 3)
	s := newClusterStore(t, addrs)

	ctx := context.Background()
	base := time.Unix(1_700_000_000, 0)
	windows := []WindowSpec{{Period: time.Minute, Limit: 1}}

	identities := []string{"user-a", "user-b", "user-c", "user-d", "user-e", "user-f"}
	slots := map[int]bool{}
	for _, id := range identities {
		res, err := s.CheckSlidingWindow(ctx, "ns", "cluster-rule", id, 1, windows, base)
		if err != nil {
			t.Fatalf("identity %s: %v", id, err)
		}
		if !res.Allowed {
			t.Fatalf("identity %s: expected allowed on first request", id)
		}

		out, err := exec.Command("redis-cli", "-h", "127.0.0.1", "-p", strings.Split(addrs[0], ":")[1],
			"cluster", "keyslot", fmt.Sprintf("unigate:{ns:cluster-rule:%s}:sw:%d", id, windows[0].Period.Milliseconds())).CombinedOutput()
		if err != nil {
			t.Fatalf("cluster keyslot for %s: %v", id, err)
		}
		var slot int
		fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &slot)
		slots[slot] = true
	}

	if len(slots) < 2 {
		t.Fatalf("expected identities to spread across multiple hash slots, all landed on: %v", slots)
	}
}

func TestRealCluster_GCRA(t *testing.T) {
	addrs := startRealRedisCluster(t, 3)
	s := newClusterStore(t, addrs)

	ctx := context.Background()
	base := time.Unix(1_700_000_000, 0)
	spec := GCRASpec{Period: time.Minute, Limit: 60, Burst: 3}

	for i := 0; i < 3; i++ {
		res, err := s.CheckGCRA(ctx, "ns", "cluster-gcra", "1.2.3.4", 1, spec, base)
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		if !res.Allowed {
			t.Fatalf("request %d expected allowed, got blocked", i)
		}
	}
	res, err := s.CheckGCRA(ctx, "ns", "cluster-gcra", "1.2.3.4", 1, spec, base)
	if err != nil {
		t.Fatalf("4th request: %v", err)
	}
	if res.Allowed {
		t.Fatalf("4th request should be throttled (burst exhausted)")
	}
}

func TestRealCluster_LockoutEscalation(t *testing.T) {
	addrs := startRealRedisCluster(t, 3)
	s := newClusterStore(t, addrs)

	ctx := context.Background()
	base := time.Unix(1_700_000_000, 0)
	steps := []LockoutStep{
		{AfterViolations: 1, Lockout: time.Minute},
		{AfterViolations: 2, Lockout: 5 * time.Minute},
	}

	res, err := s.RecordViolation(ctx, "ns", "cluster-lockout", "1.2.3.4", time.Hour, steps, base)
	if err != nil {
		t.Fatalf("violation 1: %v", err)
	}
	if !res.Locked || res.LockedFor != time.Minute {
		t.Fatalf("expected 1m lockout, got locked=%v for=%v", res.Locked, res.LockedFor)
	}

	res, err = s.RecordViolation(ctx, "ns", "cluster-lockout", "1.2.3.4", time.Hour, steps, base.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("violation 2: %v", err)
	}
	if !res.Locked || res.LockedFor != 5*time.Minute {
		t.Fatalf("expected 5m lockout after 2nd violation, got locked=%v for=%v", res.Locked, res.LockedFor)
	}
}
