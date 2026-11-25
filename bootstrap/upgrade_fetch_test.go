package bootstrap

import "testing"

func TestTheReleaseAnswerAsksForJSONAndTheArchiveForBytes(t *testing.T) {
	for address, want := range map[string]string{
		"https://api.github.com/repos/dimaskiddo/rllmx/releases/latest":                      "application/vnd.github+json",
		"https://github.com/dimaskiddo/rllmx/releases/download/v1.0.0/rllmx_1.0.0_linux.zip": "application/octet-stream",
		"https://github.com/dimaskiddo/rllmx/releases/download/v1.0.0/checksums.txt":         "application/octet-stream",
	} {
		if got := releaseAccept(address); got != want {
			t.Errorf("%s: Accept %q, want %q", address, got, want)
		}
	}
}

func TestAnUpdateRelaunchesTheBinaryItNamedOnceAndNothingElse(t *testing.T) {
	var started []string
	start := func(exe string) error { started = append(started, exe); return nil }
	relaunch(start)
	if len(started) != 0 {
		t.Fatalf("started %v with no update pending", started)
	}
	exe := "/opt/rllmx"
	pendingExe.Store(&exe)
	relaunch(start)
	relaunch(start)
	if len(started) != 1 || started[0] != exe {
		t.Fatalf("started %v, want the named binary once", started)
	}
}
