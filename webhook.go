package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/kaufland-ecommerce/ci-webhook/internal/handler"
	"github.com/kaufland-ecommerce/ci-webhook/internal/hook"
	"github.com/kaufland-ecommerce/ci-webhook/internal/hook_manager"
	"github.com/kaufland-ecommerce/ci-webhook/internal/middleware"
	"github.com/kaufland-ecommerce/ci-webhook/internal/pidfile"
	"github.com/kaufland-ecommerce/ci-webhook/internal/setup"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
)

const (
	Version = "2.8.1"
)

var (
	ip                 = flag.String("ip", "0.0.0.0", "ip the webhook should serve hooks on")
	port               = flag.Int("port", 9000, "port the webhook should serve hooks on")
	verbose            = flag.Bool("verbose", false, "show verbose output")
	logJSON            = flag.Bool("log-json", false, "show verbose output")
	logPath            = flag.String("logfile", "", "send log output to a file; implicitly enables verbose logging")
	debug              = flag.Bool("debug", false, "show debug output")
	noPanic            = flag.Bool("nopanic", false, "do not panic if hooks cannot be loaded when webhook is not running in verbose mode")
	hotReload          = flag.Bool("hotreload", false, "watch hooks file for changes and reload them automatically")
	hooksURLPrefix     = flag.String("urlprefix", "hooks", "url prefix to use for served hooks (protocol://yourserver:port/PREFIX/:hook-id)")
	secure             = flag.Bool("secure", false, "use HTTPS instead of HTTP")
	asTemplate         = flag.Bool("template", false, "parse hooks file as a Go template")
	cert               = flag.String("cert", "cert.pem", "path to the HTTPS certificate pem file")
	key                = flag.String("key", "key.pem", "path to the HTTPS certificate private key pem file")
	justDisplayVersion = flag.Bool("version", false, "display webhook version and quit")
	justListCiphers    = flag.Bool("list-cipher-suites", false, "list available TLS cipher suites")
	tlsMinVersion      = flag.String("tls-min-version", "1.2", "minimum TLS version (1.0, 1.1, 1.2, 1.3)")
	tlsCipherSuites    = flag.String("cipher-suites", "", "comma-separated list of supported TLS cipher suites")
	useXRequestID      = flag.Bool("x-request-id", false, "use X-Request-Id header, if present, as request ID")
	xRequestIDLimit    = flag.Int("x-request-id-limit", 0, "truncate X-Request-Id header to limit; default no limit")
	maxMultipartMem    = flag.Int64("max-multipart-mem", 1<<20, "maximum memory in bytes for parsing multipart form data before disk caching")
	setGID             = flag.Int("setgid", 0, "set group ID after opening listening port; must be used with setuid")
	setUID             = flag.Int("setuid", 0, "set user ID after opening listening port; must be used with setgid")
	httpMethods        = flag.String("http-methods", "", `set default allowed HTTP methods (ie. "POST"); separate methods with comma`)
	pidPath            = flag.String("pidfile", "", "create PID file at the given path")

	responseHeaders hook.ResponseHeaders
	hooksFiles      hook_manager.HooksFiles

	pidFile *pidfile.PIDFile
)

