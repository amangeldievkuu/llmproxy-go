package main

import (
	"bufio"
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amangeldievkuu/llmproxy-go/internal/store"
)

// startServe runs serve against upstream with a fresh database and returns the
// proxy's address, the database path, a function that stops serve, and a
// channel that receives serve's result.
func startServe(t *testing.T, upstream string) (addr, dbPath string, stop func(), result <-chan error) {
	t.Helper()
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	dbPath = filepath.Join(t.TempDir(), "test.db")
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	errc := make(chan error, 1)
	go func() {
		errc <- serve(ctx, []string{"-port", "0", "-upstream", upstream, "-db", dbPath}, func(a string) { ready <- a })
	}()
	select {
	case addr = <-ready:
	case err := <-errc:
		t.Fatalf("serve: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not start")
	}
	t.Cleanup(cancel)
	return addr, dbPath, cancel, errc
}

// wait returns serve's result, failing the test if it does not end.
func wait(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not shut down")
		return nil
	}
}

func summary(t *testing.T, dbPath string, since time.Duration) []store.Row {
	t.Helper()
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rows, err := st.Summary(context.Background(), time.Now().Add(-since), "model")
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestServeRecordsAndShutsDown(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"model":"gpt-4o-mini","usage":{"prompt_tokens":10,"completion_tokens":14}}`)
	}))
	defer upstream.Close()
	addr, dbPath, stop, result := startServe(t, upstream.URL)

	if !strings.HasPrefix(addr, "127.0.0.1:") {
		t.Errorf("listening on %s, want localhost only by default", addr)
	}
	resp, err := http.Post("http://"+addr+"/v1/chat/completions", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	stop()
	if err := wait(t, result); err != nil {
		t.Fatalf("serve returned %v", err)
	}
	rows := summary(t, dbPath, time.Hour)
	if len(rows) != 1 || rows[0].Requests != 1 || rows[0].InputTokens != 10 {
		t.Errorf("unexpected records %+v", rows)
	}
}

func TestServeWaitsForOpenStreams(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"model\":\"gpt-4o-mini\",\"usage\":null}\n\n")
		w.(http.Flusher).Flush()
		<-release
		io.WriteString(w, "data: {\"model\":\"gpt-4o-mini\",\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2}}\n\n")
	}))
	defer upstream.Close()
	addr, dbPath, stop, result := startServe(t, upstream.URL)

	resp, err := http.Post("http://"+addr+"/v1/chat/completions", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	if _, err := br.ReadString('\n'); err != nil { // the stream is open
		t.Fatal(err)
	}

	stop()
	select {
	case err := <-result:
		t.Fatalf("serve returned %v while a stream was still open", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	io.Copy(io.Discard, br)

	if err := wait(t, result); err != nil {
		t.Fatalf("serve returned %v", err)
	}
	rows := summary(t, dbPath, time.Hour)
	if len(rows) != 1 || rows[0].OutputTokens != 2 {
		t.Errorf("open stream not recorded before shutdown: %+v", rows)
	}
}

func TestServeRejectsBadConfig(t *testing.T) {
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	db := filepath.Join(t.TempDir(), "test.db")
	for name, args := range map[string][]string{
		"upstream": {"-upstream", "not a url", "-db", db},
		"prices":   {"-prices", filepath.Join(t.TempDir(), "missing.json"), "-db", db},
		"port":     {"-port", "not-a-port", "-db", db},
	} {
		if err := serve(context.Background(), args, nil); err == nil {
			t.Errorf("bad -%s: expected an error", name)
		}
	}
}

func TestServeRetention(t *testing.T) {
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	dbPath := oldAndNewDB(t)

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		errc <- serve(ctx, []string{"-port", "0", "-db", dbPath, "-retention", "90d"}, func(string) { cancel() })
	}()
	if err := wait(t, errc); err != nil {
		t.Fatal(err)
	}
	if n := countRecords(t, dbPath); n != 1 {
		t.Errorf("%d records left, want only the recent one", n)
	}
}
