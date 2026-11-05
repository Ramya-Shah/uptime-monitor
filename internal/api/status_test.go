package api

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// The template must escape site error text, since it comes from remote servers.
func TestStatusTemplateEscapes(t *testing.T) {
	var buf bytes.Buffer
	page := statusPage{Rows: []statusRow{{Name: "x", URL: "https://x.test", State: "down",
		FailAt: "now", FailErr: `<script>alert(1)</script>`}}, Down: 1}
	if err := statusTmpl.Execute(&buf, page); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "<script>alert") {
		t.Error("error text was not escaped")
	}
}

func TestFormatters(t *testing.T) {
	v := 99.951
	tests := []struct{ got, want string }{
		{pct(&v), "99.95%"},
		{pct(nil), "—"},
		{ms(&v), "100 ms"},
		{ago(42 * time.Second), "42s ago"},
		{ago(5 * time.Minute), "5m ago"},
		{ago(3 * time.Hour), "3h ago"},
		{ago(72 * time.Hour), "3d ago"},
	}
	for _, tc := range tests {
		if tc.got != tc.want {
			t.Errorf("got %q, want %q", tc.got, tc.want)
		}
	}
}
