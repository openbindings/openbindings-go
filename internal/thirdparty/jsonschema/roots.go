package jsonschema

import (
	"fmt"
	"strings"
)

type roots struct {
	defaultDraft *Draft
	roots        map[url]*root
	loader       defaultLoader
	regexpEngine RegexpEngine
	vocabularies map[string]*Vocabulary
	assertVocabs bool
	containers   map[url][]jsonPointer // schema locations of each container
}

func newRoots() *roots {
	return &roots{
		defaultDraft: draftLatest,
		roots:        map[url]*root{},
		loader: defaultLoader{
			docs:   map[url]any{},
			loader: FileLoader{},
		},
		regexpEngine: goRegexpCompile,
		vocabularies: map[string]*Vocabulary{},
		containers:   map[url][]jsonPointer{},
	}
}

func (rr *roots) orLoad(u url) (*root, error) {
	if r, ok := rr.roots[u]; ok {
		return r, nil
	}
	doc, err := rr.loader.load(u)
	if err != nil {
		return nil, err
	}
	return rr.addRoot(u, doc)
}

func (rr *roots) addRoot(u url, doc any) (*root, error) {
	r := &root{
		url:                 u,
		doc:                 doc,
		resources:           map[jsonPointer]*resource{},
		subschemasProcessed: map[jsonPointer]struct{}{},
	}
	if _, ok := rr.containers[u]; ok {
		// The root of a container is not a schema: only its root resource,
		// the base of every location in it, is known until a location is
		// compiled or referenced.
		res := newResource("", u)
		res.dialect = dialect{rr.defaultDraft, nil}
		r.resources[""] = res
		rr.roots[u] = r
		return r, nil
	}
	if err := rr.collectResources(r, doc, u, "", dialect{rr.defaultDraft, nil}); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(u.String(), "http://json-schema.org/") &&
		!strings.HasPrefix(u.String(), "https://json-schema.org/") {
		if err := rr.validate(r, doc, ""); err != nil {
			return nil, err
		}
	}

	rr.roots[u] = r
	return r, nil
}

func (rr *roots) resolveFragment(uf urlFrag) (urlPtr, error) {
	r, err := rr.orLoad(uf.url)
	if err != nil {
		return urlPtr{}, err
	}
	return r.resolveFragment(uf.frag)
}

func (rr *roots) collectResources(r *root, sch any, base url, schPtr jsonPointer, fallback dialect) error {
	if _, ok := r.subschemasProcessed[schPtr]; ok {
		return nil
	}
	if err := rr._collectResources(r, sch, base, schPtr, fallback); err != nil {
		return err
	}
	r.subschemasProcessed[schPtr] = struct{}{}
	return nil
}

func (rr *roots) _collectResources(r *root, sch any, base url, schPtr jsonPointer, fallback dialect) error {
	obj, ok := sch.(map[string]any)
	if !ok {
		if schPtr.isEmpty() {
			// root resource
			res := newResource(schPtr, base)
			res.dialect = fallback
			r.resources[schPtr] = res
		}
		return nil
	}

	hasSchema := false
	if sch, ok := obj["$schema"]; ok {
		if _, ok := sch.(string); ok {
			hasSchema = true
		}
	}

	draft, err := rr.loader.getDraft(urlPtr{r.url, schPtr}, sch, fallback.draft, map[url]struct{}{})
	if err != nil {
		return err
	}
	id := draft.getID(obj)
	if id == "" && !schPtr.isEmpty() {
		// ignore $schema
		draft = fallback.draft
		hasSchema = false
		id = draft.getID(obj)
	}

	var res *resource
	if id != "" {
		uf, err := base.join(id)
		if err != nil {
			loc := urlPtr{r.url, schPtr}
			return &ParseIDError{loc.String()}
		}
		base = uf.url
		res = newResource(schPtr, base)
	} else if schPtr.isEmpty() {
		// root resource
		res = newResource(schPtr, base)
	}

	if res != nil {
		found := false
		for _, res := range r.resources {
			if res.id == base {
				found = true
				if res.ptr != schPtr {
					return &DuplicateIDError{base.String(), r.url.String(), string(schPtr), string(res.ptr)}
				}
			}
		}
		if !found {
			if hasSchema {
				vocabs, err := rr.loader.getMetaVocabs(sch, draft, rr.vocabularies)
				if err != nil {
					return err
				}
				res.dialect = dialect{draft, vocabs}
			} else {
				res.dialect = fallback
			}
			r.resources[schPtr] = res
		}
	}

	var baseRes *resource
	for _, res := range r.resources {
		if res.id == base {
			baseRes = res
			break
		}
	}
	if baseRes == nil {
		panic("baseres is nil")
	}

	// found base resource
	if err := r.collectAnchors(sch, schPtr, baseRes); err != nil {
		return err
	}

	// process subschemas
	subschemas := map[jsonPointer]any{}
	for _, sp := range draft.subschemas {
		ss := sp.collect(obj, schPtr)
		for k, v := range ss {
			subschemas[k] = v
		}
	}
	for _, vocab := range baseRes.dialect.activeVocabs(true, rr.vocabularies) {
		if v := rr.vocabularies[vocab]; v != nil {
			for _, sp := range v.Subschemas {
				ss := sp.collect(obj, schPtr)
				for k, v := range ss {
					subschemas[k] = v
				}
			}
		}
	}
	for ptr, v := range subschemas {
		if err := rr.collectResources(r, v, base, ptr, baseRes.dialect); err != nil {
			return err
		}
	}

	return nil
}

