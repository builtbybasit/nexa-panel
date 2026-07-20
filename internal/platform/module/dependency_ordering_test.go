package module

import "testing"

type testModule struct{ descriptor Descriptor }

func (m testModule) Descriptor() Descriptor  { return m.descriptor }
func (m testModule) Register(Registry) error { return nil }

func TestValidateAndSort(t *testing.T) {
	modules, err := ValidateAndSort([]Module{
		testModule{descriptor: Descriptor{ID: "sites", Name: "Sites", Version: "1"}},
		testModule{descriptor: Descriptor{ID: "databases", Name: "Databases", Version: "1"}},
	})
	if err != nil {
		t.Fatalf("ValidateAndSort returned an error: %v", err)
	}
	if got := modules[0].Descriptor().ID; got != "databases" {
		t.Fatalf("first module = %q, want databases", got)
	}
}

func TestValidateAndSortRejectsMissingDependency(t *testing.T) {
	_, err := ValidateAndSort([]Module{
		testModule{descriptor: Descriptor{
			ID:           "sites",
			Name:         "Sites",
			Version:      "1",
			Dependencies: []string{"domains"},
		}},
	})
	if err == nil {
		t.Fatal("expected a missing dependency error")
	}
}

func TestValidateAndSortPlacesDependencyFirst(t *testing.T) {
	modules, err := ValidateAndSort([]Module{
		testModule{descriptor: Descriptor{
			ID:           "sites",
			Name:         "Sites",
			Version:      "1",
			Dependencies: []string{"domains"},
		}},
		testModule{descriptor: Descriptor{ID: "domains", Name: "Domains", Version: "1"}},
	})
	if err != nil {
		t.Fatalf("ValidateAndSort returned an error: %v", err)
	}
	if got := modules[0].Descriptor().ID; got != "domains" {
		t.Fatalf("first module = %q, want domains", got)
	}
}

func TestValidateAndSortRejectsCycle(t *testing.T) {
	_, err := ValidateAndSort([]Module{
		testModule{descriptor: Descriptor{
			ID:           "sites",
			Name:         "Sites",
			Version:      "1",
			Dependencies: []string{"domains"},
		}},
		testModule{descriptor: Descriptor{
			ID:           "domains",
			Name:         "Domains",
			Version:      "1",
			Dependencies: []string{"sites"},
		}},
	})
	if err == nil {
		t.Fatal("expected a dependency cycle error")
	}
}
