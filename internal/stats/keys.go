package stats

import (
	"cmp"
	"strings"

	"github.com/cespare/xxhash/v2"
)

// tableKey is the primary key of one aggregate row.
type tableKey[K any] interface {
	comparable
	// bucketUnix returns the bucket (partition key) in Unix seconds.
	bucketUnix() int64
	// shardHash returns a hash used to pick the in-memory shard.
	shardHash() uint64
	// compare orders keys by primary key (bucket first).
	compare(other K) int
}

// tableValue is the mergeable counter payload of one aggregate row.
type tableValue[V any] interface {
	merge(other V) V
}

const (
	hashPrime1 = 0x9E3779B185EBCA87
	hashPrime2 = 0xC2B2AE3D27D4EB4F
)

// hashKey combines a bucket and string fields into a shard hash without
// allocating.
func hashKey(bucket int64, fields ...string) uint64 {
	h := uint64(bucket) * hashPrime1
	for _, f := range fields {
		h = (h ^ xxhash.Sum64String(f)) * hashPrime2
	}
	return h ^ (h >> 29)
}

// acquireKey is the primary key of acquire_stats_minutely.
type acquireKey struct {
	bucket          int64
	namespaceID     string
	siteID          string
	endpointGroupID string
	result          string
}

func (k acquireKey) bucketUnix() int64 { return k.bucket }

func (k acquireKey) shardHash() uint64 {
	return hashKey(k.bucket, k.namespaceID, k.siteID, k.endpointGroupID, k.result)
}

func (k acquireKey) compare(o acquireKey) int {
	return cmp.Or(
		cmp.Compare(k.bucket, o.bucket),
		strings.Compare(k.namespaceID, o.namespaceID),
		strings.Compare(k.siteID, o.siteID),
		strings.Compare(k.endpointGroupID, o.endpointGroupID),
		strings.Compare(k.result, o.result),
	)
}

// acquireValue holds the counters of acquire_stats_minutely.
type acquireValue struct {
	count      int64
	durationUs int64
}

func (v acquireValue) merge(o acquireValue) acquireValue {
	return acquireValue{count: v.count + o.count, durationUs: v.durationUs + o.durationUs}
}

// payloadKey is the primary key of payload_access_minutely.
type payloadKey struct {
	bucket         int64
	namespaceID    string
	tokenID        string
	identityTypeID string
}

func (k payloadKey) bucketUnix() int64 { return k.bucket }

func (k payloadKey) shardHash() uint64 {
	return hashKey(k.bucket, k.namespaceID, k.tokenID, k.identityTypeID)
}

func (k payloadKey) compare(o payloadKey) int {
	return cmp.Or(
		cmp.Compare(k.bucket, o.bucket),
		strings.Compare(k.namespaceID, o.namespaceID),
		strings.Compare(k.tokenID, o.tokenID),
		strings.Compare(k.identityTypeID, o.identityTypeID),
	)
}

// countValue is a single counter.
type countValue struct {
	count int64
}

func (v countValue) merge(o countValue) countValue {
	return countValue{count: v.count + o.count}
}

// nodeKey is the primary key of node_stats_minutely.
type nodeKey struct {
	bucket      int64
	namespaceID string
	node        string
}

func (k nodeKey) bucketUnix() int64 { return k.bucket }

func (k nodeKey) shardHash() uint64 {
	return hashKey(k.bucket, k.namespaceID, k.node)
}

func (k nodeKey) compare(o nodeKey) int {
	return cmp.Or(
		cmp.Compare(k.bucket, o.bucket),
		strings.Compare(k.namespaceID, o.namespaceID),
		strings.Compare(k.node, o.node),
	)
}

// nodeValue holds the counters of node_stats_minutely.
type nodeValue struct {
	acquires  int64
	reports   int64
	abandoned int64
	rejected  int64
}

func (v nodeValue) merge(o nodeValue) nodeValue {
	return nodeValue{
		acquires:  v.acquires + o.acquires,
		reports:   v.reports + o.reports,
		abandoned: v.abandoned + o.abandoned,
		rejected:  v.rejected + o.rejected,
	}
}

// outcomeKey is the primary key of outcome_stats_minutely.
type outcomeKey struct {
	bucket          int64
	namespaceID     string
	siteID          string
	endpointGroupID string
	proxyID         string
	outcome         string
}

func (k outcomeKey) bucketUnix() int64 { return k.bucket }

func (k outcomeKey) shardHash() uint64 {
	return hashKey(k.bucket, k.namespaceID, k.siteID, k.endpointGroupID, k.proxyID, k.outcome)
}

func (k outcomeKey) compare(o outcomeKey) int {
	return cmp.Or(
		cmp.Compare(k.bucket, o.bucket),
		strings.Compare(k.namespaceID, o.namespaceID),
		strings.Compare(k.siteID, o.siteID),
		strings.Compare(k.endpointGroupID, o.endpointGroupID),
		strings.Compare(k.proxyID, o.proxyID),
		strings.Compare(k.outcome, o.outcome),
	)
}

// outcomeValue holds the counters of outcome_stats_minutely.
type outcomeValue struct {
	count         int64
	latencyMs     int64
	responseBytes int64
}

func (v outcomeValue) merge(o outcomeValue) outcomeValue {
	return outcomeValue{
		count:         v.count + o.count,
		latencyMs:     v.latencyMs + o.latencyMs,
		responseBytes: v.responseBytes + o.responseBytes,
	}
}

// identityKey is the primary key of identity_stats_hourly. site_id is not part
// of the primary key (identity ids are globally unique), so it is carried in
// the value.
type identityKey struct {
	bucket          int64
	identityID      string
	endpointGroupID string
	outcome         string
}

func (k identityKey) bucketUnix() int64 { return k.bucket }

func (k identityKey) shardHash() uint64 {
	return hashKey(k.bucket, k.identityID, k.endpointGroupID, k.outcome)
}

func (k identityKey) compare(o identityKey) int {
	return cmp.Or(
		cmp.Compare(k.bucket, o.bucket),
		strings.Compare(k.identityID, o.identityID),
		strings.Compare(k.endpointGroupID, o.endpointGroupID),
		strings.Compare(k.outcome, o.outcome),
	)
}

// identityValue holds the site and counter of identity_stats_hourly.
type identityValue struct {
	siteID string
	count  int64
}

func (v identityValue) merge(o identityValue) identityValue {
	site := v.siteID
	if site == "" {
		site = o.siteID
	}
	return identityValue{siteID: site, count: v.count + o.count}
}
