package lsp

import (
	"path/filepath"
	"strings"

	"github.com/fwrq41251/decaf/internal/uri"
)

func initializeJavaTypeAction(fileURI string, overlay string) *CodeAction {
	if !canInitializeJavaType(fileURI, overlay) {
		return nil
	}

	return &CodeAction{
		Title: "Initialize Java Type",
		Kind:  "source",
		Command: &Command{
			Title:     "Initialize Java Type",
			Command:   "decaf.initializeJavaType",
			Arguments: []any{fileURI},
		},
	}
}

func canInitializeJavaType(fileURI string, overlay string) bool {
	if !strings.HasSuffix(fileURI, ".java") {
		return false
	}
	if strings.TrimSpace(overlay) != "" {
		return false
	}

	className := strings.TrimSuffix(filepath.Base(uri.ToPath(fileURI)), ".java")
	return isJavaIdentifier(className)
}

func initializeJavaTypeEdit(rootURI, fileURI, kind string) *WorkspaceEdit {
	template := initializeJavaTypeTemplate(rootURI, fileURI, kind)
	if template == "" {
		return nil
	}

	return insertTextAtLine(fileURI, 0, template)
}

func initializeJavaTypeTemplate(rootURI, fileURI, kind string) string {
	filePath := uri.ToPath(fileURI)
	className := strings.TrimSuffix(filepath.Base(filePath), ".java")
	if !isJavaIdentifier(className) {
		return ""
	}

	packageName := deriveJavaPackage(rootURI, fileURI)

	var decl string
	switch kind {
	case "class":
		decl = "public class " + className + " {\n}\n"
	case "interface":
		decl = "public interface " + className + " {\n}\n"
	case "enum":
		decl = "public enum " + className + " {\n}\n"
	case "record":
		decl = "public record " + className + "() {\n}\n"
	default:
		return ""
	}

	if packageName == "" {
		return decl
	}
	return "package " + packageName + ";\n\n" + decl
}

func deriveJavaPackage(rootURI, fileURI string) string {
	rootPath := uri.ToPath(rootURI)
	filePath := uri.ToPath(fileURI)

	relDir, err := filepath.Rel(rootPath, filepath.Dir(filePath))
	if err != nil {
		return ""
	}
	if relDir == "." {
		return ""
	}

	segments := strings.Split(filepath.ToSlash(relDir), "/")
	segments = trimJavaSourceRootPrefix(segments)
	if len(segments) == 0 {
		return ""
	}

	for _, segment := range segments {
		if !isJavaIdentifier(segment) {
			return ""
		}
	}
	return strings.Join(segments, ".")
}

func trimJavaSourceRootPrefix(segments []string) []string {
	prefixes := [][]string{
		{"src", "main", "java"},
		{"src", "test", "java"},
		{"src", "java"},
		{"test", "java"},
		{"java"},
	}
	for _, prefix := range prefixes {
		if len(segments) < len(prefix) {
			continue
		}
		match := true
		for i := range prefix {
			if segments[i] != prefix[i] {
				match = false
				break
			}
		}
		if match {
			return segments[len(prefix):]
		}
	}
	return segments
}
