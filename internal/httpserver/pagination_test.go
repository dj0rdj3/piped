package httpserver

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"
)

func TestListCursorSurvivesDeletionAndNameTies(t *testing.T) {
	type item struct{ ID, Name string }
	items := []item{{ID: "a", Name: "same"}, {ID: "b", Name: "same"}, {ID: "c", Name: "Zulu"}}
	page := func(items []item, query string) ([]string, string) {
		t.Helper()
		w := httptest.NewRecorder()
		if err := listResponse(w, httptest.NewRequest("GET", "/api/hosts?limit=1"+query, nil), "hosts", items, func(value item) listEntry { return listEntry{id: value.ID, name: value.Name, search: value.Name} }, nil); err != nil {
			t.Fatal(err)
		}
		var result struct {
			Hosts []item
			Page  struct {
				Next string `json:"next_cursor"`
			}
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		ids := make([]string, 0, len(result.Hosts))
		for _, value := range result.Hosts {
			ids = append(ids, value.ID)
		}
		return ids, result.Page.Next
	}
	ids, cursor := page(items, "")
	if !slices.Equal(ids, []string{"a"}) {
		t.Fatal("first tie is out of order")
	}
	if cursor == "" {
		t.Fatal("missing cursor")
	}
	ids, next := page(items[1:], "&cursor="+url.QueryEscape(cursor))
	if !slices.Equal(ids, []string{"b"}) || next == "" {
		t.Fatal("deleted cursor row skipped the next tie")
	}
	ids, last := page(items[2:], "&cursor="+url.QueryEscape(next))
	if !slices.Equal(ids, []string{"c"}) || last != "" {
		t.Fatal("tail missing after deletion")
	}
}
