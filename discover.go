package main

import (
	"fmt"
	"sort"
	"strings"
)

// Convention auto-discovery: find request-counter metric families for a
// service using common naming conventions, and pick the label that splits
// traffic by route/handler so shifts can be attributed.

// serviceLabels are label names commonly used to identify a service.
var serviceLabels = []string{"service", "service_name", "job", "app", "application", "container"}

// counterConventions are common request-counter metric names.
var counterConventions = []string{
	"http_requests_total",
	"http_server_requests_total",
	"http_requests_count",
	"requests_total",
	"grpc_server_handled_total",
	"http_request_duration_seconds_count",
	"http_server_duration_seconds_count",
}

// routeLabels are label names that split traffic by endpoint.
var routeLabels = []string{"route", "handler", "path", "endpoint", "uri", "method", "grpc_method", "code", "status", "status_code", "le"}

// discovery is one discovered metric family for the service.
type discovery struct {
	Metric       string   // metric name
	ServiceLabel string   // which label carried the service name
	GroupLabels  []string // labels used to split traffic (route/status dimensions)
	Series       int      // how many series matched
}

// discoverFamilies probes counter conventions and returns the families that
// have series for the service. If service is empty, families are discovered
// without a service filter.
func discoverFamilies(p *promClient, service string) ([]discovery, []string, error) {
	var out []discovery
	var notes []string
	for _, metric := range counterConventions {
		sets, err := p.series([]string{"{" + matcherFor(metric) + "}"})
		if err != nil {
			return nil, nil, err
		}
		// filter to sets that have the metric name and, when given, the service
		var matched []map[string]string
		for _, s := range sets {
			if s["__name__"] != metric {
				continue
			}
			if service != "" && !matchesService(s, service) {
				continue
			}
			matched = append(matched, s)
		}
		if len(matched) == 0 {
			continue
		}
		d := discovery{Metric: metric, Series: len(matched)}
		d.ServiceLabel = pickServiceLabel(matched, service)
		d.GroupLabels = pickGroupLabels(matched, d.ServiceLabel)
		out = append(out, d)
	}
	if len(out) == 0 && service != "" {
		// help the user override: what service-ish label values exist?
		for _, l := range serviceLabels {
			vals, err := p.labelValues(l)
			if err == nil && len(vals) > 0 {
				sort.Strings(vals)
				if len(vals) > 10 {
					vals = vals[:10]
				}
				notes = append(notes, fmt.Sprintf("label %q values include: %s", l, strings.Join(vals, ", ")))
			}
		}
	}
	return out, notes, nil
}

func matcherFor(metric string) string {
	return fmt.Sprintf("__name__=%q", metric)
}

func matchesService(set map[string]string, service string) bool {
	for _, l := range serviceLabels {
		if set[l] == service {
			return true
		}
	}
	return false
}

func pickServiceLabel(sets []map[string]string, service string) string {
	if service == "" {
		return ""
	}
	for _, l := range serviceLabels {
		for _, s := range sets {
			if s[l] == service {
				return l
			}
		}
	}
	return ""
}

// pickGroupLabels returns route-ish labels present across the matched series,
// excluding the service label and noisy identity labels.
func pickGroupLabels(sets []map[string]string, serviceLabel string) []string {
	present := map[string]bool{}
	for _, s := range sets {
		for k := range s {
			present[k] = true
		}
	}
	var out []string
	for _, l := range routeLabels {
		if l == "le" {
			continue // histogram bucket label is not a traffic split
		}
		if present[l] && l != serviceLabel {
			out = append(out, l)
		}
	}
	sort.Strings(out)
	return out
}

// totalQuery builds sum(increase(metric{sel}[window])) for z-test denominators.
func (d discovery) totalQuery(service, window string) string {
	sel := ""
	if d.ServiceLabel != "" {
		sel = fmt.Sprintf("{%s=%q}", d.ServiceLabel, service)
	}
	return fmt.Sprintf("sum(increase(%s%s[%s]))", d.Metric, sel, window)
}

// increaseQuery builds sum by (groups) (increase(metric{sel}[window])).
func (d discovery) increaseQuery(service, window string) string {
	sel := ""
	if d.ServiceLabel != "" {
		sel = fmt.Sprintf("{%s=%q}", d.ServiceLabel, service)
	}
	by := ""
	if len(d.GroupLabels) > 0 {
		by = " by (" + strings.Join(d.GroupLabels, ", ") + ")"
	}
	return fmt.Sprintf("sum%s (increase(%s%s[%s]))", by, d.Metric, sel, window)
}
