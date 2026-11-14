package crud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/internal/naming"
	"github.com/reliant-labs/forge/pkg/authn"
	"github.com/reliant-labs/forge/pkg/observe"
	"github.com/reliant-labs/forge/pkg/orm"
)

// Fixture types named the way generated code names them, so the fallback
// that derives an event name from a type name is exercised on real shapes.
type OrderItem struct{ ID string }
type DeleteOrderItemRequest struct{ ID string }

// eventLog captures the records a handler writes through the context's
// logger, one decoded JSON object per record.
type eventLog struct{ buf bytes.Buffer }

func (l *eventLog) ctx(t *testing.T) context.Context {
	t.Helper()
	ctx := observe.WithLogger(context.Background(), slog.New(slog.NewJSONHandler(&l.buf, nil)))
	return authn.ContextWithClaims(ctx, &authn.Claims{UserID: "u-1"})
}

// events returns the event.name of every product event recorded.
func (l *eventLog) events(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(l.buf.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("bad log line %q: %v", line, err)
		}
		if n, ok := m[observe.AttrEventName].(string); ok {
			out = append(out, n)
		}
	}
	return out
}

func withAutoEvents(t *testing.T, on bool) {
	t.Helper()
	prev := autoProductEvents
	autoProductEvents = func() bool { return on }
	t.Cleanup(func() { autoProductEvents = prev })
}

func createOp(err error) CreateOp[createReq, createResp, *OrderItem] {
	return CreateOp[createReq, createResp, *OrderItem]{
		EntityLower: "orderitem",
		Entity:      func(context.Context, *createReq) (*OrderItem, error) { return &OrderItem{ID: "secret-id"}, nil },
		Persist:     func(context.Context, *OrderItem) error { return err },
		Pack:        func(*OrderItem) (*createResp, error) { return &createResp{}, nil },
	}
}

func updateOp(err error) UpdateOp[updateReq, updateResp, *OrderItem] {
	return UpdateOp[updateReq, updateResp, *OrderItem]{
		EntityLower:    "orderitem",
		EntityFieldLow: "order_item",
		Entity:         func(context.Context, *updateReq) (*OrderItem, error) { return &OrderItem{ID: "secret-id"}, nil },
		Persist:        func(context.Context, *OrderItem, ...orm.QueryOption) error { return err },
		Mask:           func(r *updateReq) []string { return r.Mask },
		PersistMasked:  func(context.Context, *OrderItem, []string, ...orm.QueryOption) error { return err },
		Pack:           func(*OrderItem) (*updateResp, error) { return &updateResp{}, nil },
	}
}

func deleteOp(err error) DeleteOp[DeleteOrderItemRequest, deleteResp] {
	return DeleteOp[DeleteOrderItemRequest, deleteResp]{
		EntityLower: "orderitem",
		ID:          func(r *DeleteOrderItemRequest) string { return r.ID },
		Persist:     func(context.Context, string, ...orm.QueryOption) error { return err },
	}
}

// TestCRUDWritesEmitProductEvents: a write that landed records one
// <entity>_<verb> event, named in snake_case even from an op generated before
// EntitySnake existed, carrying the caller and no id or field value.
func TestCRUDWritesEmitProductEvents(t *testing.T) {
	withAutoEvents(t, true)
	var l eventLog
	ctx := l.ctx(t)

	if _, err := HandleCreate(createOp(nil))(ctx, connect.NewRequest(&createReq{Name: "n", Email: "e@x"})); err != nil {
		t.Fatal(err)
	}
	if _, err := HandleUpdate(updateOp(nil))(ctx, connect.NewRequest(&updateReq{})); err != nil {
		t.Fatal(err)
	}
	if _, err := HandleUpdate(updateOp(nil))(ctx, connect.NewRequest(&updateReq{Mask: []string{"name"}})); err != nil {
		t.Fatal(err)
	}
	if _, err := HandleDelete(deleteOp(nil))(ctx, connect.NewRequest(&DeleteOrderItemRequest{ID: "secret-id"})); err != nil {
		t.Fatal(err)
	}

	want := []string{
		"product.order_item_created", "product.order_item_updated",
		"product.order_item_updated", "product.order_item_deleted",
	}
	if got := l.events(t); !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	out := l.buf.String()
	if strings.Contains(out, "secret-id") || strings.Contains(out, "e@x") {
		t.Errorf("an event carries an id or a field value: %s", out)
	}
	if strings.Count(out, `"user.id":"u-1"`) != 4 {
		t.Errorf("every event should carry the caller: %s", out)
	}
	if strings.Contains(out, `,"`+observe.AttrPropPrefix) {
		t.Errorf("an event carries properties: %s", out)
	}
}

