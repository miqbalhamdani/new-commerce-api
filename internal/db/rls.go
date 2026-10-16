package db

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
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
//
// The policies themselves are checked too, because enable_tenant_rls is the
// only thing that creates any: tenant_isolation on every table, and in
// limited-roles mode (BR-001) limited_read_bypass, SELECT only, exactly as
// 03-erd.md §3.1 writes it. Anything else is a policy nobody reviewed -- and a
// permissive one widens what every query can see.
func (s *Store) CheckTenantRLS(ctx context.Context) ([]RLSProblem, error) {
	var mode string
	if err := s.pool.QueryRow(ctx, `SELECT db_role_mode()`).Scan(&mode); err != nil {
		return nil, fmt.Errorf("read db_role_mode() -- is the database migrated? %w", err)
	}
	policies, err := s.tenantPolicies(ctx)
	if err != nil {
		return nil, err
	}

	var problems []RLSProblem
	if mode == "limited" {
		p, err := s.checkBypassFunction(ctx)
		if err != nil {
			return nil, err
		}
		problems = append(problems, p...)
	}

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
				continue
			}
			for _, reason := range policyProblems(mode, policies[table]) {
				problems = append(problems, RLSProblem{table, reason})
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

// limitedBypassQual is limited_read_bypass's USING expression as PostgreSQL
// prints it back (pg_get_expr) for the policy enable_tenant_rls creates.
const limitedBypassQual = "rls_bypass_on()"

// rlsBypassOnBody is rls_bypass_on()'s body as 000001 writes it, whitespace
// collapsed. Every limited_read_bypass policy is only as narrow as this
// function: one that returned true would hand every row to every query.
const rlsBypassOnBody = "BEGIN RETURN coalesce(current_setting('app.rls_bypass', true), '') = 'on'; END"

// checkBypassFunction holds rls_bypass_on() to its migration: IMMUTABLE, so
// the planner folds it away for the app's queries, and its body unchanged.
func (s *Store) checkBypassFunction(ctx context.Context) ([]RLSProblem, error) {
	var volatility, body string
	err := s.pool.QueryRow(ctx, `SELECT provolatile::text, prosrc FROM pg_proc
		WHERE oid = to_regprocedure('rls_bypass_on()')`).Scan(&volatility, &body)
	if errors.Is(err, pgx.ErrNoRows) {
		return []RLSProblem{{"rls_bypass_on()", "is missing in limited-roles mode"}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read rls_bypass_on(): %w", err)
	}
	var problems []RLSProblem
	if volatility != "i" {
		problems = append(problems, RLSProblem{"rls_bypass_on()",
			"is not IMMUTABLE -- the planner cannot fold it, and every tenant query scans the whole table"})
	}
	if strings.Join(strings.Fields(body), " ") != rlsBypassOnBody {
		problems = append(problems, RLSProblem{"rls_bypass_on()",
			"has a different body than 000001 gives it -- limited_read_bypass is only as narrow as this function"})
	}
	return problems, nil
}

type tenantPolicy struct {
	name, cmd   string
	permissive  bool
	qual, check string
}

// tenantPolicies returns every policy on a public table, by table name.
func (s *Store) tenantPolicies(ctx context.Context) (map[string][]tenantPolicy, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT c.relname, p.polname, p.polcmd::text, p.polpermissive,
		       coalesce(pg_get_expr(p.polqual, p.polrelid), ''),
		       coalesce(pg_get_expr(p.polwithcheck, p.polrelid), '')
		  FROM pg_policy p
		  JOIN pg_class c ON c.oid = p.polrelid
		  JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = 'public'`)
	if err != nil {
		return nil, fmt.Errorf("enumerate policies: %w", err)
	}
	defer rows.Close()
	out := map[string][]tenantPolicy{}
	for rows.Next() {
		var table string
		var p tenantPolicy
		if err := rows.Scan(&table, &p.name, &p.cmd, &p.permissive, &p.qual, &p.check); err != nil {
			return nil, fmt.Errorf("scan policy: %w", err)
		}
		out[table] = append(out[table], p)
	}
	return out, rows.Err()
}

// policyProblems checks one table's policies against what enable_tenant_rls
// creates in the database's role mode.
func policyProblems(mode string, policies []tenantPolicy) []string {
	var problems []string
	var isolation, bypass bool
	for _, p := range policies {
		switch p.name {
		case "tenant_isolation":
			isolation = true
		case "limited_read_bypass":
			bypass = true
			switch {
			case mode != "limited":
				problems = append(problems,
					"has limited_read_bypass, but the database is in "+mode+" mode -- only limited-roles mode may have it (BR-001)")
			case p.cmd != "r" || !p.permissive || p.qual != limitedBypassQual || p.check != "":
				problems = append(problems,
					"limited_read_bypass is not the SELECT-only policy enable_tenant_rls creates -- it must admit reads and nothing else (BR-001)")
			}
		default:
			problems = append(problems, fmt.Sprintf(
				"has a policy %q that enable_tenant_rls did not create -- an unreviewed policy widens or narrows every query", p.name))
		}
	}
	if !isolation {
		problems = append(problems, "has no tenant_isolation policy -- its migration should call enable_tenant_rls")
	}
	if mode == "limited" && !bypass {
		problems = append(problems,
			"has no limited_read_bypass in limited-roles mode -- sign-in lookups and backups cannot read it (BR-001, BR-003)")
	}
	return problems
}
