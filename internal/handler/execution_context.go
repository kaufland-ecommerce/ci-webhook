package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kaufland-ecommerce/ci-webhook/internal/hook"
)

type requestExecutionContext struct {
	hookRequest  *hook.Request
	hook         *hook.Hook
	logger       *slog.Logger
	httpRequest  *http.Request
	httpResponse http.ResponseWriter
	opts         options
}

func (rec *requestExecutionContext) evaluateHookRules() (bool, error) {
	if rec.hook.TriggerRule == nil {
		return true, nil
	}
	// Save signature soft failures option in request for evaluators
	rec.hookRequest.AllowSignatureErrors = rec.hook.TriggerSignatureSoftFailures

	ok, err := rec.hook.TriggerRule.Evaluate(rec.hookRequest)
	if err != nil && !hook.IsParameterNodeError(err) {
		rec.logger.Error("error evaluating hook rules", "error", err)
		return false, err
	}
	if err != nil {
		rec.logger.Warn("hook rules were not satisfied", "error", err)
	}
	return ok, nil
}

func (rec *requestExecutionContext) Handle(w http.ResponseWriter, request *http.Request) {
	// Check for allowed methods
	if !rec.IsHTTPMethodAllowed(request.Method) {
		w.WriteHeader(http.StatusMethodNotAllowed)
		rec.logger.Warn("HTTP method not allowed for this hook", "method", request.Method)
		return
	}
	// write default response headers
	for _, responseHeader := range rec.opts.responseHeaders {
		w.Header().Set(responseHeader.Name, responseHeader.Value)
	}

	if err := rec.ParseRequest(); err != nil {
		rec.writeResponse(http.StatusInternalServerError, err.Error())
	}

	ok, err := rec.evaluateHookRules()
	if err != nil {
		rec.logger.Error("error evaluating hook", "error", err)
		rec.writeResponse(
			http.StatusInternalServerError,
			"Error occurred while evaluating hook rules.",
		)
		return // bail out early
	}
	if !ok { // hook is not triggered
		// Check if a return code is configured for the hook
		rec.writeResponse(
			rec.hook.TriggerRuleMismatchHttpResponseCode,
			"Hook rules were not satisfied.",
		)
		return // bail out early
	}

	rec.logger.Info("hook triggered successfully")
	for _, responseHeader := range rec.hook.ResponseHeaders {
		w.Header().Set(responseHeader.Name, responseHeader.Value)
	}

	executor := NewExecutor(rec.hook, rec.hookRequest, rec.logger)

	switch {
	case rec.hook.StreamCommandOutput:
		if flusher, ok := w.(FlushableWriter); ok {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			// when streaming, we need to write the header before executing the command,
			// and we can't bind the status code to command exit code
			w.WriteHeader(http.StatusOK)
			// create an io.Writer that flushes after every write operation
			fw := &flushWriter{w: flusher, muteErrors: true}
			// run command
			waiter := make(chan error)
			var exitCode int
			go func() {
				defer close(waiter)
				waiter <- executor.Execute(fw)
			}()
			if err := <-waiter; err != nil {
				exitCode = 1
			}
			// print exit code
			_, _ = fmt.Fprintf(w, "\n---\n%d\n", exitCode)
			flusher.Flush()
			return // done handling the streaming request
		}
		rec.logger.Error("cant obtain flusher. are you running with `-debug`?" +
			" streaming is not available in debug mode, will fallback to non-streaming mode")
		fallthrough
	case rec.hook.CaptureCommandOutput:
		// create a buffer with io.Writer interface
		buf := &bytes.Buffer{}
		err = executor.Execute(buf)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			if !rec.hook.CaptureCommandOutputOnError {
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				rec.writeResponseBody("Error occurred while executing the hook's command. " +
					"Please check logs for more details.")
				break
			}
		} else {
			rec.writeHttpStatus(rec.hook.SuccessHttpResponseCode)
		}
		rec.writeResponseBody(buf.String())
	default:
		go func() {
			buf := &bytes.Buffer{}
			err = executor.Execute(buf)
		}()
		rec.writeResponse(rec.hook.SuccessHttpResponseCode, rec.hook.ResponseMessage)
	}
}

func (rec *requestExecutionContext) IsHTTPMethodAllowed(method string) bool {
	switch {
	case len(rec.hook.HTTPMethods) > 0:
		return methodInList(method, rec.hook.HTTPMethods)
	case len(rec.opts.defaultAllowedMethods) > 0:
		return methodInList(method, rec.opts.defaultAllowedMethods)
	}
	return true
}

func (rec *requestExecutionContext) writeResponse(status int, message string) {
	rec.writeHttpStatus(status)
	rec.writeResponseBody(message)
}

func (rec *requestExecutionContext) writeResponseBody(body string) {
	_, _ = fmt.Fprint(rec.httpResponse, body)
}

