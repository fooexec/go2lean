package mine

import (
	"context"

	"github.com/fooexec/go2lean/internal/advisory"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const cSrc = `/* Packet parsing, RFC 4271 Section 4.3. */
#include "attr.h"

static const char *s = "/* not a comment */";

// Parse an attribute; see
// RFC 7606, Section 7.
static enum ret
attr_parse(struct args *args,
	   size_t len)
{
	if (len < 2) {
		return RET_ERR;
	}
	return RET_OK;
}

int attr_count(void) {
	return 0;
}

struct table t = {
	.f = attr_count,
};

#define CHECK(x) do { if (!(x)) abort(); } while (0)
`

func TestCComments(t *testing.T) {
	cs := cComments([]byte(cSrc))
	if len(cs) != 2 || cs[0].line != 1 || !strings.Contains(cs[0].text, "RFC 4271") ||
		cs[1].line != 6 || !strings.Contains(cs[1].text, "RFC 7606, Section 7.") {
		t.Errorf("comments: %+v", cs)
	}
}

func TestCFunctions(t *testing.T) {
	if got := strings.Join(cFunctions([]byte(cSrc)), ","); got != "attr_parse,attr_count" {
		t.Errorf("functions: %s", got)
	}
}

func TestCCitations(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "bgpd/attr.c", cSrc)
	write(t, dir, "tests/attr_test.c", "/* RFC 9999 */\n")
	ev, err := Citations(dir)
	if err != nil {
		t.Fatal(err)
	}
	refs := map[string]bool{}
	for _, e := range ev {
		refs[e.Ref] = true
	}
	if !refs["RFC 4271 4.3"] || !refs["RFC 7606 7"] || refs["RFC 9999"] {
		t.Errorf("citations: %v", refs)
	}
	funcs, _, err := Functions(dir)
	if err != nil || strings.Join(funcs["bgpd"], ",") != "attr_count,attr_parse" || Language(dir) != "C" {
		t.Errorf("functions %v %v, language %s", funcs, err, Language(dir))
	}
}

func TestAdvisoriesUpstream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/query":
			w.Write([]byte(`{"vulns":[{"id":"DEBIAN-CVE-2023-0001","upstream":["CVE-2023-0001"],"details":"A crafted UPDATE crashes bgpd. More text.",
			 "affected":[{"package":{"ecosystem":"Debian:12","name":"frr"},"ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"0"},{"fixed":"8.4.4-1"}]}]}]}]}`))
		case "/v1/vulns/CVE-2023-0001":
			w.Write([]byte(`{"id":"CVE-2023-0001","details":"In bgpd/bgp_attr.c, bgp_attr_parse reads past the end of a crafted attribute, which crashes bgpd.",
			 "affected":[{"ranges":[{"type":"GIT","repo":"https://github.com/frrouting/frr","events":[{"introduced":"0"},{"fixed":"0123456789abcdef0123456789abcdef01234567"}]}]}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	ev, err := Advisories(context.Background(), srv.URL, "Debian:frr", "Debian:frr")
	if err != nil {
		t.Fatal(err)
	}
	// Without the upstream's fix, the Debian version is no revision.
	if a := fromAdvisory(&advisory.Advisory{ID: "x", Packages: []advisory.Package{{Ecosystem: "Debian:12", Fixed: []string{"8.4.4-1"}}}}, "p", true); a.After != "" {
		t.Errorf("distribution version taken as a revision: %+v", a)
	}
	if len(ev) != 1 || ev[0].After != "0123456789abcdef0123456789abcdef01234567" || ev[0].Ref != "CVE-2023-0001" ||
		!contains(ev[0].Aliases, "DEBIAN-CVE-2023-0001") || !strings.HasPrefix(ev[0].Title, "In bgpd/bgp_attr.c") || !strings.Contains(ev[0].Text, "bgp_attr_parse") {
		t.Errorf("evidence: %+v", ev)
	}
}

func TestSystemFor(t *testing.T) {
	if systemFor(proposeSystem, "Go") != proposeSystem {
		t.Error("Go prompt changed")
	}
	c := systemFor(proposeSystem, "C")
	if !strings.Contains(c, "about a C project") || !strings.Contains(c, "as a C function (a spec)") || strings.Contains(c, "Go function") {
		t.Errorf("C prompt: %s", c[:300])
	}
}
