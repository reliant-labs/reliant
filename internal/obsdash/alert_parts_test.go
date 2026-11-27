package obsdash

import (
	"reflect"
	"testing"
)

func TestComposeAlertServices(t *testing.T) {
	own := []AlertService{
		{Name: "orders", RPCService: "orders.v1.OrdersService"},
		// A client mirror of the composed project's service: same rpc
		// service, the deploying project's own name.
		{Name: "upstream_project", RPCService: "up.v1.ProjectService"},
	}
	composed := []AlertService{
		{Name: "project", RPCService: "up.v1.ProjectService"},
		{Name: "chat", RPCService: "up.v1.ChatService"},
		{Name: "orders", RPCService: "up.v1.OrdersService"},
	}
	other := []AlertService{{Name: "chat", RPCService: "up.v1.ChatService"}}

	got := ComposeAlertServices(own, composed, other)
	want := []AlertService{
		{Name: "chat", RPCService: "up.v1.ChatService"},
		{Name: "orders", RPCService: "orders.v1.OrdersService"},
		{Name: "orders", RPCService: "up.v1.OrdersService"},
		{Name: "upstream_project", RPCService: "up.v1.ProjectService"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ComposeAlertServices =\n  %+v\nwant\n  %+v", got, want)
	}

	// Nothing composed: own, in name order, unchanged.
	if got := ComposeAlertServices(own); !reflect.DeepEqual(got, own) {
		t.Fatalf("own alone = %+v, want %+v", got, own)
	}
}