func (rec *requestExecutionContext) writeHttpStatus(responseCode int) {
	if responseCode == 0 {
		responseCode = http.StatusOK
	}
	// Check if the http package supports the given return code
	// by testing if there is a StatusText for this code.
	if len(http.StatusText(responseCode)) > 0 {
		rec.httpResponse.WriteHeader(responseCode)
		return
	}
	rec.logger.Warn("hook got matched, but configured return code is unknown, using default",
		"configured_code", responseCode,
		"actual_code", http.StatusOK,
	)
}

func (rec *requestExecutionContext) parseMultipartForm() error {
	if err := rec.httpRequest.ParseMultipartForm(rec.opts.multipartMaxMemory); err != nil {
		return errors.New("error occurred while parsing multipart form")
	}

	for k, v := range rec.httpRequest.MultipartForm.Value {
		rec.logger.Debug("found multipart form value", "key", k)
		if rec.hookRequest.Payload == nil {
			rec.hookRequest.Payload = make(map[string]interface{})
		}
		// TODO: support duplicate, named values
		rec.hookRequest.Payload[k] = v[0]
	}

	for k, v := range rec.httpRequest.MultipartForm.File {
		// Force parsing as JSON regardless of Content-Type.
		var parseAsJSON bool
		for _, j := range rec.hook.JSONStringParameters {
			if j.Source == "payload" && j.Name == k {
				parseAsJSON = true
				break
			}
		}
		// TODO: we need to support multiple parts
		// with the same name instead of just processing the first
		// one. Will need #215 resolved first.

		// MIME encoding can contain duplicate headers, so check them
		// all.
		if !parseAsJSON && len(v[0].Header["Content-Type"]) > 0 {
			for _, j := range v[0].Header["Content-Type"] {
				if j == "application/json" {
					parseAsJSON = true
					break
				}
			}
		}

		if parseAsJSON {
			rec.logger.Debug("parsing multipart form file as JSON", "key", k)
			f, err := v[0].Open()
			if err != nil {
				rec.logger.Error("error parsing multipart form file", "error", err)
				return errors.New("error occurred while parsing multipart form file")
			}

			decoder := json.NewDecoder(f)
			decoder.UseNumber()

			var part map[string]interface{}
			err = decoder.Decode(&part)
			if err != nil {
				rec.logger.Error("error parsing JSON payload file", "error", err)
			}

			if rec.hookRequest.Payload == nil {
				rec.hookRequest.Payload = make(map[string]interface{})
			}
			rec.hookRequest.Payload[k] = part
		}
	}
	return nil
}

// ParseRequest parses the request body and populates the request object.
// returning error will cause the request to be rejected with 500 status code.
func (rec *requestExecutionContext) ParseRequest() error {
	// set contentType to IncomingPayloadContentType or header value
	rec.hookRequest.ContentType = rec.hookRequest.RawRequest.Header.Get("Content-Type")
	if len(rec.hook.IncomingPayloadContentType) != 0 {
		rec.hookRequest.ContentType = rec.hook.IncomingPayloadContentType
	}

	isMultipart := strings.HasPrefix(rec.hookRequest.ContentType, "multipart/form-data;")
	if !isMultipart {
		var err error
		rec.hookRequest.Body, err = io.ReadAll(rec.hookRequest.RawRequest.Body)
		if err != nil {
			rec.logger.Error("error reading the request body", "error", err)
		}
	}

	rec.hookRequest.ParseHeaders(rec.hookRequest.RawRequest.Header)
	rec.hookRequest.ParseQuery(rec.hookRequest.RawRequest.URL.Query())

	switch {
	case strings.Contains(rec.hookRequest.ContentType, "json"):
		if err := rec.hookRequest.ParseJSONPayload(); err != nil {
			rec.logger.Error("error parsing JSON payload", "error", err)
		}
	case strings.Contains(rec.hookRequest.ContentType, "x-www-form-urlencoded"):
		if err := rec.hookRequest.ParseFormPayload(); err != nil {
			rec.logger.Error("error parsing form-urlencoded payload", "error", err)
		}
	case strings.Contains(rec.hookRequest.ContentType, "xml"):
		if err := rec.hookRequest.ParseXMLPayload(); err != nil {
			rec.logger.Error("error parsing XML payload", "error", err)
		}
	case isMultipart:
		if err := rec.parseMultipartForm(); err != nil {
			rec.logger.Error("error parsing multipart form", "error", err)
			return err
		}
	default:
		rec.logger.Warn("unsupported content type, skip parsing body payload",
			"content_type", rec.hookRequest.ContentType)
	}
	if err := rec.hook.ParseJSONParameters(rec.hookRequest); err != nil {
		rec.logger.Error("error parsing JSON parameters", "error", err)
	}
	return nil
}

func methodInList(method string, methods []string) bool {
	for _, m := range methods {
		// TODO: refactor config loading and reloading to sanitize these methods once at load time.
		if strings.ToUpper(strings.TrimSpace(m)) == method {
			return true
		}
	}
	return false
}
