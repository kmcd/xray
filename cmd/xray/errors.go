package main

import "errors"

// silentErr wraps an error whose message has already been emitted to stderr.
// main() suppresses re-printing of these.
type silentErr struct {
	err  error
	code int
}

func (s *silentErr) Error() string { return s.err.Error() }
func (s *silentErr) Unwrap() error { return s.err }

// silentCode wraps err with a specific exit code and suppresses re-printing.
func silentCode(err error, code int) error {
	return &silentErr{err: err, code: code}
}

func isSilent(err error) bool {
	var s *silentErr
	return errors.As(err, &s)
}

func exitCodeFor(err error) int {
	if err == nil {
		return 0
	}
	var s *silentErr
	if errors.As(err, &s) {
		return s.code
	}
	return 1
}
