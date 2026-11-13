package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// promClient talks to the Prometheus HTTP API (v1). Stdlib only.
type promClient struct {
	base   string
	client *http.Client
}

func newPromClient(base string) *promClient {
	return &promClient{base: base, client: &http.Client{Timeout: 30 * time.Second}}
}

type apiResponse struct {
	Status    string          `json:"status"`
	Data      json.RawMessage `json:"data"`
	ErrorType string          `json:"errorType"`
	Error     string          `json:"error"`
}

type queryData struct {
	ResultType string   `json:"resultType"`
	Result     []sample `json:"result"`
}

type sample struct {
	Metric map[string]string `json:"metric"`
	Value  [2]any            `json:"value"` // [unixTime, "value"]
}

func (s sample) float() (float64, error) {
	if len(s.Value) != 2 {
		return 0, fmt.Errorf("bad value shape")
	}
	str, ok := s.Value[1].(string)
	if !ok {
		return 0, fmt.Errorf("bad value type")
	}
	return strconv.ParseFloat(str, 64)
}

// queryInstant runs an instant query at t and returns vector samples.
func (p *promClient) queryInstant(query string, t time.Time) ([]sample, error) {
	u := fmt.Sprintf("%s/api/v1/query?query=%s&time=%s",
		p.base, url.QueryEscape(query), url.QueryEscape(fmt.Sprintf("%d", t.Unix())))
	resp, err := p.client.Get(u)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var ar apiResponse
	if err := json.Unmarshal(body, &ar); err != nil {
		return nil, fmt.Errorf("prometheus: bad json: %w", err)
	}
	if ar.Status != "success" {
		return nil, fmt.Errorf("prometheus: %s: %s", ar.ErrorType, ar.Error)
	}
	var qd queryData
	if err := json.Unmarshal(ar.Data, &qd); err != nil {
		return nil, fmt.Errorf("prometheus: bad data: %w", err)
	}
	return qd.Result, nil
}

// labelValues returns the values Prometheus knows for a label name.
func (p *promClient) labelValues(name string) ([]string, error) {
	u := fmt.Sprintf("%s/api/v1/label/%s/values", p.base, url.PathEscape(name))
	resp, err := p.client.Get(u)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	var ar apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&ar); err != nil {
		return nil, err
	}
	if ar.Status != "success" {
		return nil, fmt.Errorf("prometheus: %s: %s", ar.ErrorType, ar.Error)
	}
	var vals []string
	if err := json.Unmarshal(ar.Data, &vals); err != nil {
		return nil, err
	}
	return vals, nil
}

// series returns label sets matching the given selectors.
func (p *promClient) series(match []string) ([]map[string]string, error) {
	q := url.Values{}
	for _, m := range match {
		q.Add("match[]", m)
	}
	u := fmt.Sprintf("%s/api/v1/series?%s", p.base, q.Encode())
	resp, err := p.client.Get(u)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	var ar apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&ar); err != nil {
		return nil, err
	}
	if ar.Status != "success" {
		return nil, fmt.Errorf("prometheus: %s: %s", ar.ErrorType, ar.Error)
	}
	var sets []map[string]string
	if err := json.Unmarshal(ar.Data, &sets); err != nil {
		return nil, err
	}
	return sets, nil
}
