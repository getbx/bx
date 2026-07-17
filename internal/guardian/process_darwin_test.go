//go:build darwin

package guardian

import (
	"testing"

	"golang.org/x/sys/unix"
)

func TestDarwinProcessGenerationUsesStartTime(t *testing.T) {
	got, err := darwinProcessGeneration(unix.Timeval{Sec: 123, Usec: 456})
	if err != nil {
		t.Fatal(err)
	}
	if want := "darwin:123:456"; got != want {
		t.Fatalf("generation = %q, want %q", got, want)
	}
	if _, err := darwinProcessGeneration(unix.Timeval{}); err == nil {
		t.Fatal("zero Darwin start time accepted")
	}
}
