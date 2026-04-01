package index

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"log"
	"os"
	"path/filepath"
	"testing"

	sdb "github.com/fwrq41251/decaf/internal/semanticdb"
)

// writeClass builds a minimal .class file in memory for testing.
// writeClassOpts holds options for writing a .class file with optional generic signatures.
type writeClassOpts struct {
	thisClass   string
	superClass  string
	accessFlags uint16
	interfaces  []string
	fields      []classMember
	methods     []classMember
	classSig    string // class-level Signature attribute
}

func writeClass(t *testing.T, thisClass, superClass string, accessFlags uint16, interfaces []string, fields, methods []classMember) []byte {
	t.Helper()
	return writeClassWithSig(t, writeClassOpts{
		thisClass:   thisClass,
		superClass:  superClass,
		accessFlags: accessFlags,
		interfaces:  interfaces,
		fields:      fields,
		methods:     methods,
	})
}

func writeClassWithSig(t *testing.T, opts writeClassOpts) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := &buf

	// Magic.
	binary.Write(w, binary.BigEndian, uint32(classMagic))
	// Version.
	binary.Write(w, binary.BigEndian, uint16(0))  // minor
	binary.Write(w, binary.BigEndian, uint16(52)) // major (Java 8)

	// Build constant pool entries.
	type cpEntry struct {
		tag  byte
		data []byte
	}
	var entries []cpEntry
	utf8Index := func(s string) uint16 {
		for i, e := range entries {
			if e.tag == cpUTF8 {
				var eb bytes.Buffer
				binary.Write(&eb, binary.BigEndian, uint16(len(s)))
				eb.WriteString(s)
				if bytes.Equal(e.data, eb.Bytes()) {
					return uint16(i + 1)
				}
			}
		}
		var eb bytes.Buffer
		binary.Write(&eb, binary.BigEndian, uint16(len(s)))
		eb.WriteString(s)
		entries = append(entries, cpEntry{tag: cpUTF8, data: eb.Bytes()})
		return uint16(len(entries))
	}
	classIndex := func(name string) uint16 {
		nameIdx := utf8Index(name)
		var eb bytes.Buffer
		binary.Write(&eb, binary.BigEndian, nameIdx)
		entries = append(entries, cpEntry{tag: cpClass, data: eb.Bytes()})
		return uint16(len(entries))
	}

	// Pre-register all needed strings and classes.
	thisIdx := classIndex(opts.thisClass)
	superIdx := classIndex(opts.superClass)

	var ifaceIdxs []uint16
	for _, iface := range opts.interfaces {
		ifaceIdxs = append(ifaceIdxs, classIndex(iface))
	}

	type memberIdx struct {
		nameIdx uint16
		descIdx uint16
	}
	var fieldIdxs, methodIdxs []memberIdx
	for _, f := range opts.fields {
		fieldIdxs = append(fieldIdxs, memberIdx{utf8Index(f.Name), utf8Index(f.Descriptor)})
	}
	for _, m := range opts.methods {
		methodIdxs = append(methodIdxs, memberIdx{utf8Index(m.Name), utf8Index(m.Descriptor)})
	}

	// Pre-register "Signature" attribute name and signature values.
	sigAttrNameIdx := utf8Index("Signature")
	var classSigIdx uint16
	if opts.classSig != "" {
		classSigIdx = utf8Index(opts.classSig)
	}
	var fieldSigIdxs, methodSigIdxs []uint16
	for _, f := range opts.fields {
		if f.Signature != "" {
			fieldSigIdxs = append(fieldSigIdxs, utf8Index(f.Signature))
		} else {
			fieldSigIdxs = append(fieldSigIdxs, 0)
		}
	}
	for _, m := range opts.methods {
		if m.Signature != "" {
			methodSigIdxs = append(methodSigIdxs, utf8Index(m.Signature))
		} else {
			methodSigIdxs = append(methodSigIdxs, 0)
		}
	}

	// Write constant pool.
	binary.Write(w, binary.BigEndian, uint16(len(entries)+1)) // cp count
	for _, e := range entries {
		w.WriteByte(e.tag)
		w.Write(e.data)
	}

	// Access flags, this, super.
	binary.Write(w, binary.BigEndian, opts.accessFlags)
	binary.Write(w, binary.BigEndian, thisIdx)
	binary.Write(w, binary.BigEndian, superIdx)

	// Interfaces.
	binary.Write(w, binary.BigEndian, uint16(len(ifaceIdxs)))
	for _, idx := range ifaceIdxs {
		binary.Write(w, binary.BigEndian, idx)
	}

	// Helper to write member attributes (Signature if present).
	writeMemberAttrs := func(sigIdx uint16) {
		if sigIdx != 0 {
			binary.Write(w, binary.BigEndian, uint16(1)) // 1 attribute
			binary.Write(w, binary.BigEndian, sigAttrNameIdx)
			binary.Write(w, binary.BigEndian, uint32(2))
			binary.Write(w, binary.BigEndian, sigIdx)
		} else {
			binary.Write(w, binary.BigEndian, uint16(0)) // no attributes
		}
	}

	// Fields.
	binary.Write(w, binary.BigEndian, uint16(len(opts.fields)))
	for i, f := range opts.fields {
		binary.Write(w, binary.BigEndian, f.AccessFlags)
		binary.Write(w, binary.BigEndian, fieldIdxs[i].nameIdx)
		binary.Write(w, binary.BigEndian, fieldIdxs[i].descIdx)
		writeMemberAttrs(fieldSigIdxs[i])
	}

	// Methods.
	binary.Write(w, binary.BigEndian, uint16(len(opts.methods)))
	for i, m := range opts.methods {
		binary.Write(w, binary.BigEndian, m.AccessFlags)
		binary.Write(w, binary.BigEndian, methodIdxs[i].nameIdx)
		binary.Write(w, binary.BigEndian, methodIdxs[i].descIdx)
		writeMemberAttrs(methodSigIdxs[i])
	}

	// Class attributes.
	if classSigIdx != 0 {
		binary.Write(w, binary.BigEndian, uint16(1)) // 1 attribute
		binary.Write(w, binary.BigEndian, sigAttrNameIdx)
		binary.Write(w, binary.BigEndian, uint32(2))
		binary.Write(w, binary.BigEndian, classSigIdx)
	} else {
		binary.Write(w, binary.BigEndian, uint16(0))
	}

	return buf.Bytes()
}

