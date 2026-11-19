package settings

import (
	"strings"
	"testing"

	"rllmx/tests"
)

func saverValues(raw string) tests.Config {
	return tests.Config{Values: map[string]any{"saver.headroom_url": raw}}
}

func TestLoadSaverKeepsAnEmptyURLAndTrimsAHost(t *testing.T) {
	s, err := LoadSaver(saverValues(""))
	if err != nil || s.HeadroomURL != "" {
		t.Fatalf("%+v, %v", s, err)
	}
	s, err = LoadSaver(saverValues(" http://localhost:8787/ "))
	if err != nil || s.HeadroomURL != "http://localhost:8787/" {
		t.Fatalf("%+v, %v", s, err)
	}
}

func TestLoadSaverRefusesWhatIsNotASidecarAddress(t *testing.T) {
	for _, v := range []string{"ftp://host:8787", "http://user:pw@host:8787", "http://host:8787/#x", "localhost:8787"} {
		_, err := LoadSaver(saverValues(v))
		if err == nil || !strings.Contains(err.Error(), "SAVER_HEADROOM_URL") {
			t.Errorf("%s: err %v", v, err)
		}
	}
}
