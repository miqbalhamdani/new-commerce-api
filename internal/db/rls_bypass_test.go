package db

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNothingSetsRLSBypass holds BR-001's limited-roles promise in code: only
// the four BR-003 lookup functions (in SQL) and the backup's pg_dump (in
// deploy/) turn app.rls_bypass on. Go code that names it is either this
// package's lint, which checks the policy's shape, or a test asserting the
// mode -- anything else is application code reaching past RLS.
func TestNothingSetsRLSBypass(t *testing.T) {
	allowed := map[string]bool{
		"internal/db/rls.go":             true, // lint-rls: the policy's expected shape
		"internal/db/rls_bypass_test.go": true, // this test
		"cmd/migrate/limited_test.go":    true, // proves the bypass admits no write
	}
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == ".git" || name == "contracts" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), "rls_bypass") && !allowed[rel] {
			t.Errorf("%s names app.rls_bypass: application code must never set it (BR-001)", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
