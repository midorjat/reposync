package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"reposync/internal/node"
	"reposync/internal/repo"
)

// Client talks to the agent on the other machine. It implements node.Peer.
type Client struct {
	base  string
	token string
	hc    *http.Client
}

var _ node.Peer = (*Client)(nil)

func NewClient(baseURL, token string) *Client {
	// No overall timeout: bundles of big repos can take a while. Requests
	// are bounded by their context and by the dial timeout instead.
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = 10 * time.Minute
	return &Client{base: baseURL, token: token, hc: &http.Client{Transport: tr}}
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach peer at %s (is `reposync serve` running there?): %w", c.base, err)
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		var e errorBody
		json.NewDecoder(resp.Body).Decode(&e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		if sentinel, ok := codes[e.Code]; ok {
			return nil, fmt.Errorf("peer: %w", sentinel)
		}
		return nil, fmt.Errorf("peer: %s", e.Error)
	}
	return resp, nil
}

func (c *Client) json(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	resp, err := c.do(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(out)
}

func repoPath(name, rest string) string {
	return "/v1/repos/" + url.PathEscape(name) + rest
}

func (c *Client) Hello(ctx context.Context) (node.Hello, error) {
	var h node.Hello
	err := c.json(ctx, "GET", "/v1/hello", nil, &h)
	return h, err
}

func (c *Client) State(ctx context.Context, name string) (repo.State, error) {
	var s repo.State
	err := c.json(ctx, "GET", repoPath(name, "/state"), nil, &s)
	return s, err
}

func (c *Client) Export(ctx context.Context, name string, w io.Writer) error {
	resp, err := c.do(ctx, "GET", repoPath(name, "/bundle"), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, err = io.Copy(w, resp.Body)
	return err
}

func (c *Client) Import(ctx context.Context, name, from string, r io.Reader) (repo.State, error) {
	resp, err := c.do(ctx, "POST", repoPath(name, "/bundle?from="+url.QueryEscape(from)), r)
	if err != nil {
		return repo.State{}, err
	}
	defer resp.Body.Close()
	var s repo.State
	return s, json.NewDecoder(resp.Body).Decode(&s)
}

func (c *Client) Apply(ctx context.Context, name, from, expect string) (repo.State, error) {
	var s repo.State
	err := c.json(ctx, "POST", repoPath(name, "/apply"), applyReq{From: from, Expect: expect}, &s)
	return s, err
}

func (c *Client) KeepWork(ctx context.Context, name, from string) (string, error) {
	var out keepResp
	err := c.json(ctx, "POST", repoPath(name, "/keep"), keepReq{From: from}, &out)
	return out.Branch, err
}

func (c *Client) Base(ctx context.Context, name, peer string) (*repo.Base, error) {
	var b *repo.Base
	err := c.json(ctx, "GET", repoPath(name, "/base/"+url.PathEscape(peer)), nil, &b)
	return b, err
}

func (c *Client) Lease(ctx context.Context, name, holder string, ttl time.Duration) error {
	var out struct{}
	return c.json(ctx, "POST", repoPath(name, "/lease"), leaseReq{Holder: holder, TTL: int(ttl.Seconds())}, &out)
}

func (c *Client) Release(ctx context.Context, name, holder string) error {
	var out struct{}
	return c.json(ctx, "DELETE", repoPath(name, "/lease?holder="+url.QueryEscape(holder)), nil, &out)
}

func (c *Client) Doctor(ctx context.Context) (node.Report, error) {
	var r node.Report
	err := c.json(ctx, "GET", "/v1/doctor", nil, &r)
	return r, err
}

func (c *Client) SetBase(ctx context.Context, name, peer string, b repo.Base) error {
	var out struct{}
	return c.json(ctx, "PUT", repoPath(name, "/base/"+url.PathEscape(peer)), b, &out)
}
