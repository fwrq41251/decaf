package lsp

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/fwrq41251/decaf/internal/index"
	sdb "github.com/fwrq41251/decaf/internal/semanticdb"
)

// --- Call Hierarchy ---

func (h *Handler) handlePrepareCallHierarchy(ctx context.Context, params json.RawMessage) (any, error) {
	var p CallHierarchyPrepareParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if !h.waitIndexReady(ctx) {
		return nil, nil
	}

	item := h.callHierarchyItemAt(p.TextDocument.URI, p.Position.Line, p.Position.Character)
	if item == nil {
		return nil, nil
	}
	return []CallHierarchyItem{*item}, nil
}

func (h *Handler) handleIncomingCalls(ctx context.Context, params json.RawMessage) (any, error) {
	var p CallHierarchyIncomingCallsParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if !h.waitIndexReady(ctx) {
		return []CallHierarchyIncomingCall{}, nil
	}

	sym, _ := p.Item.Data.(string)
	if sym == "" {
		return []CallHierarchyIncomingCall{}, nil
	}

	// 1. Get all references (could be thousands across many files).
	refs := h.idx.SymbolReferences(sym)

	// 2. Group references by their source file URI to minimize index access.
	refsByFile := make(map[string][]index.Occurrence)
	for _, ref := range refs {
		if ref.Role == sdb.SymbolOccurrence_DEFINITION {
			continue
		}
		refsByFile[ref.URI] = append(refsByFile[ref.URI], ref)
	}

	type callerKey struct {
		sym string
		uri string
	}
	type callerInfo struct {
		item   *CallHierarchyItem
		ranges []Range
	}
	callers := make(map[callerKey]*callerInfo)

	// 3. Process each file once.
	for uri, fileRefs := range refsByFile {
		fileURI := h.toFileURI(uri)
		symbols := h.idx.FileSymbols(fileURI)

		// Filter and sort potential callables for this file once.
		var callables []*index.Symbol
		for i := range symbols {
			s := &symbols[i]
			if isCallableKind(s.Kind) && !s.Range.IsEmpty() {
				callables = append(callables, s)
			}
		}

		// Sort callables by start position to allow early exit during search.
		sort.Slice(callables, func(i, j int) bool {
			if callables[i].Range.StartLine != callables[j].Range.StartLine {
				return callables[i].Range.StartLine < callables[j].Range.StartLine
			}
			return callables[i].Range.StartCharacter < callables[j].Range.StartCharacter
		})

		for _, ref := range fileRefs {
			// Efficiently find the enclosing callable for this reference.
			enclosingSym := findEnclosingInSortedList(callables, int(ref.Range.StartLine), int(ref.Range.StartCharacter))
			if enclosingSym == nil {
				continue
			}

			enclosing := symbolToCallHierarchyItem(enclosingSym, h)
			if enclosing == nil {
				continue
			}

			key := callerKey{sym: enclosing.Data.(string), uri: enclosing.URI}
			ci := callers[key]
			if ci == nil {
				ci = &callerInfo{item: enclosing}
				callers[key] = ci
			}
			ci.ranges = append(ci.ranges, sdbRangeToLSP(ref.Range))
		}
	}

	result := make([]CallHierarchyIncomingCall, 0, len(callers))
	for _, ci := range callers {
		result = append(result, CallHierarchyIncomingCall{
			From:       *ci.item,
			FromRanges: ci.ranges,
		})
	}
	// Sort by caller name for deterministic output.
	sort.Slice(result, func(i, j int) bool {
		return result[i].From.Name < result[j].From.Name
	})
	return result, nil
}

// findEnclosingInSortedList finds the narrowest callable containing the position.
// Assumes symbols are sorted by start position.
func findEnclosingInSortedList(symbols []*index.Symbol, line, character int) *index.Symbol {
	var best *index.Symbol
	for _, s := range symbols {
		// Optimization: Since symbols are sorted by start position, we can stop
		// once the current symbol's start is already past our target position.
		if int(s.Range.StartLine) > line || (int(s.Range.StartLine) == line && int(s.Range.StartCharacter) > character) {
			break
		}

		if index.ContainsPosition(s.Range, line, character) {
			if best == nil || isNarrower(s.Range, best.Range) {
				best = s
			}
		}
	}
	return best
}

