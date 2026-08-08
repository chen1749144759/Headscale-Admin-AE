package hscontrol

import (
	"bytes"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestScaleForgeProxyOperation(t *testing.T) {
	t.Parallel()

	operation, err := scaleForgeProxyOperation("/machine/scaleforge/traffic", http.MethodPost)
	require.NoError(t, err)
	require.Equal(t, "traffic", operation)

	_, err = scaleForgeProxyOperation("/machine/scaleforge/traffic", http.MethodGet)
	require.Error(t, err)
	_, err = scaleForgeProxyOperation("/machine/scaleforge/unknown", http.MethodPost)
	require.Error(t, err)
}

func TestScaleForgeProxyQuery(t *testing.T) {
	t.Parallel()

	query, err := scaleForgeProxyQuery("client-update", url.Values{
		"current_version":  {"0.0.2"},
		"platform":         {"windows-amd64"},
		"current_revision": {"42"},
	})
	require.NoError(t, err)
	require.Equal(t, "current_revision=42&current_version=0.0.2&platform=windows-amd64", query)

	_, err = scaleForgeProxyQuery("policy", url.Values{"machine_id": {"1"}})
	require.Error(t, err)
	_, err = scaleForgeProxyQuery("client-update", url.Values{"redirect": {"https://example.com"}})
	require.Error(t, err)
	_, err = scaleForgeProxyQuery("client-update", url.Values{"current_revision": {"-1"}})
	require.Error(t, err)
	_, err = scaleForgeProxyQuery("client-update", url.Values{"current_revision": {"9007199254740992"}})
	require.Error(t, err)
}

func TestReadScaleForgeProxyRequestBody(t *testing.T) {
	t.Parallel()

	payload := bytes.Repeat([]byte{'a'}, scaleForgeProxyRequestLimit)
	got, err := readScaleForgeProxyRequestBody(bytes.NewReader(payload))
	require.NoError(t, err)
	require.Equal(t, payload, got)

	_, err = readScaleForgeProxyRequestBody(bytes.NewReader(append(payload, 'b')))
	require.ErrorIs(t, err, errScaleForgeProxyRequestTooLarge)
}

func TestPublicClientUpdateRateLimit(t *testing.T) {
	limiter := newBoundedTokenBucketLimiter(
		publicUpdateBurst,
		publicUpdateRefillInterval,
		publicUpdateEntryTTL,
		publicUpdateMaxEntries,
	)
	now := time.Now().UTC()
	for range publicUpdateBurst {
		require.True(t, limiter.allow("192.0.2.10", now))
	}
	require.False(t, limiter.allow("192.0.2.10", now))
	require.True(t, limiter.allow("192.0.2.10", now.Add(publicUpdateRefillInterval)))
}

func TestPublicClientUpdateRateLimitEvictsOldestEntryWhenFull(t *testing.T) {
	limiter := newBoundedTokenBucketLimiter(1, time.Hour, time.Hour, 2)
	now := time.Now().UTC()

	require.True(t, limiter.allow("192.0.2.1", now))
	require.True(t, limiter.allow("192.0.2.2", now))
	require.False(t, limiter.allow("192.0.2.1", now), "denied requests must still refresh LRU recency")
	require.True(t, limiter.allow("192.0.2.3", now), "a full table must evict instead of rejecting a new source")

	require.Len(t, limiter.entries, 2)
	require.Contains(t, limiter.entries, "192.0.2.1")
	require.Contains(t, limiter.entries, "192.0.2.3")
	require.NotContains(t, limiter.entries, "192.0.2.2")
	require.True(t, limiter.allow("192.0.2.2", now), "an evicted source must be admitted on its next request")
	require.Len(t, limiter.entries, 2)
}

func TestAuthenticatedScaleForgeClientRateLimitIsPerNodeAndOperation(t *testing.T) {
	limiter := newBoundedTokenBucketLimiter(
		clientOperationBurst,
		clientOperationRefill,
		clientOperationEntryTTL,
		clientOperationMaxEntries,
	)
	now := time.Now().UTC()
	nodeOneTraffic := scaleForgeClientOperationRateLimitKey(101, "traffic")

	for range clientOperationBurst {
		require.True(t, limiter.allow(nodeOneTraffic, now))
	}
	require.False(t, limiter.allow(nodeOneTraffic, now), "one node must not report traffic without bound")
	require.True(t, limiter.allow(scaleForgeClientOperationRateLimitKey(202, "traffic"), now), "another node must have an independent bucket")
	require.True(t, limiter.allow(scaleForgeClientOperationRateLimitKey(101, "policy-state"), now), "operations for the same node must have independent buckets")
	require.True(t, limiter.allow(scaleForgeClientOperationRateLimitKey(101, "policy"), now))
	require.True(t, limiter.allow(scaleForgeClientOperationRateLimitKey(101, "client-update"), now))
	require.True(t, limiter.allow(nodeOneTraffic, now.Add(clientOperationRefill)), "the bucket must refill at the supported client interval")
}

func TestAuthenticatedScaleForgeClientRateLimitRemainsBounded(t *testing.T) {
	limiter := newBoundedTokenBucketLimiter(1, time.Hour, time.Hour, 3)
	now := time.Now().UTC()

	for _, key := range []string{"traffic:1", "traffic:2", "traffic:3", "traffic:4", "traffic:5"} {
		require.True(t, limiter.allow(key, now))
		require.LessOrEqual(t, len(limiter.entries), 3)
	}
	require.Len(t, limiter.entries, 3)
	require.NotContains(t, limiter.entries, "traffic:1")
	require.NotContains(t, limiter.entries, "traffic:2")
	require.Contains(t, limiter.entries, "traffic:5")
}
