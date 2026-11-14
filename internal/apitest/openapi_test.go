package apitest

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/oasdiff/yaml"

	"tixgo/backend/internal/apidoc"
)

var paramRe = regexp.MustCompile(`:[A-Za-z_]+|\*[A-Za-z_]+`)

// TestOpenAPIMatchesRouter: kontrak api/openapi.yaml dibangkitkan dari router nyata dan HARUS sama dengan berkas di repo.
// Perbarui: OPENAPI_UPDATE=1 go test ./internal/apitest -run TestOpenAPIMatchesRouter
func TestOpenAPIMatchesRouter(t *testing.T) {
	e := NewEnv(t)
	probe := func(method, path string) bool {
		url := paramRe.ReplaceAllString(path, "01ARZ3NDEKTSV4RRFFQ69G5FAV")
		var body any
		if method != "GET" && method != "DELETE" && method != "HEAD" {
			body = M{}
		}
		r := e.Anon().Do(method, url, body)
		return r.Code == 401 && strings.Contains(string(r.Body), "UNAUTHENTICATED")
	}
	doc, err := apidoc.Build(e.Engine.Routes(), probe)
	if err != nil {
		t.Fatal(err)
	}
	js, err := doc.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	// muat ulang dari JSON agar $ref teresolusi, lalu validasi penuh
	loaded, err := openapi3.NewLoader().LoadFromData(js)
	if err != nil {
		t.Fatalf("OpenAPI gagal dimuat: %v", err)
	}
	if err := loaded.Validate(context.Background()); err != nil {
		t.Fatalf("OpenAPI tidak valid: %v", err)
	}
	y, err := yaml.JSONToYAML(js)
	if err != nil {
		t.Fatal(err)
	}
	const file = "../../api/openapi.yaml"
	if os.Getenv("OPENAPI_UPDATE") == "1" {
		if err := os.WriteFile(file, y, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Logf("api/openapi.yaml diperbarui (%d byte, %d path)", len(y), doc.Paths.Len())
		return
	}
	cur, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(cur) != string(y) {
		t.Fatal("api/openapi.yaml tidak sinkron dengan router; jalankan: OPENAPI_UPDATE=1 go test ./internal/apitest -run TestOpenAPIMatchesRouter")
	}
}
