package svgsafe

import (
	"strings"
	"testing"
)

func TestSanitizeStripsDangerousContent(t *testing.T) {
	cases := map[string]string{
		"script":        `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script><circle data-seat="A1" cx="1" cy="1" r="2"/></svg>`,
		"onload":        `<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"><circle cx="1" cy="1" r="2" onclick="x()"/></svg>`,
		"foreignObject": `<svg xmlns="http://www.w3.org/2000/svg"><foreignObject><div>hi</div></foreignObject><rect width="1" height="1"/></svg>`,
		"href":          `<svg xmlns="http://www.w3.org/2000/svg"><a href="javascript:alert(1)"><text>x</text></a></svg>`,
		"use":           `<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink"><use xlink:href="http://evil/x.svg#a"/></svg>`,
		"style":         `<svg xmlns="http://www.w3.org/2000/svg"><style>*{background:url(http://evil)}</style><rect style="x" width="1" height="1"/></svg>`,
		"image":         `<svg xmlns="http://www.w3.org/2000/svg"><image href="http://evil/x.png"/></svg>`,
		"urlfill":       `<svg xmlns="http://www.w3.org/2000/svg"><rect width="1" height="1" fill="url(http://evil/x)"/></svg>`,
		"jsvalue":       `<svg xmlns="http://www.w3.org/2000/svg"><rect width="1" height="1" fill="javascript:alert(1)"/></svg>`,
	}
	for name, in := range cases {
		out, err := Sanitize([]byte(in))
		if err != nil {
			t.Errorf("%s: error tak terduga %v", name, err)
			continue
		}
		low := strings.ToLower(string(out))
		for _, bad := range []string{"script", "onload", "onclick", "foreignobject", "javascript", "href", "<use", "<style", "<image", "evil", "style="} {
			if strings.Contains(low, bad) {
				t.Errorf("%s: output masih memuat %q: %s", name, bad, out)
			}
		}
	}
}

func TestSanitizeKeepsSeatMapStructure(t *testing.T) {
	in := `<?xml version="1.0"?><svg viewBox="0 0 100 100"><g data-section="A" data-tier="CAT1"><circle data-seat="A-1-1" cx="10" cy="10" r="3" fill="#0f0"/><text x="5" y="5">A</text></g><!-- c --></svg>`
	out, err := Sanitize([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{`xmlns="http://www.w3.org/2000/svg"`, `data-section="A"`, `data-tier="CAT1"`, `data-seat="A-1-1"`, `cx="10"`, `viewBox="0 0 100 100"`, ">A</text>"} {
		if !strings.Contains(s, want) {
			t.Errorf("hilang %q di: %s", want, s)
		}
	}
	if strings.Contains(s, "<?xml") || strings.Contains(s, "<!--") {
		t.Errorf("PI/komentar harus dibuang: %s", s)
	}
}

func TestSanitizeRejects(t *testing.T) {
	for name, in := range map[string]string{
		"kosong":   ``,
		"bukan":    `<html><body/></html>`,
		"doctype":  `<!DOCTYPE svg [<!ENTITY x SYSTEM "file:///etc/passwd">]><svg>&x;</svg>`,
		"rusak":    `<svg><g></svg>`,
		"billion":  `<!DOCTYPE lolz [<!ENTITY lol "lol">]><svg/>`,
		"bukanxml": `bukan xml`,
	} {
		if _, err := Sanitize([]byte(in)); err == nil {
			t.Errorf("%s: harus ditolak", name)
		}
	}
	if _, err := Sanitize([]byte(strings.Repeat("a", MaxBytes+1))); err != ErrTooLarge {
		t.Errorf("terlalu besar: %v", err)
	}
}
