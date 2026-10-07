// Command leanfeed is a minimal self-hosted RSS and Atom reader.
//
// Usage:
//
//	leanfeed [flags] serve           run the web server and the fetcher
//	leanfeed [flags] import FILE     import subscriptions from OPML (server stopped)
//	leanfeed [flags] export          write subscriptions as OPML to stdout
//	leanfeed --version
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"leanfeed/internal/fetcher"
	"leanfeed/internal/store/filestore"
	"leanfeed/internal/web"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `Usage:
  leanfeed [flags] serve          run the web server and the fetcher
  leanfeed [flags] import FILE    import subscriptions from an OPML file
  leanfeed [flags] export         write subscriptions as OPML to stdout
  leanfeed --version              print the version

Run import and export only while the server is stopped.

Flags (environment variable in brackets):
  --data DIR        data directory [LEANFEED_DATA] (default ./data)
  --addr HOST:PORT  listen address [LEANFEED_ADDR] (default 127.0.0.1:8080)
  --interval DUR    time between fetches of a feed [LEANFEED_INTERVAL] (default 30m)
  --workers N       concurrent fetches [LEANFEED_WORKERS] (default 4)
`

// minInterval matches the scheduler, which looks for due feeds once a minute.
const minInterval = time.Minute

type config struct {
	Command  string
	Data     string
	Addr     string
	Interval time.Duration
	Workers  int
}

// parseConfig reads settings from defaults, then environment variables,
// then flags, which may come before or after the command. It returns the
// command's remaining arguments.
func parseConfig(args []string, getenv func(string) string) (config, []string, error) {
	cfg := config{Data: "./data", Addr: "127.0.0.1:8080", Interval: 30 * time.Minute, Workers: 4}
	if v := getenv("LEANFEED_DATA"); v != "" {
		cfg.Data = v
	}
	if v := getenv("LEANFEED_ADDR"); v != "" {
		cfg.Addr = v
	}
	if v := getenv("LEANFEED_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return cfg, nil, fmt.Errorf("LEANFEED_INTERVAL: %w", err)
		}
		cfg.Interval = d
	}
	if v := getenv("LEANFEED_WORKERS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return cfg, nil, fmt.Errorf("LEANFEED_WORKERS: %w", err)
		}
		cfg.Workers = n
	}

	fs := flag.NewFlagSet("leanfeed", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&cfg.Data, "data", cfg.Data, "")
	fs.StringVar(&cfg.Addr, "addr", cfg.Addr, "")
	fs.DurationVar(&cfg.Interval, "interval", cfg.Interval, "")
	fs.IntVar(&cfg.Workers, "workers", cfg.Workers, "")
	showVersion := fs.Bool("version", false, "")
	if err := fs.Parse(args); err != nil {
		return cfg, nil, err
	}
	if *showVersion {
		cfg.Command = "version"
		return cfg, nil, nil
	}
	if fs.NArg() == 0 {
		return cfg, nil, errors.New("no command given")
	}
	cfg.Command = fs.Arg(0)
	if err := fs.Parse(fs.Args()[1:]); err != nil {
		return cfg, nil, err
	}
	rest := fs.Args()

	if cfg.Interval < minInterval {
		return cfg, nil, fmt.Errorf("interval %v is shorter than %v", cfg.Interval, minInterval)
	}
	if cfg.Workers < 1 {
		return cfg, nil, fmt.Errorf("workers must be at least 1, not %d", cfg.Workers)
	}
	switch {
	case cfg.Command == "import" && len(rest) != 1:
		return cfg, nil, errors.New("import needs exactly one OPML file")
	case (cfg.Command == "serve" || cfg.Command == "export") && len(rest) != 0:
		return cfg, nil, fmt.Errorf("%s takes no arguments", cfg.Command)
	case cfg.Command != "serve" && cfg.Command != "import" && cfg.Command != "export":
		return cfg, nil, fmt.Errorf("unknown command %q", cfg.Command)
	}
	if len(rest) == 0 {
		rest = nil
	}
	return cfg, rest, nil
}

// run executes the command line and returns the exit code: 0 for success,
// 1 for failure and 2 for usage errors.
func run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	cfg, rest, err := parseConfig(args, getenv)
	if err != nil {
		fmt.Fprintf(stderr, "leanfeed: %v\n\n%s", err, usage)
		return 2
	}
	switch cfg.Command {
	case "version":
		fmt.Fprintf(stdout, "leanfeed %s\n", version)
		return 0
	case "serve":
		err = serve(cfg, slog.New(slog.NewTextHandler(stdout, nil)))
	case "import":
		err = importOPML(cfg, rest[0], stdout, slog.New(slog.NewTextHandler(stderr, nil)))
	case "export":
		err = exportOPML(cfg, stdout, slog.New(slog.NewTextHandler(stderr, nil)))
	}
	if err != nil {
		fmt.Fprintf(stderr, "leanfeed: %v\n", err)
		return 1
	}
	return 0
}

func importOPML(cfg config, file string, stdout io.Writer, log *slog.Logger) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := filestore.Open(cfg.Data, log)
	if err != nil {
		return err
	}
	defer st.Close()
	stats, err := st.ImportOPML(context.Background(), f)
	if err != nil {
		return err
	}
	noun := "feeds"
	if stats.Added == 1 {
		noun = "feed"
	}
	fmt.Fprintf(stdout, "Imported %d %s, skipped %d.\n", stats.Added, noun, stats.Skipped)
	return nil
}

func exportOPML(cfg config, stdout io.Writer, log *slog.Logger) error {
	st, err := filestore.Open(cfg.Data, log)
	if err != nil {
		return err
	}
	defer st.Close()
	return st.ExportOPML(context.Background(), stdout)
}

// serve runs the web server and the fetcher until SIGINT or SIGTERM. On
// shutdown it stops the scheduler and cancels in-flight fetches, gives
// open requests 10 s to finish, then closes the store.
func serve(cfg config, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := filestore.Open(cfg.Data, log)
	if err != nil {
		return err
	}
	f := fetcher.New(st, fetcher.Config{
		Interval:  cfg.Interval,
		Workers:   cfg.Workers,
		UserAgent: "leanfeed/" + version,
	}, log)
	handler, err := web.New(st, f, log)
	if err != nil {
		st.Close()
		return err
	}
	srv := &http.Server{Addr: cfg.Addr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}

	fetchCtx, cancelFetch := context.WithCancel(context.Background())
	fetchDone := make(chan struct{})
	go func() {
		f.Run(fetchCtx)
		close(fetchDone)
	}()
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServe() }()
	log.Info("leanfeed started", "version", version, "addr", "http://"+cfg.Addr, "data", cfg.Data)

	select {
	case <-ctx.Done():
		log.Info("shutting down")
	case err = <-serveErr:
	}
	cancelFetch()
	<-fetchDone
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if serr := srv.Shutdown(shutdownCtx); serr != nil && err == nil {
		err = serr
	}
	if cerr := st.Close(); cerr != nil && err == nil {
		err = cerr
	}
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	return err
}

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}
