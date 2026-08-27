package openapi

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/getkin/kin-openapi/openapi3"
)

const (
	actualContentTypeHeader = "X-Openbindings-Actual-Content-Type"
	unaryEventStreamType    = "application/x-openbindings-event-stream-unary"
)

type mediaGovernance struct {
	document                 *openapi3.T
	operation                *openapi3.Operation
	parameters               openapi3.Parameters
	bindingSpec              string
	multipartTransferHeaders map[string]map[string]bool
	emptyResponse            atomic.Bool
	failureMu                sync.Mutex
	failure                  error
}

func applyDeclaredMultipartTransferHeaders(request *http.Request) error {
	governance, _ := request.Context().Value(mediaGovernanceContextKey{}).(*mediaGovernance)
	if governance == nil || governance.bindingSpec != BindingSpecOpenAPI30 || len(governance.multipartTransferHeaders) == 0 {
		return nil
	}
	mediaType, parameters, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "multipart/form-data") {
		return nil
	}
	properties := governance.multipartTransferHeaders[normalizeMediaType(mediaType)]
	if len(properties) == 0 {
		return nil
	}
	boundary := parameters["boundary"]
	if boundary == "" {
		return &preDispatchMediaError{message: "multipart request has no boundary"}
	}
	body, err := readRequestBody(request)
	if err != nil {
		return &preDispatchMediaError{message: err.Error()}
	}
	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	var rewritten bytes.Buffer
	writer := multipart.NewWriter(&rewritten)
	if err := writer.SetBoundary(boundary); err != nil {
		return &preDispatchMediaError{message: err.Error()}
	}
	for {
		part, nextErr := reader.NextPart()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			return &preDispatchMediaError{message: nextErr.Error()}
		}
		partBody, readErr := io.ReadAll(part)
		if readErr != nil {
			return &preDispatchMediaError{message: readErr.Error()}
		}
		if properties[part.FormName()] {
			part.Header.Set("Content-Transfer-Encoding", "base64")
		}
		out, createErr := writer.CreatePart(part.Header)
		if createErr != nil {
			return &preDispatchMediaError{message: createErr.Error()}
		}
		if _, writeErr := out.Write(partBody); writeErr != nil {
			return &preDispatchMediaError{message: writeErr.Error()}
		}
	}
	if err := writer.Close(); err != nil {
		return &preDispatchMediaError{message: err.Error()}
	}
	replaceRequestBody(request, rewritten.Bytes())
	return nil
}

func (g *mediaGovernance) recordFailure(err error) {
	if g == nil || err == nil {
		return
	}
	g.failureMu.Lock()
	defer g.failureMu.Unlock()
	if g.failure == nil {
		g.failure = err
	}
}

func (g *mediaGovernance) recordedFailure() error {
	if g == nil {
		return nil
	}
	g.failureMu.Lock()
	defer g.failureMu.Unlock()
	return g.failure
}

type preDispatchMediaError struct{ message string }

func (e *preDispatchMediaError) Error() string { return e.message }

type responseProtocolError struct{ message string }

func (e *responseProtocolError) Error() string { return e.message }

func normalizeContentEncoders(input map[string]ContentEncoder) (map[string]ContentEncoder, error) {
	result := make(map[string]ContentEncoder, len(input))
	for token, codec := range input {
		normalized := strings.ToLower(strings.TrimSpace(token))
		if !httpToken(normalized) || codec == nil {
			return nil, fmt.Errorf("invalid request content-coding capability %q", token)
		}
		if _, duplicate := result[normalized]; duplicate {
			return nil, fmt.Errorf("request content-coding capabilities collide at %q", normalized)
		}
		result[normalized] = codec
	}
	return result, nil
}

func normalizeContentDecoders(input map[string]ContentDecoder) (map[string]ContentDecoder, error) {
	result := make(map[string]ContentDecoder, len(input))
	for token, codec := range input {
		normalized := strings.ToLower(strings.TrimSpace(token))
		if !httpToken(normalized) || codec == nil {
			return nil, fmt.Errorf("invalid response content-coding capability %q", token)
		}
		if _, duplicate := result[normalized]; duplicate {
			return nil, fmt.Errorf("response content-coding capabilities collide at %q", normalized)
		}
		result[normalized] = codec
	}
	return result, nil
}

func parsedContentCodings(raw string) ([]string, error) {
	members, err := splitHTTPList(raw)
	if err != nil {
		return nil, err
	}
	result := make([]string, len(members))
	for index, member := range members {
		token := strings.ToLower(strings.TrimSpace(member))
		if !httpToken(token) {
			return nil, fmt.Errorf("content-coding %q is not an HTTP token", member)
		}
		result[index] = token
	}
	return result, nil
}

