package httpdiscovery

import (
	"bytes"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	openbindings "github.com/openbindings/openbindings-go"
)

// HandlerOptions configures publication policy outside core document semantics.
type HandlerOptions struct {
	// AllowOrigin is a fixed Access-Control-Allow-Origin value. Use "*" for
	// public browser discovery, or a permitted origin for restricted CORS.
	// Empty emits no CORS header. Credentialed CORS and preflight handling,
	// when needed, belong to the application's middleware.
	AllowOrigin string
}

// NewHandler validates and copies data once, then serves the exact snapshot at
// WellKnownPath. It accepts every Accept header, including none, and returns
// the OBI media type. It supports GET and HEAD; other methods receive 405.
// Other paths receive 404. Register it at WellKnownPath in a ServeMux.
//
// The constructor returns core's VersionRefusalError or ValidationError when
// applicable. Undetermined conformance returns an error matching
// openbindings.ErrInconclusive: the server must publish a conformant OBI
// (DISC-S-01), so this convenience requires positive evidence before serving.
// The handler is safe for concurrent use and later changes to data cannot
// affect it. To change the published document, construct a new handler.
//
// A service publishing no document leaves this path unregistered (404) or uses
// http.NotFoundHandler. Authentication/authorization middleware may answer
// 401/403 before this handler, including when publication is gated.
func NewHandler(data []byte, options HandlerOptions) (http.Handler, error) {
	if strings.ContainsAny(options.AllowOrigin, "\r\n\x00") {
		return nil, fmt.Errorf("http discovery: AllowOrigin contains a header control character")
	}
	body := bytes.Clone(data)
	_, report, err := openbindings.ValidateDocument(body)
	if err != nil {
		return nil, fmt.Errorf("http discovery: publication: %w", err)
	}
	if report.Conclusion != openbindings.ConclusionConformant {
		return nil, fmt.Errorf("http discovery: publication conformance is undetermined (rules %s): %w", strings.Join(report.Inconclusive, ", "), openbindings.ErrInconclusive)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != WellKnownPath {
			http.NotFound(w, r)
			return
		}
		if options.AllowOrigin != "" {
			w.Header().Set("Access-Control-Allow-Origin", options.AllowOrigin)
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", openbindings.MediaType)
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write(body)
		}
	}), nil
}
