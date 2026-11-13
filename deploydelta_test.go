package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeProm serves canned Prometheus API responses.
func fakeProm(t *testing.T, seriesSets []map[string]string, queryResults map[string][]sample) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/series"):
			writeAPI(w, seriesSets)
		case strings.HasPrefix(r.URL.Path, "/api/v1/label/"):
			writeAPI(w, []string{"payments", "checkout"})
		case strings.HasPrefix(r.URL.Path, "/api/v1/query"):
			q := r.URL.Query().Get("query")
			res, ok := queryResults[q]
			if !ok {
				writeAPI(w, []sample{})
				return
			}
			writeAPI(w, map[string]any{"resultType": "vector", "result": res})
		default:
			http.NotFound(w, r)
		}
	}))
}

func writeAPI(w http.ResponseWriter, data any) {
	raw, _ := json.Marshal(data)
	_, _ = fmt.Fprintf(w, `{"status":"success","data":%s}`, raw)
}

func samp(labels map[string]string, v float64) sample {
	return sample{Metric: labels, Value: [2]any{float64(1763000000), fmt.Sprintf("%v", v)}}
}

func TestDiscoveryFindsConvention(t *testing.T) {
	series := []map[string]string{
		{"__name__": "http_requests_total", "service": "payments", "route": "/api/pay", "code": "200"},
		{"__name__": "http_requests_total", "service": "payments", "route": "/api/pay", "code": "500"},
		{"__name__": "http_requests_total", "service": "other", "route": "/x"},
		{"__name__": "go_goroutines", "job": "payments"},
	}
	srv := fakeProm(t, series, nil)
	defer srv.Close()
	pc := newPromClient(srv.URL)

	fams, notes, err := discoverFamilies(pc, "payments")
	if err != nil {
		t.Fatal(err)
	}
	if len(fams) != 1 {
		t.Fatalf("want 1 family, got %d (%+v) notes=%v", len(fams), fams, notes)
	}
	f := fams[0]
	if f.Metric != "http_requests_total" || f.ServiceLabel != "service" || f.Series != 2 {
		t.Fatalf("bad discovery: %+v", f)
	}
	if !contains(f.GroupLabels, "route") || !contains(f.GroupLabels, "code") {
		t.Fatalf("bad group labels: %v", f.GroupLabels)
	}
	q := f.increaseQuery("payments", "15m")
	want := `sum by (code, route) (increase(http_requests_total{service="payments"}[15m]))`
	if q != want {
		t.Fatalf("query:\n got %s\nwant %s", q, want)
	}
}

func TestDiscoveryNoMatchGivesHints(t *testing.T) {
	srv := fakeProm(t, nil, nil)
	defer srv.Close()
	pc := newPromClient(srv.URL)
	fams, notes, err := discoverFamilies(pc, "missing")
	if err != nil {
		t.Fatal(err)
	}
	if len(fams) != 0 {
		t.Fatalf("want 0 families, got %d", len(fams))
	}
	if len(notes) == 0 {
		t.Fatal("want hint notes listing known label values")
	}
}

func TestCompareFlagsRegression(t *testing.T) {
	pre := []sample{
		samp(map[string]string{"route": "/api/pay", "code": "500"}, 2),
		samp(map[string]string{"route": "/api/pay", "code": "200"}, 998),
	}
	post := []sample{
		samp(map[string]string{"route": "/api/pay", "code": "500"}, 31),
		samp(map[string]string{"route": "/api/pay", "code": "200"}, 969),
	}
	shifts, err := compareResults("http_requests_total", pre, post, 1000, 1000, 20, 2.0, 0.01)
	if err != nil {
		t.Fatal(err)
	}
	if len(shifts) != 2 {
		t.Fatalf("want 2 shifts, got %d", len(shifts))
	}
	s := shifts[0]
	if !s.Flagged || s.Group != `code=500, route=/api/pay` {
		t.Fatalf("first shift should be flagged 500 group: %+v", s)
	}
	if s.Ratio < 15 {
		t.Fatalf("ratio: %v", s.Ratio)
	}
	if shifts[1].Flagged {
		t.Fatalf("200 group should not be flagged: %+v", shifts[1])
	}
}

func TestZTestSanity(t *testing.T) {
	// identical proportions -> p ~ 1
	if p := zTestP(50, 1000, 50, 1000); p < 0.9 {
		t.Fatalf("p=%v", p)
	}
	// big shift with big samples -> tiny p
	if p := zTestP(200, 1000, 20, 1000); p > 0.001 {
		t.Fatalf("p=%v", p)
	}
	// degenerate input
	if p := zTestP(0, 0, 0, 0); p != 1 {
		t.Fatalf("p=%v", p)
	}
	// small counts: 3/10 vs 1/10 should NOT be hugely significant
	if p := zTestP(3, 10, 1, 10); p < 0.01 {
		t.Fatalf("small-count shift over-flagged: p=%v", p)
	}
}

func TestCompareNewGroup(t *testing.T) {
	post := []sample{samp(map[string]string{"route": "/api/new", "code": "500"}, 50)}
	shifts, err := compareResults("http_requests_total", nil, post, 0, 1000, 20, 2.0, 0.01)
	if err != nil {
		t.Fatal(err)
	}
	if len(shifts) != 1 || !math.IsInf(shifts[0].Ratio, 1) {
		t.Fatalf("new group should have Inf ratio: %+v", shifts)
	}
}

func TestEndToEndAgainstFakeProm(t *testing.T) {
	series := []map[string]string{
		{"__name__": "http_requests_total", "service": "payments", "route": "/api/pay", "code": "500"},
	}
	srv := fakeProm(t, series, nil)
	defer srv.Close()
	pc := newPromClient(srv.URL)
	fams, _, err := discoverFamilies(pc, "payments")
	if err != nil {
		t.Fatal(err)
	}
	if len(fams) != 1 {
		t.Fatalf("families: %v", fams)
	}
}

func TestParseTime(t *testing.T) {
	if _, err := parseTime(""); err == nil {
		t.Fatal("want error on empty")
	}
	tm, err := parseTime("2026-11-13T14:00:00Z")
	if err != nil || tm.UTC().Format(time.RFC3339) != "2026-11-13T14:00:00Z" {
		t.Fatalf("rfc3339: %v %v", tm, err)
	}
	tm2, err := parseTime("1763007600")
	if err != nil || tm2.Unix() != 1763007600 {
		t.Fatalf("unix: %v %v", tm2, err)
	}
	if _, err := parseTime("banana"); err == nil {
		t.Fatal("want error on banana")
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
