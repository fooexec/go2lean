package advisory

import (
	"context"
	"strings"
	"testing"
)

const osv = `{"id":"GHSA-x","aliases":["CVE-1"],"summary":"open redirect","details":"The Host header is used.",
"affected":[{"package":{"ecosystem":"Go","name":"example.com/chi"},"ranges":[{"events":[{"introduced":"0"},{"fixed":"5.2.2"}]}]}],
"references":[{"type":"FIX","url":"https://github.com/x/y/commit/abc"},{"type":"WEB","url":"https://example.com"}]}`

type fake struct{ sys, user string }

func (f *fake) Complete(_ context.Context, s, u string) (string, error) {
	f.sys, f.user = s, u
	return "# A redirect stays on the same site\n", nil
}

func TestParseAndDraft(t *testing.T) {
	a, err := Parse([]byte(osv))
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != "GHSA-x" || len(a.Packages) != 1 || a.Packages[0].Fixed[0] != "5.2.2" || len(a.Fix) != 1 || len(a.Reference) != 1 {
		t.Fatalf("parsed %+v", a)
	}
	f := &fake{}
	d, err := Draft(context.Background(), f, a)
	if err != nil || !Checkable(d) || !strings.Contains(f.user, "Host header") || !strings.Contains(f.sys, "Never write the advisory's trigger") {
		t.Fatalf("draft %q err %v", d, err)
	}
	if Checkable("NOT CHECKABLE: build issue") {
		t.Fatal("refusal taken as a property")
	}
	if _, err := Parse([]byte(`{}`)); err == nil {
		t.Fatal("an empty record parsed")
	}
}
