package storefront

import (
	"os"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// TestMain installs a real tracer provider, as cmd/api does, so error
// responses carry a trace id that means something.
func TestMain(m *testing.M) {
	otel.SetTracerProvider(sdktrace.NewTracerProvider())
	os.Exit(m.Run())
}
