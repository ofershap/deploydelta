package main

import (
	"math"
	"sort"
	"strings"
)

// shift is one traffic group's pre/post comparison.
type shift struct {
	Group   string  // rendered label set, e.g. route="/api/pay",code="500"
	Metric  string  // metric family
	Pre     float64 // pre-window count
	Post    float64 // post-window count
	Ratio   float64 // post/pre (Inf when pre==0)
	PValue  float64 // two-proportion z-test against the window totals
	Flagged bool    // meets regression criteria
}

// compareResults pairs pre and post instant-vector samples by label key and
// scores each group. totalPre/totalPost are the window totals across all
// traffic of this family, used as the z-test denominators.
func compareResults(metric string, pre, post []sample, totalPre, totalPost, minCount, ratioThreshold, pThreshold float64) ([]shift, error) {
	preMap := map[string]float64{}
	preLabels := map[string]string{}
	for _, s := range pre {
		v, err := s.float()
		if err != nil {
			return nil, err
		}
		k := labelKey(s.Metric)
		preMap[k] += v
		preLabels[k] = renderLabels(s.Metric)
	}
	postMap := map[string]float64{}
	postLabels := map[string]string{}
	for _, s := range post {
		v, err := s.float()
		if err != nil {
			return nil, err
		}
		k := labelKey(s.Metric)
		postMap[k] += v
		postLabels[k] = renderLabels(s.Metric)
	}
	keys := map[string]bool{}
	for k := range preMap {
		keys[k] = true
	}
	for k := range postMap {
		keys[k] = true
	}
	var out []shift
	for k := range keys {
		a, b := preMap[k], postMap[k]
		labels := preLabels[k]
		if labels == "" {
			labels = postLabels[k]
		}
		sh := shift{Group: labels, Metric: metric, Pre: a, Post: b}
		sh.Ratio = ratio(b, a)
		sh.PValue = zTestP(b, totalPost, a, totalPre)
		sh.Flagged = sh.PValue < pThreshold && sh.Ratio >= ratioThreshold && b >= minCount
		out = append(out, sh)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Flagged != out[j].Flagged {
			return out[i].Flagged
		}
		return out[i].Ratio > out[j].Ratio
	})
	return out, nil
}

func ratio(post, pre float64) float64 {
	if pre <= 0 {
		if post > 0 {
			return math.Inf(1)
		}
		return 1
	}
	return post / pre
}

// zTestP is a two-proportion z-test p-value (two-sided approximation):
// did group g's share of total traffic change between windows?
func zTestP(x1, n1, x2, n2 float64) float64 {
	if n1 <= 0 || n2 <= 0 {
		return 1
	}
	p1, p2 := x1/n1, x2/n2
	pooled := (x1 + x2) / (n1 + n2)
	se := math.Sqrt(pooled * (1 - pooled) * (1/n1 + 1/n2))
	if se == 0 {
		return 1
	}
	z := math.Abs(p1-p2) / se
	// two-sided normal p-value via erfc
	return math.Erfc(z / math.Sqrt(2))
}

// labelKey is a stable identity for a label set.
func labelKey(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte(0)
		b.WriteString(m[k])
		b.WriteByte(1)
	}
	return b.String()
}

// renderLabels renders a label set readably, sorted, dropping __name__.
func renderLabels(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		if k != "__name__" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, ", ")
}