func main() {
	flag.Var(&hooksFiles, "hooks", "path to the json file containing defined hooks the webhook should serve, use multiple times to load from different files")
	flag.Var(&responseHeaders, "header", "response header to return, specified in format name=value, use multiple times to set multiple headers")

	flag.Parse()

	if *justDisplayVersion {
		fmt.Println("webhook version " + Version)
		os.Exit(0)
	}

	if *justListCiphers {
		err := writeTLSSupportedCipherStrings(os.Stdout, getTLSMinVersion(*tlsMinVersion))
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	if (*setUID != 0 || *setGID != 0) && (*setUID == 0 || *setGID == 0) {
		fmt.Println("error: setuid and setgid options must be used together")
		os.Exit(1)
	}

	logInit := setup.NewLogInit()

	if *debug || *logPath != "" {
		*verbose = true
	}

	if len(hooksFiles) == 0 {
		hooksFiles = append(hooksFiles, "hooks.json")
	}

	addr := fmt.Sprintf("%s:%d", *ip, *port)

	// Open listener early so we can drop privileges.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		logInit.PreInitLogf("error listening on port: %s", err)
		// we'll bail out below
	}

	if *setUID != 0 {
		if err := dropPrivileges(*setUID, *setGID); err != nil {
			logInit.PreInitLogf("error dropping privileges: %s", err)
			// we'll bail out below
		}
	}
	// setup logger
	logInit.SetLogFile(*logPath)
	logInit.SetVerbose(*verbose)
	logInit.SetJSON(*logJSON)
	logger := logInit.InitLogger()
	if logInit.ShouldExit() {
		os.Exit(1)
	}

	// Create pidfile
	if *pidPath != "" {
		var err error

		pidFile, err = pidfile.New(*pidPath)
		if err != nil {
			logger.Error("failed creating pidfile", "error", err, "path", *pidPath)
			os.Exit(1)
		}

		defer func() {
			// NOTE: my testing shows that this doesn't work with
			// ^C, so we also do a Remove in the signal handler elsewhere.
			if err := pidFile.Remove(); err != nil {
				logger.Error("failed removing pidfile", "error", err, "path", *pidPath)
			}
		}()
	}
	logger.Info("webhook server starting", "version", Version, "address", addr)

	// global context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// setup hook management
	hooks := hook_manager.NewManager(ctx, hooksFiles, *asTemplate, *hotReload)
	if err := hooks.Load(); err != nil {
		logger.Error("error loading hooks", "error", err)
		os.Exit(1)
	}
	// set os signal watcher
	setupSignals(hooks.Notify)

	if !*verbose && !*noPanic && hooks.Len() < 1 {
		logger.Error("couldn't load any hooks from file!\n" +
			"aborting webhook execution since the -verbose flag is set to false.\n" +
			"If, for some reason, you want webhook to start without the hooks, either use -verbose flag, or -nopanic")
		os.Exit(1)
	}

	if *hotReload {
		err := hooks.StartFileWatcher()
		defer hooks.Close()
		if err != nil {
			logger.Error("error starting file watcher", "error", err)
			os.Exit(1)
		}
	}
	if !*verbose && !*noPanic && hooks.Len() < 1 {
		logger.Error("couldn't load any hooks from file!\n" +
			"aborting webhook execution since the -verbose flag is set to false.\n" +
			"If, for some reason, you want webhook to run without the hooks, either use -verbose flag, or -nopanic")
		os.Exit(1)
	}

	// setup Request Handler
	reqHandler := handler.NewRequestHandler(
		hooks,
		logger,
		responseHeaders,
		parseMethodList(*httpMethods),
		*maxMultipartMem,
	)

	// setup HTTP Router & Server
	r := chi.NewRouter()
	r.Use(middleware.RequestID(
		middleware.UseXRequestIDHeaderOption(*useXRequestID),
		middleware.XRequestIDLimitOption(*xRequestIDLimit),
	))
	r.Use(chimiddleware.RequestLogger(middleware.NewLogFormatter(logger.With("logger", "http"))))
	r.Use(chimiddleware.Recoverer)

	if *debug {
		r.Use(middleware.Dumper(log.Writer()))
	}
	// healthcheck handler
	r.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		for _, responseHeader := range responseHeaders {
			w.Header().Set(responseHeader.Name, responseHeader.Value)
		}
		_, _ = fmt.Fprint(w, "OK")
	})
	// hooks handler
	r.Handle(
		handler.MakeRoutePattern(hooksURLPrefix),
		reqHandler,
	)
	// Create common HTTP server settings
	server := &http.Server{
		Addr:    addr,
		Handler: r,
	}

	// Serve HTTP
	if !*secure {
		logger.Info(fmt.Sprintf("serving hooks on http://%s%s", addr, handler.MakeHumanPattern(hooksURLPrefix)))
		if err := server.Serve(ln); err != nil {
			logger.Error("error serving http", "error", err)
		}
		return
	}

	// Server HTTPS
	server.TLSConfig = &tls.Config{
		CipherSuites:     getTLSCipherSuites(*tlsCipherSuites),
		CurvePreferences: []tls.CurveID{tls.CurveP521, tls.CurveP384, tls.CurveP256},
		MinVersion:       getTLSMinVersion(*tlsMinVersion),
	}
	server.TLSNextProto = make(map[string]func(*http.Server, *tls.Conn, http.Handler)) // disable http/2

	logger.Info(fmt.Sprintf("serving hooks on https://%s%s", addr, handler.MakeHumanPattern(hooksURLPrefix)))
	if err := server.ServeTLS(ln, *cert, *key); err != nil {
		logger.Error("error serving https", "error", err)
	}
}

func parseMethodList(methods string) []string {
	methods = strings.ReplaceAll(methods, " ", "")
	methods = strings.ToUpper(methods)
	var normalized []string
	for _, v := range strings.Split(methods, ",") {
		v = strings.TrimSpace(v)
		if v == "" { // this fixes a bug when empty lists are not empty :)
			continue
		}
		normalized = append(normalized, v)
	}
	return normalized
}
