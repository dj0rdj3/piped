package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"

	"piped/internal/problem"
)

var ErrListRequest = problem.New(problem.Invalid, "invalid list request")

type ListRequest struct {
	Limit  int      `json:"limit,omitempty"` // Zero requests a complete read or explicit ID selection.
	Cursor string   `json:"cursor,omitempty"`
	Query  string   `json:"q,omitempty"`
	Tag    string   `json:"tag,omitempty"`
	IDs    []string `json:"ids,omitempty"`
}

type ListInfo struct {
	Total      int    `json:"total"`
	Matched    int    `json:"matched"`
	NextCursor string `json:"next_cursor"`
}

type Page[T any] struct {
	Items []T
	Info  ListInfo
	Tags  []string
}

type listCursor struct {
	Key string `json:"key"`
	ID  string `json:"id"`
}

func listIDArgs(ids []string) []any {
	args := make([]any, len(ids))
	for index, id := range ids {
		args[index] = id
	}
	return args
}

func listIDFilter(column string, ids []string) string {
	if ids == nil {
		return ""
	}
	if len(ids) == 0 {
		return " WHERE 1 = 0"
	}
	return " WHERE " + column + " IN (" + placeholders(len(ids)) + ")"
}

func listIDCondition(column string, ids []string) string {
	return strings.Replace(listIDFilter(column, ids), " WHERE ", " AND ", 1)
}

// readPage counts, filters, seeks and loads only the requested records under one
// fresh authorization transaction. Cursor values are data, never SQL fragments.
func readPage[T any](ctx context.Context, s *Store, actor Actor, kind, scope string, request ListRequest,
	load func(context.Context, *authorizedTransaction, Actor, []string) ([]T, error), id func(T) string) (Page[T], error) {
	var result Page[T]
	var cursor listCursor
	if request.Limit < 0 || request.Limit > 200 || len(request.IDs) > 100 || len(request.Cursor) > 4096 {
		return result, ErrListRequest
	}
	for _, text := range append([]string{request.Query, request.Tag}, request.IDs...) {
		if !utf8.ValidString(text) || strings.ContainsRune(text, '\x00') {
			return result, ErrListRequest
		}
	}
	if request.Limit == 0 && request.Cursor != "" {
		return result, ErrListRequest
	}
	if request.Cursor != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(request.Cursor)
		if err != nil || json.Unmarshal(decoded, &cursor) != nil || cursor.ID == "" || strings.ContainsRune(cursor.ID, 0) {
			return result, ErrListRequest
		}
	}
	var tx *authorizedTransaction
	var err error
	if kind == "keys" {
		tx, err = s.beginKeyManagement(ctx, actor)
	} else {
		tx, err = s.beginAuthorized(ctx, actor, scope)
	}
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if request.Limit == 0 {
		if len(request.IDs) == 0 {
			result.Items, err = load(ctx, tx, actor, nil)
			if err != nil {
				return result, err
			}
			return result, tx.Commit()
		}
		// At most 100 explicit IDs: resolve their canonical forms in the index,
		// then load those rows only. This also avoids casting arbitrary IDs to SQL integers.
		request.Limit = 200
		request.Query, request.Tag = "", ""
	}
	where := " WHERE e.kind = ? AND e.owner_id = ?"
	owner := int64(0)
	if kind == "keys" {
		owner = actor.ID
	}
	args := []any{kind, owner}
	if len(request.IDs) > 0 {
		where += listIDCondition("e.id", request.IDs)
		args = append(args, listIDArgs(request.IDs)...)
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM list_entries e`+where, args...).Scan(&result.Info.Total); err != nil {
		return result, err
	}
	if kind == "hosts" {
		result.Tags, err = tx.queryStrings(ctx, `SELECT DISTINCT t.tag FROM list_entries e JOIN host_tags t ON e.kind = 'hosts' AND t.host_id = e.id`+where+` ORDER BY t.tag`, args...)
		if err != nil {
			return result, err
		}
		sort.Strings(result.Tags)
	}
	search := "e.search_text"
	if kind == "jobs" && request.Query != "" {
		if err := authorize(ctx, tx, actor, ScopeHostsRead); err == nil {
			search = "e.host_search"
		} else if !errors.Is(err, ErrForbidden) {
			return result, err
		}
	}
	if request.Query != "" {
		where += " AND " + s.db.adapter().contains(search)
		args = append(args, strings.ToLower(request.Query))
	}
	if request.Tag != "" {
		where += ` AND EXISTS(SELECT 1 FROM host_tags t WHERE e.kind = 'hosts' AND t.host_id = e.id AND t.tag = ?)`
		args = append(args, request.Tag)
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM list_entries e`+where, args...).Scan(&result.Info.Matched); err != nil {
		return result, err
	}
	if cursor.ID != "" {
		where += ` AND (e.name_key > ? OR (e.name_key = ? AND e.id > ?))`
		args = append(args, []byte(cursor.Key), []byte(cursor.Key), cursor.ID)
	}
	args = append(args, request.Limit+1)
	rows, err := tx.QueryContext(ctx, `SELECT e.id, e.name_key FROM list_entries e`+where+` ORDER BY e.name_key, e.id LIMIT ?`, args...)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	ids := []string{}
	var last listCursor
	for rows.Next() {
		var item listCursor
		if err := rows.Scan(&item.ID, &item.Key); err != nil {
			return result, err
		}
		if len(ids) == request.Limit {
			encoded, err := json.Marshal(last)
			if err != nil {
				return result, err
			}
			result.Info.NextCursor = base64.RawURLEncoding.EncodeToString(encoded)
			break
		}
		last = item
		ids = append(ids, item.ID)
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	if err := rows.Close(); err != nil {
		return result, err
	}
	items, err := load(ctx, tx, actor, ids)
	if err != nil {
		return result, err
	}
	byID := make(map[string]T, len(items))
	for _, item := range items {
		byID[id(item)] = item
	}
	result.Items = make([]T, 0, len(ids))
	for _, itemID := range ids {
		item, exists := byID[itemID]
		if !exists {
			return result, errors.New("list metadata is inconsistent")
		}
		result.Items = append(result.Items, item)
	}
	return result, tx.Commit()
}

