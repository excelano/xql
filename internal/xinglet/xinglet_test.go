package xinglet

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excelano/xql/internal/cell"
)

func TestParseURL(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"xinglet://4babff02-909f-4dba-b3df-3edf14b778bf", "4babff02-909f-4dba-b3df-3edf14b778bf", false},
		{"xinglet://4BABFF02-909F-4DBA-B3DF-3EDF14B778BF", "4BABFF02-909F-4DBA-B3DF-3EDF14B778BF", false},
		{"xinglet://nope", "", true},
		{"xinglet://", "", true},
		{"https://xinglet.com/...", "", true},
		{"", "", true},
		{"xinglet://4babff02-909f-4dba-b3df-3edf14b778bfZZ", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseURL(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseURL(%q): want error, got %q", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseURL(%q): unexpected error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("ParseURL(%q): want %q, got %q", tc.in, tc.want, got)
			}
		})
	}
}

func TestClassifyHeaderCell(t *testing.T) {
	cases := []struct {
		raw      string
		wantName string
		wantType cell.ColumnType
		wantOK   bool
	}{
		{"Email", "Email", 0, false},
		{"Count:number", "Count", cell.TypeFloat, true},
		{"Joined:date", "Joined", cell.TypeDate, true},
		{"Status:choice", "Status", cell.TypeString, true},
		{"Status:choice(active|inactive|pending)", "Status", cell.TypeString, true},
		// Unknown colon suffix stays literal -- matches server tolerance.
		{"Email:work", "Email:work", 0, false},
		{"Time:ratio:absolute", "Time:ratio:absolute", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			name, ty, ok := classifyHeaderCell(tc.raw)
			if name != tc.wantName || ty != tc.wantType || ok != tc.wantOK {
				t.Fatalf("classifyHeaderCell(%q) = (%q, %v, %v); want (%q, %v, %v)",
					tc.raw, name, ty, ok, tc.wantName, tc.wantType, tc.wantOK)
			}
		})
	}
}

func TestTranslateHeader(t *testing.T) {
	body := []byte("Email,Count:number,Joined:date,Status:choice(a|b|c)\nalice@x.com,3,2026-06-18,a\nbob@x.com,7,2026-06-17,b\n")

	bareHeader, hints, rest, err := translateHeader(body)
	if err != nil {
		t.Fatalf("translateHeader: %v", err)
	}

	wantHeader := "Email,Count,Joined,Status"
	if bareHeader != wantHeader {
		t.Fatalf("bareHeader = %q; want %q", bareHeader, wantHeader)
	}

	if hints["Count"] != cell.TypeFloat {
		t.Errorf("Count hint = %v; want TypeFloat", hints["Count"])
	}
	if hints["Joined"] != cell.TypeDate {
		t.Errorf("Joined hint = %v; want TypeDate", hints["Joined"])
	}
	if hints["Status"] != cell.TypeString {
		t.Errorf("Status hint = %v; want TypeString", hints["Status"])
	}
	if _, ok := hints["Email"]; ok {
		t.Errorf("Email should have no hint; got %v", hints["Email"])
	}

	wantRest := "alice@x.com,3,2026-06-18,a\nbob@x.com,7,2026-06-17,b\n"
	if rest != wantRest {
		t.Fatalf("rest = %q; want %q", rest, wantRest)
	}
}

func TestTranslateHeader_EmptyBody(t *testing.T) {
	if _, _, _, err := translateHeader([]byte("")); err == nil {
		t.Fatal("translateHeader(empty): want error, got nil")
	}
}

func TestLoadList_StatusMapping(t *testing.T) {
	cases := []struct {
		name   string
		status int
		want   string
	}{
		{"401 unauthorized", http.StatusUnauthorized, "authentication failed"},
		{"403 forbidden", http.StatusForbidden, "access denied"},
		{"404 not found", http.StatusNotFound, "xinglet not found"},
		{"500 generic", http.StatusInternalServerError, "HTTP 500"},
		{"502 generic", http.StatusBadGateway, "HTTP 502"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()

			_, err := LoadList(Config{
				BaseURL: srv.URL,
				Token:   "xglt_test",
			}, "00000000-0000-0000-0000-000000000000")
			if err == nil {
				t.Fatalf("want error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.want)
			}
		})
	}
}

func TestLoadList_SendsBearerToken(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/csv")
		_, _ = io.WriteString(w, "Email,Count:number\nalice@x.com,3\n")
	}))
	defer srv.Close()

	tbl, err := LoadList(Config{
		BaseURL: srv.URL,
		Token:   "xglt_secret",
	}, "00000000-0000-0000-0000-000000000000")
	if err != nil {
		t.Fatalf("LoadList: %v", err)
	}
	if gotAuth != "Bearer xglt_secret" {
		t.Fatalf("Authorization header = %q; want %q", gotAuth, "Bearer xglt_secret")
	}
	if tbl.Path != "xinglet://00000000-0000-0000-0000-000000000000" {
		t.Errorf("Path = %q; want %q", tbl.Path, "xinglet://00000000-0000-0000-0000-000000000000")
	}
	if len(tbl.Columns) != 2 {
		t.Fatalf("Columns = %v; want 2", tbl.Columns)
	}
	if tbl.Columns[0] != "Email" || tbl.Columns[1] != "Count" {
		t.Errorf("Columns = %v; want [Email Count]", tbl.Columns)
	}
	if tbl.Schema["Count"].Type != cell.TypeFloat {
		t.Errorf("Count type = %v; want TypeFloat (hint should win over inference)", tbl.Schema["Count"].Type)
	}
}

func TestLoadList_TransportError(t *testing.T) {
	// Point at a URL that will fail to connect: bind a server and close it.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()

	_, err := LoadList(Config{
		BaseURL: url,
		Token:   "xglt_test",
	}, "00000000-0000-0000-0000-000000000000")
	if err == nil {
		t.Fatal("want transport error, got nil")
	}
	if !strings.Contains(err.Error(), "connecting to") {
		t.Errorf("error %q should mention \"connecting to\"", err.Error())
	}
}
