package probe

import (
	"strings"
	"testing"
	"time"
)

// fakeRadio answers writes from a canned reply map and delivers replies a few bytes at a time.
type fakeRadio struct {
	replies map[string]string
	pending string
	written []string
}

func (f *fakeRadio) Write(p []byte) (int, error) {
	f.written = append(f.written, string(p))
	f.pending += f.replies[string(p)]
	return len(p), nil
}

func (f *fakeRadio) Read(p []byte) (int, error) {
	if f.pending == "" {
		time.Sleep(time.Millisecond) // behaves like a serial read timeout
		return 0, nil
	}
	n := copy(p[:min(3, len(p))], f.pending)
	f.pending = f.pending[n:]
	return n, nil
}

func TestQuerySkipsUnrelatedAndReassembles(t *testing.T) {
	f := &fakeRadio{replies: map[string]string{"VN;": "IF00014074000;VN1_04_004QMX;"}}
	got, err := Query(f, "VN", 200*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if got != "VN1_04_004QMX;" {
		t.Fatalf("got %q", got)
	}
	if f.written[0] != "VN;" {
		t.Fatalf("wrote %q", f.written[0])
	}
}

func TestQueryTimeout(t *testing.T) {
	f := &fakeRadio{replies: map[string]string{}}
	_, err := Query(f, "Q9;", 20*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "no reply") {
		t.Fatalf("err = %v", err)
	}
}
