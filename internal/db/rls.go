package db

import (
	"context"
	"fmt"
)

// RLSProblem is one tenant-owned table whose row level security is not set up
// correctly, and what specifically is wrong with it.
type RLSProblem struct {
	Table  string
	Reason string
}

func (p RLSProblem) String() string { return p.Table + ": " + p.Reason }

// CheckTenantRLS returns every table in the public schema that has a tenant_id
// column but is not properly protected, and every such view that reads as its
// owner rather than its caller.
//
// Three things can be wrong and only one of them is loud:
//
//   - RLS not enabled -- the table is wide open, and every query returns every
//     tenant's rows.
//   - RLS enabled but not FORCEd -- correct for everyone except the table's
//     owner, who silently sees everything. Nothing looks broken.
//   - RLS enabled but no policy -- the opposite failure. Every row is filtered
//     out and the table reads as permanently empty.
//
// The middle one is why this exists as a check rather than as a code review
// habit. See BR-003.
func (s *Store) CheckTenantRLS(ctx context.Context) ([]RLSProblem, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT c.relname, c.relrowsecurity, c.relforcerowsecurity,
		       EXISTS (SELECT 1 FROM pg_policy p WHERE p.polrelid = c.oid)
		  FROM pg_class c
		  JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE c.relkind = 'r'
		   AND n.nspname = 'public'
		   AND EXISTS (
		       SELECT 1 FROM pg_attribute a
		        WHERE a.attrelid = c.oid AND a.attname = 'tenant_id'
		          AND a.attnum > 0 AND NOT a.attisdropped)
		 ORDER BY c.relname`)
	if err != nil {
		return nil, fmt.Errorf("enumerate tenant tables: %w", err)
	}
	defer rows.Close()

	var problems []RLSProblem
	for rows.Next() {
		var table string
		var enabled, forced, hasPolicy bool
		if err := rows.Scan(&table, &enabled, &forced, &hasPolicy); err != nil {
			return nil, fmt.Errorf("scan tenant table: %w", err)
		}

		switch {
		case !enabled:
			// The other two are moot until this is fixed, and listing them
			// would read as three separate problems with one table.
			problems = append(problems, RLSProblem{table, fmt.Sprintf(
				"has tenant_id but row level security is not enabled -- "+
					"its migration is missing SELECT enable_tenant_rls('%s')", table)})
		default:
			if !forced {
				problems = append(problems, RLSProblem{table,
					"row level security is enabled but not FORCEd -- the table's owner bypasses its own policy"})
			}
			if !hasPolicy {
				problems = append(problems, RLSProblem{table,
					"row level security is enabled but there is no policy -- every row is filtered out"})
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tenant tables: %w", err)
	}

	// A view has no policy of its own. With security_invoker it reads its
	// tables as the caller, so their policies apply; without it, it reads them
	// as its owner, who bypasses RLS, and every tenant's rows come through
	// (03-erd.md §4).
	views, err := s.pool.Query(ctx, `
		SELECT c.relname
		  FROM pg_class c
		  JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE c.relkind = 'v'
		   AND n.nspname = 'public'
		   AND EXISTS (
		       SELECT 1 FROM pg_attribute a
		        WHERE a.attrelid = c.oid AND a.attname = 'tenant_id'
		          AND a.attnum > 0 AND NOT a.attisdropped)
		   AND NOT EXISTS (
		       SELECT 1 FROM unnest(coalesce(c.reloptions, '{}')) o
		        WHERE o ~* '^security_invoker=(true|on|yes|1)$')
		 ORDER BY c.relname`)
	if err != nil {
		return nil, fmt.Errorf("enumerate tenant views: %w", err)
	}
	defer views.Close()
	for views.Next() {
		var view string
		if err := views.Scan(&view); err != nil {
			return nil, fmt.Errorf("scan tenant view: %w", err)
		}
		problems = append(problems, RLSProblem{view,
			"view has tenant_id but not security_invoker = true -- it reads as its owner and bypasses RLS"})
	}
	if err := views.Err(); err != nil {
		return nil, fmt.Errorf("iterate tenant views: %w", err)
	}
	return problems, nil
}
