// Package envelope provides custom error types and utility functions for API error handling.
package envelope

import (
	"net/http"

	"github.com/valyala/fasthttp"
)

// API errors.
const (
	GeneralError    = "GeneralException"
	PermissionError = "PermissionException"
	InputError      = "InputException"
	DataError       = "DataException"
	NetworkError    = "NetworkException"
)

// Error is the error type used for all API errors.
type Error struct {
	Code      int         // HTTP status code.
	ErrorType string      // Type of the error.
	Message   string      // Error message.
	Data      interface{} // Additional data related to the error.
}

// Error returns the error message and satisfies the Go error interface.
func (e Error) Error() string {
	return e.Message
}

// NewError creates and returns a new instance of Error with custom error metadata.
func NewError(etype string, message string, data interface{}) error {
	err := Error{
		Message:   message,
		ErrorType: etype,
		Data:      data,
	}

	switch etype {
	case GeneralError:
		err.Code = fasthttp.StatusInternalServerError
	case PermissionError:
		err.Code = http.StatusForbidden
	case InputError:
		err.Code = fasthttp.StatusBadRequest
	case DataError:
		err.Code = http.StatusBadGateway
	case NetworkError:
		err.Code = http.StatusGatewayTimeout
	default:
		err.Code = fasthttp.StatusInternalServerError
		err.ErrorType = GeneralError
	}
	return err
}

// NewErrorWithCode creates and returns a new instance of Error with custom error metadata and an HTTP status code.
func NewErrorWithCode(etype string, code int, message string, data interface{}) error {
	return Error{
		Message:   message,
		ErrorType: etype,
		Data:      data,
		Code:      code,
	}
}
