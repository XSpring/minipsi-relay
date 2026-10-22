// Package core is the adapter between the port layer (internal/psi) and the PSI core in
// internal/protocol. Services never import it; the composition root wires it in.
//
// This part holds the JSON codec and the relay connection. The codec turns a protocol.Frame
// into the kind and payload of a relay message, and back. The payload carries exactly the
// fields of the frame, so both sides check the same values as on the direct gRPC transport,
// including the transcript digest in Done.
//
// # Relay connection
//
// relayConn is the protocol.Conn of one session. Send encodes a frame, numbers it with the
// next seq and posts it through the session's psi.Outbox; a failed Post ends the session.
// deliver takes a message that the orchestrator pushed, checks its session ID, seq (from 1,
// without gaps) and size, decodes it and queues the frame for Recv. A message that deliver
// refuses ends the inbound side. The queue holds 8 frames, enough for the peer's Hello and
// sizes before Run starts; when it is full, deliver waits.
//
// # Payload format
//
// Field names are snake_case. Byte fields are standard base64 strings, numbers are JSON
// numbers, and the role and hash are short strings:
//
//	kind          payload
//	hello         {"protocol_version", "suite", "role": "receiver"|"sender",
//	               "hash": "sha256"|"aes", "lambda", "nonce", "instance_count",
//	               "software_version"}
//	sizes_chunk   {"ids": [...], "sizes": [...]}
//	sender_keys   {"first_instance_index", "keys"}
//	poly_chunk    {"parts": [{"instance_id", "offset", "coeffs"}]}
//	output_chunk  {"parts": [{"instance_id", "values"}]}
//	done          {"frames_sent", "transcript_digest"}
//	abort         {"code", "message"}
//
// Decoding refuses an unknown kind, an unknown field, a wrong type, invalid base64, invalid
// UTF-8, a null payload and trailing data. The role and hash are required and must be one of
// the names above; any other missing field decodes as its zero value, and the protocol then
// checks it. Field names match the way encoding/json matches them (case-insensitively; the
// last duplicate wins). Errors never contain payload contents.
package core
