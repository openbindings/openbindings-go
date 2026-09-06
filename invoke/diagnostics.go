package invoke

import (
	"errors"
	"net/url"
	"sort"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// ValidationPhase identifies the abstract operation boundary that rejected a
// value. It deliberately carries no binding-native or transport semantics.
type ValidationPhase string

const (
	ValidationPhaseInput  ValidationPhase = "input"
	ValidationPhaseOutput ValidationPhase = "output"
)

// InvocationDiagnostic is process-local evidence explaining an operation
// validation failure. It never rides InvocationError.Data and never contains
// the rejected value, protocol metadata, credentials, or validator prose.
type InvocationDiagnostic struct {
	Phase           ValidationPhase `json:"phase"`
	OperationKey    string          `json:"operationKey"`
	BindingKey      string          `json:"bindingKey"`
	InstancePointer string          `json:"instancePointer"`
	SchemaPointer   string          `json:"schemaPointer,omitempty"`
	Keyword         string          `json:"keyword,omitempty"`
}

// DiagnosticCollector is a bounded, concurrency-safe side channel for local
// invocation diagnostics. It has no callback into consumer code and therefore
// cannot change or delay the invocation outcome. A nil collector disables it.
type DiagnosticCollector struct {
	mu        sync.Mutex
	limit     int
	records   []InvocationDiagnostic
	truncated bool
}

// NewDiagnosticCollector returns a collector retaining at most limit records.
// Values <= 0 select the default limit of 32. The limit is immutable so the
// collector remains safe when an invocation reports from another goroutine.
func NewDiagnosticCollector(limit int) *DiagnosticCollector {
	if limit <= 0 {
		limit = 32
	}
	return &DiagnosticCollector{limit: limit}
}

// Snapshot returns private copies of the retained diagnostics and whether
// additional records were truncated.
func (c *DiagnosticCollector) Snapshot() (records []InvocationDiagnostic, truncated bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]InvocationDiagnostic(nil), c.records...), c.truncated
}

func (c *DiagnosticCollector) recordValidation(
	phase ValidationPhase,
	operationKey, bindingKey string,
	err error,
) {
	if c == nil || err == nil {
		return
	}
	var validationError *jsonschema.ValidationError
	if !errors.As(err, &validationError) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	limit := c.limit
	if limit <= 0 {
		limit = 32
	}
	remaining := limit - len(c.records)
	if remaining <= 0 {
		c.truncated = true
		return
	}
	records, truncated := validationDiagnostics(phase, operationKey, bindingKey, validationError, remaining)
	c.records = append(c.records, records...)
	if truncated {
		c.truncated = true
	}
}

func validationDiagnostics(
	phase ValidationPhase,
	operationKey, bindingKey string,
	err *jsonschema.ValidationError,
	limit int,
) ([]InvocationDiagnostic, bool) {
	leaves := validationLeaves(err, limit+1)
	truncated := len(leaves) > limit
	if truncated {
		leaves = leaves[:limit]
	}
	records := make([]InvocationDiagnostic, 0, len(leaves))
	for _, leaf := range leaves {
		var keywordPath []string
		if leaf.ErrorKind != nil {
			keywordPath = leaf.ErrorKind.KeywordPath()
		}
		schemaPath := schemaLocationTokens(leaf.SchemaURL, keywordPath)
		keyword := ""
		if len(keywordPath) > 0 {
			keyword = keywordPath[len(keywordPath)-1]
		}
		records = append(records, InvocationDiagnostic{
			Phase:           phase,
			OperationKey:    operationKey,
			BindingKey:      bindingKey,
			InstancePointer: safeInstancePointer(leaf.InstanceLocation, schemaPath),
			SchemaPointer:   jsonPointer(schemaPath),
			Keyword:         keyword,
		})
	}
	sort.SliceStable(records, func(i, j int) bool {
		if records[i].InstancePointer != records[j].InstancePointer {
			return records[i].InstancePointer < records[j].InstancePointer
		}
		if records[i].SchemaPointer != records[j].SchemaPointer {
			return records[i].SchemaPointer < records[j].SchemaPointer
		}
		return records[i].Keyword < records[j].Keyword
	})
	return records, truncated
}

func schemaLocationTokens(schemaURL string, keywordPath []string) []string {
	tokens := make([]string, 0, len(keywordPath)+4)
	if parsed, err := url.Parse(schemaURL); err == nil && strings.HasPrefix(parsed.Fragment, "/") {
		for _, token := range strings.Split(strings.TrimPrefix(parsed.Fragment, "/"), "/") {
			tokens = append(tokens, strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~"))
		}
	}
	return append(tokens, keywordPath...)
}

// safeInstancePointer preserves only member names written as literal
// `properties` keys in the governing schema. Array positions and runtime-
// derived map keys become `*`, so a diagnostic can locate the contract without
// copying data-derived names out of a rejected value.
func safeInstancePointer(instance, schema []string) string {
	declared := make(map[string]struct{})
	for index := 0; index+1 < len(schema); index++ {
		if schema[index] == "properties" {
			declared[schema[index+1]] = struct{}{}
			index++
		}
	}
	safe := make([]string, len(instance))
	for index, token := range instance {
		if _, ok := declared[token]; ok {
			safe[index] = token
		} else {
			safe[index] = "*"
		}
	}
	return jsonPointer(safe)
}

func validationLeaves(err *jsonschema.ValidationError, limit int) []*jsonschema.ValidationError {
	if err == nil || limit <= 0 {
		return nil
	}
	if len(err.Causes) == 0 {
		return []*jsonschema.ValidationError{err}
	}
	var leaves []*jsonschema.ValidationError
	for _, cause := range err.Causes {
		leaves = append(leaves, validationLeaves(cause, limit-len(leaves))...)
		if len(leaves) >= limit {
			break
		}
	}
	return leaves
}

func jsonPointer(tokens []string) string {
	if len(tokens) == 0 {
		return ""
	}
	escaped := make([]string, len(tokens))
	for index, token := range tokens {
		escaped[index] = strings.ReplaceAll(strings.ReplaceAll(token, "~", "~0"), "/", "~1")
	}
	return "/" + strings.Join(escaped, "/")
}
