package spinneret_test

import (
	"context"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/TikHub/Spinneret/sdk/go/spinneret"
)

// A node acquires a lease, sends the request with the leased credential and
// proxy, and reports the outcome. Close releases the lease with the last report.
func ExampleClient_Lease() {
	ctx := context.Background()
	client, err := spinneret.New(spinneret.Options{BaseURL: "https://spinneret.internal", Token: "spn_xxx"})
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = client.Close(ctx) }()

	target := "https://target.example.com/api/v1/search?keyword=go"
	lease, err := client.Lease(ctx, &spinneret.AcquireRequest{Site: "shop", Client: "web", Uri: target})
	switch {
	case spinneret.IsNoIdentity(err):
		time.Sleep(spinneret.RetryAfterOf(err))
		return
	case spinneret.IsCircuitOpen(err), spinneret.IsSitePaused(err):
		return // pause this endpoint group or site
	case err != nil:
		log.Fatal(err)
	}
	defer func() { _ = lease.Close(ctx) }()

	transport, err := lease.Transport(nil)
	if err != nil {
		log.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	httpClient := &http.Client{Transport: transport, Timeout: 15 * time.Second}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		log.Fatal(err)
	}
	lease.Apply(req)
	started := time.Now()
	resp, err := httpClient.Do(req)
	if err != nil {
		_ = lease.ReportError(err, spinneret.ReportInput{StartedAt: started})
		return
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	var markers []string
	if len(body) == 0 {
		markers = append(markers, "empty_list")
	}
	_ = lease.ReportResponse(resp, spinneret.ReportInput{StartedAt: started, ResponseBytes: int64(len(body)), Markers: markers})
}

// A config watcher keeps items up to date and survives server outages with
// local snapshots (items with secret references are never written to disk).
func ExampleClient_NewConfigWatcher() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client, err := spinneret.New(spinneret.Options{}) // SPINNERET_URL, SPINNERET_TOKEN
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = client.Close(ctx) }()

	watcher, err := client.NewConfigWatcher(spinneret.WatcherOptions{
		Items:       []spinneret.ConfigKey{{Group: "crawler", Key: "search.json"}},
		SnapshotDir: "/var/cache/spinneret",
		OnChange: func(item *spinneret.ConfigItem) {
			log.Printf("config %s/%s is now version %d", item.GetGroup(), item.GetKey(), item.GetVersion())
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	if err := watcher.Start(ctx); err != nil { // stops when ctx is cancelled
		log.Fatal(err)
	}
	if item, ok := watcher.Get("crawler", "search.json"); ok {
		_ = item.GetContent()
	}
}

// Error helpers expose the reason and retry hint sent by the server.
func ExampleError() {
	client, err := spinneret.New(spinneret.Options{BaseURL: "http://127.0.0.1:8080", Token: "spn_xxx"})
	if err != nil {
		log.Fatal(err)
	}
	_, err = client.Acquire(context.Background(), &spinneret.AcquireRequest{Site: "shop", Client: "web"})
	if e, ok := spinneret.AsError(err); ok {
		log.Printf("code=%s reason=%s retry_after=%s transport=%v kind=%s",
			e.Code, e.Reason, e.RetryAfter, e.Transport(), e.ErrorKind)
	}
}

// ClassifyError maps request failures to report error kinds.
func ExampleClassifyError() {
	_, err := http.Get("http://127.0.0.1:1/") //nolint:noctx // example
	log.Println(spinneret.ClassifyError(err)) // conn_refused
}
