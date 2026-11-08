package pages

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// The signed-in page has to carry the one thing a guarded route can still do:
// a sign-out form for the session whose cookie authenticated the request.
func TestAppSignedInCarriesTheLogoutForm(t *testing.T) {
	var out bytes.Buffer
	if err := AppSignedIn("Ada", "csrf-token-123").Render(context.Background(), &out); err != nil {
		t.Fatalf("render: %v", err)
	}
	page := out.String()
	for _, want := range []string{
		`action="/logout"`,
		`method="post"`,
		`name="csrf"`,
		`value="csrf-token-123"`,
		"Ada",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("signed-in page is missing %q", want)
		}
	}
}

func TestAppSignedInEscapesTheDisplayName(t *testing.T) {
	var out bytes.Buffer
	err := AppSignedIn("<script>alert(1)</script>", "t").Render(context.Background(), &out)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(out.String(), "<script>alert(1)</script>") {
		t.Fatal("the display name is not escaped")
	}
}
