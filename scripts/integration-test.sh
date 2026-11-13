#!/usr/bin/env bash
# Integration test against a real, disposable local Prometheus.
# Downloads Prometheus (linux-amd64), runs a synthetic fixture exporter whose
# error rate jumps on /flip, and verifies deploydelta flags the regression
# (exit 1) and stays quiet on a healthy window (exit 0).
#
# Not run in CI (downloads a binary, takes ~3 minutes). Run locally:
#   ./scripts/integration-test.sh
set -u
PROM_VERSION=3.16.0
WORK=$(mktemp -d)
trap 'pkill -f fixture-exporter 2>/dev/null; pkill -f "prometheus-$PROM_VERSION" 2>/dev/null; rm -rf "$WORK"' EXIT

cd "$WORK"
curl -sL -o prom.tar.gz "https://github.com/prometheus/prometheus/releases/download/v$PROM_VERSION/prometheus-$PROM_VERSION.linux-amd64.tar.gz"
tar xzf prom.tar.gz

cat > exporter.go <<'GO'
package main

import (
	"fmt"
	"log"
	"net/http"
	"sync/atomic"
)

var ok200, err500 atomic.Int64
var flipped atomic.Bool

func main() {
	http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		var e int64
		if flipped.Load() {
			e = 3
		}
		ok200.Add(10)
		err500.Add(e)
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "# TYPE http_requests_total counter\n")
		fmt.Fprintf(w, "http_requests_total{service=\"payments\",route=\"/api/pay\",code=\"200\"} %d\n", ok200.Load())
		fmt.Fprintf(w, "http_requests_total{service=\"payments\",route=\"/api/pay\",code=\"500\"} %d\n", err500.Load())
	})
	http.HandleFunc("/flip", func(w http.ResponseWriter, r *http.Request) {
		flipped.Store(true)
		fmt.Fprintln(w, "flipped")
	})
	log.Fatal(http.ListenAndServe("127.0.0.1:19091", nil))
}
GO

go mod init fixture >/dev/null 2>&1
go build -o exporter exporter.go

cat > prom.yml <<'YAML'
global:
  scrape_interval: 1s
scrape_configs:
  - job_name: fixture
    static_configs:
      - targets: ['127.0.0.1:19091']
YAML

(./exporter &)
("./prometheus-$PROM_VERSION.linux-amd64/prometheus" --config.file=prom.yml --storage.tsdb.path="$WORK/data" --web.listen-address=127.0.0.1:19090 --log.level=warn &)

go build -o deploydelta-under-test github.com/ofershap/deploydelta 2>/dev/null || go build -o deploydelta-under-test ..

fail=0

echo "--- scenario 1: regression after flip"
sleep 45
T=$(date +%s)
curl -s http://127.0.0.1:19091/flip
sleep 45
./deploydelta-under-test --prometheus http://127.0.0.1:19090 --service payments --since "$T" --window 30s --min-count 5
[ $? -eq 1 ] && echo "PASS: regression flagged (exit 1)" || { echo "FAIL: expected exit 1"; fail=1; }

pkill -f fixture-exporter; pkill -f "prometheus-$PROM_VERSION"; sleep 2
rm -rf "$WORK/data"

(./exporter &)
("./prometheus-$PROM_VERSION.linux-amd64/prometheus" --config.file=prom.yml --storage.tsdb.path="$WORK/data" --web.listen-address=127.0.0.1:19090 --log.level=warn &)

echo "--- scenario 2: healthy window, no flip"
sleep 50
T=$(date +%s)
sleep 35
./deploydelta-under-test --prometheus http://127.0.0.1:19090 --service payments --since "$T" --window 30s --min-count 5
[ $? -eq 0 ] && echo "PASS: no false positive (exit 0)" || { echo "FAIL: expected exit 0"; fail=1; }

exit $fail
