package index

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync"

	sdb "github.com/fwrq41251/decaf/internal/semanticdb"
	"golang.org/x/sync/errgroup"
)

// IndexClasspathJARs scans multiple classpath JARs concurrently and indexes
// all public/protected classes, fields, and methods. This enables completion,
// hover, and type resolution for third-party dependencies that have no
// .semanticdb files.
func (idx *Index) IndexClasspathJARs(jars []string) {
	if len(jars) == 0 {
		return
	}

	// Filter to JARs we haven't indexed yet.
	idx.mu.Lock()
	var toIndex []string
	for _, jar := range jars {
		if _, ok := idx.indexedJARs[jar]; !ok {
			toIndex = append(toIndex, jar)
		}
	}
	idx.mu.Unlock()

	if len(toIndex) == 0 {
		return
	}

	idx.logger.Printf("classindex: scanning %d classpath JARs", len(toIndex))

	// Phase 1: Concurrent JAR scanning.
	allSymbols := scanJARsConcurrently(toIndex)

	// Phase 2: Merge into index under write lock.
	idx.mu.Lock()
	defer idx.mu.Unlock()

	merged := 0
	for _, cs := range allSymbols {
		if idx.mergeClassSymbols(cs) {
			merged++
		}
	}
	for _, jar := range toIndex {
		idx.indexedJARs[jar] = struct{}{}
	}
	idx.logger.Printf("classindex: indexed %d classes from %d JARs", merged, len(toIndex))
}

// classSymbols holds all the symbols extracted from a single .class file,
// ready to be merged into the Index.
type classSymbols struct {
	// Class-level info.
	classSym  string // e.g. "java/util/ArrayList#"
	className string // e.g. "ArrayList"
	classKind sdb.SymbolInformation_Kind

	// Parent types (super class + interfaces).
	parents []string // SemanticDB symbols

	// Members.
	members []memberSymbol

	// Lazy indexing info.
	jarPath        string
	entryName      string
	membersScanned bool // true if members were actually parsed (not skipped)
}

type memberSymbol struct {
	sym       string // e.g. "java/util/ArrayList#add(+1)."
	name      string // e.g. "add"
	kind      sdb.SymbolInformation_Kind
	typeSym   string         // return type / field type as SemanticDB symbol
	signature *SignatureInfo // human-readable signature
	isStatic  bool
}

// scanJARsConcurrently reads all .class files from the given JARs in parallel,
// extracts public/protected symbols, and returns them.
func scanJARsConcurrently(jars []string) []classSymbols {
	workers := runtime.NumCPU()
	if workers > len(jars) {
		workers = len(jars)
	}

	var mu sync.Mutex
	var allSymbols []classSymbols

	g := new(errgroup.Group)
	g.SetLimit(workers)

	for _, jar := range jars {
		g.Go(func() error {
			syms, err := scanJAR(jar, false) // lazy: skip members
			if err != nil {
				// Non-fatal: skip this JAR.
				return nil
			}
			mu.Lock()
			allSymbols = append(allSymbols, syms...)
			mu.Unlock()
			return nil
		})
	}
	_ = g.Wait()
	return allSymbols
}

// scanJAR opens a single JAR and parses all .class files within it.
func scanJAR(jarPath string, includeMembers bool) ([]classSymbols, error) {
	r, err := zip.OpenReader(jarPath)
	if err != nil {
		return nil, err
	}
	defer r.Close()

	var result []classSymbols
	for _, f := range r.File {
		if !strings.HasSuffix(f.Name, ".class") {
			continue
		}
		// Skip inner classes with $ — they complicate things and are less
		// commonly needed for top-level completion. We index Outer$Inner
		// as "Outer.Inner" by handling the $ separator.
		// Skip module-info and package-info.
		base := f.Name[strings.LastIndex(f.Name, "/")+1:]
		if base == "module-info.class" || base == "package-info.class" {
			continue
		}

		cs, err := parseClassEntry(f, jarPath, includeMembers)
		if err != nil || cs == nil {
			continue
		}
		result = append(result, *cs)
	}
	return result, nil
}

