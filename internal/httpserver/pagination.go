package httpserver

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"piped/internal/httpserver/response"
)

type listEntry struct {
	id     string
	name   string
	search string
	tags   []string
}

type listCursor struct {
	Key string `json:"key"`
	ID  string `json:"id"`
}

func (cursor listCursor) before(other listCursor) bool {
	if cursor.Key == other.Key {
		return cursor.ID < other.ID
	}
	return cursor.Key < other.Key
}

// Pagination operates on already-authorized public metadata. Unpaged reads remain
// available for form choices and integrations that need the complete collection.
func listResponse[T any](w http.ResponseWriter, r *http.Request, field string, items []T, describe func(T) listEntry, metadata map[string]any) error {
	if metadata == nil {
		metadata = map[string]any{}
	}
	values := r.URL.Query()
	if ids := values.Get("ids"); ids != "" {
		wanted := strings.Split(ids, ",")
		if len(wanted) > 100 {
			return response.Fail(400, "request at most 100 IDs")
		}
		selected := make(map[string]bool, len(wanted))
		for _, id := range wanted {
			selected[id] = true
		}
		filtered := make([]T, 0)
		for _, item := range items {
			if selected[describe(item).id] {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	if !values.Has("limit") {
		if values.Has("cursor") {
			return response.Fail(400, "cursor requires limit")
		}
		metadata[field] = items
		response.JSON(w, 200, metadata)
		return nil
	}
	limit, err := strconv.Atoi(values.Get("limit"))
	if err != nil || limit < 1 || limit > 200 {
		return response.Fail(400, "limit must be between 1 and 200")
	}
	var cursor listCursor
	if encoded := values.Get("cursor"); encoded != "" {
		if len(encoded) > 4096 {
			return response.Fail(400, "invalid list cursor")
		}
		decoded, err := base64.RawURLEncoding.DecodeString(encoded)
		if err != nil || json.Unmarshal(decoded, &cursor) != nil || cursor.ID == "" {
			return response.Fail(400, "invalid list cursor")
		}
	}
	query := strings.ToLower(values.Get("q"))
	tag := values.Get("tag")
	total := len(items)
	tags := map[string]bool{}
	type describedItem struct {
		value  T
		cursor listCursor
	}
	filtered := make([]describedItem, 0, len(items))
	for _, item := range items {
		entry := describe(item)
		tagMatches := tag == ""
		for _, value := range entry.tags {
			tags[value] = true
			if value == tag {
				tagMatches = true
			}
		}
		if tagMatches && strings.Contains(strings.ToLower(entry.search), query) {
			filtered = append(filtered, describedItem{value: item, cursor: listCursor{
				Key: strings.ToLower(entry.name) + "\x00" + entry.name, ID: entry.id,
			}})
		}
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].cursor.before(filtered[j].cursor) })
	start := 0
	if cursor.ID != "" {
		start = sort.Search(len(filtered), func(i int) bool { return cursor.before(filtered[i].cursor) })
	}
	end := min(start+limit, len(filtered))
	next := ""
	if end < len(filtered) {
		encoded, err := json.Marshal(filtered[end-1].cursor)
		if err != nil {
			return err
		}
		next = base64.RawURLEncoding.EncodeToString(encoded)
	}
	page := make([]T, 0, end-start)
	for _, item := range filtered[start:end] {
		page = append(page, item.value)
	}
	metadata[field] = page
	metadata["page"] = map[string]any{"total": total, "matched": len(filtered), "next_cursor": next}
	if field == "hosts" {
		allTags := make([]string, 0, len(tags))
		for value := range tags {
			allTags = append(allTags, value)
		}
		sort.Strings(allTags)
		metadata["tags"] = allTags
	}
	response.JSON(w, 200, metadata)
	return nil
}
