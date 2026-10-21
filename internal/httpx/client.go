package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

type StatusError struct {
	Code int
	Body string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("http %d: %s", e.Code, e.Body)
}

func IsStatus(err error, code int) bool {
	var se *StatusError
	return errors.As(err, &se) && se.Code == code
}

type Client struct {
	http    *http.Client
	limiter *Limiter
	headers map[string]string
	retries int
}

func New(timeout time.Duration, limiter *Limiter, headers map[string]string) *Client {
	return &Client{
		http:    &http.Client{Timeout: timeout},
		limiter: limiter,
		headers: headers,
		retries: 2,
	}
}

func (c *Client) GetJSON(ctx context.Context, url string, out any) error {
	body, err := c.do(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	return decode(body, out)
}

func (c *Client) GetText(ctx context.Context, url string) (string, error) {
	body, err := c.do(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	return string(bytes.TrimSpace(body)), nil
}

func (c *Client) PostJSON(ctx context.Context, url string, in, out any) error {
	payload, err := json.Marshal(in)
	if err != nil {
		return err
	}
	body, err := c.do(ctx, http.MethodPost, url, payload)
	if err != nil {
		return err
	}
	return decode(body, out)
}

func decode(body []byte, out any) error {
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		snippet := string(body)
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		return fmt.Errorf("decode response: %w (body: %s)", err, snippet)
	}
	return nil
}

func (c *Client) do(ctx context.Context, method, url string, payload []byte) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt <= c.retries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(attempt*attempt) * 700 * time.Millisecond
			t := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				t.Stop()
				return nil, ctx.Err()
			case <-t.C:
			}
		}
		if err := c.limiter.Wait(ctx); err != nil {
			return nil, err
		}
		body, retry, err := c.once(ctx, method, url, payload)
		if err == nil {
			return body, nil
		}
		lastErr = err
		if !retry || ctx.Err() != nil {
			break
		}
	}
	return nil, lastErr
}

func (c *Client) once(ctx context.Context, method, url string, payload []byte) ([]byte, bool, error) {
	var reader io.Reader
	if payload != nil {
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "chainwatch-bot/1.0")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, true, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, true, err
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return body, false, nil
	}
	snippet := string(body)
	if len(snippet) > 300 {
		snippet = snippet[:300]
	}
	retry := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
	return nil, retry, &StatusError{Code: resp.StatusCode, Body: snippet}
}
