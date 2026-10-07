package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func TestParseConfig(t *testing.T) {
	defaults := config{Data: "./data", Addr: "127.0.0.1:8080", Interval: 30 * time.Minute, Workers: 4}
	allEnv := map[string]string{
		"LEANFEED_DATA": "/srv/feeds", "LEANFEED_ADDR": "0.0.0.0:9000",
		"LEANFEED_INTERVAL": "1h", "LEANFEED_WORKERS": "8",
	}
	tests := []struct {
		name     string
		args     []string
		env      map[string]string
		want     config
		wantArgs []string
	}{
		{"defaults", []string{"serve"}, nil, withCmd(defaults, "serve"), nil},
		{"environment", []string{"serve"}, allEnv,
			config{Command: "serve", Data: "/srv/feeds", Addr: "0.0.0.0:9000", Interval: time.Hour, Workers: 8}, nil},
		{"flags before command override environment", []string{"--data", "/x", "--interval", "5m", "serve"}, allEnv,
			config{Command: "serve", Data: "/x", Addr: "0.0.0.0:9000", Interval: 5 * time.Minute, Workers: 8}, nil},
		{"flags after command", []string{"serve", "--addr", ":1234", "--workers=2"}, nil,
			config{Command: "serve", Data: "./data", Addr: ":1234", Interval: 30 * time.Minute, Workers: 2}, nil},
		{"import file", []string{"--data", "d", "import", "subs.opml"}, nil,
			config{Command: "import", Data: "d", Addr: "127.0.0.1:8080", Interval: 30 * time.Minute, Workers: 4}, []string{"subs.opml"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, args, err := parseConfig(tt.args, env(tt.env))
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want || !reflect.DeepEqual(args, tt.wantArgs) {
				t.Errorf("parseConfig = %+v %q, want %+v %q", got, args, tt.want, tt.wantArgs)
			}
		})
	}
}

func withCmd(c config, cmd string) config {
	c.Command = cmd
	return c
}

func TestParseConfigErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		env  map[string]string
	}{
		{"no command", nil, nil},
		{"unknown command", []string{"frobnicate"}, nil},
		{"bad interval flag", []string{"--interval", "soon", "serve"}, nil},
		{"bad interval env", []string{"serve"}, map[string]string{"LEANFEED_INTERVAL": "soon"}},
		{"interval too short", []string{"--interval", "10s", "serve"}, nil},
		{"zero workers", []string{"--workers", "0", "serve"}, nil},
		{"bad workers env", []string{"serve"}, map[string]string{"LEANFEED_WORKERS": "many"}},
		{"import without file", []string{"import"}, nil},
		{"serve with extra args", []string{"serve", "extra"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if cfg, _, err := parseConfig(tt.args, env(tt.env)); err == nil {
				t.Errorf("parseConfig(%q) = %+v, want an error", tt.args, cfg)
			}
		})
	}
}

func TestVersionFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	version = "v1.2.3"
	if code := run([]string{"--version"}, env(nil), &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if stdout.String() != "leanfeed v1.2.3\n" {
		t.Errorf("stdout = %q", stdout.String())
	}
}

func TestUsageErrorExitCode(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"frobnicate"}, env(nil), &stdout, &stderr); code != 2 {
		t.Errorf("exit %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "Usage") {
		t.Errorf("stderr = %q, want usage", stderr.String())
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

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--data", data, "import", file}, env(nil), &stdout, &stderr); code != 0 {
		t.Fatalf("import exit %d: %s", code, stderr.String())
	}
	if got := stdout.String(); got != "Imported 2 feeds, skipped 1.\n" {
		t.Errorf("import stdout = %q", got)
	}

	stdout.Reset()
	if code := run([]string{"--data", data, "export"}, env(nil), &stdout, &stderr); code != 0 {
		t.Fatalf("export exit %d: %s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{`<opml version="2.0">`, `xmlUrl="https://example.com/feed.xml"`, `<outline text="Tech"`, `xmlUrl="https://news.example/rss"`} {
		if !strings.Contains(out, want) {
			t.Errorf("export output missing %q:\n%s", want, out)
		}
	}
}

func TestImportMissingFile(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--data", t.TempDir(), "import", "/does/not/exist.opml"}, env(nil), &stdout, &stderr); code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
}
