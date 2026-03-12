package jsonrpc

import "encoding/json"

// Request is a JSON-RPC 2.0 request or notification.
// If ID is nil, it is a notification.
type Request struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method"`
	Params  json.RawMessage  `json:"params,omitempty"`
}

// IsNotification returns true if this message has no ID (i.e., it's a notification).
func (r *Request) IsNotification() bool {
	return r.ID == nil
}

// Response is a JSON-RPC 2.0 response.
type Response struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *ResponseError   `json:"error,omitempty"`
}

// ResponseError represents a JSON-RPC 2.0 error object.
type ResponseError struct {
	Code    int              `json:"code"`
	Message string           `json:"message"`
	Data    *json.RawMessage `json:"data,omitempty"`
}

// Standard JSON-RPC error codes.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603

	// LSP-specific error codes.
	CodeRequestCancelled = -32800
	CodeContentModified  = -32801
)

// NewResponse creates a successful response for the given request ID.
func NewResponse(id *json.RawMessage, result any) (*Response, error) {
	data, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return &Response{
		JSONRPC: "2.0",
		ID:      id,
		Result:  data,
	}, nil
}

// NewRequestWithID creates a JSON-RPC request with an explicit string ID.
func NewRequestWithID(id string, method string, params any) (*Request, error) {
	data, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	rawID := json.RawMessage(id)
	return &Request{
		JSONRPC: "2.0",
		ID:      &rawID,
		Method:  method,
		Params:  data,
	}, nil
}

// NewNotification creates a JSON-RPC notification (no ID).
func NewNotification(method string, params any) (*Request, error) {
	data, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	return &Request{
		JSONRPC: "2.0",
		Method:  method,
		Params:  data,
	}, nil
}

// NewErrorResponse creates an error response for the given request ID.
func NewErrorResponse(id *json.RawMessage, code int, message string) *Response {
	return &Response{
		JSONRPC: "2.0",
		ID:      id,
		Error: &ResponseError{
			Code:    code,
			Message: message,
		},
	}
}
