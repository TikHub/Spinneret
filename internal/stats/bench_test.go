package stats

import (
	"testing"
	"time"
)

func BenchmarkRecordAcquire(b *testing.B) {
	a := NewAggregator(nil, nil, nil, nil)
	rec := AcquireRecord{At: time.Now(), TenantID: "ten_0192a3f4c1d27b8e9a01f2c3d4e5a6b7", NamespaceID: "ns_0192a3f4c1d27b8e9a01f2c3d4e5a6b7",
		SiteID: "sit_0192a3f4c1d27b8e9a01f2c3d4e5a6b7", EndpointGroupID: "eg_0192a3f4c1d27b8e9a01f2c3d4e5a6b7",
		IdentityTypeID: "ity_0192a3f4c1d27b8e9a01f2c3d4e5a6b7", TokenID: "tok_0192a3f4c1d27b8e9a01f2c3d4e5a6b7",
		Node: "crawler-hk-03", Result: ResultOK, Duration: 900 * time.Microsecond}
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			a.RecordAcquire(rec)
		}
	})
}

func BenchmarkRecordReportSuccess(b *testing.B) {
	a := NewAggregator(nil, nil, nil, nil)
	now := time.Now()
	rec := ReportRecord{ReceivedAt: now, FinishedAt: now.Add(-time.Second), NamespaceID: "ns_0192a3f4c1d27b8e9a01f2c3d4e5a6b7",
		SiteID: "sit_0192a3f4c1d27b8e9a01f2c3d4e5a6b7", EndpointGroupID: "eg_0192a3f4c1d27b8e9a01f2c3d4e5a6b7",
		IdentityID: "idt_0192a3f4c1d27b8e9a01f2c3d4e5a6b7", ProxyID: "pxy_0192a3f4c1d27b8e9a01f2c3d4e5a6b7",
		Node: "crawler-hk-03", Outcome: "success", LatencyMs: 842, ResponseBytes: 48213}
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			a.RecordReport(rec)
		}
	})
}
