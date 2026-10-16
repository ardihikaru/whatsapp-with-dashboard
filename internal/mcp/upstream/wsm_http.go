package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
)

// WSMResult is one WSM API call's outcome.
type WSMResult struct {
	Host   string
	Status int
	Body   json.RawMessage // raw decoded JSON body, or {"raw": "..."} if not JSON
}

// CallWSM runs one "METHOD /api/v1/..." route against the pool, substituting
// pathParams into "{name}" segments, attaching query params and a JSON
// body, and applying the read/write failover rule (see pool.go). write
// must be true for every non-GET/HEAD route.
func (p *Pool) CallWSM(ctx context.Context, method, routePath string, pathParams map[string]string, query map[string]string, jsonBody any, write bool, target string) (WSMResult, error) {
	path := routePath
	for k, v := range pathParams {
		path = strings.ReplaceAll(path, "{"+k+"}", v)
	}
	var bodyBytes []byte
	if jsonBody != nil {
		b, err := json.Marshal(jsonBody)
		if err != nil {
			return WSMResult{}, fmt.Errorf("encode request body: %w", err)
		}
		bodyBytes = b
	}
	hosts, err := p.orderedHosts(target)
	if err != nil {
		return WSMResult{}, err
	}
	var lastErr error
	for i, h := range hosts {
		last := i == len(hosts)-1
		res, err := h.doJSON(ctx, method, path, query, bodyBytes)
		if err != nil {
			if !last && shouldFailover(write, 0, err) {
				lastErr = fmt.Errorf("host %s: %w", h.Name, err)
				continue
			}
			return WSMResult{}, fmt.Errorf("host %s: %w", h.Name, err)
		}
		if !last && shouldFailover(write, res.Status, nil) {
			lastErr = fmt.Errorf("host %s: status %d", h.Name, res.Status)
			continue
		}
		return res, nil
	}
	return WSMResult{}, fmt.Errorf("all hosts failed: %w", lastErr)
}

func (h *Host) doJSON(ctx context.Context, method, path string, query map[string]string, body []byte) (WSMResult, error) {
	url := h.WSMBaseURL + path
	if len(query) > 0 {
		q := make([]string, 0, len(query))
		for k, v := range query {
			if v != "" {
				q = append(q, k+"="+v)
			}
		}
		if len(q) > 0 {
			url += "?" + strings.Join(q, "&")
		}
	}
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return WSMResult{}, err
	}
	req.SetBasicAuth(h.poolUser, h.poolPass)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := h.httpClient.Do(req)
	if err != nil {
		return WSMResult{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return WSMResult{}, err
	}
	var rb json.RawMessage
	if json.Valid(raw) {
		rb = raw
	} else {
		enc, _ := json.Marshal(map[string]string{"raw": string(raw)})
		rb = enc
	}
	return WSMResult{Host: h.Name, Status: resp.StatusCode, Body: rb}, nil
}

// CallWSMMultipart runs one multipart/form-data route (media sends). fields
// are plain form fields; fileField/fileName/fileBytes add the one file
// part every media-send route needs.
func (p *Pool) CallWSMMultipart(ctx context.Context, method, routePath string, pathParams map[string]string, fields map[string]string, fileField, fileName string, fileBytes []byte, write bool, target string) (WSMResult, error) {
	path := routePath
	for k, v := range pathParams {
		path = strings.ReplaceAll(path, "{"+k+"}", v)
	}
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range fields {
		if v == "" {
			continue
		}
		if err := w.WriteField(k, v); err != nil {
			return WSMResult{}, err
		}
	}
	if fileField != "" {
		fw, err := w.CreateFormFile(fileField, fileName)
		if err != nil {
			return WSMResult{}, err
		}
		if _, err := fw.Write(fileBytes); err != nil {
			return WSMResult{}, err
		}
	}
	if err := w.Close(); err != nil {
		return WSMResult{}, err
	}
	payload := buf.Bytes()
	hosts, err := p.orderedHosts(target)
	if err != nil {
		return WSMResult{}, err
	}
	var lastErr error
	for i, h := range hosts {
		last := i == len(hosts)-1
		req, err := http.NewRequestWithContext(ctx, method, h.WSMBaseURL+path, bytes.NewReader(payload))
		if err != nil {
			return WSMResult{}, err
		}
		req.Header.Set("Content-Type", w.FormDataContentType())
		req.SetBasicAuth(h.poolUser, h.poolPass)
		resp, err := h.httpClient.Do(req)
		if err != nil {
			if !last && shouldFailover(write, 0, err) {
				lastErr = fmt.Errorf("host %s: %w", h.Name, err)
				continue
			}
			return WSMResult{}, fmt.Errorf("host %s: %w", h.Name, err)
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if err != nil {
			return WSMResult{}, err
		}
		if !last && shouldFailover(write, resp.StatusCode, nil) {
			lastErr = fmt.Errorf("host %s: status %d", h.Name, resp.StatusCode)
			continue
		}
		var rb json.RawMessage
		if json.Valid(raw) {
			rb = raw
		} else {
			enc, _ := json.Marshal(map[string]string{"raw": string(raw)})
			rb = enc
		}
		return WSMResult{Host: h.Name, Status: resp.StatusCode, Body: rb}, nil
	}
	return WSMResult{}, fmt.Errorf("all hosts failed: %w", lastErr)
}
