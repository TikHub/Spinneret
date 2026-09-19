package updatecheck

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v0.1.0", "v0.1.0", 0},
		{"0.1.0", "v0.1.0", 0},   // the tag's v is not part of the version
		{"1.2", "1.2.0", 0},      // a missing component is zero
		{"v0.2.0", "v0.1.9", 1},  // minor beats patch
		{"v0.1.9", "v0.2.0", -1}, //
		{"v1.0.0", "v0.99.99", 1},
		{"v0.10.0", "v0.9.0", 1}, // numeric, not lexicographic
		{"v1.0.0", "v1.0.0-rc1", 1},
		{"v1.0.0-rc1", "v1.0.0", -1},
		{"v1.0.0-rc2", "v1.0.0-rc1", 1},
		{"v1.0.1-rc1", "v1.0.0", 1}, // a newer number wins over release-ness
		{"garbage", "v0.1.0", -1},   // unreadable sorts below every release
	}
	for _, c := range cases {
		require.Equalf(t, c.want, Compare(c.a, c.b), "Compare(%q, %q)", c.a, c.b)
		require.Equalf(t, -c.want, Compare(c.b, c.a), "Compare(%q, %q) reversed", c.b, c.a)
	}
}

func TestIsNewer(t *testing.T) {
	require.True(t, IsNewer("v0.1.0", "v0.2.0"))
	require.False(t, IsNewer("v0.2.0", "v0.1.0"))
	require.False(t, IsNewer("v0.1.0", "v0.1.0"))
	require.False(t, IsNewer("v0.1.0", ""), "no latest, no claim")

	// A build with no release number must never be told it is out of date: the
	// operator built it themselves and the comparison is meaningless.
	require.False(t, IsNewer("dev", "v9.9.9"))
	require.False(t, IsNewer("dev-abc123", "v9.9.9"))
	require.False(t, IsNewer("", "v9.9.9"))
}

// feed serves one release payload and counts the requests it answered.
func feed(t *testing.T, body string, status int) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		require.NotEmpty(t, r.Header.Get("User-Agent"), "the feed rejects requests without one")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

const releaseBody = `{"tag_name":"v0.2.0","html_url":"https://example.invalid/releases/v0.2.0"}`

func TestCheckReportsAnUpdate(t *testing.T) {
	srv, _ := feed(t, releaseBody, http.StatusOK)
	c := New(Config{URL: srv.URL, Current: "v0.1.0"})

	got, err := c.Check(context.Background())
	require.NoError(t, err)
	require.Equal(t, "v0.2.0", got.Latest)
	require.Equal(t, "https://example.invalid/releases/v0.2.0", got.ReleaseURL)
	require.True(t, got.UpdateAvailable)
	require.Equal(t, "v0.1.0", got.Current)
	require.False(t, got.CheckedAt.IsZero())
}

func TestCheckOnTheLatestReleaseOffersNothing(t *testing.T) {
	srv, _ := feed(t, releaseBody, http.StatusOK)
	c := New(Config{URL: srv.URL, Current: "v0.2.0"})

	got, err := c.Check(context.Background())
	require.NoError(t, err)
	require.False(t, got.UpdateAvailable)
	require.Equal(t, "v0.2.0", got.Latest)
}

func TestSourceBuildIsNotReportedAsUpToDate(t *testing.T) {
	// A build from source carries no release number, so it cannot be ordered
	// against a release: it may be ahead of the latest one. UpdateAvailable is
	// false for it, and CurrentIsRelease is what stops a caller from rendering
	// that as "you are on the latest release".
	for _, current := range []string{"dev", "dev-1a2b3c4d5e6f", "dev-1a2b3c4d5e6f-dirty", ""} {
		t.Run(current, func(t *testing.T) {
			srv, _ := feed(t, releaseBody, http.StatusOK)
			c := New(Config{URL: srv.URL, Current: current})

			got, err := c.Check(context.Background())
			require.NoError(t, err)
			require.False(t, got.CurrentIsRelease)
			require.False(t, got.UpdateAvailable, "an unversioned build is never told it is out of date")
			require.Equal(t, "v0.2.0", got.Latest, "the latest release is still worth showing")
		})
	}
}

