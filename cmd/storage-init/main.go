// Command storage-init creates the development bucket in MinIO with public
// read on product images and the BR-053 expiry rules, standing in for the R2
// setup of P1-045.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/miqbalhamdani/new-commerce-api/internal/platform/config"
	"github.com/miqbalhamdani/new-commerce-api/internal/platform/logging"
	"github.com/miqbalhamdani/new-commerce-api/internal/storage"
)

func main() {
	slog.SetDefault(logging.New(os.Stderr))
	s, err := storage.FromEnv()
	if err == nil {
		err = s.EnsureBucket(context.Background())
	}
	if err != nil {
		slog.Error("storage-init failed", "error", err)
		os.Exit(1)
	}
	slog.Info("bucket ready", "bucket", config.S3Bucket(), "expiry_days", storage.Expiry)
}
