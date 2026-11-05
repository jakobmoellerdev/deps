package githubapi

import (
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"fmt"
	"net/url"
	"strings"
)

// File returns the content of path at ref ("" = default branch) in r; a missing file wraps
// ErrNotFound.
func (c *Client) File(ctx context.Context, r Repo, path, ref string) ([]byte, error) {
	if err := c.acquire(ctx); err != nil {
		return nil, err
	}
	defer c.release()
	segs := strings.Split(path, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	p := "/repos/" + r.Path() + "/contents/" + strings.Join(segs, "/")
	if ref != "" {
		p += "?ref=" + url.QueryEscape(ref)
	}
	resp, err := c.restGet(ctx, r, p)
	if err != nil {
		return nil, err
	}
	defer closeBody(resp)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, statusError(resp)
	}
	var raw struct {
		Type     string `json:"type"`
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
	}
	if err := json.UnmarshalRead(resp.Body, &raw); err != nil {
		return nil, fmt.Errorf("malformed response: %w", err)
	}
	if raw.Type != "file" || raw.Encoding != "base64" {
		return nil, fmt.Errorf("%s: %s is not a file", r.Path(), path)
	}
	b, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(raw.Content, "\n", ""))
	if err != nil {
		return nil, fmt.Errorf("malformed response: %w", err)
	}
	return b, nil
}