func TestParseClassFile(t *testing.T) {
	data := writeClass(t,
		"com/example/Foo",
		"java/lang/Object",
		accPublic,
		[]string{"java/io/Serializable"},
		[]classMember{
			{accPublic, "name", "Ljava/lang/String;", ""},
			{accPrivate, "count", "I", ""},
		},
		[]classMember{
			{accPublic, "<init>", "()V", ""},
			{accPublic, "getName", "()Ljava/lang/String;", ""},
			{accPublic, "setName", "(Ljava/lang/String;)V", ""},
			{accPrivate, "validate", "()Z", ""},
		},
	)

	cf, err := parseClassFile(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("parseClassFile: %v", err)
	}

	if cf.ThisClass != "com/example/Foo" {
		t.Errorf("thisClass = %q, want com/example/Foo", cf.ThisClass)
	}
	if cf.SuperClass != "java/lang/Object" {
		t.Errorf("superClass = %q, want java/lang/Object", cf.SuperClass)
	}
	if len(cf.Interfaces) != 1 || cf.Interfaces[0] != "java/io/Serializable" {
		t.Errorf("interfaces = %v, want [java/io/Serializable]", cf.Interfaces)
	}
	if len(cf.Fields) != 2 {
		t.Fatalf("fields count = %d, want 2", len(cf.Fields))
	}
	if cf.Fields[0].Name != "name" || cf.Fields[0].Descriptor != "Ljava/lang/String;" {
		t.Errorf("field[0] = %+v", cf.Fields[0])
	}
	if len(cf.Methods) != 4 {
		t.Fatalf("methods count = %d, want 4", len(cf.Methods))
	}
	if cf.Methods[1].Name != "getName" {
		t.Errorf("methods[1].Name = %q, want getName", cf.Methods[1].Name)
	}
}

