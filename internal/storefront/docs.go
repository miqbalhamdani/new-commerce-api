package storefront

import (
	_ "embed"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/miqbalhamdani/new-commerce-api/internal/httpkit"
)

// The public reference for a shop's developer (P1-213, 04-api-spec.md §11):
// the spec this package was generated from, and a page that renders it.
// Neither needs a key -- the document is the same for every shop.

// specYAML is contracts/storefront.yaml, copied here by `make generate` with
// the routes generated from it, so the document served is always theirs. A
// plain copy rather than oapi-codegen's embedded spec, which gzips it: gzip's
// bytes differ between Go versions, and generated-diff would too.
//
//go:embed storefront.yaml
var specYAML []byte

var specJSON = sync.OnceValues(func() ([]byte, error) {
	spec, err := openapi3.NewLoader().LoadFromData(specYAML)
	if err != nil {
		return nil, err
	}
	return json.Marshal(spec)
})

// redoc is pinned and checked by its hash, and the page may load nothing
// else: it is served from the API's own origin, where a tampered script
// could call the admin API with the staff refresh cookie.
const docsPage = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Storefront API</title>
</head>
<body>
<redoc spec-url="` + BaseURL + `/openapi.json"></redoc>
<script src="https://cdn.jsdelivr.net/npm/redoc@2.5.4/bundles/redoc.standalone.js"
  integrity="sha384-w447zOpYfw/1Tv/5AK9NfHTlQIqE3RVR6KY62jCyy9zNDgO64cMwGGP1Fj0zJVf5"
  crossorigin="anonymous"></script>
</body>
</html>
`

const docsPolicy = "default-src 'none'; script-src https://cdn.jsdelivr.net; style-src 'unsafe-inline'; " +
	"img-src data: https:; font-src data:; connect-src 'self'; worker-src blob:; " +
	"base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// docs answers the two documentation routes ahead of the key check, and
// passes everything else on.
func docs(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		switch r.URL.Path {
		case BaseURL + "/openapi.json":
			body, err := specJSON()
			if err != nil {
				slog.ErrorContext(r.Context(), "embedded storefront spec does not load", "error", err)
				httpkit.WriteError(w, r, err)
				return
			}
			// Public, so any tool may read it from a browser.
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Cache-Control", "public, max-age=300")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		case BaseURL + "/docs":
			w.Header().Set("Content-Security-Policy", docsPolicy)
			w.Header().Set("Cache-Control", "public, max-age=300")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(docsPage))
		default:
			next.ServeHTTP(w, r)
		}
	})
}
