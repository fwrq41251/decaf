package lsp

func singleFileEdit(fileURI string, editRange Range, newText string) *WorkspaceEdit {
	return &WorkspaceEdit{
		Changes: map[string][]TextEdit{
			fileURI: {{
				Range:   editRange,
				NewText: newText,
			}},
		},
	}
}

func insertTextAtLine(fileURI string, line int, newText string) *WorkspaceEdit {
	return singleFileEdit(fileURI, Range{
		Start: Position{Line: line, Character: 0},
		End:   Position{Line: line, Character: 0},
	}, newText)
}
