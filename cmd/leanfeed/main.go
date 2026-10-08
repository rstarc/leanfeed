// Command leanfeed is a minimal self-hosted RSS and Atom reader.
//
// Usage:
//
//	leanfeed serve           run the web server and the fetcher
//	leanfeed import FILE     import subscriptions from OPML (server stopped)
//	leanfeed export          write subscriptions as OPML to stdout
//	leanfeed healthcheck     check that the server at --addr is healthy
//	leanfeed --version
//
// Run leanfeed --help for the flags and their environment variables.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"leanfeed/internal/fetcher"
	"leanfeed/internal/store/filestore"
	"leanfeed/internal/web"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

// minInterval matches the scheduler, which looks for due feeds once a minute.
const minInterval = time.Minute

type config struct {
	Data     string
	Addr     string
	Interval time.Duration
	Workers  int
}

// envVars maps each flag to the environment variable that sets it when the
// flag is not given.
var envVars = []struct{ flag, env string }{
	{"data", "LEANFEED_DATA"},
	{"addr", "LEANFEED_ADDR"},
	{"interval", "LEANFEED_INTERVAL"},
	{"workers", "LEANFEED_WORKERS"},
}

// usageError marks a mistake in the command line. It exits with code 2.
type usageError struct{ err error }

func (e usageError) Error() string { return e.err.Error() }

// usageArgs marks argument errors from v as usage errors.
func usageArgs(v cobra.PositionalArgs) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := v(cmd, args); err != nil {
			return usageError{err}
		}
		return nil
	}
}

// execute runs the command line and returns the exit code: 0 for success,
// 1 for failure and 2 for usage errors. serve runs the server; tests pass
// their own.
func execute(args []string, getenv func(string) string, stdout, stderr io.Writer, serve func(config, *slog.Logger) error) int {
	cfg := config{Data: "./data", Addr: "127.0.0.1:8080", Interval: 30 * time.Minute, Workers: 4}

	root := &cobra.Command{
		Use:           "leanfeed",
		Short:         "leanfeed is a minimal self-hosted RSS, Atom and JSON Feed reader.",
		Version:       version,
		Args:          usageArgs(cobra.NoArgs),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetVersionTemplate("leanfeed {{.Version}}\n")
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usageError{err} })
	root.CompletionOptions.DisableDefaultCmd = true

	flags := root.PersistentFlags()
	flags.StringVar(&cfg.Data, "data", cfg.Data, "data directory [LEANFEED_DATA]")
	flags.StringVar(&cfg.Addr, "addr", cfg.Addr, "listen address [LEANFEED_ADDR]")
	flags.DurationVar(&cfg.Interval, "interval", cfg.Interval, "time between fetches of a feed, at least 1m [LEANFEED_INTERVAL]")
	flags.IntVar(&cfg.Workers, "workers", cfg.Workers, "number of feeds fetched at the same time [LEANFEED_WORKERS]")
	root.PersistentPreRunE = func(*cobra.Command, []string) error {
		for _, v := range envVars {
			value := getenv(v.env)
			if value == "" || flags.Changed(v.flag) {
				continue
			}
			if err := flags.Set(v.flag, value); err != nil {
				return usageError{fmt.Errorf("%s: %w", v.env, err)}
			}
		}
		if cfg.Interval < minInterval {
			return usageError{fmt.Errorf("interval %v is shorter than %v", cfg.Interval, minInterval)}
		}
		if cfg.Workers < 1 {
			return usageError{fmt.Errorf("workers must be at least 1, not %d", cfg.Workers)}
		}
		return nil
	}

	root.AddCommand(
		&cobra.Command{
			Use:   "serve",
			Short: "Run the web server and fetch feeds on a schedule",
			Args:  usageArgs(cobra.NoArgs),
			RunE: func(*cobra.Command, []string) error {
				return serve(cfg, slog.New(slog.NewTextHandler(stdout, nil)))
			},
		},
		&cobra.Command{
			Use:   "import FILE",
			Short: "Import subscriptions from an OPML file",
			Long:  "Import subscriptions from an OPML file. Run it only while the server is stopped.",
			Args:  usageArgs(cobra.ExactArgs(1)),
			RunE: func(_ *cobra.Command, args []string) error {
				return importOPML(cfg, args[0], stdout, slog.New(slog.NewTextHandler(stderr, nil)))
			},
		},
		&cobra.Command{
			Use:   "export",
			Short: "Write subscriptions as OPML to standard output",
			Long:  "Write subscriptions as OPML to standard output. Run it only while the server is stopped.",
			Args:  usageArgs(cobra.NoArgs),
			RunE: func(*cobra.Command, []string) error {
				return exportOPML(cfg, stdout, slog.New(slog.NewTextHandler(stderr, nil)))
			},
		},
		&cobra.Command{
			Use:   "healthcheck",
			Short: "Check that the server at --addr is healthy",
			Long: "Check that the server at --addr is healthy. It exits with 0 if GET /healthz answers 200 within 5 s, " +
				"and with 1 otherwise. Use it for health checks in images without an HTTP client.",
			Args: usageArgs(cobra.NoArgs),
			RunE: func(*cobra.Command, []string) error {
				return healthcheck(cfg.Addr)
			},
		},
	)

	err := root.Execute()
	var uerr usageError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &uerr):
		_, _ = fmt.Fprintf(stderr, "leanfeed: %v\nRun 'leanfeed --help' for usage.\n", err)
		return 2
	default:
		_, _ = fmt.Fprintf(stderr, "leanfeed: %v\n", err)
		return 1
	}
}

func importOPML(cfg config, file string, stdout io.Writer, log *slog.Logger) (err error) {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	st, err := filestore.Open(cfg.Data, log)
	if err != nil {
		return err
	}
	// Close waits for pending writes, so its error matters.
	defer func() { err = errors.Join(err, st.Close()) }()
	stats, err := st.ImportOPML(context.Background(), f)
	if err != nil {
		return err
	}
	noun := "feeds"
	if stats.Added == 1 {
		noun = "feed"
	}
	_, err = fmt.Fprintf(stdout, "Imported %d %s, skipped %d.\n", stats.Added, noun, stats.Skipped)
	return err
}

func exportOPML(cfg config, stdout io.Writer, log *slog.Logger) (err error) {
	st, err := filestore.Open(cfg.Data, log)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, st.Close()) }()
	return st.ExportOPML(context.Background(), stdout)
}

// healthcheck asks the server at addr for /healthz and fails unless it
// answers 200 within 5 s.
func healthcheck(addr string) error {
	u, err := healthcheckURL(addr)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(u)
	if err != nil {
		return err
	}
	_ = resp.Body.Close() // the status is all the check needs
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %s", u, resp.Status)
	}
	return nil
}

// healthcheckURL returns the /healthz address of a server listening on
// addr. An unspecified host, as in 0.0.0.0:8080 or :8080, means this
// machine.
func healthcheckURL(addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", err
	}
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/healthz", nil
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
		return errors.Join(err, st.Close())
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
	os.Exit(execute(os.Args[1:], os.Getenv, os.Stdout, os.Stderr, serve))
}
