// Package immich implements the documented, public Immich REST API surface
// needed by the relay. It intentionally does not touch Immich's database or
// backing storage.
package immich

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/immich-relay/immich-relay/internal/model"
)

const DefaultPageSize = 250

// Source allows the sync engine to be tested without an Immich server.
type Source interface {
	Version(context.Context) (string, error)
	ListAssets(context.Context, *time.Time) ([]model.Asset, error)
	DownloadOriginal(context.Context, string, io.Writer) error
}

type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

func New(baseURL, apiKey string) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("invalid Immich URL")
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("Immich API key is empty")
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey,
		http: &http.Client{Timeout: 90 * time.Second},
	}, nil
}

// WithHTTPClient is primarily useful for tests and custom transport policies.
func (c *Client) WithHTTPClient(h *http.Client) *Client {
	if h != nil {
		c.http = h
	}
	return c
}

func (c *Client) Version(ctx context.Context) (string, error) {
	var payload struct {
		Version string `json:"version"`
		Major   int    `json:"major"`
		Minor   int    `json:"minor"`
		Patch   int    `json:"patch"`
	}
	if err := c.requestJSON(ctx, http.MethodGet, "/api/server/version", nil, &payload); err != nil {
		return "", err
	}
	if payload.Version != "" {
		return payload.Version, nil
	}
	if payload.Major != 0 || payload.Minor != 0 || payload.Patch != 0 {
		return fmt.Sprintf("%d.%d.%d", payload.Major, payload.Minor, payload.Patch), nil
	}
	return "unknown", nil
}

func (c *Client) ListAssets(ctx context.Context, takenAfter *time.Time) ([]model.Asset, error) {
	var result []model.Asset
	for page := 1; ; page++ {
		body := map[string]any{
			"page":         page,
			"size":         DefaultPageSize,
			"order":        "asc",
			"withArchived": false,
		}
		if takenAfter != nil {
			body["takenAfter"] = takenAfter.UTC().Format(time.RFC3339Nano)
		}
		var response metadataSearchResponse
		if err := c.requestJSON(ctx, http.MethodPost, "/api/search/metadata", body, &response); err != nil {
			return nil, fmt.Errorf("list Immich assets page %d: %w", page, err)
		}
		for _, item := range response.Assets.Items {
			asset, err := item.model()
			if err != nil {
				return nil, err
			}
			result = append(result, asset)
		}
		if len(response.Assets.Items) < DefaultPageSize {
			return result, nil
		}
		// A total is optional in Immich's response. Prefer it when supplied;
		// otherwise a short page is the portable end-of-pagination sentinel.
		if response.Assets.Total > 0 && len(result) >= response.Assets.Total {
			return result, nil
		}
	}
}

func (c *Client) DownloadOriginal(ctx context.Context, id string, dst io.Writer) error {
	req, err := c.newRequest(ctx, http.MethodGet, "/api/assets/"+url.PathEscape(id)+"/original", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("download original: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return responseError(resp)
	}
	if _, err := io.Copy(dst, resp.Body); err != nil {
		return fmt.Errorf("write original: %w", err)
	}
	return nil
}

func (c *Client) requestJSON(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		encoded, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := c.newRequest(ctx, method, path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return responseError(resp)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode Immich response: %w", err)
	}
	return nil
}

func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("Accept", "application/json")
	return req, nil
}

type HTTPError struct {
	StatusCode int
	Status     string
}

func (e *HTTPError) Error() string { return fmt.Sprintf("Immich API %s", e.Status) }

func responseError(resp *http.Response) error {
	// Do not include the body: a proxy could echo sensitive request details.
	return &HTTPError{StatusCode: resp.StatusCode, Status: resp.Status}
}

type metadataSearchResponse struct {
	Assets struct {
		Items []assetDTO `json:"items"`
		Total int        `json:"total"`
	} `json:"assets"`
}

type assetDTO struct {
	ID               string `json:"id"`
	OriginalFileName string `json:"originalFileName"`
	OriginalMimeType string `json:"originalMimeType"`
	Checksum         string `json:"checksum"`
	FileCreatedAt    string `json:"fileCreatedAt"`
	UpdatedAt        string `json:"updatedAt"`
	ExifInfo         struct {
		FileSizeInByte json.RawMessage `json:"fileSizeInByte"`
	} `json:"exifInfo"`
}

func (a assetDTO) model() (model.Asset, error) {
	if a.ID == "" || a.OriginalFileName == "" {
		return model.Asset{}, fmt.Errorf("Immich returned an asset without an id or original file name")
	}
	created, err := parseTime(a.FileCreatedAt)
	if err != nil {
		return model.Asset{}, fmt.Errorf("asset %s fileCreatedAt: %w", a.ID, err)
	}
	updated, err := parseTime(a.UpdatedAt)
	if err != nil {
		updated = created // old Immich versions do not always include updatedAt.
	}
	return model.Asset{
		ID: a.ID, OriginalName: a.OriginalFileName, MIMEType: a.OriginalMimeType,
		Checksum: a.Checksum, ByteSize: parseByteSize(a.ExifInfo.FileSizeInByte),
		CreatedAt: created, UpdatedAt: updated,
	}, nil
}

func parseTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, fmt.Errorf("missing timestamp")
	}
	return time.Parse(time.RFC3339, value)
}

func parseByteSize(raw json.RawMessage) int64 {
	var n int64
	if json.Unmarshal(raw, &n) == nil {
		return n
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		var n int64
		_, _ = fmt.Sscan(s, &n)
		return n
	}
	return 0
}
