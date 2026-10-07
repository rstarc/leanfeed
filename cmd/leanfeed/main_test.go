package main

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

// result is the outcome of one command line.
type result struct {
	code           int
	stdout, stderr string
	served         *config // the config serve was called with, if it was
}

// exec runs args with a serve function that records its config instead of
// starting a server.
func exec(args []string, vars map[string]string) result {
	var stdout, stderr bytes.Buffer
	var r result
	serve := func(cfg config, _ *slog.Logger) error {
		r.served = &cfg
		return nil
	}
	r.code = execute(args, env(vars), &stdout, &stderr, serve)
	r.stdout, r.stderr = stdout.String(), stderr.String()
	return r
}

func TestServeConfig(t *testing.T) {
	allEnv := map[string]string{
		"LEANFEED_DATA": "/srv/feeds", "LEANFEED_ADDR": "0.0.0.0:9000",
		"LEANFEED_INTERVAL": "1h", "LEANFEED_WORKERS": "8",
	}
	tests := []struct {
		name string
		args []string
		env  map[string]string
		want config
	}{
		{"defaults", []string{"serve"}, nil,
			config{Data: "./data", Addr: "127.0.0.1:8080", Interval: 30 * time.Minute, Workers: 4}},
		{"environment", []string{"serve"}, allEnv,
			config{Data: "/srv/feeds", Addr: "0.0.0.0:9000", Interval: time.Hour, Workers: 8}},
		{"flags before command override environment", []string{"--data", "/x", "--interval", "5m", "serve"}, allEnv,
			config{Data: "/x", Addr: "0.0.0.0:9000", Interval: 5 * time.Minute, Workers: 8}},
		{"flags after command", []string{"serve", "--addr", ":1234", "--workers=2"}, nil,
			config{Data: "./data", Addr: ":1234", Interval: 30 * time.Minute, Workers: 2}},
		{"empty environment variables are ignored", []string{"serve"}, map[string]string{"LEANFEED_DATA": "", "LEANFEED_WORKERS": ""},
			config{Data: "./data", Addr: "127.0.0.1:8080", Interval: 30 * time.Minute, Workers: 4}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := exec(tt.args, tt.env)
			if r.code != 0 {
				t.Fatalf("exit %d: %s", r.code, r.stderr)
			}
			if r.served == nil {
				t.Fatal("serve was not called")
			}
			if *r.served != tt.want {
				t.Errorf("serve config = %+v, want %+v", *r.served, tt.want)
			}
		})
	}
}

func TestUsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		env  map[string]string
		msg  string // part of the error message
	}{
		{"unknown command", []string{"frobnicate"}, nil, "unknown command"},
		{"unknown flag", []string{"serve", "--colour"}, nil, "unknown flag"},
		{"bad interval flag", []string{"--interval", "soon", "serve"}, nil, "interval"},
		{"bad interval env", []string{"serve"}, map[string]string{"LEANFEED_INTERVAL": "soon"}, "LEANFEED_INTERVAL"},
		{"interval too short", []string{"--interval", "10s", "serve"}, nil, "shorter than 1m"},
		{"zero workers", []string{"--workers", "0", "serve"}, nil, "at least 1"},
		{"bad workers env", []string{"serve"}, map[string]string{"LEANFEED_WORKERS": "many"}, "LEANFEED_WORKERS"},
		{"import without file", []string{"import"}, nil, "accepts 1 arg"},
		{"serve with extra args", []string{"serve", "extra"}, nil, "unknown command"},
		{"export with extra args", []string{"export", "x"}, nil, "unknown command"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := exec(tt.args, tt.env)
			if r.code != 2 {
				t.Errorf("exit %d, want 2", r.code)
			}
			if r.served != nil {
				t.Error("serve ran despite a usage error")
			}
			if !strings.Contains(r.stderr, tt.msg) || !strings.Contains(r.stderr, "leanfeed --help") {
				t.Errorf("stderr = %q, want %q and a pointer to --help", r.stderr, tt.msg)
			}
		})
	}
}

func TestNoCommandPrintsHelp(t *testing.T) {
	r := exec(nil, nil)
	if r.code != 0 || r.served != nil {
		t.Errorf("exit %d, served %v", r.code, r.served)
	}
	for _, want := range []string{"serve", "import", "export"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("help does not mention %q:\n%s", want, r.stdout)
		}
	}
}

func TestHelpDocumentsFlagsAndEnvironment(t *testing.T) {
	r := exec([]string{"serve", "--help"}, nil)
	if r.code != 0 || r.served != nil {
		t.Fatalf("exit %d, served %v", r.code, r.served)
	}
	for _, want := range []string{
		"--data", "LEANFEED_DATA", "./data",
		"--addr", "LEANFEED_ADDR", "127.0.0.1:8080",
		"--interval", "LEANFEED_INTERVAL", "30m",
		"--workers", "LEANFEED_WORKERS", "4",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("serve --help does not mention %q:\n%s", want, r.stdout)
		}
	}
}

func TestVersionFlag(t *testing.T) {
	version = "v1.2.3"
	r := exec([]string{"--version"}, nil)
	if r.code != 0 || r.stdout != "leanfeed v1.2.3\n" {
		t.Errorf("exit %d, stdout %q", r.code, r.stdout)
	}
}

func TestServeErrorExitCode(t *testing.T) {
	var stdout, stderr bytes.Buffer
	fail := func(config, *slog.Logger) error { return errors.New("address already in use") }
	if code := execute([]string{"serve"}, env(nil), &stdout, &stderr, fail); code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "address already in use") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestImportAndExportCommands(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "in.opml")
	os.WriteFile(file, []byte(`<opml version="2.0"><body>
		<outline text="Tech"><outline type="rss" text="Example Blog" xmlUrl="https://example.com/feed.xml"/></outline>
		<outline type="rss" text="News" xmlUrl="https://news.example/rss"/>
		<outline type="rss" text="Bad" xmlUrl="ftp://bad.example/"/>
	</body></opml>`), 0o644)
	data := filepath.Join(dir, "data")

	r := exec([]string{"--data", data, "import", file}, nil)
	if r.code != 0 {
		t.Fatalf("import exit %d: %s", r.code, r.stderr)
	}
	if r.stdout != "Imported 2 feeds, skipped 1.\n" {
		t.Errorf("import stdout = %q", r.stdout)
	}

	// The data directory can also come from the environment.
	r = exec([]string{"export"}, map[string]string{"LEANFEED_DATA": data})
	if r.code != 0 {
		t.Fatalf("export exit %d: %s", r.code, r.stderr)
	}
	for _, want := range []string{`<opml version="2.0">`, `xmlUrl="https://example.com/feed.xml"`, `<outline text="Tech"`, `xmlUrl="https://news.example/rss"`} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("export output missing %q:\n%s", want, r.stdout)
		}
	}
}

func TestImportMissingFile(t *testing.T) {
	if r := exec([]string{"--data", t.TempDir(), "import", "/does/not/exist.opml"}, nil); r.code != 1 {
		t.Errorf("exit %d, want 1", r.code)
	}
}
