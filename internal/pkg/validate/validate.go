package validate

import (
	"fmt"
	"strings"

	"github.com/go-playground/validator/v10"

	"aloqa/internal/pkg/cerrors"
)

var v = validator.New(validator.WithRequiredStructEnabled())

// Struct validates a struct and returns an AppError with field details.
func Struct(s any) error {
	if err := v.Struct(s); err != nil {
		var fields []string
		for _, fe := range err.(validator.ValidationErrors) {
			fields = append(fields, fmt.Sprintf("%s: failed on '%s'", fe.Field(), fe.Tag()))
		}
		return cerrors.InvalidInput(strings.Join(fields, "; "))
	}
	return nil
}