func (h *Handler) handleOutgoingCalls(ctx context.Context, params json.RawMessage) (any, error) {
	var p CallHierarchyOutgoingCallsParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if !h.waitIndexReady(ctx) {
		return []CallHierarchyOutgoingCall{}, nil
	}

	sym, _ := p.Item.Data.(string)
	if sym == "" {
		return []CallHierarchyOutgoingCall{}, nil
	}

	// Get all occurrences in the file where this method is defined,
	// and find references to other methods within this method's range.
	fileURI := p.Item.URI
	occs := h.idx.AllFileOccurrences(fileURI)

	// Collect method/constructor references within the item's range.
	type calleeInfo struct {
		item   *CallHierarchyItem
		ranges []Range
	}
	callees := make(map[string]*calleeInfo)

	for _, occ := range occs {
		if occ.Role == sdb.SymbolOccurrence_DEFINITION {
			continue
		}
		if !rangeContains(p.Item.Range, sdbRangeToLSP(occ.Range)) {
			continue
		}
		def := h.idx.SymbolDefinition(occ.Symbol)
		if def == nil || !isCallableKind(def.Kind) {
			continue
		}
		ci := callees[occ.Symbol]
		if ci == nil {
			item := symbolToCallHierarchyItem(def, h)
			if item == nil {
				continue
			}
			ci = &calleeInfo{item: item}
			callees[occ.Symbol] = ci
		}
		ci.ranges = append(ci.ranges, sdbRangeToLSP(occ.Range))
	}

	result := make([]CallHierarchyOutgoingCall, 0, len(callees))
	for _, ci := range callees {
		result = append(result, CallHierarchyOutgoingCall{
			To:         *ci.item,
			FromRanges: ci.ranges,
		})
	}
	return result, nil
}

// callHierarchyItemAt builds a CallHierarchyItem for the symbol at the given position.
func (h *Handler) callHierarchyItemAt(fileURI string, line, character int) *CallHierarchyItem {
	occ := h.idx.OccurrenceAt(fileURI, line, character)
	if occ == nil {
		return nil
	}
	def := h.idx.SymbolDefinition(occ.Symbol)
	if def == nil {
		return nil
	}
	// We only care about callable symbols for Call Hierarchy.
	if !isCallableKind(def.Kind) {
		return nil
	}
	return symbolToCallHierarchyItem(def, h)
}

// findEnclosingCallable finds the method/constructor that contains the given position.
func (h *Handler) findEnclosingCallable(fileURI string, line, character int) *CallHierarchyItem {
	symbols := h.idx.FileSymbols(fileURI)

	var callables []*index.Symbol
	for i := range symbols {
		s := &symbols[i]
		if isCallableKind(s.Kind) && !s.Range.IsEmpty() {
			callables = append(callables, s)
		}
	}

	sort.Slice(callables, func(i, j int) bool {
		if callables[i].Range.StartLine != callables[j].Range.StartLine {
			return callables[i].Range.StartLine < callables[j].Range.StartLine
		}
		return callables[i].Range.StartCharacter < callables[j].Range.StartCharacter
	})

	best := findEnclosingInSortedList(callables, line, character)
	if best == nil {
		return nil
	}
	return symbolToCallHierarchyItem(best, h)
}

func symbolToCallHierarchyItem(s *index.Symbol, h *Handler) *CallHierarchyItem {
	if s == nil || s.Range.IsEmpty() {
		return nil
	}
	detail := ""
	if s.Signature != nil {
		detail = s.Signature.Label
	}
	return &CallHierarchyItem{
		Name:           s.Name,
		Kind:           sdbKindToLSP(s.Kind),
		Detail:         detail,
		URI:            h.toFileURI(s.URI),
		Range:          sdbRangeToLSP(s.Range),
		SelectionRange: sdbRangeToLSP(s.Range),
		Data:           s.Symbol,
	}
}

// --- Type Hierarchy ---

