package toolkeys

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"
)

func TestDaemonUsesIndependentUnixSocket(t *testing.T) {
	path := "/tmp/bx-keyd-" + fmt.Sprint(os.Getpid()) + ".sock"
	d, err := StartDaemon(context.Background(), path, http.NewServeMux())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if d.SocketPath() != path {
		t.Fatalf("socket=%q", d.SocketPath())
	}
	client := localHTTPClient(path)
	defer client.CloseIdleConnections()
	deadline := time.Now().Add(time.Second)
	for {
		resp, err := client.Get("http://local/")
		if err == nil {
			_ = resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("socket not serving: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
