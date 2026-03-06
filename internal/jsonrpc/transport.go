package jsonrpc

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

// Transport handles reading and writing JSON-RPC messages
// with Content-Length framing over a byte stream (typically stdin/stdout).
type Transport struct {
	reader *bufio.Reader
	writer io.Writer
	mu     sync.Mutex // protects writer
}

// NewTransport creates a new Transport reading from r and writing to w.
func NewTransport(r io.Reader, w io.Writer) *Transport {
	return &Transport{
		reader: bufio.NewReader(r),
		writer: w,
	}
}

// readBody reads one framed message body from the stream.
func (t *Transport) readBody() ([]byte, error) {
	contentLength := -1

	// Read headers until we hit an empty line.
	for {
		line, err := t.reader.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("reading header: %w", err)
		}
		line = strings.TrimRight(line, "\r\n")

		if line == "" {
			break
		}

		// Parse "Content-Length: <number>"
		if strings.HasPrefix(line, "Content-Length: ") {
			val := strings.TrimPrefix(line, "Content-Length: ")
			n, err := strconv.Atoi(val)
			if err != nil {
				return nil, fmt.Errorf("invalid Content-Length %q: %w", val, err)
			}
			contentLength = n
		}
		// Content-Type and other headers are ignored per LSP spec.
	}

	if contentLength < 0 {
		return nil, fmt.Errorf("missing Content-Length header")
	}

	body := make([]byte, contentLength)
	if _, err := io.ReadFull(t.reader, body); err != nil {
		return nil, fmt.Errorf("reading body: %w", err)
	}
	return body, nil
}

// ReadRaw reads one framed message and returns the raw JSON bytes.
func (t *Transport) ReadRaw() ([]byte, error) {
	return t.readBody()
}

// Read reads one JSON-RPC request/notification from the stream.
func (t *Transport) Read() (*Request, error) {
	body, err := t.readBody()
	if err != nil {
		return nil, err
	}
	var req Request
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("decoding message: %w", err)
	}
	return &req, nil
}

// ReadResponse reads one JSON-RPC response from the stream.
func (t *Transport) ReadResponse() (*Response, error) {
	body, err := t.readBody()
	if err != nil {
		return nil, err
	}
	var resp Response
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	return &resp, nil
}

// writeJSON marshals v and writes it with Content-Length framing.
func (t *Transport) writeJSON(v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encoding message: %w", err)
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))
	if _, err := io.WriteString(t.writer, header); err != nil {
		return fmt.Errorf("writing header: %w", err)
	}
	if _, err := t.writer.Write(body); err != nil {
		return fmt.Errorf("writing body: %w", err)
	}
	return nil
}

// Write writes a JSON-RPC response to the stream with Content-Length framing.
func (t *Transport) Write(resp *Response) error {
	return t.writeJSON(resp)
}

// WriteRequest writes a JSON-RPC request to the stream with Content-Length framing.
func (t *Transport) WriteRequest(req *Request) error {
	return t.writeJSON(req)
}