func applyRequestContentCodings(request *http.Request, codecs map[string]ContentEncoder) error {
	governance, _ := request.Context().Value(mediaGovernanceContextKey{}).(*mediaGovernance)
	if governance == nil {
		return nil
	}
	raw := strings.Join(request.Header.Values("Content-Encoding"), ",")
	if raw == "" {
		return nil
	}
	parameter := effectiveContentEncodingParameter(governance.parameters)
	if parameter == nil {
		return &preDispatchMediaError{message: "request Content-Encoding has no effective governing Header Parameter"}
	}
	if !schemaAdmitsHeaderValue(parameter.Schema, raw, governance.bindingSpec) {
		return &preDispatchMediaError{message: "request Content-Encoding is not admitted by its governing Header Parameter"}
	}
	tokens, err := parsedContentCodings(raw)
	if err != nil {
		return &preDispatchMediaError{message: err.Error()}
	}
	body, err := readRequestBody(request)
	if err != nil {
		return &preDispatchMediaError{message: err.Error()}
	}
	for _, token := range tokens {
		if token == "identity" {
			continue
		}
		codec := codecs[token]
		if codec == nil {
			return &preDispatchMediaError{message: fmt.Sprintf("request content-coding %q is unsupported", token)}
		}
		body, err = codec(body)
		if err != nil {
			return &preDispatchMediaError{message: fmt.Sprintf("request content-coding %q failed: %v", token, err)}
		}
	}
	replaceRequestBody(request, body)
	return nil
}

func effectiveContentEncodingParameter(parameters openapi3.Parameters) *openapi3.Parameter {
	var found *openapi3.Parameter
	for _, ref := range parameters {
		if ref == nil || ref.Value == nil || ref.Value.In != openapi3.ParameterInHeader || !strings.EqualFold(ref.Value.Name, "Content-Encoding") {
			continue
		}
		if found != nil {
			return nil
		}
		found = ref.Value
	}
	return found
}

func schemaAdmitsHeaderValue(ref *openapi3.SchemaRef, value, bindingSpec string) bool {
	if ref == nil || ref.Value == nil {
		return true
	}
	declaration := resolveDeclaration(ref.Value, bindingSpec == BindingSpecOpenAPI30)
	if declaration.ambiguous || (!declaration.typeless() && !declaration.admitsStringAsSoleNonNullType()) {
		return false
	}
	for _, conjunct := range declaration.conjuncts {
		if len(conjunct.Enum) == 0 {
			continue
		}
		admitted := false
		for _, candidate := range conjunct.Enum {
			if text, ok := candidate.(string); ok && text == value {
				admitted = true
				break
			}
		}
		if !admitted {
			return false
		}
	}
	return true
}

func readRequestBody(request *http.Request) ([]byte, error) {
	if request.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(request.Body)
	_ = request.Body.Close()
	return body, err
}

func replaceRequestBody(request *http.Request, body []byte) {
	request.Body = io.NopCloser(bytes.NewReader(body))
	request.ContentLength = int64(len(body))
	request.Header.Set("Content-Length", strconv.Itoa(len(body)))
	request.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
}

func applyResponseGovernance(request *http.Request, response *http.Response, codecs map[string]ContentDecoder) (*http.Response, error) {
	governance, _ := request.Context().Value(mediaGovernanceContextKey{}).(*mediaGovernance)
	if governance == nil || response == nil {
		return response, nil
	}
	governing := governingResponse(governance.operation, response.StatusCode)
	if governing != nil {
		if err := requireGovernedResponseHeaders(governing.response, response.Header); err != nil {
			closeResponse(response)
			return nil, err
		}
	}
	body, err := readResponseBody(response)
	if err != nil {
		return nil, &responseProtocolError{message: err.Error()}
	}
	if request.Method == http.MethodHead {
		body = nil
	}
	rawCoding := strings.Join(response.Header.Values("Content-Encoding"), ",")
	if rawCoding != "" {
		if governing == nil {
			return nil, &responseProtocolError{message: "coded response has no governing Response Object"}
		}
		header := responseHeader(governing.response, "Content-Encoding")
		if header == nil {
			return nil, &responseProtocolError{message: "actual response Content-Encoding has no governing Header Object"}
		}
		if !schemaAdmitsHeaderValue(header.Schema, rawCoding, governance.bindingSpec) {
			return nil, &responseProtocolError{message: "actual response Content-Encoding is not admitted by its governing Header Object"}
		}
		tokens, parseErr := parsedContentCodings(rawCoding)
		if parseErr != nil {
			return nil, &responseProtocolError{message: parseErr.Error()}
		}
		for index := len(tokens) - 1; index >= 0; index-- {
			token := tokens[index]
			if token == "identity" {
				continue
			}
			codec := codecs[token]
			if codec == nil {
				return nil, &responseProtocolError{message: fmt.Sprintf("response content-coding %q is unsupported", token)}
			}
			body, parseErr = codec(body)
			if parseErr != nil {
				return nil, &responseProtocolError{message: fmt.Sprintf("response content-coding %q failed: %v", token, parseErr)}
			}
		}
	}
	if len(body) > 0 {
		if governing == nil {
			return nil, &responseProtocolError{message: "non-empty response has no governing Response Object"}
		}
		contentType := response.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "application/octet-stream"
			response.Header.Set("Content-Type", contentType)
		}
		match, matchErr := governingResponseMediaMatchFor(governing.response, contentType, governance.bindingSpec)
		if matchErr != nil {
			return nil, &responseProtocolError{message: matchErr.Error()}
		}
		if laneErr := validateResponseMediaLane(governance.document, match.media, contentType, governance.bindingSpec); laneErr != nil {
			return nil, &responseProtocolError{message: laneErr.Error()}
		}
		if parsed, parseErr := parseRevision3MediaType(contentType); parseErr == nil && parsed.base == "text/event-stream" {
			response.Header.Set(actualContentTypeHeader, contentType)
			response.Header.Set("Content-Type", unaryEventStreamType)
		}
	}
	governance.emptyResponse.Store(len(body) == 0)
	replaceResponseBody(response, body)
	return response, nil
}

