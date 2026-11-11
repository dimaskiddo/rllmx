package claude

import "testing"

func TestReadToolArgumentsAreRepaired(t *testing.T) {
	cases := map[string]string{
		`{"file_path":"/a","limit":"50","offset":"-3"}`: `{"file_path":"/a","limit":50,"offset":0}`,
		`{"file_path":"/a","limit":99999}`:              `{"file_path":"/a","limit":2000}`,
		`{"file_path":"/a","limit":0}`:                  `{"file_path":"/a"}`,
		`{"file_path":"/a.pdf","pages":"1-3"}`:          `{"file_path":"/a.pdf","pages":"1-3"}`,
		`{"file_path":"/a.txt","pages":"1-3"}`:          `{"file_path":"/a.txt"}`,
		`{"file_path":"/a.pdf","pages":"all"}`:          `{"file_path":"/a.pdf"}`,
		``:                                              `{}`,
		`not json`:                                      `not json`,
	}
	for in, want := range cases {
		if got := repairToolArgs("Read", in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
	if got := repairToolArgs("Bash", `{"limit":"5"}`); got != `{"limit":"5"}` {
		t.Fatalf("another tool was touched: %q", got)
	}
}
