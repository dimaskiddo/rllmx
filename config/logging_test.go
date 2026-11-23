package config

import (
	"testing"

	"github.com/goravel/framework/contracts/log"

	"rllmx/app/facades"
	"rllmx/app/platform/redactor"
	"rllmx/app/platform/settings"
	"rllmx/tests"
)

// stubDriver is a file driver a test can tell apart from the console one.
type stubDriver struct{}

func (stubDriver) Handle(string) (log.Handler, error) { return &tests.Handler{}, nil }

func TestLogSinksFollowTheOutput(t *testing.T) {
	cases := []struct {
		output    settings.LogOutput
		hasFile   bool
		hasStdout bool
	}{
		{settings.LogOutput{File: true}, true, false},
		{settings.LogOutput{Stdout: true}, false, true},
		{settings.LogOutput{File: true, Stdout: true}, true, true},
	}
	for _, c := range cases {
		sinks := logSinks(c.output, tests.Config{}, stubDriver{})
		var file, stdoutSink bool
		for _, s := range sinks {
			switch s.(type) {
			case stubDriver:
				file = true
			case stdout:
				stdoutSink = true
			}
		}
		if file != c.hasFile || stdoutSink != c.hasStdout {
			t.Errorf("%+v: file %t stdout %t", c.output, file, stdoutSink)
		}
	}
}

func TestLoggingOutputIsASinkName(t *testing.T) {
	if _, err := settings.ParseLogOutput(facades.Config().GetString("logging.output")); err != nil {
		t.Fatal(err)
	}
}

// Every file channel must go through the redactor. A channel that goes back to
// the framework's plain "single" or "daily" driver would write secrets as they
// are, and nothing else would notice.
func TestEveryFileChannelIsBehindTheRedactor(t *testing.T) {
	cfg := facades.Config()
	for _, name := range []string{"single", "daily"} {
		path := "logging.channels." + name
		if got := cfg.GetString(path + ".driver"); got != "custom" {
			t.Errorf("%s: driver %q, want custom", name, got)
		}
		via, ok := cfg.Get(path + ".via").(log.Logger)
		if !ok {
			t.Errorf("%s: via is %T, not a log.Logger", name, cfg.Get(path+".via"))
			continue
		}
		if _, ok := via.(*redactor.Channel); !ok {
			t.Errorf("%s: via is %T, want *redactor.Channel", name, via)
		}
	}
}

func TestTheStackLogsOnlyThroughRedactedChannels(t *testing.T) {
	cfg := facades.Config()
	for _, name := range cfg.GetStringSlice("logging.channels.stack.channels") {
		if cfg.GetString("logging.channels."+name+".driver") != "custom" {
			t.Errorf("the stack includes %q, which is not behind the redactor", name)
		}
	}
}

func TestTheFileChannelsKeepTheKeysTheFrameworkReads(t *testing.T) {
	cfg := facades.Config()
	for _, name := range []string{"single", "daily"} {
		path := "logging.channels." + name
		if cfg.GetString(path+".path") == "" || cfg.GetString(path+".level") == "" || cfg.GetString(path+".formatter") == "" {
			t.Errorf("%s lacks path, level or formatter", name)
		}
	}
	if cfg.GetInt("logging.channels.daily.days") != 7 {
		t.Error("the daily channel does not keep 7 days")
	}
	if cfg.GetString("logging.channels.single.path") != "storage/logs/rllmx.log" {
		t.Errorf("log file %q", cfg.GetString("logging.channels.single.path"))
	}
}
