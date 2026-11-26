package httpserver

import (
	"errors"
	"net/http"

	"piped/internal/httpserver/response"
	"piped/internal/notifications"
	"piped/internal/store"
)

func notificationFailure(err error) error {
	switch {
	case errors.Is(err, notifications.ErrInput), errors.Is(err, store.ErrNotificationType), errors.Is(err, store.ErrNotificationSecret):
		return response.Fail(400, err.Error())
	case errors.Is(err, store.ErrNotificationChanged), errors.Is(err, store.ErrNotificationInUse):
		return response.Fail(409, err.Error())
	case errors.Is(err, store.ErrNotFound):
		return response.Fail(404, "notification target not found")
	default:
		return err
	}
}

func (s *Server) listNotifications(w http.ResponseWriter, r *http.Request, actor store.Actor) error {
	return listResponse(w, r, actor, "targets", s.auth.Store.NotificationsPage, nil)
}

func (s *Server) saveNotification(w http.ResponseWriter, r *http.Request, actor store.Actor) error {
	var input *notifications.Input
	if err := response.DecodeLimit(w, r, &input, 1024*1024); err != nil {
		return err
	}
	if input == nil {
		return response.Fail(400, "provide a notification target object")
	}
	defer func() {
		input.Endpoint = nil
		for index := range input.Secrets {
			input.Secrets[index].Value = nil
		}
	}()
	target, err := s.config.Notifications.Save(r.Context(), actor, r.PathValue("id"), *input)
	if err != nil {
		return notificationFailure(err)
	}
	status := 200
	if r.Method == http.MethodPost {
		status = 201
	}
	response.JSON(w, status, map[string]any{"target": target})
	return nil
}

func (s *Server) deleteNotification(w http.ResponseWriter, r *http.Request, actor store.Actor) error {
	var input struct {
		Version int64 `json:"version"`
	}
	if err := response.Decode(w, r, &input); err != nil {
		return err
	}
	if err := s.config.Notifications.Delete(r.Context(), actor, r.PathValue("id"), input.Version); err != nil {
		return notificationFailure(err)
	}
	w.WriteHeader(204)
	return nil
}
