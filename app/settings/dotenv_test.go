package settings

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeEnvFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadKeys(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"plain", "A=1", []string{"A"}},
		{"sorted and unique", "B=1\nA=2\nB=3", []string{"A", "B"}},
		{"export", "export A=1", []string{"A"}},
		{"spaces", "A = 1", []string{"A"}},
		{"comment line", "# c\nA=1", []string{"A"}},
		{"inline comment", "A=1 # c", []string{"A"}},
		{"quoted hash", `A="x # y"`, []string{"A"}},
		{"single quoted dollar", `A='pa$word'`, []string{"A"}},
		{"crlf", "A=1\r\nB=2\r\n", []string{"A", "B"}},
		{"empty value", "A=", []string{"A"}},
		{"url with hash", "A=postgres://u:p#w@h/db", []string{"A"}},
		{"leading bom", "\ufeffA=1", []string{"A"}},
		{"empty file", "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ReadKeys(writeEnvFile(t, tt.in))
			if err != nil {
				t.Fatalf("error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestReadKeysMissingFile(t *testing.T) {
	got, err := ReadKeys(filepath.Join(t.TempDir(), "absent.env"))
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v; want no keys and no error", got, err)
	}
}

func TestReadKeysSizeLimit(t *testing.T) {
	line := "A=" + strings.Repeat("x", 1022) + "\n"
	atLimit := strings.Repeat(line, maxDotenvBytes/len(line)-1)
	atLimit += "B=" + strings.Repeat("y", maxDotenvBytes-len(atLimit)-len("B=\n")) + "\n"
	if len(atLimit) != maxDotenvBytes {
		t.Fatalf("test setup: %d != %d", len(atLimit), maxDotenvBytes)
	}
	if _, err := ReadKeys(writeEnvFile(t, atLimit)); err != nil {
		t.Fatalf("file of exactly the limit rejected: %v", err)
	}
	if _, err := ReadKeys(writeEnvFile(t, atLimit+"B=1\n")); err == nil {
		t.Fatal("file over the limit accepted")
	}
}

// The parser reads line by line with a 64 KiB token limit, the same one the
// framework's loader hits, so a longer line is invalid syntax there too.
func TestReadKeysRejectsALineOverTheParserLimit(t *testing.T) {
	p := writeEnvFile(t, "A="+strings.Repeat("x", 70<<10)+"\n")
	_, err := ReadKeys(p)
	if err == nil || !strings.Contains(err.Error(), filepath.Base(p)) {
		t.Fatalf("error = %v", err)
	}
}

func TestReadKeysErrorsNeverEchoContent(t *testing.T) {
	const marker = "s3cr3t-marker-value"
	inputs := []string{
		"A=\"" + marker + "\nB=2\n",
		"A='" + marker,
		marker,
		"1KEY=" + marker,
		"A=\"x\" " + marker,
	}
	for _, in := range inputs {
		p := writeEnvFile(t, in)
		_, err := ReadKeys(p)
		if err == nil {
			continue
		}
		if strings.Contains(err.Error(), marker) {
			t.Fatalf("error text %q echoes file content", err.Error())
		}
		if !strings.Contains(err.Error(), filepath.Base(p)) {
			t.Fatalf("error %q does not name the file", err.Error())
		}
	}
}

func TestReadKeysNeverReturnsAnEmptyKey(t *testing.T) {
	keys, err := ReadKeys(writeEnvFile(t, "just-a-word\n"))
	if err != nil {
		return
	}
	for _, k := range keys {
		if k == "" {
			t.Fatalf("empty key leaked into the result: %#v", keys)
		}
	}
}

func TestCheckEnvFile(t *testing.T) {
	cases := []struct {
		name    string
		file    string
		wantErr string
	}{
		{"known names", "APP_KEY=x\nDB_CONNECTION=sqlite\nSERVER_MAX_BODY_MB=7\n", ""},
		{"unknown in owned group", "SERVER_PROT=1\n", "SERVER_PROT"},
		{"unknown http client", "HTTP_CLIENT_TIMEOUT=5s\n", "HTTP_CLIENT_TIMEOUT"},
		{"lower case spelling", "db_connection=postgres\n", "db_connection"},
		{"mixed case spelling", "Server_Max_Body_Mb=1\n", "Server_Max_Body_Mb"},
		{"encryption secret is known now", "DB_ENCRYPTION_SECRET=x\nANALYTICS_RETENTION_DAYS=30\n", ""},
		{"stream names are known now", "STREAM_STALL_TIMEOUT=30s\nSTREAM_USAGE_WAIT=1s\n", ""},
		{"planned name", "STREAM_EMPTY_PEEK_DEADLINE=30s\n", "STREAM_EMPTY_PEEK_DEADLINE"},
		{"planned oauth name", "OAUTH_CLAUDE_CLIENT_ID=x\n", "OAUTH_CLAUDE_CLIENT_ID"},
		{"names of other programs", "PATH=/usr/bin\nHOME=/home/x\nTEST_REDIS_URL=redis://127.0.0.1\nSERVERX=1\n", ""},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckEnvFile(writeEnvFile(t, tt.file))
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("rejected: %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("error = %v, want mention of %s", err, tt.wantErr)
			}
		})
	}
}

func TestCheckEnvFileNeverQuotesAValue(t *testing.T) {
	const marker = "s3cr3t-marker-value"
	err := CheckEnvFile(writeEnvFile(t, "AUTH_NOPE="+marker+"\n"))
	if err == nil || strings.Contains(err.Error(), marker) {
		t.Fatalf("error = %v", err)
	}
}
