# Completion Issue Drafts

## 1. Lambda lexical completion does not include lambda parameters

### Title

`completion: include lambda parameters in lexical completion`

### Background

Dot completion inside lambdas is partially supported, but lexical completion still ignores lambda parameters.

Example:

```java
list.stream().map(user -> us)
```

At `us`, completion should suggest `user`, but current lexical completion only considers locals, method params, and class fields.

Relevant code:

- `internal/lsp/completion.go`
- `internal/lsp/completion_context.go`

### Problem

`CompletionCtx` already tracks `LambdaParams`, but `completeLexical` does not include them in the candidate set. This makes lambda bodies feel inconsistent:

- `user.` may work in some cases
- `us` does not complete to `user`

### Proposed change

Update `completeLexical` to include `LambdaParams` in lexical completion.

Implementation notes:

- Treat lambda parameters as the innermost lexical scope.
- Ensure nested lambdas prefer the nearest lambda's parameters.
- Deduplicate correctly against locals and method params.
- Add tests for:
  - single-parameter lambdas
  - multi-parameter lambdas
  - explicitly typed lambda parameters
  - nested lambda shadowing

### Acceptance criteria

- In lambda bodies, lexical completion suggests lambda parameters.
- Nested lambda shadowing behaves correctly.
- Existing lexical completion ordering is preserved or improved.

---

## 2. Dot completion returns empty when receiver type resolution fails

### Title

`completion: add fallback candidates when dot receiver type cannot be resolved`

### Background

Current dot completion is all-or-nothing. If receiver type resolution fails, completion returns no items.

Example:

```java
foo().bar().baz.
```

If any link in the chain cannot be resolved, the user gets an empty completion list.

Relevant code:

- `internal/lsp/completion.go`

### Problem

Returning zero results is too brittle for incomplete or partially invalid code, especially during active editing. This makes completion feel unreliable even when there are plausible fallback candidates.

### Proposed change

Add a bounded fallback path in `completeDot` when receiver type resolution fails.

Fallback strategy should be conservative:

- same-file or current-context high-signal members first
- common `java.lang.Object` methods as a last-resort fallback
- avoid dumping a huge global candidate set

Implementation notes:

- Keep candidate count small and stable.
- Preserve low latency.
- Avoid obvious noise explosions.
- Consider ranking fallback results below type-resolved results.

### Acceptance criteria

- Dot completion no longer returns an empty list in common unresolved-receiver cases.
- Fallback results remain bounded and reasonably relevant.
- Performance does not regress noticeably.

---

## 3. General SAM resolution for lambda parameter typing

### Title

`completion: support generic SAM resolution for lambda parameter type inference`

### Background

Current lambda parameter inference is based on hardcoded support for a small set of JDK functional interfaces such as:

- `Function`
- `Consumer`
- `Predicate`
- `BiFunction`
- `Comparator`

This improves common cases, but fails for custom functional interfaces and non-hardcoded APIs.

Relevant code:

- `internal/lsp/completion.go`

### Problem

Lambda completion remains heuristic and incomplete. This works for common Stream cases, but breaks for:

- custom functional interfaces
- library-specific callback interfaces
- non-JDK APIs using SAM types

Example:

```java
interface Mapper<T, R> {
    R apply(T value);
}

stream.map((Mapper<User, String>) user -> user.)
```

Here `user` should resolve to `User`, but current hardcoded logic cannot infer it.

### Proposed change

Implement general SAM resolution:

1. Resolve the target functional interface type for the current lambda argument.
2. Find its single abstract method.
3. Substitute generic type arguments into the SAM signature.
4. Map lambda parameter positions to SAM parameter types.

Implementation notes:

- Keep existing hardcoded fast paths if useful.
- Do not attempt full JLS constraint solving.
- It is acceptable to skip complex overload disambiguation and capture-conversion edge cases.
- Focus on stable, high-value cases first.

### Acceptance criteria

- Custom functional interfaces work for common lambda dot completion cases.
- Existing JDK functional interface support remains intact.
- Implementation does not require full compiler-style constraint solving.

---

## 4. Completion ranking is too coarse

### Title

`completion: improve ranking so high-probability candidates appear first`

### Background

Current ranking is functional but coarse. It mainly considers:

- case-sensitive prefix match
- basic scope ordering
- a shallow expected-type boost

This often produces completion lists that contain the right answer, but not near the top.

Relevant code:

- `internal/lsp/completion.go`
- `internal/lsp/snippets.go`

### Problem

Completion quality is currently limited more by ranking than by candidate generation in many common cases.

Observed issues:

- snippets can outrank more relevant semantic candidates
- local/project symbols and auto-imported types are not always ranked intuitively
- expected-type ranking is too shallow
- dot completion and lexical completion share ranking assumptions that should likely diverge

### Proposed change

Refine ranking for both lexical and dot completion.

Candidate ranking factors to consider:

- lexical scope distance
- exact prefix vs case-insensitive prefix vs fuzzy match
- type compatibility strength
- same-file / same-package / imported / global symbol proximity
- member kind preference depending on context
- snippet demotion relative to strong semantic matches

Implementation notes:

- Tune dot and lexical completion separately.
- Preserve determinism.
- Prefer simple, explainable ranking rules over opaque scoring.

### Acceptance criteria

- Commonly intended candidates consistently appear near the top.
- Snippets no longer displace strong semantic matches in ordinary code completion.
- Ranking changes are covered by focused tests.
