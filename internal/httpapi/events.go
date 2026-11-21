package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/ordo-systems/wrench/api"
	"github.com/ordo-systems/wrench/internal/auth"
	"github.com/ordo-systems/wrench/internal/config"
	"github.com/ordo-systems/wrench/internal/events"
)

// keepAlive is how often an idle stream sends a comment line so proxies keep the connection open.
var keepAlive = 25 * time.Second

// StreamEvents subscribes the caller before answering, so no event published after the response starts is missed.
func (s *server) StreamEvents(ctx context.Context, _ api.StreamEventsRequestObject) (api.StreamEventsResponseObject, error) {
	if s.deps.Events == nil {
		return api.StreamEvents404JSONResponse{NotFoundJSONResponse: notFoundBody("this server has no event stream")}, nil
	}
	id, _ := auth.FromContext(ctx)
	ch, unsubscribe := s.deps.Events.Subscribe(id.Email, id.Role == config.RoleAdmin)
	return eventStream{ctx: ctx, events: ch, unsubscribe: unsubscribe}, nil
}

// eventStream is a streaming response: the generated event-stream type copies without flushing, so it would
// hold every frame back.
type eventStream struct {
	ctx         context.Context
	events      <-chan events.Topic
	unsubscribe func()
}

// VisitStreamEventsResponse writes one frame per topic and flushes each, until the request ends.
func (e eventStream) VisitStreamEventsResponse(w http.ResponseWriter) error {
	defer e.unsubscribe()
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	if err := rc.Flush(); err != nil {
		return err
	}
	ticker := time.NewTicker(keepAlive)
	defer ticker.Stop()
	for {
		var frame string
		select {
		case <-e.ctx.Done():
			return nil
		case t := <-e.events:
			frame = fmt.Sprintf("event: %s\ndata:\n\n", t)
		case <-ticker.C:
			frame = ": keep-alive\n\n"
		}
		if _, err := fmt.Fprint(w, frame); err != nil {
			return nil
		}
		if err := rc.Flush(); err != nil {
			return nil
		}
	}
}
