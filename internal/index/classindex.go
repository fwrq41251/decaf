package index

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"runtime"
	"strings"

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

	merged := idx.streamJARsIntoIndex(toIndex)

	idx.mu.Lock()
	for _, jar := range toIndex {
		idx.indexedJARs[jar] = struct{}{}
	}
	idx.mu.Unlock()
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

	// Generic type information parsed from Signature attributes.
	typeParams     []string    // type parameter symbols, e.g. ["java/util/List#[E]"]
	parentTypesGen []*TypeExpr // parent types with generic args

	// Members.
	members []memberSymbol

	// Lazy indexing info.
	jarPath        string
	entryName      string
	membersScanned bool // true if members were actually parsed (not skipped)
}

type memberSymbol struct {
	sym        string // e.g. "java/util/ArrayList#add(+1)."
	name       string // e.g. "add"
	kind       sdb.SymbolInformation_Kind
	typeSym    string         // return type / field type as SemanticDB symbol
	declType   *TypeExpr      // declared type preserving generics (from Signature attribute)
	signature  *SignatureInfo // human-readable signature
	isStatic   bool
	isAbstract bool
}

// streamJARsIntoIndex reads classpath JARs concurrently and incrementally
// merges each parsed class summary into the index to avoid retaining a large
// intermediate slice in memory.
func (idx *Index) streamJARsIntoIndex(jars []string) int {
	workers := runtime.NumCPU()
	if workers > len(jars) {
		workers = len(jars)
	}

	classCh := make(chan classSymbols, workers)
	done := make(chan struct{})

	go func() {
		defer close(classCh)

		g := new(errgroup.Group)
		g.SetLimit(workers)

		for _, jar := range jars {
			jar := jar
			g.Go(func() error {
				scanJAR(jar, false, func(cs classSymbols) {
					classCh <- cs
				})
				return nil
			})
		}

		_ = g.Wait()
		close(done)
	}()

	merged := 0
	for cs := range classCh {
		idx.mu.Lock()
		if idx.mergeTypeDirectoryEntry(cs) {
			merged++
		}
		idx.mu.Unlock()
	}
	<-done
	return merged
}

// scanJAR opens a single JAR and parses all .class files within it, invoking
// emit for each extracted public/protected class summary.
func scanJAR(jarPath string, includeMembers bool, emit func(classSymbols)) {
	r, err := zip.OpenReader(jarPath)
	if err != nil {
		return
	}
	defer r.Close()

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
		emit(*cs)
	}
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

	// Parents (non-generic, from descriptor).
	var parents []string
	if cf.SuperClass != "" && cf.SuperClass != "java/lang/Object" {
		parents = append(parents, cf.SuperClass+"#")
	}
	for _, iface := range cf.Interfaces {
		parents = append(parents, iface+"#")
	}

	// Parse class-level generic signature if present.
	var typeParamSyms []string
	var typeParamNames []string
	var parentTypesGen []*TypeExpr
	if cf.Signature != "" {
		if ginfo := parseClassGenericSig(cf.Signature, classSym); ginfo != nil {
			typeParamNames = ginfo.typeParams
			for _, name := range ginfo.typeParams {
				typeParamSyms = append(typeParamSyms, classSym+"["+name+"]")
			}
			parentTypesGen = ginfo.parents
		}
	}

	// Members: only public/protected.
	var members []memberSymbol

	if includeMembers {
		methodOverloads := make(map[string]int)

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

			ms := memberSymbol{
				sym:      fieldSym,
				name:     f.Name,
				kind:     sdb.SymbolInformation_FIELD,
				typeSym:  typeSym,
				isStatic: f.AccessFlags&accStatic != 0,
				signature: &SignatureInfo{
					Label: fmt.Sprintf("%s: %s", f.Name, descriptorToSimpleName(f.Descriptor)),
				},
			}

			// Parse field generic signature for type-parameterized fields.
			if f.Signature != "" {
				if finfo := parseFieldGenericSig(f.Signature, classSym, typeParamNames); finfo != nil && finfo.returnType != nil {
					ms.declType = finfo.returnType
				}
			}

			members = append(members, ms)
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

			overloadIndex := methodOverloads[m.Name]
			methodOverloads[m.Name]++
			methodSuffix := "()."
			if overloadIndex > 0 {
				methodSuffix = fmt.Sprintf("(+%d).", overloadIndex)
			}
			methodSym := cf.ThisClass + "#" + m.Name + methodSuffix
			sig := formatMethodSignature(methodName, m.Descriptor)

			ms := memberSymbol{
				sym:        methodSym,
				name:       methodName,
				kind:       methodKind,
				typeSym:    retSym,
				isStatic:   m.AccessFlags&accStatic != 0,
				isAbstract: m.AccessFlags&accAbstract != 0,
				signature:  sig,
			}

			// Parse method generic signature for return type with generics.
			if m.Signature != "" {
				if minfo := parseMethodGenericSig(m.Signature, classSym, typeParamNames); minfo != nil {
					if minfo.returnType != nil {
						ms.declType = minfo.returnType
					}
					if sig != nil && len(minfo.paramTypes) > 0 {
						for i := range minfo.paramTypes {
							if i >= len(sig.Params) {
								break
							}
							sig.Params[i].Type = typeExprToDisplay(minfo.paramTypes[i])
							sig.Params[i].TypeSym = typeExprBaseSym(minfo.paramTypes[i])
						}
					}
				}
			}

			members = append(members, ms)
		}
	}

	return &classSymbols{
		classSym:       classSym,
		className:      className,
		classKind:      kind,
		parents:        parents,
		typeParams:     typeParamSyms,
		parentTypesGen: parentTypesGen,
		members:        members,
		membersScanned: includeMembers,
	}
}

