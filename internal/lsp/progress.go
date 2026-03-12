package lsp

import (
	"fmt"
	"sync/atomic"

	"github.com/fwrq41251/decaf/internal/jsonrpc"
)

var progressSeq atomic.Int64

// progress tracks a single work-done progress sequence.
type progress struct {
	token     string
	transport *jsonrpc.Transport
}

// beginProgress creates a progress token with the client and sends the begin notification.
// Returns a progress handle that can be used to report/end, or nil if creation failed.
func (h *Handler) beginProgress(title, message string) *progress {
	token := fmt.Sprintf("decaf-%d", progressSeq.Add(1))

	// Ask the client to create the progress token.
	id := progressSeq.Add(1)
	rawID := fmt.Sprintf("%d", id)
	req, err := jsonrpc.NewRequestWithID(rawID, "window/workDoneProgress/create", WorkDoneProgressCreateParams{
		Token: token,
	})
	if err != nil {
		h.logger.Printf("failed to create progress request: %v", err)
		return nil
	}
	if err := h.transport.WriteRequest(req); err != nil {
		h.logger.Printf("failed to send progress create: %v", err)
		return nil
	}

	p := &progress{token: token, transport: h.transport}

	// Send begin notification.
	p.notify(WorkDoneProgressBegin{
		Kind:    "begin",
		Title:   title,
		Message: message,
	})

	return p
}

// report sends an intermediate progress update.
func (p *progress) report(message string, pct *int) {
	if p == nil {
		return
	}
	p.notify(WorkDoneProgressReport{
		Kind:       "report",
		Message:    message,
		Percentage: pct,
	})
}

// end sends the final progress notification.
func (p *progress) end(message string) {
	if p == nil {
		return
	}
	p.notify(WorkDoneProgressEnd{
		Kind:    "end",
		Message: message,
	})
}

func (p *progress) notify(value any) {
	notification, err := jsonrpc.NewNotification("$/progress", ProgressParams{
		Token: p.token,
		Value: value,
	})
	if err != nil {
		return
	}
	_ = p.transport.WriteRequest(notification)
}
