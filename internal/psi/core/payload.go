package core

// The DTOs below are the JSON payloads of relay messages, one per frame type. They mirror the
// frame types in internal/protocol/messages.go field by field; []byte fields encode as
// standard base64.

type helloDTO struct {
	ProtocolVersion uint32 `json:"protocol_version"`
	Suite           string `json:"suite"`
	Role            string `json:"role"`
	Hash            string `json:"hash"`
	Lambda          uint32 `json:"lambda"`
	Nonce           []byte `json:"nonce"`
	InstanceCount   uint32 `json:"instance_count"`
	SoftwareVersion string `json:"software_version"`
}

type sizesChunkDTO struct {
	IDs   []uint32 `json:"ids"`
	Sizes []uint32 `json:"sizes"`
}

type senderKeysDTO struct {
	FirstInstanceIndex uint32 `json:"first_instance_index"`
	Keys               []byte `json:"keys"`
}

type polyPartDTO struct {
	InstanceID uint32 `json:"instance_id"`
	Offset     uint32 `json:"offset"`
	Coeffs     []byte `json:"coeffs"`
}

type polyChunkDTO struct {
	Parts []polyPartDTO `json:"parts"`
}

type outputPartDTO struct {
	InstanceID uint32 `json:"instance_id"`
	Values     []byte `json:"values"`
}

type outputChunkDTO struct {
	Parts []outputPartDTO `json:"parts"`
}

type doneDTO struct {
	FramesSent       uint64 `json:"frames_sent"`
	TranscriptDigest []byte `json:"transcript_digest"`
}

type abortDTO struct {
	Code    uint32 `json:"code"`
	Message string `json:"message"`
}
