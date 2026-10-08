package db

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// Violation reports which constraint err violated and its SQLSTATE class:
// "23505" unique, "23503" foreign key, "23514" check, "P0001" raise.
// Services map these to client errors; anything else stays internal.
func Violation(err error) (code, constraint string, ok bool) {
	var pg *pgconn.PgError
	if !errors.As(err, &pg) {
		return "", "", false
	}
	return pg.Code, pg.ConstraintName, true
}