func (h *Handler) handlePrepareTypeHierarchy(ctx context.Context, params json.RawMessage) (any, error) {
	var p TypeHierarchyPrepareParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if !h.waitIndexReady(ctx) {
		return nil, nil
	}

	occ := h.idx.OccurrenceAt(p.TextDocument.URI, p.Position.Line, p.Position.Character)
	if occ == nil {
		return nil, nil
	}
	def := h.idx.SymbolDefinition(occ.Symbol)
	if def == nil || !index.IsTypeKind(def.Kind) {
		return nil, nil
	}
	item := symbolToTypeHierarchyItem(def, h)
	if item == nil {
		return nil, nil
	}
	return []TypeHierarchyItem{*item}, nil
}

func (h *Handler) handleSupertypes(ctx context.Context, params json.RawMessage) (any, error) {
	var p TypeHierarchySupertypesParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if !h.waitIndexReady(ctx) {
		return []TypeHierarchyItem{}, nil
	}

	sym, _ := p.Item.Data.(string)
	if sym == "" {
		return []TypeHierarchyItem{}, nil
	}

	parents := h.idx.ParentsOf(sym)
	var result []TypeHierarchyItem
	for _, parentSym := range parents {
		def := h.idx.SymbolDefinition(parentSym)
		if def == nil {
			def = &index.Symbol{
				Name:   index.ExtractShortName(parentSym),
				Symbol: parentSym,
				Kind:   sdb.SymbolInformation_CLASS,
			}
		}
		if item := symbolToTypeHierarchyItem(def, h); item != nil {
			result = append(result, *item)
		}
	}
	return result, nil
}

func (h *Handler) handleSubtypes(ctx context.Context, params json.RawMessage) (any, error) {
	var p TypeHierarchySubtypesParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if !h.waitIndexReady(ctx) {
		return []TypeHierarchyItem{}, nil
	}

	sym, _ := p.Item.Data.(string)
	if sym == "" {
		return []TypeHierarchyItem{}, nil
	}

	children := h.idx.Implementors(sym)
	var result []TypeHierarchyItem
	for _, childSym := range children {
		def := h.idx.SymbolDefinition(childSym)
		if def == nil {
			continue
		}
		if item := symbolToTypeHierarchyItem(def, h); item != nil {
			result = append(result, *item)
		}
	}
	return result, nil
}

func symbolToTypeHierarchyItem(s *index.Symbol, h *Handler) *TypeHierarchyItem {
	if s == nil {
		return nil
	}
	detail := ""
	if s.Signature != nil {
		detail = s.Signature.Label
	}
	item := &TypeHierarchyItem{
		Name:   s.Name,
		Kind:   sdbKindToLSP(s.Kind),
		Detail: detail,
		URI:    h.toFileURI(s.URI),
		Data:   s.Symbol,
	}
	if !s.Range.IsEmpty() {
		item.Range = sdbRangeToLSP(s.Range)
		item.SelectionRange = sdbRangeToLSP(s.Range)
	}
	return item
}

// --- Helpers ---

func isCallableKind(kind sdb.SymbolInformation_Kind) bool {
	return kind == sdb.SymbolInformation_METHOD || kind == sdb.SymbolInformation_CONSTRUCTOR
}

func isNarrower(a, b index.Range) bool {
	aLines := a.EndLine - a.StartLine
	bLines := b.EndLine - b.StartLine
	return aLines < bLines
}

func rangeContains(outer Range, inner Range) bool {
	// Check if outer range contains the start and end of the inner range.
	// We convert LSP Range back to index.Range for consistent logic.
	o := index.Range{
		StartLine:      int32(outer.Start.Line),
		StartCharacter: int32(outer.Start.Character),
		EndLine:        int32(outer.End.Line),
		EndCharacter:   int32(outer.End.Character),
	}

	// Start must be within outer.
	if !index.ContainsPosition(o, inner.Start.Line, inner.Start.Character) {
		return false
	}

	// End must be within outer or exactly at the outer end (since both are exclusive).
	if int32(inner.End.Line) > o.EndLine {
		return false
	}
	if int32(inner.End.Line) == o.EndLine && int32(inner.End.Character) > o.EndCharacter {
		return false
	}

	return true
}
