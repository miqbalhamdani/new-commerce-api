package httpapi_test

import (
	"fmt"
	"strings"
	"testing"
)

// assertNumberedPages is P1-231's paging contract for one list query (path
// carries its filters and sort): with limit=1, page N is the Nth row of the
// unpaged list, the page after the last is empty with no next_cursor, every
// page answers total = wantTotal, and page=0 or page with cursor is 422.
func assertNumberedPages(t *testing.T, do func(method, path string, body any) (int, map[string]any), path string, wantTotal int) {
	t.Helper()
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	code, p := do("GET", path+sep+"limit=200", nil)
	if code != 200 {
		t.Fatalf("%s: %d %v", path, code, p)
	}
	var want []string
	for _, d := range p["data"].([]any) {
		want = append(want, d.(map[string]any)["id"].(string))
	}
	if len(want) != wantTotal || p["total"] != float64(wantTotal) {
		t.Fatalf("%s: %d rows, total %v, want %d", path, len(want), p["total"], wantTotal)
	}
	for n := 1; n <= wantTotal+1; n++ {
		page := fmt.Sprintf("%s%slimit=1&page=%d", path, sep, n)
		code, p := do("GET", page, nil)
		data, _ := p["data"].([]any)
		if code != 200 || p["total"] != float64(wantTotal) {
			t.Errorf("%s: %d, total %v, want %d", page, code, p["total"], wantTotal)
			continue
		}
		if n > wantTotal {
			if len(data) != 0 || p["next_cursor"] != nil {
				t.Errorf("%s: %v past the last page, want [] and no next_cursor", page, p)
			}
			continue
		}
		if len(data) != 1 || data[0].(map[string]any)["id"] != want[n-1] {
			t.Errorf("%s: %v, want row %d of %v", page, data, n, want)
		}
	}
	for _, bad := range []string{path + sep + "page=0", path + sep + "page=1&cursor=eyJ9"} {
		code, p := do("GET", bad, nil)
		assertProblem(t, code, p, 422, "validation_failed", "page")
	}
}