func requireGovernedResponseHeaders(response *openapi3.Response, actual http.Header) error {
	if response == nil {
		return nil
	}
	for name, ref := range response.Headers {
		if strings.EqualFold(name, "Content-Type") || ref == nil || ref.Value == nil || !ref.Value.Required {
			continue
		}
		if len(actual.Values(name)) == 0 {
			return &responseProtocolError{message: fmt.Sprintf("required response header %q is absent", name)}
		}
	}
	return nil
}

func responseHeader(response *openapi3.Response, name string) *openapi3.Header {
	if response == nil {
		return nil
	}
	var found *openapi3.Header
	for declared, ref := range response.Headers {
		if !strings.EqualFold(declared, name) || ref == nil || ref.Value == nil {
			continue
		}
		if found != nil {
			return nil
		}
		found = ref.Value
	}
	return found
}

func readResponseBody(response *http.Response) ([]byte, error) {
	if response.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	return body, err
}

func replaceResponseBody(response *http.Response, body []byte) {
	response.Body = io.NopCloser(bytes.NewReader(body))
	response.ContentLength = int64(len(body))
	response.Header.Set("Content-Length", strconv.Itoa(len(body)))
}

func closeResponse(response *http.Response) {
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
}

func validateResponseMediaLane(document *openapi3.T, media *openapi3.MediaType, contentType, bindingSpec string) error {
	parsed, err := parseRevision3MediaType(contentType)
	if err != nil {
		return err
	}
	if isJSONMediaType(parsed.base) {
		return nil
	}
	oas30 := document != nil && isOpenAPI30(majorMinor(document.OpenAPI))
	declaration := resolveDeclaration(mediaSchema(media), oas30)
	if isCharacterDataMedia(parsed.base) && declaration.admitsStringAsSoleNonNullType() {
		return supportedTextCharset(parsed)
	}
	if declaration.typeless() {
		return nil
	}
	if declaration.admitsStringAsSoleNonNullType() {
		format, conflict := declaration.format()
		if conflict {
			return fmt.Errorf("response declaration has conflicting format annotations")
		}
		encoding, conflict := declaration.keywordString("contentEncoding")
		if conflict {
			return fmt.Errorf("response declaration has conflicting contentEncoding annotations")
		}
		if (oas30 && (format == "binary" || format == "byte")) || (!oas30 && encoding != "") {
			return nil
		}
	}
	return fmt.Errorf("response media %q and its resolved declaration select no incorporated carriage lane", contentType)
}

func prepareEngineResponseView(operation *openapi3.Operation, bindingSpec string) {
	if operation == nil || operation.Responses == nil {
		return
	}
	for _, ref := range operation.Responses.Map() {
		if ref == nil || ref.Value == nil {
			continue
		}
		response := ref.Value
		if match, err := governingResponseMediaMatchFor(response, "text/event-stream", bindingSpec); err == nil {
			if response.Content == nil {
				response.Content = openapi3.Content{}
			}
			response.Content[unaryEventStreamType] = match.media
		}
		for _, media := range response.Content {
			if resolveDeclaration(mediaSchema(media), bindingSpec == BindingSpecOpenAPI30).typeless() {
				media.Schema = nil
			}
		}
	}
}
