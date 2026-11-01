package immich_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/immich-relay/immich-relay/internal/immich"
)

func TestListAssetsPaginatesAndPassesIncrementalCursor(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "secret" {
			t.Error("API key header missing")
		}
		if r.URL.Path != "/api/search/metadata" {
			t.Errorf("path = %s", r.URL.Path)
		}
		var body struct {
			Page       int    `json:"page"`
			TakenAfter string `json:"takenAfter"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.TakenAfter == "" {
			t.Error("takenAfter was not sent")
		}
		requests++
		items := make([]string, 0)
		if body.Page == 1 {
			for i := 0; i < immich.DefaultPageSize; i++ {
				items = append(items, fmt.Sprintf(`{"id":"%03d","originalFileName":"x.jpg","originalMimeType":"image/jpeg","fileCreatedAt":"2026-01-01T00:00:00Z"}`, i))
			}
		}
		fmt.Fprintf(w, `{"assets":{"items":[%s],"total":251}}`, strings.Join(items, ","))
	}))
	defer server.Close()
	c, err := immich.New(server.URL, "secret")
	if err != nil {
		t.Fatal(err)
	}
	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	assets, err := c.ListAssets(context.Background(), &after)
	if err != nil {
		t.Fatal(err)
	}
	if len(assets) != immich.DefaultPageSize {
		t.Fatalf("assets = %d", len(assets))
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
}