// TestCRUDEventsUseEntitySnake: the generated op's EntitySnake names the event
// whatever the Go types are called.
func TestCRUDEventsUseEntitySnake(t *testing.T) {
	withAutoEvents(t, true)
	var l eventLog
	ctx := l.ctx(t)

	c := createOp(nil)
	c.EntitySnake = "line_item"
	d := deleteOp(nil)
	d.EntitySnake = "line_item"
	if _, err := HandleCreate(c)(ctx, connect.NewRequest(&createReq{})); err != nil {
		t.Fatal(err)
	}
	if _, err := HandleDelete(d)(ctx, connect.NewRequest(&DeleteOrderItemRequest{})); err != nil {
		t.Fatal(err)
	}
	want := []string{"product.line_item_created", "product.line_item_deleted"}
	if got := l.events(t); !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
}

// TestCRUDEventsOff: FORGE_PRODUCT_EVENTS=off records nothing and the rpc
// still succeeds.
func TestCRUDEventsOff(t *testing.T) {
	withAutoEvents(t, false)
	var l eventLog
	ctx := l.ctx(t)
	if _, err := HandleCreate(createOp(nil))(ctx, connect.NewRequest(&createReq{})); err != nil {
		t.Fatal(err)
	}
	if _, err := HandleUpdate(updateOp(nil))(ctx, connect.NewRequest(&updateReq{})); err != nil {
		t.Fatal(err)
	}
	if _, err := HandleDelete(deleteOp(nil))(ctx, connect.NewRequest(&DeleteOrderItemRequest{})); err != nil {
		t.Fatal(err)
	}
	if got := l.events(t); len(got) != 0 {
		t.Fatalf("events with product events off: %v", got)
	}
}

// TestCRUDFailedWritesEmitNothing: a write the repository refused records no
// event.
func TestCRUDFailedWritesEmitNothing(t *testing.T) {
	withAutoEvents(t, true)
	var l eventLog
	ctx := l.ctx(t)
	boom := errors.New("boom")
	if _, err := HandleCreate(createOp(boom))(ctx, connect.NewRequest(&createReq{})); err == nil {
		t.Fatal("create: want an error")
	}
	if _, err := HandleUpdate(updateOp(boom))(ctx, connect.NewRequest(&updateReq{})); err == nil {
		t.Fatal("update: want an error")
	}
	if _, err := HandleUpdate(updateOp(boom))(ctx, connect.NewRequest(&updateReq{Mask: []string{"name"}})); err == nil {
		t.Fatal("masked update: want an error")
	}
	if _, err := HandleDelete(deleteOp(orm.ErrNoRows))(ctx, connect.NewRequest(&DeleteOrderItemRequest{})); err == nil {
		t.Fatal("delete: want an error")
	}
	if got := l.events(t); len(got) != 0 {
		t.Fatalf("events for failed writes: %v", got)
	}
}

func TestEventEntity(t *testing.T) {
	tests := map[string]struct {
		snake, lower         string
		t                    reflect.Type
		prefix, suffix, want string
	}{
		"explicit wins":          {snake: "x_y", lower: "xy", t: reflect.TypeFor[*OrderItem](), want: "x_y"},
		"entity pointer":         {lower: "orderitem", t: reflect.TypeFor[*OrderItem](), want: "order_item"},
		"delete request":         {lower: "orderitem", t: reflect.TypeFor[DeleteOrderItemRequest](), prefix: "Delete", suffix: "Request", want: "order_item"},
		"request not named so":   {lower: "orderitem", t: reflect.TypeFor[OrderItem](), prefix: "Delete", suffix: "Request", want: "orderitem"},
		"unnamed type":           {lower: "thing", t: reflect.TypeFor[map[string]string](), want: "thing"},
		"nothing to go on":       {want: ""},
		"generic instantiation":  {lower: "box", t: reflect.TypeFor[genericBox[int]](), want: "generic_box"},
		"unexported test entity": {lower: "user", t: reflect.TypeFor[*user](), want: "user"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := eventEntity(tc.snake, tc.lower, tc.t, tc.prefix, tc.suffix); got != tc.want {
				t.Errorf("eventEntity = %q, want %q", got, tc.want)
			}
		})
	}
}

type genericBox[T any] struct{ v T }

// TestSnakeCaseMatchesGenerator: the fallback must spell a name exactly as the
// generator's EntitySnake does, or a project's event names change the day it
// regenerates.
func TestSnakeCaseMatchesGenerator(t *testing.T) {
	for _, n := range []string{
		"User", "OrderItem", "APIKey", "HTTPStatus", "Oauth2token", "Oauth2Token",
		"IDPConfig", "S3Bucket", "Patient", "a", "ABC", "X509Cert", "Ünïcode", "lowercase",
	} {
		if got, want := snakeCase(n), naming.ToSnakeCase(n); got != want {
			t.Errorf("snakeCase(%q) = %q, generator spells %q", n, got, want)
		}
	}
}