func TestDescriptorConversion(t *testing.T) {
	tests := []struct {
		desc    string
		symbol  string
		simple  string
	}{
		{"I", "", "int"},
		{"Z", "", "boolean"},
		{"V", "", "void"},
		{"Ljava/lang/String;", "java/lang/String#", "String"},
		{"Ljava/util/List;", "java/util/List#", "List"},
		{"[Ljava/lang/String;", "java/lang/String#", "String[]"},
		{"[[I", "", "int[][]"},
		{"[B", "", "byte[]"},
	}

	for _, tt := range tests {
		if got := descriptorToSymbol(tt.desc); got != tt.symbol {
			t.Errorf("descriptorToSymbol(%q) = %q, want %q", tt.desc, got, tt.symbol)
		}
		if got := descriptorToSimpleName(tt.desc); got != tt.simple {
			t.Errorf("descriptorToSimpleName(%q) = %q, want %q", tt.desc, got, tt.simple)
		}
	}
}

func TestParseMethodDescriptor(t *testing.T) {
	tests := []struct {
		desc       string
		wantParams []string
		wantRet    string
	}{
		{"()V", nil, "V"},
		{"(I)V", []string{"I"}, "V"},
		{"(Ljava/lang/String;I)Z", []string{"Ljava/lang/String;", "I"}, "Z"},
		{"(Ljava/lang/String;Ljava/util/List;)Ljava/util/Map;", []string{"Ljava/lang/String;", "Ljava/util/List;"}, "Ljava/util/Map;"},
		{"([B)V", []string{"[B"}, "V"},
	}

	for _, tt := range tests {
		params, ret := parseMethodDescriptor(tt.desc)
		if ret != tt.wantRet {
			t.Errorf("parseMethodDescriptor(%q) ret = %q, want %q", tt.desc, ret, tt.wantRet)
		}
		if len(params) != len(tt.wantParams) {
			t.Errorf("parseMethodDescriptor(%q) params = %v, want %v", tt.desc, params, tt.wantParams)
			continue
		}
		for i, p := range params {
			if p != tt.wantParams[i] {
				t.Errorf("parseMethodDescriptor(%q) param[%d] = %q, want %q", tt.desc, i, p, tt.wantParams[i])
			}
		}
	}
}

func TestFormatMethodSignature(t *testing.T) {
	sig := formatMethodSignature("add", "(Ljava/lang/Object;)Z")
	if sig.Label != "boolean add(Object)" {
		t.Errorf("label = %q, want %q", sig.Label, "boolean add(Object)")
	}
	if !sig.HasParams {
		t.Error("expected HasParams = true")
	}
	params := sig.ParseParams()
	if len(params) != 1 || params[0] != "Object" {
		t.Errorf("ParseParams() = %v, want [Object]", params)
	}

	sig2 := formatMethodSignature("main", "([Ljava/lang/String;)V")
	if sig2.Label != "void main(String[])" {
		t.Errorf("label = %q, want %q", sig2.Label, "void main(String[])")
	}
}

func TestConvertClassFile(t *testing.T) {
	cf := &classFile{
		AccessFlags: accPublic,
		ThisClass:   "com/example/Foo",
		SuperClass:  "com/example/Base",
		Interfaces:  []string{"java/io/Serializable"},
		Fields: []classMember{
			{accPublic, "name", "Ljava/lang/String;", ""},
			{accPrivate, "secret", "I", ""},
		},
		Methods: []classMember{
			{accPublic, "<init>", "()V", ""},
			{accPublic, "getName", "()Ljava/lang/String;", ""},
			{accPrivate, "validate", "()Z", ""},
			{accPublic | accStatic, "<clinit>", "()V", ""},
		},
	}

	cs := convertClassFile(cf, true)

	if cs.classSym != "com/example/Foo#" {
		t.Errorf("classSym = %q", cs.classSym)
	}
	if cs.className != "Foo" {
		t.Errorf("className = %q", cs.className)
	}
	if cs.classKind != sdb.SymbolInformation_CLASS {
		t.Errorf("classKind = %v", cs.classKind)
	}

	// Parents: Base + Serializable (no java/lang/Object).
	if len(cs.parents) != 2 {
		t.Fatalf("parents = %v, want 2", cs.parents)
	}
	if cs.parents[0] != "com/example/Base#" {
		t.Errorf("parents[0] = %q", cs.parents[0])
	}

	// Members: only public/protected, no <clinit>, no private.
	// Expected: name (public field), <init> (constructor → "Foo"), getName (method).
	if len(cs.members) != 3 {
		t.Fatalf("members count = %d, want 3, got %+v", len(cs.members), cs.members)
	}

	// Field: name.
	if cs.members[0].name != "name" || cs.members[0].kind != sdb.SymbolInformation_FIELD {
		t.Errorf("members[0] = %+v", cs.members[0])
	}
	if cs.members[0].typeSym != "java/lang/String#" {
		t.Errorf("members[0].typeSym = %q", cs.members[0].typeSym)
	}

	// Constructor.
	if cs.members[1].name != "Foo" || cs.members[1].kind != sdb.SymbolInformation_CONSTRUCTOR {
		t.Errorf("members[1] = %+v", cs.members[1])
	}

	// Method: getName.
	if cs.members[2].name != "getName" || cs.members[2].kind != sdb.SymbolInformation_METHOD {
		t.Errorf("members[2] = %+v", cs.members[2])
	}
	if cs.members[2].typeSym != "java/lang/String#" {
		t.Errorf("members[2].typeSym = %q", cs.members[2].typeSym)
	}
}

