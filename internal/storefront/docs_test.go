package storefront

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// endpointsTable is 04-api-spec.md §11.3 as "METHOD /path", query strings
// dropped.
func endpointsTable(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile("../../contracts/04-api-spec.md")
	if err != nil {
		t.Fatal(err)
	}
	_, section, _ := strings.Cut(string(raw), "### 11.3 Endpoints")
	section, _, _ = strings.Cut(section, "\n### ")
	row := regexp.MustCompile("(?m)^\\| `([A-Z]+)` \\| `([^`?]+)")
	var out []string
	for _, m := range row.FindAllStringSubmatch(section, -1) {
		out = append(out, m[1]+" "+m[2])
	}
	if len(out) == 0 {
		t.Fatal("no rows found in 04-api-spec.md §11.3")
	}
	return out
}

// TestStorefrontDocs is P1-213's acceptance: the spec and its page are served
// with no key, and every §11.3 route is in it.
func TestStorefrontDocs(t *testing.T) {
	h := NewRouter(&Server{}, nil, nil, nil)
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		return rec
	}

	rec := get(BaseURL + "/openapi.json")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("openapi.json: %d %s", rec.Code, rec.Body)
	}
	var spec struct {
		OpenAPI string                    `json:"openapi"`
		Paths   map[string]map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &spec); err != nil || spec.OpenAPI != "3.1.0" {
		t.Fatalf("openapi.json is not the spec (%v): %.200s", err, rec.Body)
	}
	served := map[string]bool{}
	for path, ops := range spec.Paths {
		for method := range ops {
			served[strings.ToUpper(method)+" "+BaseURL+path] = true
		}
	}

	routes, table := storefrontRoutes(t), endpointsTable(t)
	for _, r := range routes {
		if !served[r] {
			t.Errorf("%s is routed but not in the served spec", r)
		}
	}
	for _, r := range table {
		if strings.Contains(r, " /v1/webhooks/") {
			// Midtrans's, not a shop developer's: it lives in openapi.yaml.
			admin, _ := os.ReadFile("../../contracts/openapi.yaml")
			_, path, _ := strings.Cut(r, " ")
			if !strings.Contains(string(admin), "\n  "+strings.TrimPrefix(path, "/v1")+":") {
				t.Errorf("%s is in §11.3 but not in openapi.yaml", r)
			}
			continue
		}
		if !slices.Contains(routes, r) {
			t.Errorf("%s is in §11.3 but not in storefront.yaml", r)
		}
	}
	for _, r := range routes {
		if !slices.Contains(table, r) {
			t.Errorf("%s is in storefront.yaml but not in §11.3", r)
		}
	}

	rec = get(BaseURL + "/docs")
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") ||
		!strings.Contains(rec.Body.String(), `data-url="/v1/storefront/openapi.json"`) ||
		!strings.Contains(rec.Body.String(), `integrity="sha384-`) ||
		!strings.Contains(rec.Header().Get("Content-Security-Policy"), "default-src 'none'") {
		t.Errorf("docs: %d %v %s", rec.Code, rec.Header(), rec.Body)
	}
}
