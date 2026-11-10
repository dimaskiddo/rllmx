package mux

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"rllmx/app/llm/executor"
	"rllmx/app/llm/upstream"
	"rllmx/tests"
	"rllmx/tests/llm"
)

func TestAnUnregisteredExecutorFailsTheCallAndNeverFallsBack(t *testing.T) {
	up := tests.NewUpstream(t, tests.Reply{Body: `{}`})
	m := New(&executor.Default{Client: http.DefaultClient}, Options{})
	_, err := m.Execute(context.Background(), executor.Request{URL: up.URL, Key: llm.Secret, Auth: llm.Bearer(), Entry: upstream.Entry{Executor: "mystery"}})
	if !errors.Is(err, ErrUnknownExecutor) || len(up.Calls()) != 0 {
		t.Fatalf("err %v after %d calls", err, len(up.Calls()))
	}
	resp, err := m.Execute(context.Background(), executor.Request{URL: up.URL, Key: llm.Secret, Auth: llm.Bearer()})
	if err != nil {
		t.Fatalf("a request with no entry (a user node) must use the default executor: %v", err)
	}
	resp.Body.Close()
}

func TestTheMuxServesExactlyTheExecutorsADataFileMayName(t *testing.T) {
	got := map[string]bool{}
	for _, n := range New(&executor.Default{}, Options{}).Names() {
		got[n] = true
	}
	want := upstream.ExecutorNames()
	if len(got) != len(want) {
		t.Fatalf("mux %v, data files may name %v", got, want)
	}
	for _, n := range want {
		if !got[n] {
			t.Errorf("a data file may name %q and the mux has no such executor", n)
		}
	}
}

func TestAMuxServesTheCopilotExecutor(t *testing.T) {
	if _, ok := New(&executor.Default{}, Options{}).byName[upstream.ExecutorGitHub]; !ok {
		t.Fatal("github is not registered")
	}
}
