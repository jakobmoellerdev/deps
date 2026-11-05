package yamlspan

import "testing"

func TestParsedDocument(t *testing.T) {
	a := []byte("a: 1\n")
	var p Parsed
	d1, err := p.Document(a)
	if err != nil || len(d1.Content) != 1 || Value(Get(d1.Content[0], "a")) != "1" {
		t.Fatalf("Document = %+v, %v", d1, err)
	}
	if d2, _ := p.Document(a); d2 != d1 {
		t.Error("same bytes: want the memoized node")
	}
	// equal content in another slice is another file: parsed anew
	if d3, _ := p.Document([]byte("b: 2\n")); Value(Get(d3.Content[0], "b")) != "2" {
		t.Errorf("other bytes: got %+v", d3)
	}
	bad := []byte("a: [\n")
	if _, err := p.Document(bad); err == nil {
		t.Error("broken YAML: want the parse error")
	}
	if _, err := p.Document(bad); err == nil {
		t.Error("broken YAML, memoized: want the parse error again")
	}
	var nilP *Parsed
	if d, err := nilP.Document(a); err != nil || Value(Get(d.Content[0], "a")) != "1" {
		t.Errorf("nil Parsed: %+v, %v", d, err)
	}
	if d, err := p.Document(nil); err != nil || len(d.Content) != 0 {
		t.Errorf("empty: %+v, %v", d, err)
	}
}
