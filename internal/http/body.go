package httpapi

import (
	"regexp"

	apperrors "github.com/miqbalhamdani/new-commerce-api/internal/platform/errors"
)

// fieldErr is a 422 naming one field.
func fieldErr(field, detail string) error {
	return apperrors.ValidationFailed(detail).WithFields(apperrors.Field{Name: field, Detail: detail})
}

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
