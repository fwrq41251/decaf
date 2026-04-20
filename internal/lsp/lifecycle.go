package lsp

import (
	"context"
	"encoding/json"
	"github.com/fwrq41251/decaf/internal/jsonrpc"
)

func (h *Handler) handleInitialize(_ context.Context, params json.RawMessage) (any, error) {
	var p InitializeParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	h.rootURI = p.RootURI
	h.clientCaps = p.Capabilities
	h.logger.Printf("initialize: rootURI=%s, processID=%v", p.RootURI, p.ProcessID)
	if p.Capabilities.TextDocument != nil && p.Capabilities.TextDocument.Completion != nil && p.Capabilities.TextDocument.Completion.CompletionItem != nil {
		h.logger.Printf("initialize: completion.snippetSupport=%v", p.Capabilities.TextDocument.Completion.CompletionItem.SnippetSupport)
	} else {
		h.logger.Printf("initialize: completion.snippetSupport=unknown")
	}

	caps := ServerCapabilities{
		TextDocumentSync: &TextDocumentSyncOptions{
			OpenClose: true,
			Change:    SyncIncremental,
			Save:      &SaveOptions{IncludeText: false},
		},
		DefinitionProvider:        true,
		ReferencesProvider:        true,
		HoverProvider:             true,
		CompletionProvider:        &CompletionOptions{TriggerCharacters: []string{"."}},
		SignatureHelpProvider:     &SignatureHelpOptions{TriggerCharacters: []string{"(", ","}},
		RenameProvider:            &RenameOptions{PrepareProvider: true},
		DocumentSymbolProvider:    true,
		DocumentHighlightProvider: true,
		ImplementationProvider:    true,
		WorkspaceSymbolProvider:   true,
		CodeActionProvider:        &CodeActionOptions{CodeActionKinds: []string{CodeActionSourceOrganizeImports, CodeActionQuickFix, "source"}},
		ExecuteCommandProvider:    &ExecuteCommandOptions{Commands: []string{"decaf.overrideMethod", "decaf.generateGetter", "decaf.generateSetter", "decaf.initializeJavaType"}},
		InlayHintProvider:         true,
		CallHierarchyProvider:     true,
		TypeHierarchyProvider:     true,
	}

	return InitializeResult{
		Capabilities: caps,
		ServerInfo: &ServerInfo{
			Name:    "decaf",
			Version: "0.0.1",
		},
	}, nil
}

func (h *Handler) handleInitialized(ctx context.Context, _ json.RawMessage) (any, error) {
	h.initialized.Store(true)
	h.workspace.start(ctx)
	return nil, nil
}

func (h *Handler) handleShutdown(ctx context.Context, _ json.RawMessage) (any, error) {
	h.shutdown.Store(true)
	h.workspace.shutdown(ctx)
	return nil, nil
}

func (h *Handler) handleExit(_ context.Context, _ json.RawMessage) (any, error) {
	h.logger.Println("exit notification received")
	h.exitOnce.Do(func() { close(h.exitCh) })
	return nil, jsonrpc.ErrExit
}

func (h *Handler) registerFileWatchers(ctx context.Context) {
	registration := map[string]any{
		"registrations": []map[string]any{
			{
				"id":     "decaf-file-watcher",
				"method": "workspace/didChangeWatchedFiles",
				"registerOptions": DidChangeWatchedFilesRegistrationOptions{
					Watchers: []FileSystemWatcher{
						{
							GlobPattern: "**/*.java",
							Kind:        WatchKindCreate | WatchKindChange | WatchKindDelete,
						},
					},
				},
			},
		},
	}

	if err := h.dispatcher.Call(ctx, "client/registerCapability", registration, nil); err != nil {
		h.logger.Printf("failed to register file watchers: %v", err)
		return
	}
	h.logger.Println("registered file watcher for **/*.java")
}
