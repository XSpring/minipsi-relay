package service

import "errors"

// Errors the service returns, wrapped; the handler maps them to HTTP status codes.
var (
	// ErrInvalidState reports a request that the state of the session does not allow: run
	// before the session is ready, abort after it ended, delete before it ended.
	ErrInvalidState = errors.New("service: invalid session state")
	// ErrMisrouted reports a request meant for another CC: its cc_id or receiver_cc_id is not
	// this CC.
	ErrMisrouted = errors.New("service: request is for another CC")
)
