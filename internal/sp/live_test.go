//go:build live

package sp

// Live test for the SharePoint backend, run against a real list through the
// shared token cache. It is the pre-release pass the unit tests cannot be: CI
// holds no token, so this runs locally on a machine that has signed in once.
//
//	XQL_LIVE_LIST=https://<tenant>.sharepoint.com/sites/<test-site>/Lists/<test-list> go test -tags live ./...
//
// The list needs a Title column and a text column named TestText. One row is
// inserted, read back, updated, and deleted, so the list ends as it began.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/excelano/spauth"
	"github.com/excelano/xql/internal/parse"
)

func TestLiveRowRoundTrip(t *testing.T) {
	listURL := os.Getenv("XQL_LIVE_LIST")
	if listURL == "" {
		t.Fatal("XQL_LIVE_LIST is not set; point it at a SharePoint test list to run the live tests")
	}
	ctx := context.Background()
	client, err := spauth.NewPublicClient(spauth.CachePath(), "")
	if err != nil {
		t.Fatal(err)
	}
	st, err := spauth.CheckStatus(ctx, client, spauth.CachePath())
	if err != nil || !st.SignedIn {
		t.Fatalf("no usable session in %s; sign in with xql sp or any xfiles tool first (%v %s)", spauth.CachePath(), err, st.Reason)
	}
	accounts, _ := client.Accounts(ctx)
	graph := spauth.NewGraphClient(client, accounts[0],
		spauth.WithTimeout(60*time.Second),
		spauth.WithHeader("Prefer", "HonorNonIndexedQueriesWarningMayFailRandomly"),
	)
	bound, err := ResolveListBinding(ctx, graph, listURL)
	if err != nil {
		t.Fatalf("binding %s: %v", listURL, err)
	}

	var out bytes.Buffer
	exec := &Executor{Graph: graph, Bound: bound, Mode: "json", Headers: true, Out: &out}
	run := func(sql string) {
		t.Helper()
		out.Reset()
		stmt, err := parse.Parse(sql)
		if err != nil {
			t.Fatalf("parse %q: %v", sql, err)
		}
		if err := exec.Execute(ctx, stmt, true); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	rows := func(sql string) []map[string]any {
		t.Helper()
		run(sql)
		var got []map[string]any
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatalf("%s: output is not a JSON array: %v\n%s", sql, err, out.String())
		}
		return got
	}

	marker := fmt.Sprintf("xql-live-%d", time.Now().UnixNano())
	where := fmt.Sprintf(" WHERE Title = '%s'", marker)
	// Whatever happens below, the marker row must not outlive the test.
	t.Cleanup(func() {
		out.Reset()
		if stmt, err := parse.Parse("DELETE" + where); err == nil {
			_ = exec.Execute(ctx, stmt, true)
		}
	})

	run(fmt.Sprintf("INSERT (Title, TestText) VALUES ('%s', 'first')", marker))

	got := rows("SELECT Title, TestText" + where)
	if len(got) != 1 || got[0]["TestText"] != "first" {
		t.Fatalf("after INSERT, SELECT returned %v; want one row with TestText=first", got)
	}

	run("UPDATE SET TestText = 'second'" + where)
	got = rows("SELECT TestText" + where)
	if len(got) != 1 || got[0]["TestText"] != "second" {
		t.Fatalf("after UPDATE, SELECT returned %v; want one row with TestText=second", got)
	}

	run("DELETE" + where)
	if got = rows("SELECT Title" + where); len(got) != 0 {
		t.Fatalf("after DELETE, SELECT still returned %v", got)
	}
}
