package storage

import (
	"testing"

	"github.com/google/uuid"

	"github.com/miqbalhamdani/new-commerce-api/internal/platform/config"
)

// P1-045, local half: EnsureBucket gives a fresh bucket the BR-053 expiry
// rules. Needs MinIO on the host, as the other storage tests do.
func TestEnsureBucketExpiry(t *testing.T) {
	ctx := t.Context()
	s, err := New(Config{Endpoint: config.S3Endpoint(), AccessKey: config.S3AccessKey(), SecretKey: config.S3SecretKey(),
		Bucket: "test-" + uuid.NewString()[:8], UseSSL: config.S3UseSSL()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.client.RemoveBucket(ctx, s.bucket) })
	if err := s.EnsureBucket(ctx); err != nil {
		t.Fatalf("ensure bucket (is MinIO running?): %v", err)
	}
	if err := s.EnsureBucket(ctx); err != nil {
		t.Fatalf("second run: %v", err)
	}

	cfg, err := s.client.GetBucketLifecycle(ctx, s.bucket)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, r := range cfg.Rules {
		if r.Status == "Enabled" {
			got[r.RuleFilter.Prefix] = int(r.Expiration.Days)
		}
	}
	if len(got) != len(Expiry) || got["jobs/"] != 30 || got["exports/"] != 7 {
		t.Errorf("lifecycle rules: %v, want %v", got, Expiry)
	}
}