func TestCurrentIsReleaseSurvivesEveryAnswer(t *testing.T) {
	// It is a fact about the running build, not about the feed, so it holds even
	// when the check is off or the feed could not be read.
	disabled := New(Config{URL: "", Current: "v0.1.0"})
	got, err := disabled.Check(context.Background())
	require.NoError(t, err)
	require.True(t, got.CurrentIsRelease)

	srv, _ := feed(t, `{"message":"boom"}`, http.StatusInternalServerError)
	failing := New(Config{URL: srv.URL, Current: "v0.1.0"})
	got, err = failing.Check(context.Background())
	require.Error(t, err)
	require.True(t, got.CurrentIsRelease)
}

func TestCheckCachesSuccess(t *testing.T) {
	srv, hits := feed(t, releaseBody, http.StatusOK)
	now := time.Now()
	c := New(Config{URL: srv.URL, Current: "v0.1.0", CacheTTL: time.Hour, Now: func() time.Time { return now }})

	for range 5 {
		_, err := c.Check(context.Background())
		require.NoError(t, err)
	}
	require.EqualValues(t, 1, hits.Load(), "the feed is rate limited; repeated clicks must not spend the budget")

	now = now.Add(time.Hour + time.Second)
	_, err := c.Check(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 2, hits.Load(), "the cache expires")
}

func TestCheckCachesFailureBriefly(t *testing.T) {
	srv, hits := feed(t, `{}`, http.StatusInternalServerError)
	now := time.Now()
	c := New(Config{URL: srv.URL, Current: "v0.1.0", Now: func() time.Time { return now }})

	got, err := c.Check(context.Background())
	require.Error(t, err)
	require.Equal(t, "v0.1.0", got.Current, "a failure still reports the running build")
	require.False(t, got.UpdateAvailable)

	_, err = c.Check(context.Background())
	require.Error(t, err)
	require.EqualValues(t, 1, hits.Load(), "a network that cannot reach the feed is not retried on every click")

	now = now.Add(failureCacheTTL + time.Second)
	_, err = c.Check(context.Background())
	require.Error(t, err)
	require.EqualValues(t, 2, hits.Load())
}

func TestCheckIgnoresPrereleases(t *testing.T) {
	srv, _ := feed(t, `{"tag_name":"v9.9.9","prerelease":true,"html_url":"https://example.invalid/x"}`, http.StatusOK)
	c := New(Config{URL: srv.URL, Current: "v0.1.0"})

	got, err := c.Check(context.Background())
	require.NoError(t, err)
	require.Empty(t, got.Latest)
	require.False(t, got.UpdateAvailable, "a pre-release is never offered as an upgrade")
}

func TestNoReleasesYetIsNotAFailure(t *testing.T) {
	// A repository with nothing published answers 404 on releases/latest. An
	// operator running the first build must not be shown an error for it.
	srv, _ := feed(t, `{"message":"Not Found"}`, http.StatusNotFound)
	c := New(Config{URL: srv.URL, Current: "v0.1.0"})

	got, err := c.Check(context.Background())
	require.NoError(t, err)
	require.Empty(t, got.Latest)
	require.False(t, got.UpdateAvailable)
	require.Equal(t, "v0.1.0", got.Current)
	require.False(t, got.CheckedAt.IsZero(), "the feed was reached, it just had nothing")
}

func TestDisabledMakesNoCall(t *testing.T) {
	srv, hits := feed(t, releaseBody, http.StatusOK)
	_ = srv
	c := New(Config{URL: "", Current: "v0.1.0"})
	require.False(t, c.Enabled())

	got, err := c.Check(context.Background())
	require.NoError(t, err)
	require.True(t, got.Disabled)
	require.Equal(t, "v0.1.0", got.Current)
	require.Empty(t, got.Latest)
	require.EqualValues(t, 0, hits.Load())
}

func TestCheckIsConcurrencySafe(t *testing.T) {
	srv, _ := feed(t, releaseBody, http.StatusOK)
	c := New(Config{URL: srv.URL, Current: "v0.1.0"})

	done := make(chan struct{})
	for range 16 {
		go func() {
			defer func() { done <- struct{}{} }()
			_, _ = c.Check(context.Background())
		}()
	}
	for range 16 {
		<-done
	}
	got, err := c.Check(context.Background())
	require.NoError(t, err)
	require.True(t, got.UpdateAvailable)
}
