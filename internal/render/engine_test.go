package render

import "testing"

func TestSpecs_BothEnginesDefined(t *testing.T) {
	for _, name := range []string{"chrome", "lightpanda"} {
		s, ok := Specs[name]
		if !ok || s.Name != name || s.DefaultPath == "" || len(s.Args) == 0 {
			t.Fatalf("spec %s incomplete: %+v", name, s)
		}
	}
	if !Specs["chrome"].HasLayout || Specs["lightpanda"].HasLayout {
		t.Fatal("HasLayout: chrome=true, lightpanda=false expected")
	}
}
