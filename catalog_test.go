package toolcatalog

import "testing"

func TestEmbeddedCatalogLoads(t *testing.T) {
	catalog, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Tools) != 3 {
		t.Fatalf("tool count = %d, want 3", len(catalog.Tools))
	}
}
