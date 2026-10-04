// Package httpdiscovery implements the client and server contracts of the
// optional OpenBindings HTTP Discovery companion specification, version 0.1.0.
// It discovers or publishes a document at an origin's well-known path.
//
// Client.Discover uses core document validation. A 404 is ErrNotFound; gated
// discovery (401/403) and other non-200 responses are StatusErrors. A refused
// document version remains a core VersionRefusalError, an established violation
// remains a core ValidationError, and an unreadable document remains
// ErrInconclusive. The returned Result retains the core validation report;
// successful retrieval alone is not a conformance claim.
//
// NewHandler publishes an immutable snapshot only after core establishes its
// conformance. Authentication, deployment routing, and CORS policy belong to the
// application. HandlerOptions.AllowOrigin enables CORS for public discovery or
// a selected browser origin.
//
// Retrieval supplies neither document identity nor a reference base. Discovery
// does not resolve dependencies, acquire referenced resources, select bindings,
// synthesize documents, or invoke operations. RequestedURL and FinalURL are
// transport metadata only.
package httpdiscovery