// parseClassEntry reads and parses a single .class file from a ZIP entry.
func parseClassEntry(f *zip.File, jarPath string, includeMembers bool) (*classSymbols, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	// Read into memory (class files are typically small).
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, err
	}

	cf, err := parseClassFile(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}

	// Only index public or protected classes.
	if cf.AccessFlags&accPublic == 0 && cf.AccessFlags&accProtected == 0 {
		return nil, nil
	}

	cs := convertClassFile(cf, includeMembers)
	cs.jarPath = jarPath
	cs.entryName = f.Name
	return cs, nil
}

// convertClassFile converts a parsed classFile into classSymbols for indexing.
func convertClassFile(cf *classFile, includeMembers bool) *classSymbols {
	classSym := cf.ThisClass + "#"
	className := cf.ThisClass
	if idx := strings.LastIndex(className, "/"); idx >= 0 {
		className = className[idx+1:]
	}
	// Handle inner classes: Outer$Inner → Inner
	if idx := strings.LastIndex(className, "$"); idx >= 0 {
		className = className[idx+1:]
	}

	// Determine kind.
	kind := sdb.SymbolInformation_CLASS
	if cf.AccessFlags&accInterface != 0 {
		kind = sdb.SymbolInformation_INTERFACE
	} else if cf.AccessFlags&accEnum != 0 {
		kind = sdb.SymbolInformation_CLASS // enum is a special class
	}

	// Parents.
	var parents []string
	if cf.SuperClass != "" && cf.SuperClass != "java/lang/Object" {
		parents = append(parents, cf.SuperClass+"#")
	}
	for _, iface := range cf.Interfaces {
		parents = append(parents, iface+"#")
	}

	// Members: only public/protected.
	var members []memberSymbol

	if includeMembers {
		for _, f := range cf.Fields {
			if f.AccessFlags&accPublic == 0 && f.AccessFlags&accProtected == 0 {
				continue
			}
			// Skip synthetic fields.
			if f.Name == "" || strings.HasPrefix(f.Name, "$") {
				continue
			}

			fieldSym := cf.ThisClass + "#" + f.Name + "."
			typeSym := descriptorToSymbol(f.Descriptor)

			members = append(members, memberSymbol{
				sym:      fieldSym,
				name:     f.Name,
				kind:     sdb.SymbolInformation_FIELD,
				typeSym:  typeSym,
				isStatic: f.AccessFlags&accStatic != 0,
				signature: &SignatureInfo{
					Label: fmt.Sprintf("%s: %s", f.Name, descriptorToSimpleName(f.Descriptor)),
				},
			})
		}

		for _, m := range cf.Methods {
			if m.AccessFlags&accPublic == 0 && m.AccessFlags&accProtected == 0 {
				continue
			}
			// Skip synthetic / bridge methods.
			if m.Name == "" || strings.HasPrefix(m.Name, "$") {
				continue
			}
			// Skip static initializer.
			if m.Name == "<clinit>" {
				continue
			}

			methodKind := sdb.SymbolInformation_METHOD
			methodName := m.Name
			if m.Name == "<init>" {
				methodKind = sdb.SymbolInformation_CONSTRUCTOR
				methodName = className
			}

			_, ret := parseMethodDescriptor(m.Descriptor)
			retSym := descriptorToSymbol(ret)

			methodSym := cf.ThisClass + "#" + m.Name + "()."
			sig := formatMethodSignature(methodName, m.Descriptor)

			members = append(members, memberSymbol{
				sym:       methodSym,
				name:      methodName,
				kind:      methodKind,
				typeSym:   retSym,
				isStatic:  m.AccessFlags&accStatic != 0,
				signature: sig,
			})
		}
	}

	return &classSymbols{
		classSym:       classSym,
		className:      className,
		classKind:      kind,
		parents:        parents,
		members:        members,
		membersScanned: includeMembers,
	}
}

