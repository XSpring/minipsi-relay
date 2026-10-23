package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/XSpring/minipsi/internal/orcapi"
	"github.com/XSpring/minipsi/internal/psi"
)

// maxDrainBytes is how much of an ORC response body is read and dropped, so that the
// connection can be reused.
const maxDrainBytes = 64 << 10

// orcClient calls the endpoints of the orchestrator: O1 posts a PSI message, O2 reports the
// state of a session. The service shares one client, and its http.Client, between sessions.
type orcClient struct {
	base *url.URL
	http *http.Client
	ccID string
}

// newORCClient returns a client for the ORC at baseURL. ccID is this CC.
func newORCClient(baseURL string, c *http.Client, ccID string) (*orcClient, error) {
	u, err := parseORCURL(baseURL)
	if err != nil {
		return nil, err
	}
	return &orcClient{base: u, http: c, ccID: ccID}, nil
}

// parseORCURL accepts an http or https URL with a host and no query or fragment. Its error
// does not repeat the URL, which may hold credentials.
func parseORCURL(s string) (*url.URL, error) {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" ||
		u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("service: the ORC URL must be an http or https URL with a host")
	}
	return u, nil
}

// postMessage sends one PSI message to the ORC (O1, POST /v1/messages).
func (c *orcClient) postMessage(ctx context.Context, env *orcapi.Envelope) error {
	return c.post(ctx, c.base.JoinPath("v1", "messages"), env)
}

// reportStatus reports the state of session sessionID to the ORC (O2,
// POST /v1/sessions/{session_id}/status).
func (c *orcClient) reportStatus(ctx context.Context, sessionID string, r *orcapi.StatusReport) error {
	if !orcapi.ValidSessionID(sessionID) {
		return errors.New("service: invalid session ID")
	}
	return c.post(ctx, c.base.JoinPath("v1", "sessions", sessionID, "status"), r)
}

// post sends v as JSON and returns nil for a 2xx answer. It does not retry. Its errors never
// contain the body.
func (c *orcClient) post(ctx context.Context, endpoint *url.URL, v orcapi.Validator) error {
	if err := v.Validate(); err != nil {
		return fmt.Errorf("service: invalid request to the ORC: %w", err)
	}
	body, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("service: encode a request to the ORC: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("service: request to the ORC: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("service: post to the ORC: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrainBytes))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("service: orc status %d", resp.StatusCode)
	}
	return nil
}

// orcOutbox is the psi.Outbox of one session. It adds the CC IDs that the ORC routes by and
// posts each message as an Envelope (O1).
type orcOutbox struct {
	c         *orcClient
	sessionID string
	peerCCID  string
}

var _ psi.Outbox = (*orcOutbox)(nil)

// Post implements psi.Outbox.
func (o *orcOutbox) Post(ctx context.Context, m *psi.Message) error {
	if m == nil || m.SessionID != o.sessionID {
		return errors.New("service: message for another session")
	}
	return o.c.postMessage(ctx, toEnvelope(m, o.c.ccID, o.peerCCID))
}

// toEnvelope wraps m for the ORC: sender is this CC, receiver the other CC of the session.
// The payload is shared, not copied.
func toEnvelope(m *psi.Message, sender, receiver string) *orcapi.Envelope {
	return &orcapi.Envelope{
		SessionID: m.SessionID, SenderCCID: sender, ReceiverCCID: receiver,
		Seq: m.Seq, Kind: m.Kind, Payload: m.Payload,
	}
}

// toMessage removes the routing fields of env. The payload is shared, not copied.
func toMessage(env *orcapi.Envelope) *psi.Message {
	return &psi.Message{SessionID: env.SessionID, Seq: env.Seq, Kind: env.Kind, Payload: env.Payload}
}
