// SPDX-License-Identifier: Apache-2.0

// Package client is a small client of the public Polipan API (REQ-TF-001). The provider uses only
// this client. It does not import the server or the generated API code, so the provider is its own module.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Obj is a JSON object from the API.
type Obj = map[string]any

// Client calls the API with a bearer token.
type Client struct {
	base  string
	token string
	http  *http.Client
}

// New makes a client. url is the server address, with or without the /api/v1 suffix.
func New(url, token string) *Client {
	base := strings.TrimRight(url, "/")
	if !strings.HasSuffix(base, "/api/v1") {
		base += "/api/v1"
	}
	return &Client{base: base, token: token, http: &http.Client{Timeout: 60 * time.Second}}
}

// APIError is an error answer of the API ({code, message, details, request_id}).
type APIError struct {
	Status    int
	Code      string
	Message   string
	RequestID string
	Method    string
	Path      string
}

func (e *APIError) Error() string {
	s := fmt.Sprintf("%s %s: HTTP %d", e.Method, e.Path, e.Status)
	if e.Code != "" {
		s += " " + e.Code
	}
	if e.Message != "" {
		s += ": " + e.Message
	}
	if e.RequestID != "" {
		s += " (request " + e.RequestID + ")"
	}
	return s
}

// IsNotFound tells if err is a 404 answer.
func IsNotFound(err error) bool {
	var e *APIError
	return errors.As(err, &e) && e.Status == http.StatusNotFound
}

// Do sends a request. in is encoded as JSON when not nil. A JSON answer is decoded into out when out is not nil.
func (c *Client) Do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "terraform-provider-polipan")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		e := &APIError{Status: resp.StatusCode, Method: method, Path: path}
		var b struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			RequestID string `json:"request_id"`
		}
		if json.Unmarshal(raw, &b) == nil {
			e.Code, e.Message, e.RequestID = b.Code, b.Message, b.RequestID
		}
		return e
	}
	if out != nil && len(bytes.TrimSpace(raw)) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// Get, Post, Patch, Put and Delete are short forms of Do. They decode into an Obj.
func (c *Client) Get(ctx context.Context, path string) (Obj, error) {
	var o Obj
	err := c.Do(ctx, http.MethodGet, path, nil, &o)
	return o, err
}

// Post sends a POST and returns the answer object.
func (c *Client) Post(ctx context.Context, path string, in any) (Obj, error) {
	var o Obj
	err := c.Do(ctx, http.MethodPost, path, in, &o)
	return o, err
}

// Patch sends a PATCH and returns the answer object.
func (c *Client) Patch(ctx context.Context, path string, in any) (Obj, error) {
	var o Obj
	err := c.Do(ctx, http.MethodPatch, path, in, &o)
	return o, err
}

// Put sends a PUT and returns the answer object (empty for 204).
func (c *Client) Put(ctx context.Context, path string, in any) (Obj, error) {
	var o Obj
	err := c.Do(ctx, http.MethodPut, path, in, &o)
	return o, err
}

// Delete sends a DELETE. A 404 is not an error: the object is gone, which is what the caller wants.
func (c *Client) Delete(ctx context.Context, path string) error {
	err := c.Do(ctx, http.MethodDelete, path, nil, nil)
	if IsNotFound(err) {
		return nil
	}
	return err
}

// List returns the items of a list answer. It accepts a JSON array and {"items": [...], "next_cursor": "..."}
// and follows the cursor until the last page.
func (c *Client) List(ctx context.Context, path string) ([]Obj, error) {
	var all []Obj
	cursor := ""
	for page := 0; page < 1000; page++ {
		p := path
		if cursor != "" {
			sep := "?"
			if strings.Contains(p, "?") {
				sep = "&"
			}
			p += sep + "cursor=" + url.QueryEscape(cursor)
		}
		var raw json.RawMessage
		if err := c.Do(ctx, http.MethodGet, p, nil, &raw); err != nil {
			return nil, err
		}
		var arr []Obj
		if err := json.Unmarshal(raw, &arr); err == nil {
			return append(all, arr...), nil
		}
		var body struct {
			Items      []Obj  `json:"items"`
			NextCursor string `json:"next_cursor"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, fmt.Errorf("list %s: unexpected answer: %w", path, err)
		}
		all = append(all, body.Items...)
		if body.NextCursor == "" {
			return all, nil
		}
		cursor = body.NextCursor
	}
	return all, nil
}

// Find lists path and returns the item whose key field equals id. It returns a 404 APIError when there is none.
// It is used for objects that have no GET by id in the API.
func (c *Client) Find(ctx context.Context, path, key, id string) (Obj, error) {
	items, err := c.List(ctx, path)
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		if Str(it, key) == id {
			return it, nil
		}
	}
	return nil, &APIError{Status: http.StatusNotFound, Code: "not_found", Message: "not in the list", Method: http.MethodGet, Path: path}
}

// Str returns a string field, or "".
func Str(o Obj, key string) string {
	s, _ := o[key].(string)
	return s
}

// Sub returns a nested object, or nil.
func Sub(o Obj, key string) Obj {
	m, _ := o[key].(map[string]any)
	return m
}

// Strings returns a list of strings field.
func Strings(o Obj, key string) []string {
	list, _ := o[key].([]any)
	out := make([]string, 0, len(list))
	for _, v := range list {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
