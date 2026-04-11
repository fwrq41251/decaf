package lsp

import (
	"context"
	"encoding/json"
	"fmt"
)

func (h *Handler) handleExecuteCommand(ctx context.Context, params json.RawMessage) (any, error) {
	var p ExecuteCommandParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	switch p.Command {
	case "decaf.overrideMethod":
		return h.executeOverrideMethod(ctx, p.Arguments)
	default:
		return nil, fmt.Errorf("unknown command: %s", p.Command)
	}
}

func (h *Handler) executeOverrideMethod(ctx context.Context, args []json.RawMessage) (any, error) {
	if len(args) < 2 {
		return nil, fmt.Errorf("overrideMethod requires 2 arguments (fileURI, cursorLine)")
	}

	var fileURI string
	var cursorLine int
	if err := json.Unmarshal(args[0], &fileURI); err != nil {
		return nil, fmt.Errorf("invalid fileURI argument: %w", err)
	}
	if err := json.Unmarshal(args[1], &cursorLine); err != nil {
		return nil, fmt.Errorf("invalid cursorLine argument: %w", err)
	}

	overlay, _ := h.docs.Get(fileURI)
	methods, insertLine := collectOverridableMethods(fileURI, h.idx, overlay, cursorLine)
	if len(methods) == 0 {
		return nil, nil
	}

	// Build action items for showMessageRequest.
	var actions []MessageActionItem
	for _, m := range methods {
		actions = append(actions, MessageActionItem{Title: m.method.Name})
	}

	// Send window/showMessageRequest to let the user pick a method.
	var selected *MessageActionItem
	err := h.dispatcher.Call(ctx, "window/showMessageRequest", ShowMessageRequestParams{
		Type:    MessageTypeInfo,
		Message: "Select a method to override:",
		Actions: actions,
	}, &selected)
	if err != nil {
		return nil, fmt.Errorf("showMessageRequest failed: %w", err)
	}

	// User dismissed the dialog.
	if selected == nil {
		return nil, nil
	}

	// Find the selected method and apply the edit.
	for _, m := range methods {
		if m.method.Name == selected.Title {
			editRange := Range{
				Start: Position{Line: insertLine, Character: 0},
				End:   Position{Line: insertLine, Character: 0},
			}
			edit := &WorkspaceEdit{
				Changes: map[string][]TextEdit{
					fileURI: {{Range: editRange, NewText: generateMethodStubForOwner(m.method, m.parentType, h.idx)}},
				},
			}

			// Apply the edit via workspace/applyEdit.
			return nil, h.dispatcher.Call(ctx, "workspace/applyEdit", ApplyWorkspaceEditParams{
				Label: fmt.Sprintf("Override '%s'", m.method.Name),
				Edit:  edit,
			}, nil)
		}
	}

	return nil, nil
}