func (s *Store) HostsPage(ctx context.Context, actor Actor, request ListRequest) (Page[Host], error) {
	return readPage(ctx, s, actor, "hosts", ScopeHostsRead, request, readHosts, func(item Host) string { return item.ID })
}
func (s *Store) CredentialsPage(ctx context.Context, actor Actor, request ListRequest) (Page[Credential], error) {
	return readPage(ctx, s, actor, "credentials", ScopeCredentialsRead, request, readCredentials, func(item Credential) string { return item.ID })
}
func (s *Store) JobsPage(ctx context.Context, actor Actor, request ListRequest) (Page[Job], error) {
	return readPage(ctx, s, actor, "jobs", ScopeJobsRead, request, readJobs, func(item Job) string { return item.ID })
}
func (s *Store) NotificationsPage(ctx context.Context, actor Actor, request ListRequest) (Page[NotificationTarget], error) {
	return readPage(ctx, s, actor, "notifications", ScopeNotificationsRead, request, readNotifications, func(item NotificationTarget) string { return item.ID })
}
func (s *Store) APIKeysPage(ctx context.Context, actor Actor, request ListRequest) (Page[APIKey], error) {
	return readPage(ctx, s, actor, "keys", "", request, readKeys, func(item APIKey) string { return item.ID })
}
func (s *Store) UsersPage(ctx context.Context, actor Actor, request ListRequest) (Page[User], error) {
	return readPage(ctx, s, actor, "users", ScopeUsersRead, request, readUsers, func(item User) string { return userListID(item.ID) })
}

func readUsers(ctx context.Context, tx *authorizedTransaction, _ Actor, ids []string) ([]User, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, username, created_at FROM users`+listIDFilter("id", ids)+` ORDER BY username`, listIDArgs(ids)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []User{}
	for rows.Next() {
		var item User
		if err := rows.Scan(&item.ID, &item.Username, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