// mergeClassSymbols adds the symbols from a parsed class into the index maps.
// Returns true if the class was newly added (not a duplicate).
func (idx *Index) mergeClassSymbols(cs classSymbols) bool {
	classSym := idx.intern(cs.classSym)

	// Store lazy indexing info.
	if cs.jarPath != "" {
		idx.classToJAR[classSym] = cs.jarPath
		idx.classToEntryName[classSym] = cs.entryName
	}

	// If the class is already defined (e.g. from SemanticDB), keep the
	// existing class definition but still merge members and parent info
	// that SemanticDB doesn't provide for external types.
	alreadyDefined := len(idx.definitions[classSym]) > 0

	if !alreadyDefined {
		// Add class definition.
		s := &Symbol{
			Name:   cs.className,
			Symbol: classSym,
			Kind:   cs.classKind,
		}
		idx.definitions[classSym] = append(idx.definitions[classSym], s)

		// typeBySimpleName index.
		name := strings.ToLower(cs.className)
		idx.typeBySimpleName[name] = append(idx.typeBySimpleName[name], s)
	}

	// Merge parent types if not already set (SemanticDB may or may not have them).
	if len(idx.childToParents[classSym]) == 0 {
		for _, parent := range cs.parents {
			parentSym := idx.intern(parent)
			idx.implementors[parentSym] = append(idx.implementors[parentSym], classSym)
			idx.childToParents[classSym] = append(idx.childToParents[classSym], parentSym)
		}
	}

	// Merge members from classfile. SemanticDB may have already added some
	// members (only those referenced by project code), so we deduplicate
	// by member name to avoid duplicates while ensuring the full method list
	// from the classfile is available.
	if len(cs.members) > 0 {
		existingMembers := make(map[string]struct{})
		for _, m := range idx.ownerMembers[classSym] {
			existingMembers[m.Name] = struct{}{}
		}
		for _, m := range cs.members {
			if _, ok := existingMembers[m.name]; ok {
				continue
			}
			memberSym := idx.intern(m.sym)
			ms := &Symbol{
				Name:      m.name,
				Symbol:    memberSym,
				Kind:      m.kind,
				Signature: m.signature,
				IsStatic:  m.isStatic,
			}
			idx.definitions[memberSym] = append(idx.definitions[memberSym], ms)
			idx.ownerMembers[classSym] = append(idx.ownerMembers[classSym], ms)

			if m.typeSym != "" {
				idx.symbolType[memberSym] = idx.intern(m.typeSym)
			}
		}
	}
	if cs.membersScanned {
		idx.fullyIndexedClasses[classSym] = struct{}{}
	}

	return !alreadyDefined
}

// ensureMembersIndexed ensures that all public/protected members of the given
// class symbol are indexed. This is called on-demand for completion/hover.
func (idx *Index) ensureMembersIndexed(classSym string) {
	if !strings.HasSuffix(classSym, "#") {
		return
	}

	idx.mu.RLock()
	if _, ok := idx.fullyIndexedClasses[classSym]; ok {
		idx.mu.RUnlock()
		return
	}
	jarPath, ok := idx.classToJAR[classSym]
	entryName := idx.classToEntryName[classSym]
	idx.mu.RUnlock()

	if !ok || jarPath == "" {
		return
	}

	// Perform I/O outside the lock.
	cs, err := idx.indexClassMembers(jarPath, entryName)
	if err != nil || cs == nil {
		return
	}

	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.mergeClassSymbols(*cs)
}

func (idx *Index) indexClassMembers(jarPath, entryName string) (*classSymbols, error) {
	r, err := zip.OpenReader(jarPath)
	if err != nil {
		return nil, err
	}
	defer r.Close()

	f, err := r.Open(entryName)
	if err != nil {
		return nil, nil
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}

	cf, err := parseClassFile(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}

	// Only index public or protected classes.
	if cf.AccessFlags&accPublic == 0 && cf.AccessFlags&accProtected == 0 {
		return nil, nil
	}

	cs := convertClassFile(cf, true)
	cs.jarPath = jarPath
	cs.entryName = entryName
	return cs, nil
}

