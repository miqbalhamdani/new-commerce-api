// Package config reads the handful of settings the binaries need from the
// environment.
//
// It exists because cmd/api and cmd/migrate both need DATABASE_URL and would
// otherwise each carry their own copy of the default -- two copies that can
// drift, pointing two binaries at two different databases.
package config

import "os"

// Defaults target a host install -- Homebrew's postgresql@18 and redis on their
// standard ports (contracts/tdd.md 2.2). They are compiled in so that a clean
// checkout runs without a .env; anything else overrides via the environment.
const (
	DefaultDatabaseURL = "postgres://localhost:5432/new_commerce_dev?sslmode=disable"
	DefaultRedisURL    = "redis://localhost:6379/0"
	DefaultPort        = "8080"
)

// Getenv returns the value of key, or fallback when it is unset or empty.
func Getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// DatabaseURL is the PostgreSQL DSN the API and the migration runner share.
func DatabaseURL() string { return Getenv("DATABASE_URL", DefaultDatabaseURL) }

// RedisURL is the Redis DSN.
func RedisURL() string { return Getenv("REDIS_URL", DefaultRedisURL) }
