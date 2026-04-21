package lsp

import (
	"io"
	"log"
	"strings"
	"testing"

	"github.com/fwrq41251/decaf/internal/index"
	sdb "github.com/fwrq41251/decaf/internal/semanticdb"
)

func TestFormatJavadoc_FullExample(t *testing.T) {
	doc := `Looks up a value by key.
@param key the key to look up
@param defaultValue fallback value
@return the resolved value
@throws IllegalArgumentException if key is null`

	got := formatJavadoc(doc)
	expected := `Looks up a value by key.

**Parameters:**
- ` + "`key`" + ` — the key to look up
- ` + "`defaultValue`" + ` — fallback value

**Returns:** the resolved value

**Throws:**
- ` + "`IllegalArgumentException`" + ` — if key is null`

	if got != expected {
		t.Fatalf("formatJavadoc mismatch.\nGot:\n%s\n\nExpected:\n%s", got, expected)
	}
}

func TestFormatJavadoc_DescriptionOnly(t *testing.T) {
	doc := "Simple description with no tags."
	got := formatJavadoc(doc)
	if got != "Simple description with no tags." {
		t.Fatalf("expected plain description, got %q", got)
	}
}

func TestFormatJavadoc_ParamOnly(t *testing.T) {
	doc := "@param name the name"
	got := formatJavadoc(doc)
	expected := "**Parameters:**\n- `name` — the name"
	if got != expected {
		t.Fatalf("got %q, expected %q", got, expected)
	}
}

func TestFormatJavadoc_ParamNoDescription(t *testing.T) {
	doc := "@param name"
	got := formatJavadoc(doc)
	expected := "**Parameters:**\n- `name`"
	if got != expected {
		t.Fatalf("got %q, expected %q", got, expected)
	}
}

func TestFormatJavadoc_Exception(t *testing.T) {
	doc := "@exception IOException if an I/O error occurs"
	got := formatJavadoc(doc)
	expected := "**Throws:**\n- `IOException` — if an I/O error occurs"
	if got != expected {
		t.Fatalf("got %q, expected %q", got, expected)
	}
}

func TestFormatJavadoc_Deprecated(t *testing.T) {
	doc := "Old method.\n@deprecated Use newMethod() instead"
	got := formatJavadoc(doc)
	expected := "Old method.\n\n**Deprecated:** Use newMethod() instead"
	if got != expected {
		t.Fatalf("got:\n%s\n\nexpected:\n%s", got, expected)
	}
}

func TestFormatJavadoc_InlineTags(t *testing.T) {
	doc := "Returns {@code null} if not found.\n@param key the {@link Map} key\n@return a {@literal raw <value>}"
	got := formatJavadoc(doc)
	expected := "Returns `null` if not found.\n\n**Parameters:**\n- `key` — the `Map` key\n\n**Returns:** a raw <value>"
	if got != expected {
		t.Fatalf("formatJavadoc inline tags mismatch.\nGot:\n%s\n\nExpected:\n%s", got, expected)
	}
}

func TestFormatJavadoc_DeprecatedNoMessage(t *testing.T) {
	doc := "@deprecated"
	got := formatJavadoc(doc)
	expected := "**Deprecated**"
	if got != expected {
		t.Fatalf("got %q, expected %q", got, expected)
	}
}

func TestFormatJavadoc_SinceAndSee(t *testing.T) {
	doc := "Does something.\n@since 1.5\n@see OtherClass#method()"
	got := formatJavadoc(doc)
	expected := "Does something.\n\n**Since:** 1.5\n\n**See:** `OtherClass#method()`"
	if got != expected {
		t.Fatalf("got:\n%s\n\nexpected:\n%s", got, expected)
	}
}

func TestFormatJavadoc_StarPrefixLines(t *testing.T) {
	doc := " * Computes the result.\n * @param x the input\n * @return the output"
	got := formatJavadoc(doc)
	expected := "Computes the result.\n\n**Parameters:**\n- `x` — the input\n\n**Returns:** the output"
	if got != expected {
		t.Fatalf("got:\n%s\n\nexpected:\n%s", got, expected)
	}
}

func TestFormatHover_MemberShowsOwnerAndPackage(t *testing.T) {
	sym := &index.Symbol{
		Name:       "add",
		Symbol:     "java/util/List#add().",
		Kind:       sdb.SymbolInformation_METHOD,
		Visibility: index.VisibilityProtected,
		IsStatic:   true,
		IsAbstract: true,
		IsOverride: true,
		Signature: &index.SignatureInfo{
			Label:     "boolean add(E element)",
			HasParams: true,
		},
	}

	got := formatHover(sym, nil)
	if !strings.Contains(got, "```java\nprotected static abstract boolean add(E element)\n```") {
		t.Fatalf("expected declaration code block in hover, got:\n%s", got)
	}
	if !strings.Contains(got, "**Owner:** `java.util.List`") {
		t.Fatalf("expected owner in hover, got:\n%s", got)
	}
	if strings.Contains(got, "**Package:** `java.util`") {
		t.Fatalf("did not expect redundant package in member hover, got:\n%s", got)
	}
}

func TestFormatHover_TypeShowsPackageAndInheritance(t *testing.T) {
	idx := index.NewIndex(log.New(io.Discard, "", 0), "")
	idx.SetClassTypeParamsForTest("java/util/ArrayList#", []string{
		"java/util/ArrayList#[E]",
	})
	idx.SetParentTypesForTest("java/util/ArrayList#", []*index.TypeExpr{
		{Sym: "java/util/AbstractList#", Args: []*index.TypeExpr{{Sym: "java/lang/String#"}}},
		{Sym: "java/util/List#", Args: []*index.TypeExpr{{Sym: "java/lang/String#"}}},
		{Sym: "java/util/RandomAccess#"},
		{Sym: "java/lang/Cloneable#"},
	})

	sym := &index.Symbol{
		Name:       "ArrayList",
		Symbol:     "java/util/ArrayList#",
		Kind:       sdb.SymbolInformation_CLASS,
		Visibility: index.VisibilityPublic,
		Signature: &index.SignatureInfo{
			Label: "ArrayList<E>",
		},
	}

	got := formatHover(sym, idx)
	if !strings.Contains(got, "```java\npublic class ArrayList<E>\n```") {
		t.Fatalf("expected type declaration code block in hover, got:\n%s", got)
	}
	if !strings.Contains(got, "**Package:** `java.util`") {
		t.Fatalf("expected package in hover, got:\n%s", got)
	}
	if !strings.Contains(got, "**Type Parameters:** `E`") {
		t.Fatalf("expected type params in hover, got:\n%s", got)
	}
	if !strings.Contains(got, "**Extends:** `AbstractList<String>`") {
		t.Fatalf("expected extends info in hover, got:\n%s", got)
	}
	if !strings.Contains(got, "**Implements:** `List<String>`, `RandomAccess`, `Cloneable`") {
		t.Fatalf("expected implements info in hover, got:\n%s", got)
	}
}

func TestFormatHover_AbstractClassDeclaration(t *testing.T) {
	sym := &index.Symbol{
		Name:       "BaseThing",
		Symbol:     "com/example/BaseThing#",
		Kind:       sdb.SymbolInformation_CLASS,
		Visibility: index.VisibilityPublic,
		IsAbstract: true,
		IsSealed:   true,
		Signature: &index.SignatureInfo{
			Label: "BaseThing<T>",
		},
	}

	got := formatHover(sym, nil)
	if !strings.Contains(got, "```java\npublic abstract sealed class BaseThing<T>\n```") {
		t.Fatalf("expected abstract class declaration in hover, got:\n%s", got)
	}
}
