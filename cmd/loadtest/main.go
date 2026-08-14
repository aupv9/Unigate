// Command loadtest measures CheckLimit end-to-end latency and
// throughput against a running Unigate instance (NFR1: p99 <= 5ms
// when co-located with the gateway; NFR2 informed by the error rate
// observed here under load).
//
// It is deliberately a small, dependency-free Go tool rather than a
// separate load-testing product: this repo's CI/dev environment can
// run `go run ./cmd/loadtest` directly with no extra tooling install.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

type result struct {
	latency    time.Duration
	statusCode int
	err        error
}

func main() {
	url := flag.String("url", "http://localhost:8080/v1/check", "CheckLimit endpoint to hit")
	ruleID := flag.String("rule-id", "anonymous-ip-limit", "rule_id to evaluate")
	gateway := flag.String("gateway", "loadtest", "gateway label sent in each request")
	concurrency := flag.Int("concurrency", 50, "number of concurrent workers")
	duration := flag.Duration("duration", 10*time.Second, "how long to run")
	cardinality := flag.Int("key-cardinality", 500, "number of distinct synthetic IPs to cycle through, so most requests stay under the rate limit instead of all colliding on one key")
	flag.Parse()

	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        *concurrency * 2,
			MaxIdleConnsPerHost: *concurrency * 2,
			IdleConnTimeout:     30 * time.Second,
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), *duration)
	defer cancel()

	resultsCh := make(chan result, 4096)
	var wg sync.WaitGroup
	var sent int64

	start := time.Now()
	for w := 0; w < *concurrency; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(workerID) + start.UnixNano()))
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				idx := rng.Intn(*cardinality)
				ip := fmt.Sprintf("10.0.%d.%d", idx/256%256, idx%256)
				body, _ := json.Marshal(map[string]any{
					"rule_id": *ruleID,
					"key":     []map[string]string{{"kind": "ip", "value": ip}},
					"gateway": *gateway,
				})

				reqStart := time.Now()
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, *url, bytes.NewReader(body))
				if err != nil {
					resultsCh <- result{err: err}
					continue
				}
				req.Header.Set("Content-Type", "application/json")

				resp, err := client.Do(req)
				lat := time.Since(reqStart)
				atomic.AddInt64(&sent, 1)
				if err != nil {
					resultsCh <- result{latency: lat, err: err}
					continue
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				resultsCh <- result{latency: lat, statusCode: resp.StatusCode}
			}
		}(w)
	}

	go func() {
		wg.Wait()
		close(resultsCh)
	}()

	var (
		latencies  []time.Duration
		errCount   int
		statusCnts = map[int]int{}
	)
	for r := range resultsCh {
		if r.err != nil {
			errCount++
			continue
		}
		latencies = append(latencies, r.latency)
		statusCnts[r.statusCode]++
	}
	elapsed := time.Since(start)

	if len(latencies) == 0 {
		fmt.Fprintln(os.Stderr, "no successful requests completed - is the service reachable at", *url, "?")
		os.Exit(1)
	}

	sortDurations(latencies)
	total := len(latencies) + errCount

	fmt.Printf("Unigate load test\n")
	fmt.Printf("  target:        %s\n", *url)
	fmt.Printf("  rule_id:       %s\n", *ruleID)
	fmt.Printf("  concurrency:   %d\n", *concurrency)
	fmt.Printf("  duration:      %s (actual: %s)\n", *duration, elapsed.Round(time.Millisecond))
	fmt.Printf("  total requests: %d (%.0f req/s)\n", total, float64(total)/elapsed.Seconds())
	fmt.Printf("  errors:        %d (%.2f%%)\n", errCount, 100*float64(errCount)/float64(total))
	fmt.Printf("  status codes:  %v\n", statusCnts)
	fmt.Println()
	fmt.Printf("  latency p50:   %s\n", percentile(latencies, 50))
	fmt.Printf("  latency p90:   %s\n", percentile(latencies, 90))
	fmt.Printf("  latency p99:   %s\n", percentile(latencies, 99))
	fmt.Printf("  latency p999:  %s\n", percentile(latencies, 99.9))
	fmt.Printf("  latency max:   %s\n", latencies[len(latencies)-1])
	fmt.Println()
	fmt.Println("  NFR1 targets p99 <= 5ms when co-located with the gateway;")
	fmt.Println("  numbers from a shared/virtualized dev machine won't be representative")
	fmt.Println("  of that - re-run this in the target environment for a real baseline.")
}
