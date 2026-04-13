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
	case "decaf.generateGetter":
		return h.executeGenerateAccessor(ctx, p.Arguments, "getter", generateGetter)
	case "decaf.generateSetter":
		return h.executeGenerateAccessor(ctx, p.Arguments, "setter", generateSetter)
	case "decaf.initializeJavaType":
		return h.executeInitializeJavaType(ctx, p.Arguments)
	default:
		return nil, fmt.Errorf("unknown command: %s", p.Command)
	}
}

type generateFunc func(fieldWithType) string

func (h *Handler) executeGenerateAccessor(ctx context.Context, args []json.RawMessage, kind string, generate generateFunc) (any, error) {
	if len(args) < 2 {
		return nil, fmt.Errorf("generate%s requires 2 arguments (fileURI, cursorLine)", kind)
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
	allCandidates := collectFieldCandidates(fileURI, h.idx, overlay, cursorLine)
	var candidates []fieldWithType
	for _, c := range allCandidates {
		if (kind == "getter" && !c.hasGetter) || (kind == "setter" && !c.hasSetter) {
			candidates = append(candidates, c)
		}
	}

	if len(candidates) == 0 {
		return nil, nil
	}

	// Build action items for showMessageRequest.
	var actions []MessageActionItem
	for _, c := range candidates {
		actions = append(actions, MessageActionItem{Title: c.field.Name})
	}

	var selected *MessageActionItem
	err := h.dispatcher.Call(ctx, "window/showMessageRequest", ShowMessageRequestParams{
		Type:    MessageTypeInfo,
		Message: fmt.Sprintf("Select a field to generate %s:", kind),
		Actions: actions,
	}, &selected)
	if err != nil {
		return nil, fmt.Errorf("showMessageRequest failed: %w", err)
	}

	if selected == nil {
		return nil, nil
	}

	for _, c := range candidates {
		if c.field.Name != selected.Title {
			continue
		}

		newText := generate(c)

		content := readContent(fileURI, overlay)
		if content == nil {
			return nil, nil
		}
		tree, treeErr := getTree(content)
		if treeErr != nil {
			return nil, nil
		}

		insertLine := findClassInsertionPoint(tree.RootNode(), content, cursorLine)
		if insertLine < 0 {
			return nil, nil
		}

		editRange := Range{
			Start: Position{Line: insertLine, Character: 0},
			End:   Position{Line: insertLine, Character: 0},
		}
		edit := &WorkspaceEdit{
			Changes: map[string][]TextEdit{
				fileURI: {{Range: editRange, NewText: newText}},
			},
		}

		return nil, h.dispatcher.Call(ctx, "workspace/applyEdit", ApplyWorkspaceEditParams{
			Label: fmt.Sprintf("Generate %s for '%s'", kind, c.field.Name),
			Edit:  edit,
		}, nil)
	}

	return nil, nil
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
	methods, _ := collectOverridableMethods(fileURI, h.idx, overlay, cursorLine)
	if len(methods) == 0 {
		return nil, nil
	}

	// Find class insertion point early.
	content := readContent(fileURI, overlay)
	tree, _ := getTree(content)
	insertLine := findClassInsertionPoint(tree.RootNode(), content, cursorLine)
	if insertLine < 0 {
		return nil, nil
	}

	// Build action items for showMessageRequest.
	var actions []MessageActionItem
	for _, m := range methods {
		actions = append(actions, MessageActionItem{Title: m.method.Name})
	}

	var selected *MessageActionItem
	err := h.dispatcher.Call(ctx, "window/showMessageRequest", ShowMessageRequestParams{
		Type:    MessageTypeInfo,
		Message: "Select a method to override:",
		Actions: actions,
	}, &selected)
	if err != nil {
		return nil, fmt.Errorf("showMessageRequest failed: %w", err)
	}

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

func (h *Handler) executeInitializeJavaType(ctx context.Context, args []json.RawMessage) (any, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("initializeJavaType requires 1 argument (fileURI)")
	}

	var fileURI string
	if err := json.Unmarshal(args[0], &fileURI); err != nil {
		return nil, fmt.Errorf("invalid fileURI argument: %w", err)
	}

	overlay, _ := h.docs.Get(fileURI)
	if !canInitializeJavaType(fileURI, overlay) {
		return nil, nil
	}

	actions := []MessageActionItem{
		{Title: "class"},
		{Title: "interface"},
		{Title: "enum"},
		{Title: "record"},
	}

	var selected *MessageActionItem
	err := h.dispatcher.Call(ctx, "window/showMessageRequest", ShowMessageRequestParams{
		Type:    MessageTypeInfo,
		Message: "Select a Java type to initialize:",
		Actions: actions,
	}, &selected)
	if err != nil {
		return nil, fmt.Errorf("showMessageRequest failed: %w", err)
	}
	if selected == nil {
		return nil, nil
	}

	edit := initializeJavaTypeEdit(h.rootURI, fileURI, selected.Title)
	if edit == nil {
		return nil, nil
	}

	return nil, h.dispatcher.Call(ctx, "workspace/applyEdit", ApplyWorkspaceEditParams{
		Label: fmt.Sprintf("Initialize Java %s", selected.Title),
		Edit:  edit,
	}, nil)
}
