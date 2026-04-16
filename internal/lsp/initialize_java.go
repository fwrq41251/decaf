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
	filePath := uri.ToPath(fileURI)
	if rootURI != "" {
		rootPath := uri.ToPath(rootURI)
		if relDir, err := filepath.Rel(rootPath, filepath.Dir(filePath)); err == nil {
			if relDir == "." {
				return ""
			}
			return packageFromDirSegments(splitPathSegments(relDir))
		}
	}

	return packageFromDirSegments(splitPathSegments(filepath.Dir(filePath)))
}

func packageFromDirSegments(segments []string) string {
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

	bestStart := -1
	bestLen := -1
	for i := 0; i < len(segments); i++ {
		for _, prefix := range prefixes {
			if i+len(prefix) > len(segments) {
				continue
			}
			match := true
			for j := range prefix {
				if segments[i+j] != prefix[j] {
					match = false
					break
				}
			}
			if match && (i > bestStart || (i == bestStart && len(prefix) > bestLen)) {
				bestStart = i
				bestLen = len(prefix)
			}
		}
	}
	if bestStart >= 0 {
		return segments[bestStart+bestLen:]
	}
	return segments
}

func splitPathSegments(p string) []string {
	p = filepath.ToSlash(filepath.Clean(p))
	if p == "." || p == "/" {
		return nil
	}
	raw := strings.Split(p, "/")
	segments := raw[:0]
	for _, segment := range raw {
		if segment == "" || segment == "." {
			continue
		}
		segments = append(segments, segment)
	}
	return segments
}
