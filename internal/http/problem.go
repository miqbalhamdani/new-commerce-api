package httpapi

import "github.com/miqbalhamdani/new-commerce-api/internal/httpkit"

// The plumbing the admin API shares with the storefront lives in httpkit
// (P1-202), so neither imports the other. These names keep the admin
// handlers reading as they did before the move.
var (
	writeError   = httpkit.WriteError
	decodeJSON   = httpkit.DecodeJSON
	rejectNull   = httpkit.RejectNull
	writeJSON    = httpkit.WriteJSON
	encodeCursor = httpkit.EncodeCursor
	decodeCursor = httpkit.DecodeCursor
	pageLimit    = httpkit.PageLimit
	pageNumber   = httpkit.PageNumber
	bearerToken  = httpkit.BearerToken
	clientIP     = httpkit.ClientIP
	tracing      = httpkit.Tracing
)

type optional[T any] = httpkit.Optional[T]
