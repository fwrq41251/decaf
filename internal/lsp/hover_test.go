package lsp

import "testing"

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