func TestIndexClasspathJARs(t *testing.T) {
	// Build a JAR with two .class files.
	tmpDir := t.TempDir()
	jarPath := filepath.Join(tmpDir, "lib.jar")

	fooClass := writeClass(t,
		"com/example/Foo",
		"java/lang/Object",
		accPublic,
		nil,
		[]classMember{
			{accPublic, "value", "I", ""},
		},
		[]classMember{
			{accPublic, "getValue", "()I", ""},
			{accPublic, "setValue", "(I)V", ""},
		},
	)

	barClass := writeClass(t,
		"com/example/Bar",
		"com/example/Foo",
		accPublic,
		[]string{"java/io/Serializable"},
		nil,
		[]classMember{
			{accPublic, "doWork", "(Ljava/lang/String;)Ljava/lang/String;", ""},
		},
	)

	// Private class should be skipped.
	privateClass := writeClass(t,
		"com/example/Internal",
		"java/lang/Object",
		0, // package-private
		nil, nil, nil,
	)

	f, err := os.Create(jarPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	addEntry := func(name string, data []byte) {
		w, _ := zw.Create(name)
		w.Write(data)
	}
	addEntry("com/example/Foo.class", fooClass)
	addEntry("com/example/Bar.class", barClass)
	addEntry("com/example/Internal.class", privateClass)
	zw.Close()
	f.Close()

	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	idx := NewIndex(logger, tmpDir)
	defer idx.Close()

	idx.IndexClasspathJARs([]string{jarPath})

	// Trigger lazy indexing.
	idx.MembersOfType("com/example/Foo#")

	// Check that Foo is indexed.
	fooDefs := idx.definitions["com/example/Foo#"]
	if len(fooDefs) == 0 {
		t.Fatal("Foo# not in definitions")
	}
	if fooDefs[0].Name != "Foo" {
		t.Errorf("Foo name = %q", fooDefs[0].Name)
	}

	// Check members.
	fooMembers := idx.ownerMembers["com/example/Foo#"]
	if len(fooMembers) != 3 { // value, getValue, setValue
		t.Fatalf("Foo members = %d, want 3", len(fooMembers))
	}

	// Check Bar extends Foo.
	barChildren := idx.implementors["com/example/Foo#"]
	found := false
	for _, child := range barChildren {
		if child == "com/example/Bar#" {
			found = true
		}
	}
	if !found {
		t.Errorf("Bar not in Foo implementors: %v", barChildren)
	}

	// Check private class was skipped.
	if defs := idx.definitions["com/example/Internal#"]; len(defs) > 0 {
		t.Error("Internal (package-private) should not be indexed")
	}

	// Check TypeBySimpleName.
	fooTypes := idx.TypeBySimpleName("Foo")
	if len(fooTypes) == 0 {
		t.Error("TypeBySimpleName(Foo) returned empty")
	}

	// Check symbolType.
	getValType := idx.TypeOfSymbol("com/example/Bar#doWork().")
	if getValType != "java/lang/String#" {
		t.Errorf("TypeOfSymbol(doWork) = %q, want java/lang/String#", getValType)
	}

	// Second call should be a no-op (deduplicated).
	idx.IndexClasspathJARs([]string{jarPath})
}

func TestIndexClasspathJARs_MergesWithSemanticDB(t *testing.T) {
	// Simulate SemanticDB having partial data (1 method) and the classfile
	// having the full list (3 methods including the 1 from SemanticDB).
	tmpDir := t.TempDir()
	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	idx := NewIndex(logger, tmpDir)
	defer idx.Close()

	// Pre-populate ownerMembers with 1 method from SemanticDB.
	sdbMethod := &Symbol{
		Name:   "getValue",
		Symbol: "com/example/Foo#getValue().",
		Kind:   sdb.SymbolInformation_METHOD,
	}
	idx.ownerMembers["com/example/Foo#"] = []*Symbol{sdbMethod}
	idx.definitions["com/example/Foo#"] = []*Symbol{{
		Name:   "Foo",
		Symbol: "com/example/Foo#",
		Kind:   sdb.SymbolInformation_CLASS,
		URI:    "src/Foo.java",
	}}

	// Build JAR with same class having 3 methods (getValue, setValue, reset).
	jarPath := filepath.Join(tmpDir, "lib.jar")
	fooClass := writeClass(t,
		"com/example/Foo",
		"java/lang/Object",
		accPublic,
		nil,
		nil,
		[]classMember{
			{accPublic, "getValue", "()I", ""},
			{accPublic, "setValue", "(I)V", ""},
			{accPublic, "reset", "()V", ""},
		},
	)
	f, err := os.Create(jarPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, _ := zw.Create("com/example/Foo.class")
	w.Write(fooClass)
	zw.Close()
	f.Close()

	idx.IndexClasspathJARs([]string{jarPath})
	idx.MembersOfType("com/example/Foo#")

	// After merge, ownerMembers should have all 3 methods:
	// the 1 from SemanticDB + 2 new from classfile (getValue is deduped).
	members := idx.ownerMembers["com/example/Foo#"]
	if len(members) != 3 {
		names := make([]string, len(members))
		for i, m := range members {
			names[i] = m.Name
		}
		t.Fatalf("expected 3 members, got %d: %v", len(members), names)
	}

	nameSet := make(map[string]bool)
	for _, m := range members {
		nameSet[m.Name] = true
	}
	for _, want := range []string{"getValue", "setValue", "reset"} {
		if !nameSet[want] {
			t.Errorf("missing member %q in ownerMembers", want)
		}
	}
}

func TestIndexClasspathJARs_StaticFlag(t *testing.T) {
	tmpDir := t.TempDir()
	jarPath := filepath.Join(tmpDir, "lib.jar")

	fooClass := writeClass(t,
		"com/example/Foo",
		"java/lang/Object",
		accPublic,
		nil,
		[]classMember{
			{accPublic | accStatic, "CONSTANT", "I", ""},
			{accPublic, "value", "I", ""},
		},
		[]classMember{
			{accPublic | accStatic, "create", "()Lcom/example/Foo;", ""},
			{accPublic, "getValue", "()I", ""},
		},
	)

	f, err := os.Create(jarPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, _ := zw.Create("com/example/Foo.class")
	w.Write(fooClass)
	zw.Close()
	f.Close()

	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	idx := NewIndex(logger, tmpDir)
	defer idx.Close()

	idx.IndexClasspathJARs([]string{jarPath})
	idx.MembersOfType("com/example/Foo#")

	members := idx.ownerMembers["com/example/Foo#"]
	staticMap := make(map[string]bool)
	for _, m := range members {
		staticMap[m.Name] = m.IsStatic
	}

	if !staticMap["CONSTANT"] {
		t.Error("CONSTANT should be static")
	}
	if staticMap["value"] {
		t.Error("value should not be static")
	}
	if !staticMap["create"] {
		t.Error("create should be static")
	}
	if staticMap["getValue"] {
		t.Error("getValue should not be static")
	}
}

func TestIndexClasspathJARs_ProjectTakesPriority(t *testing.T) {
	// Simulate a class that exists in both .semanticdb (project) and JAR.
	// Project definitions should not be overwritten.
	tmpDir := t.TempDir()
	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	idx := NewIndex(logger, tmpDir)
	defer idx.Close()

	// Pre-populate with a "project" definition.
	projectSym := &Symbol{
		Name:   "Foo",
		Symbol: "com/example/Foo#",
		Kind:   sdb.SymbolInformation_CLASS,
		URI:    "src/Foo.java",
	}
	idx.definitions["com/example/Foo#"] = []*Symbol{projectSym}

	// Build JAR with same class.
	jarPath := filepath.Join(tmpDir, "lib.jar")
	fooClass := writeClass(t, "com/example/Foo", "java/lang/Object", accPublic, nil, nil, nil)
	f, _ := os.Create(jarPath)
	zw := zip.NewWriter(f)
	w, _ := zw.Create("com/example/Foo.class")
	w.Write(fooClass)
	zw.Close()
	f.Close()

	idx.IndexClasspathJARs([]string{jarPath})

	// Should still have the project definition, not replaced.
	defs := idx.definitions["com/example/Foo#"]
	if len(defs) != 1 || defs[0].URI != "src/Foo.java" {
		t.Errorf("project definition was overwritten: %+v", defs)
	}
}

func TestIndexClasspathJARs_GenericSignatures(t *testing.T) {
	// Build a generic class: MyList<E> extends AbstractList<E> implements List<E>
	// with a method E get(int) that has Signature "(I)TE;"
	tmpDir := t.TempDir()
	jarPath := filepath.Join(tmpDir, "lib.jar")

	listClass := writeClassWithSig(t, writeClassOpts{
		thisClass:   "com/example/MyList",
		superClass:  "java/util/AbstractList",
		accessFlags: accPublic,
		interfaces:  []string{"java/util/List"},
		classSig:    "<E:Ljava/lang/Object;>Ljava/util/AbstractList<TE;>;Ljava/util/List<TE;>;",
		methods: []classMember{
			{accPublic, "get", "(I)Ljava/lang/Object;", "(I)TE;"},
			{accPublic, "add", "(Ljava/lang/Object;)Z", "(TE;)Z"},
			{accPublic, "size", "()I", ""},
		},
		fields: []classMember{
			{accPublic, "data", "[Ljava/lang/Object;", "[TE;"},
		},
	})

	f, err := os.Create(jarPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, _ := zw.Create("com/example/MyList.class")
	w.Write(listClass)
	zw.Close()
	f.Close()

	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	idx := NewIndex(logger, tmpDir)
	defer idx.Close()

	idx.IndexClasspathJARs([]string{jarPath})
	idx.MembersOfType("com/example/MyList#")

	// Verify classTypeParams.
	typeParams := idx.ClassTypeParams("com/example/MyList#")
	if len(typeParams) != 1 {
		t.Fatalf("classTypeParams count = %d, want 1", len(typeParams))
	}
	if typeParams[0] != "com/example/MyList#[E]" {
		t.Errorf("classTypeParams[0] = %q, want %q", typeParams[0], "com/example/MyList#[E]")
	}

	// Verify parentTypes has generic args.
	parentTypes := idx.ParentTypesOf("com/example/MyList#")
	if len(parentTypes) < 2 {
		t.Fatalf("parentTypes count = %d, want >= 2", len(parentTypes))
	}
	// AbstractList<E>
	if parentTypes[0].Sym != "java/util/AbstractList#" {
		t.Errorf("parentTypes[0].Sym = %q, want java/util/AbstractList#", parentTypes[0].Sym)
	}
	if len(parentTypes[0].Args) != 1 || parentTypes[0].Args[0].Sym != "com/example/MyList#[E]" {
		t.Errorf("parentTypes[0].Args = %v, want [{Sym: com/example/MyList#[E]}]", parentTypes[0].Args)
	}

	// Verify symbolDeclType for get() returns E (type param ref).
	getSym := "com/example/MyList#get()."
	getDeclType := idx.DeclTypeOf(getSym)
	if getDeclType == nil {
		t.Fatal("DeclTypeOf(get) is nil")
	}
	if getDeclType.Sym != "com/example/MyList#[E]" {
		t.Errorf("DeclTypeOf(get).Sym = %q, want %q", getDeclType.Sym, "com/example/MyList#[E]")
	}

	// Verify size() has no declType (primitive return, no generic signature).
	sizeSym := "com/example/MyList#size()."
	sizeDeclType := idx.DeclTypeOf(sizeSym)
	if sizeDeclType != nil {
		t.Errorf("DeclTypeOf(size) = %v, want nil (primitive return has no declType)", sizeDeclType)
	}
}
