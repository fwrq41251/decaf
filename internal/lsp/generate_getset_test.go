package lsp

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fwrq41251/decaf/internal/index"
	sdb "github.com/fwrq41251/decaf/internal/semanticdb"
	"github.com/fwrq41251/decaf/internal/uri"
	"google.golang.org/protobuf/proto"
)

func setupGetSetIndex(t *testing.T, javaSource string, symbols []*sdb.SymbolInformation) (*index.Index, string) {
	t.Helper()
	tmpDir := t.TempDir()
	srcDir := filepath.Join(tmpDir, "src", "main", "java", "com", "example")
	os.MkdirAll(srcDir, 0755)

	javaPath := filepath.Join(srcDir, "User.java")
	os.WriteFile(javaPath, []byte(javaSource), 0644)

	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	idx := index.NewIndex(logger, tmpDir)

	sdbDir := filepath.Join(tmpDir, "META-INF", "semanticdb")
	os.MkdirAll(sdbDir, 0755)

	docs := &sdb.TextDocuments{
		Documents: []*sdb.TextDocument{
			{
				Uri:     "src/main/java/com/example/User.java",
				Symbols: symbols,
				Occurrences: []*sdb.SymbolOccurrence{
					{
						Symbol: "com/example/User#",
						Range:  &sdb.Range{StartLine: 2, StartCharacter: 13, EndLine: 2, EndCharacter: 17},
						Role:   sdb.SymbolOccurrence_DEFINITION,
					},
				},
			},
		},
	}
	data, _ := proto.Marshal(docs)
	os.WriteFile(filepath.Join(sdbDir, "User.java.semanticdb"), data, 0644)

	idx.Load()
	return idx, uri.FromPath(javaPath)
}

func fieldSignature(typeSym string) *sdb.Signature {
	return &sdb.Signature{
		SealedValue: &sdb.Signature_ValueSignature{
			ValueSignature: &sdb.ValueSignature{
				Tpe: &sdb.Type{
					SealedValue: &sdb.Type_TypeRef{
						TypeRef: &sdb.TypeRef{Symbol: typeSym},
					},
				},
			},
		},
	}
}

var baseSource = `package com.example;

public class User {
    private String name;
    private int age;
    private boolean active;
}
`

func TestCollectFieldCandidates(t *testing.T) {
	idx, fileURI := setupGetSetIndex(t, baseSource, []*sdb.SymbolInformation{
		{Symbol: "com/example/User#", DisplayName: "User", Kind: sdb.SymbolInformation_CLASS},
		{Symbol: "com/example/User#name.", DisplayName: "name", Kind: sdb.SymbolInformation_FIELD, Signature: fieldSignature("java/lang/String#")},
		{Symbol: "com/example/User#age.", DisplayName: "age", Kind: sdb.SymbolInformation_FIELD, Signature: fieldSignature("scala/Int#")},
		{Symbol: "com/example/User#active.", DisplayName: "active", Kind: sdb.SymbolInformation_FIELD, Signature: fieldSignature("scala/Boolean#")},
	})

	candidates := collectFieldCandidates(fileURI, idx, baseSource, 3)
	if len(candidates) != 3 {
		t.Fatalf("expected 3 field candidates, got %d", len(candidates))
	}
	if candidates[0].field.Name != "name" || candidates[0].typeName != "String" {
		t.Errorf("unexpected first candidate: %s %s", candidates[0].field.Name, candidates[0].typeName)
	}
	if candidates[1].field.Name != "age" || candidates[1].typeName != "int" {
		t.Errorf("unexpected second candidate: %s %s", candidates[1].field.Name, candidates[1].typeName)
	}
	if candidates[2].field.Name != "active" || candidates[2].typeName != "boolean" {
		t.Errorf("unexpected third candidate: %s %s", candidates[2].field.Name, candidates[2].typeName)
	}
}

func TestCollectFieldCandidates_DetectsExisting(t *testing.T) {
	idx, fileURI := setupGetSetIndex(t, baseSource, []*sdb.SymbolInformation{
		{Symbol: "com/example/User#", DisplayName: "User", Kind: sdb.SymbolInformation_CLASS},
		{Symbol: "com/example/User#name.", DisplayName: "name", Kind: sdb.SymbolInformation_FIELD},
		{Symbol: "com/example/User#getName().", DisplayName: "getName", Kind: sdb.SymbolInformation_METHOD},
		{Symbol: "com/example/User#setName(Ljava/lang/String;)V.", DisplayName: "setName", Kind: sdb.SymbolInformation_METHOD},
	})

	candidates := collectFieldCandidates(fileURI, idx, baseSource, 3)
	if len(candidates) != 1 {
		t.Fatalf("expected 1 field candidate, got %d", len(candidates))
	}
	c := candidates[0]
	if c.field.Name != "name" {
		t.Errorf("expected candidate 'name', got %q", c.field.Name)
	}
	if !c.hasGetter {
		t.Error("expected hasGetter to be true")
	}
	if !c.hasSetter {
		t.Error("expected hasSetter to be true")
	}
}

func TestGenerateGetter(t *testing.T) {
	f := fieldWithType{field: index.Symbol{Name: "name"}, typeName: "String"}
	result := generateGetter(f)

	if !strings.Contains(result, "public String getName()") {
		t.Errorf("expected getter signature, got: %s", result)
	}
	if !strings.Contains(result, "return name;") {
		t.Errorf("expected getter body, got: %s", result)
	}
}

func TestGenerateGetter_Boolean(t *testing.T) {
	tests := []struct {
		name     string
		typeName string
		expected string
	}{
		{"active", "boolean", "public boolean isActive()"},
		{"enabled", "Boolean", "public Boolean isEnabled()"},
		{"isActive", "boolean", "public boolean isActive()"}, // Should not be isIsActive
	}

	for _, tc := range tests {
		f := fieldWithType{field: index.Symbol{Name: tc.name}, typeName: tc.typeName}
		result := generateGetter(f)
		if !strings.Contains(result, tc.expected) {
			t.Errorf("field %s (%s): expected %q, got: %s", tc.name, tc.typeName, tc.expected, result)
		}
	}
}

func TestGenerateSetter(t *testing.T) {
	f := fieldWithType{field: index.Symbol{Name: "name"}, typeName: "String"}
	result := generateSetter(f)

	if !strings.Contains(result, "public void setName(String name)") {
		t.Errorf("expected setter signature, got: %s", result)
	}
	if !strings.Contains(result, "this.name = name;") {
		t.Errorf("expected setter body, got: %s", result)
	}
}
