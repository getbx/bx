package mcp

import (
	"context"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestServerPing(t *testing.T) {
	ctx := context.Background()
	srv := newServer(nil)
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test", Version: "v0"}, nil)

	st, ct := mcpsdk.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer cs.Close()

	res, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "bx_ping", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("call bx_ping: %v", err)
	}
	if res.IsError {
		t.Fatalf("bx_ping returned error result")
	}
}