// mergeTypeDirectoryEntry adds the lightweight class directory entry used for
// type-name completion and later lazy loading. It intentionally avoids eagerly
// merging hierarchy/member data.
func (idx *Index) mergeTypeDirectoryEntry(cs classSymbols) bool {
	classSym := idx.intern(cs.classSym)

	// Store lazy indexing info.
	if cs.jarPath != "" {
		idx.classLocations[classSym] = classLocation{
			jarPath:   cs.jarPath,
			entryName: cs.entryName,
		}
	}

	// If the class is already defined (e.g. from SemanticDB), keep the
	// existing class definition. Hierarchy/member data is loaded lazily.
	alreadyDefined := len(idx.definitions[classSym]) > 0
	if _, ok := idx.externalTypeInfo[classSym]; ok {
		return false
	}

	if !alreadyDefined {
		// Store in the lightweight external type directory instead of
		// reusing workspace definitions.
		idx.externalTypeInfo[classSym] = externalTypeInfo{
			name: cs.className,
			kind: cs.classKind,
		}
		name := strings.ToLower(cs.className)
		idx.externalTypesBySimpleName[name] = append(idx.externalTypesBySimpleName[name], classSym)
	}

	return !alreadyDefined
}

// mergeLazyClassData merges hierarchy, generic type information, and optional
// members for a class parsed on-demand from a classfile.
func (idx *Index) mergeLazyClassData(cs classSymbols) {
	classSym := idx.intern(cs.classSym)

	if len(idx.childToParents[classSym]) == 0 {
		for _, parent := range cs.parents {
			parentSym := parent
			idx.implementors[parentSym] = append(idx.implementors[parentSym], classSym)
			idx.childToParents[classSym] = append(idx.childToParents[classSym], parentSym)
		}
	}

	if len(cs.typeParams) > 0 && len(idx.classTypeParams[classSym]) == 0 {
		idx.classTypeParams[classSym] = cs.typeParams
	}
	if len(cs.parentTypesGen) > 0 && len(idx.parentTypes[classSym]) == 0 {
		idx.parentTypes[classSym] = cs.parentTypesGen
	}
	idx.skeletonIndexedClasses[classSym] = struct{}{}

	if len(cs.members) == 0 {
		if cs.membersScanned {
			idx.fullyIndexedClasses[classSym] = struct{}{}
		}
		return
	}

	existingMembers := make(map[string]struct{})
	for _, id := range idx.ownerMembers[classSym] {
		existingMembers[idx.symbol(id).Symbol] = struct{}{}
	}
	for _, m := range cs.members {
		if _, ok := existingMembers[m.sym]; ok {
			continue
		}
		memberSym := idx.intern(m.sym)
		sid := idx.addSymbol(Symbol{
			Name:       m.name,
			Symbol:     memberSym,
			Kind:       m.kind,
			Signature:  m.signature,
			IsStatic:   m.isStatic,
			IsAbstract: m.isAbstract,
		})
		idx.definitions[memberSym] = append(idx.definitions[memberSym], sid)
		idx.ownerMembers[classSym] = append(idx.ownerMembers[classSym], sid)
		existingMembers[m.sym] = struct{}{}
		idx.memberBySimpleName[strings.ToLower(m.name)] = append(idx.memberBySimpleName[strings.ToLower(m.name)], sid)

		if m.typeSym != "" {
			idx.symbolType[memberSym] = m.typeSym
		}
		if m.declType != nil {
			idx.symbolDeclType[memberSym] = m.declType
		}
	}
	if cs.membersScanned {
		idx.fullyIndexedClasses[classSym] = struct{}{}
	}
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
	loc, ok := idx.classLocations[classSym]
	idx.mu.RUnlock()

	if !ok || loc.jarPath == "" {
		return
	}

	// Perform I/O outside the lock.
	cs, err := idx.indexClassMembers(loc.jarPath, loc.entryName)
	if err != nil || cs == nil {
		return
	}

	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.mergeLazyClassData(*cs)
}

// ensureClassSkeletonIndexed ensures hierarchy and generic type information is
// indexed for the given external class without requiring members.
func (idx *Index) ensureClassSkeletonIndexed(classSym string) {
	if !strings.HasSuffix(classSym, "#") {
		return
	}

	idx.mu.RLock()
	if _, ok := idx.skeletonIndexedClasses[classSym]; ok {
		idx.mu.RUnlock()
		return
	}
	loc, ok := idx.classLocations[classSym]
	idx.mu.RUnlock()

	if !ok || loc.jarPath == "" {
		return
	}

	cs, err := idx.indexClassSkeleton(loc.jarPath, loc.entryName)
	if err != nil || cs == nil {
		return
	}

	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.mergeLazyClassData(*cs)
}

func (idx *Index) indexClassMembers(jarPath, entryName string) (*classSymbols, error) {
	return idx.indexClass(jarPath, entryName, true)
}

func (idx *Index) indexClassSkeleton(jarPath, entryName string) (*classSymbols, error) {
	return idx.indexClass(jarPath, entryName, false)
}

func (idx *Index) indexClass(jarPath, entryName string, includeMembers bool) (*classSymbols, error) {
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

	cs := convertClassFile(cf, includeMembers)
	cs.jarPath = jarPath
	cs.entryName = entryName
	return cs, nil
}
