// Package logging is the one slog handler every binary installs (BR-013).
//
// Fields are allow-listed: a key not named in allowed has its value replaced
// with [REDACTED]. A new field is therefore hidden until someone decides it is
// safe to log, rather than leaked until someone notices. Credentials are on a
// deny list that wins even over the allow list, so allowing "authorization"
// by mistake still logs nothing.
package logging

import (
	"io"
	"log/slog"
	"strings"
)

// Redacted replaces the value of any field not on the allow list.
const Redacted = "[REDACTED]"

// allowed is every field key the code logs today. Adding one is a review
// question: could this value ever hold PII or a secret?
var allowed = map[string]bool{
	// slog's own keys.
	slog.TimeKey: true, slog.LevelKey: true, slog.MessageKey: true, slog.SourceKey: true,

	"code": true, "status": true, "method": true, "path": true, "trace_id": true,
	"cause": true, "error": true, "addr": true, "endpoint": true,
	"database": true, "version": true, // "source" is slog.SourceKey above
}

// denied never appears, allow list or not (BR-013). Compared lowercased.
var denied = map[string]bool{
	"authorization": true, "x-api-key": true, "x-order-token": true,
	"cookie": true, "set-cookie": true, "password": true,
}

// New returns a JSON logger writing to w with the allow list applied.
func New(w io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{ReplaceAttr: redact}))
}

func redact(groups []string, a slog.Attr) slog.Attr {
	if a.Value.Kind() == slog.KindGroup {
		return a // its leaves come through here one by one
	}
	key := strings.ToLower(a.Key)
	if denied[key] || !allowed[a.Key] || (len(groups) > 0 && !allowed[strings.Join(groups, ".")+"."+a.Key]) {
		return slog.String(a.Key, Redacted)
	}
	return a
}
