package handler

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/gorilla/mux"

	"github.com/kaufland-ecommerce/ci-webhook/internal/hook"
	"github.com/kaufland-ecommerce/ci-webhook/internal/hook_manager"
	"github.com/kaufland-ecommerce/ci-webhook/internal/middleware"
)

type options struct {
	defaultAllowedMethods []string
	responseHeaders       hook.ResponseHeaders
	multipartMaxMemory    int64
}

type RequestHandler struct {
	hookManager *hook_manager.Manager
	logger      *slog.Logger
	opts        options
}

func NewRequestHandler(
	hookManager *hook_manager.Manager,
	logger *slog.Logger,
	responseHeaders hook.ResponseHeaders,
	defaultAllowedMethods []string,
	multipartMaxMemory int64,
) *RequestHandler {
	return &RequestHandler{
		hookManager: hookManager,
		logger:      logger,
		opts: options{
			responseHeaders:       responseHeaders,
			defaultAllowedMethods: defaultAllowedMethods,
			multipartMaxMemory:    multipartMaxMemory,
		},
	}
}

func (r *RequestHandler) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	hookRequest := &hook.Request{
		ID:         middleware.GetReqID(request.Context()),
		RawRequest: request,
	}
	requestLog := r.logger.With("request_id", hookRequest.ID)
	requestLog.Info(
		"incoming HTTP request",
		"method", request.Method,
		"path", request.URL.Path,
		"remote_addr", request.RemoteAddr,
	)
	// TODO: rename this to avoid confusion with Request.ID
	id := mux.Vars(request)["id"]
	// try loading the hook
	matchedHook := r.hookManager.Get(id)
	if matchedHook == nil {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, "Hook not found.")
		return
	}
	requestLog.Info("hook matched", "hook_id", matchedHook.ID)

	// create execution context
	executionContext := requestExecutionContext{
		hookRequest:  hookRequest,
		hook:         matchedHook,
		logger:       requestLog.With("hook_id", matchedHook.ID),
		httpRequest:  request,
		httpResponse: w,
		opts:         r.opts,
	}
	executionContext.Handle(w, request)
}

type FlushableWriter interface {
	io.Writer
	http.Flusher
}

type flushWriter struct {
	w FlushableWriter
}

func (fw *flushWriter) Write(p []byte) (n int, err error) {
	n, err = fw.w.Write(p)
	if n > 0 {
		fw.w.Flush()
	}
	return
}
