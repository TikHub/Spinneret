// Package spinneret is the Go client of the Spinneret node API.
//
// Spinneret is the control plane that leases identities (cookies, device
// parameters, accounts) and proxies to crawler nodes, turns request reports
// into cooldowns, bans and circuit breaking, and distributes configuration and
// secrets. This package wraps the generated Connect clients of the four node
// services (LeaseService, ReportService, ConfigService and SecretService) and
// adds the helpers a node needs:
//
//   - [Client]: authenticated unary calls with retries and typed errors
//     ([Error], [IsNoIdentity], [IsCircuitOpen], ...), over Connect JSON by
//     default or gRPC with [Options.UseGRPC].
//   - [Lease]: applies a leased credential to an [net/http.Request], exposes
//     the proxy for an [net/http.Transport], queues reports and releases the
//     lease with the last report (or LeaseService/Release when nothing was
//     reported).
//   - [Reporter]: background batching of reports with a bounded queue and
//     retries.
//   - [ConfigWatcher]: long-polls configuration changes, invokes callbacks and
//     optionally keeps local snapshots that never contain secrets.
//   - [ClassifyError]: maps request errors to report error kinds.
//
// A minimal node:
//
//	client, err := spinneret.New(spinneret.Options{BaseURL: "https://spinneret.internal", Token: "spn_xxx"})
//	if err != nil { ... }
//	defer client.Close(context.Background())
//
//	lease, err := client.Lease(ctx, &spinneret.AcquireRequest{Site: "shop", Client: "web", Uri: "/api/v1/feed"})
//	if err != nil { ... }
//	defer lease.Close(ctx)
//
//	transport, err := lease.Transport(nil) // routed through the leased proxy
//	if err != nil { ... }
//	defer transport.CloseIdleConnections()
//
//	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://target.example.com/api/v1/feed", nil)
//	lease.Apply(req) // cookies, headers and query parameters of the credential
//	started := time.Now()
//	resp, err := (&http.Client{Transport: transport}).Do(req)
//	if err != nil {
//		_ = lease.ReportError(err, spinneret.ReportInput{StartedAt: started})
//		return
//	}
//	defer resp.Body.Close()
//	_ = lease.ReportResponse(resp, spinneret.ReportInput{StartedAt: started})
package spinneret
