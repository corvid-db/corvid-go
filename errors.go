// errors.go — the binding's error type.
//
// Every failing corvid call returns a *CorvidError (never a panic): the
// ABI's detailed code (FFI.md §1.3, read from the thread-local slot
// immediately after the failing call) plus the engine-recorded message.

package corvid

import "fmt"

// CorvidError is the error type returned by every failing corvid call.
// It carries the ABI's detailed error code and the message the engine
// recorded for the failure. Use Code to branch on failure classes:
//
//	var ce *corvid.CorvidError
//	if errors.As(err, &ce) && ce.Code() == corvid.ErrSchemaViolation { ... }
type CorvidError struct {
	code    ErrCode
	message string
}

func newErr(code ErrCode, format string, args ...any) *CorvidError {
	return &CorvidError{code: code, message: fmt.Sprintf(format, args...)}
}

// Code returns the detailed corvid error code (FFI.md §1.3): 1–18 map
// 1:1 onto the engine's error variants, 19 (ErrBusy) is FFI-only.
func (e *CorvidError) Code() ErrCode { return e.code }

// Message returns the failure detail the engine recorded.
func (e *CorvidError) Message() string { return e.message }

// Error implements the error interface.
func (e *CorvidError) Error() string {
	return fmt.Sprintf("corvid: %s (code %d)", e.message, e.code)
}