func (rr *roots) ensureSubschema(up urlPtr) error {
	r, err := rr.orLoad(up.url)
	if err != nil {
		return err
	}
	if _, ok := r.subschemasProcessed[up.ptr]; ok {
		return nil
	}
	v, err := up.lookup(r.doc)
	if err != nil {
		return err
	}
	rClone := r.clone()
	if err := rr.addSubschema(rClone, up.ptr); err != nil {
		return err
	}
	if err := rr.validate(rClone, v, up.ptr); err != nil {
		return err
	}
	rr.roots[r.url] = rClone
	return nil
}

func (rr *roots) addSubschema(r *root, ptr jsonPointer) error {
	if enclosing, ok := rr.enclosingSchemaLocation(r, ptr); ok {
		if err := rr.addSubschema(r, enclosing); err != nil {
			return err
		}
		v, err := (&urlPtr{r.url, enclosing}).lookup(r.doc)
		if err != nil {
			return err
		}
		if err := rr.validate(r, v, enclosing); err != nil {
			return err
		}
		if _, ok := r.subschemasProcessed[ptr]; ok {
			return nil
		}
	}
	v, err := (&urlPtr{r.url, ptr}).lookup(r.doc)
	if err != nil {
		return err
	}
	base := r.resource(ptr)
	baseURL := base.id
	if err := rr.collectResources(r, v, baseURL, ptr, base.dialect); err != nil {
		return err
	}

	// collect anchors
	if _, ok := r.resources[ptr]; !ok {
		res := r.resource(ptr)
		if err := r.collectAnchors(v, ptr, res); err != nil {
			return err
		}
	}
	return nil
}

// enclosingSchemaLocation returns the longest schema location of container r
// that strictly encloses ptr and has not been processed.
func (rr *roots) enclosingSchemaLocation(r *root, ptr jsonPointer) (jsonPointer, bool) {
	var enclosing jsonPointer
	found := false
	for _, location := range rr.containers[r.url] {
		if len(location) < len(ptr) && strings.HasPrefix(string(ptr), string(location)+"/") && (!found || len(location) > len(enclosing)) {
			enclosing, found = location, true
		}
	}
	if !found {
		return "", false
	}
	if _, processed := r.subschemasProcessed[enclosing]; processed {
		return "", false
	}
	return enclosing, true
}

func (rr *roots) validate(r *root, v any, ptr jsonPointer) error {
	dialect := r.resource(ptr).dialect
	meta := dialect.getSchema(rr.assertVocabs, rr.vocabularies)
	if err := meta.validate(v, rr.regexpEngine, meta, r.resources, rr.assertVocabs, rr.vocabularies); err != nil {
		up := urlPtr{r.url, ptr}
		return &SchemaValidationError{URL: up.String(), Err: err}
	}
	return nil
}

// --

type InvalidMetaSchemaURLError struct {
	URL string
	Err error
}

func (e *InvalidMetaSchemaURLError) Error() string {
	return fmt.Sprintf("invalid $schema in %q: %v", e.URL, e.Err)
}

// --

type UnsupportedDraftError struct {
	URL string
}

func (e *UnsupportedDraftError) Error() string {
	return fmt.Sprintf("draft %q is not supported", e.URL)
}

// --

type MetaSchemaCycleError struct {
	URL string
}

func (e *MetaSchemaCycleError) Error() string {
	return fmt.Sprintf("cycle in resolving $schema in %q", e.URL)
}

// --

type MetaSchemaMismatchError struct {
	URL string
}

func (e *MetaSchemaMismatchError) Error() string {
	return fmt.Sprintf("$schema in %q does not match with $schema in root", e.URL)
}
