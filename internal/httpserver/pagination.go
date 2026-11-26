package httpserver

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"piped/internal/httpserver/response"
	"piped/internal/store"
)

func listResponse[T any](w http.ResponseWriter, r *http.Request, actor store.Actor, field string,
	read func(context.Context, store.Actor, store.ListRequest) (store.Page[T], error), metadata map[string]any) error {
	values := r.URL.Query()
	limit := 0
	if !values.Has("limit") && values.Has("cursor") {
		return response.Fail(400, "cursor requires limit")
	}
	if values.Has("limit") {
		value, err := strconv.Atoi(values.Get("limit"))
		if err != nil || value < 1 || value > 200 {
			return response.Fail(400, "limit must be between 1 and 200")
		}
		limit = value
	}
	request := store.ListRequest{Limit: limit, Cursor: values.Get("cursor"), Query: values.Get("q"), Tag: values.Get("tag")}
	if raw := values.Get("ids"); raw != "" {
		request.IDs = strings.Split(raw, ",")
	}
	page, err := read(r.Context(), actor, request)
	if errors.Is(err, store.ErrListRequest) {
		return response.Fail(400, err.Error())
	}
	if err != nil {
		return err
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata[field] = page.Items
	if limit > 0 {
		metadata["page"] = page.Info
		if field == "hosts" {
			metadata["tags"] = page.Tags
		}
	}
	response.JSON(w, 200, metadata)
	return nil
}
