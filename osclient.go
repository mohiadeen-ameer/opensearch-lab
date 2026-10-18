package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// The application always talks to the alias. The concrete index behind it can be
// swapped without downtime (see LABS.md, zero-downtime reindex).
const (
	indexName = "wikipedia_v1"
	aliasName = "wikipedia"
)

var (
	osBase     = envOr("OPENSEARCH_URL", "http://localhost:9200")
	httpClient = &http.Client{Timeout: 60 * time.Second}
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// osError carries the status and body of a failed OpenSearch call.
type osError struct {
	Status int
	Body   string
}

func (e *osError) Error() string {
	return fmt.Sprintf("opensearch returned %d: %s", e.Status, e.Body)
}

// osRaw sends one request to OpenSearch and returns the body and status code.
// We use plain HTTP on purpose: every call maps 1:1 to the REST API you will
// type into Dev Tools, so nothing is hidden behind a client library.
func osRaw(method, path, contentType string, body []byte) ([]byte, int, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, osBase+path, rdr)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return data, resp.StatusCode, err
}

// osJSON sends a JSON request (body may be nil, a string, bytes, or any value
// that marshals to JSON). Non-2xx responses come back as *osError.
func osJSON(method, path string, body any) ([]byte, error) {
	var payload []byte
	switch b := body.(type) {
	case nil:
	case []byte:
		payload = b
	case string:
		payload = []byte(b)
	default:
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return nil, err
		}
	}
	data, status, err := osRaw(method, path, "application/json", payload)
	if err != nil {
		return nil, err
	}
	if status >= 300 {
		return data, &osError{Status: status, Body: string(data)}
	}
	return data, nil
}
