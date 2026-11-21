package money

import (
	"encoding/json"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestParse(t *testing.T) {
	cases := map[string]Amount{
		"0":           0,
		"1":           1_000_000_000,
		"0.50":        500_000_000,
		"15.00":       15_000_000_000,
		"0.000001234": 1_234,
		"0.000000001": 1,
		"12.3":        12_300_000_000,
		"9.99":        9_990_000_000,
		// The largest representable amount: math.MaxInt64 nano-dollars.
		"9223372036.854775807": 9_223_372_036_854_775_807,
	}
	for in, want := range cases {
		got, err := Parse(in)
		if err != nil || got != want {
			t.Errorf("Parse(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
}

func TestParseRejects(t *testing.T) {
	for _, in := range []string{"", "-1", "+1", "1.", ".5", "0.0000000001", "1e3", "abc", "1.2.3", " 1", "99999999999", "9223372036.999999999", "9223372036.854775808"} {
		if got, err := Parse(in); err == nil || got != 0 {
			t.Errorf("Parse(%q) = %d, %v; want 0 and an error", in, got, err)
		}
	}
}

func TestString(t *testing.T) {
	cases := map[Amount]string{
		0:              "0",
		1:              "0.000000001",
		500_000_000:    "0.5",
		15_000_000_000: "15",
		1_234:          "0.000001234",
		-1_500_000_000: "-1.5",
	}
	for in, want := range cases {
		if got := in.String(); got != want {
			t.Errorf("Amount(%d).String() = %q; want %q", int64(in), got, want)
		}
	}
}

func TestParseCost(t *testing.T) {
	cases := map[string]Amount{
		"0.00123":        1_230_000,
		"1.2e-05":        12_000,
		"0.0000000014":   1,
		"0.0000000015":   2,
		"3":              3_000_000_000,
		"0":              0,
		"2.5E-3":         2_500_000,
		"0.000012345678": 12_346,
	}
	for in, want := range cases {
		got, err := ParseCost(json.Number(in))
		if err != nil || got != want {
			t.Errorf("ParseCost(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"-0.1", "abc", "", "1e30"} {
		if got, err := ParseCost(json.Number(in)); err == nil || got != 0 {
			t.Errorf("ParseCost(%q) = %d, %v; want 0 and an error", in, got, err)
		}
	}
}

func TestYAMLUsesLiteralText(t *testing.T) {
	var v struct {
		A Amount `yaml:"a"`
	}
	if err := yaml.Unmarshal([]byte("a: 0.10\n"), &v); err != nil || v.A != 100_000_000 {
		t.Fatalf("got %d, %v", v.A, err)
	}
	if err := yaml.Unmarshal([]byte("a: [1]\n"), &v); err == nil {
		t.Fatal("non-scalar accepted")
	}
	if err := yaml.Unmarshal([]byte("a: 0.1234567891\n"), &v); err == nil {
		t.Fatal("excess precision accepted")
	}
}

func TestJSONIsDecimalString(t *testing.T) {
	b, err := json.Marshal(struct{ C Amount }{C: 1_500_000_000})
	if err != nil || string(b) != `{"C":"1.5"}` {
		t.Fatalf("got %s, %v", b, err)
	}
}

func TestJSONRoundTrip(t *testing.T) {
	for _, a := range []Amount{0, 1, 1_500_000_000, -2_000_000_001} {
		b, _ := json.Marshal(a)
		var got Amount
		if err := json.Unmarshal(b, &got); err != nil || got != a {
			t.Errorf("%d -> %s -> %d, %v", int64(a), b, int64(got), err)
		}
	}
	var a Amount
	if err := json.Unmarshal([]byte(`"--1"`), &a); err == nil {
		t.Error("double sign accepted")
	}
}

func TestParsePerMillion(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"0.0000003", "0.3", false},
		{"0.00000027", "0.27", false},
		{"3e-7", "0.3", false},
		{"0.000075", "75", false},
		{"0", "0", false},
		{"1e-16", "0", false},
		{"-0.1", "", true},
		{"abc", "", true},
		{"", "", true},
	} {
		got, err := ParsePerMillion(tc.in)
		if (err != nil) != tc.wantErr || (err == nil && got.String() != tc.want) {
			t.Errorf("ParsePerMillion(%q) = %v, %v; want %q, error %v", tc.in, got, err, tc.want, tc.wantErr)
		}
	}
}
