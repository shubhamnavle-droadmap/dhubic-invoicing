package domain

import (
	"errors"
	"strings"
	"testing"
)

func isFieldErr(err error) bool {
	var fe *CustomerFieldError
	return errors.As(err, &fe)
}

func TestMergeMetadata_AddsOverwritesAndRemoves(t *testing.T) {
	existing := map[string]any{"tier": "gold", "renewal_month": float64(6)}
	got, err := MergeMetadata(existing, map[string]any{"tier": "silver", "region": "NA", "renewal_month": nil})
	if err != nil {
		t.Fatal(err)
	}
	if got["tier"] != "silver" || got["region"] != "NA" {
		t.Fatalf("merge result wrong: %v", got)
	}
	if _, ok := got["renewal_month"]; ok {
		t.Fatalf("null value must remove the key: %v", got)
	}
	if existing["tier"] != "gold" || len(existing) != 2 {
		t.Fatalf("existing map was modified: %v", existing)
	}
}

func TestMergeMetadata_Limits(t *testing.T) {
	long := strings.Repeat("x", MaxMetadataValueLen+1)
	manyKeys := map[string]any{}
	for i := 0; i <= MaxMetadataKeys; i++ {
		manyKeys[strings.Repeat("k", 5)+string(rune('a'+i%26))+string(rune('a'+i/26))] = "v"
	}
	cases := map[string]map[string]any{
		"value too long":  {"a": long},
		"key too long":    {strings.Repeat("k", MaxMetadataKeyLength+1): "v"},
		"empty key":       {"": "v"},
		"nested object":   {"a": map[string]any{"b": "c"}},
		"array":           {"a": []any{"b"}},
		"too many keys":   manyKeys,
		"total too large": bigMetadata(),
	}
	for name, patch := range cases {
		if _, err := MergeMetadata(nil, patch); !isFieldErr(err) {
			t.Errorf("%s: want a field error, got %v", name, err)
		}
	}
}

// bigMetadata fits the per-key and per-value limits but exceeds the 8 KB total.
func bigMetadata() map[string]any {
	m := map[string]any{}
	for i := 0; i < 30; i++ {
		m[strings.Repeat("a", 10)+string(rune('a'+i%26))+string(rune('a'+i/26))] = strings.Repeat("v", 400)
	}
	return m
}

func TestMergeMetadata_AcceptsScalars(t *testing.T) {
	_, err := MergeMetadata(nil, map[string]any{"s": "x", "n": float64(1.5), "i": 3, "b": true})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNormalizeProfile(t *testing.T) {
	blank := "   "
	c := &Customer{
		ExternalID:    &blank,
		LegalName:     "  Acme Inc.  ",
		Locale:        "en-GB",
		Tags:          []string{"vip", " vip ", "", "annual"},
		BillingEmails: []string{"a@x.com", "A@X.com", "b@x.com"},
	}
	if err := c.NormalizeProfile(); err != nil {
		t.Fatal(err)
	}
	if c.ExternalID != nil {
		t.Errorf("blank external_id must become nil, got %q", *c.ExternalID)
	}
	if c.LegalName != "Acme Inc." {
		t.Errorf("legal_name not trimmed: %q", c.LegalName)
	}
	if len(c.Tags) != 2 || c.Tags[0] != "vip" || c.Tags[1] != "annual" {
		t.Errorf("tags not cleaned: %v", c.Tags)
	}
	if len(c.BillingEmails) != 2 {
		t.Errorf("duplicate emails (case-insensitive) must collapse: %v", c.BillingEmails)
	}
}

func TestNormalizeProfile_Rejects(t *testing.T) {
	longID := strings.Repeat("x", MaxExternalIDLength+1)
	tooMany := make([]string, MaxTags+1)
	for i := range tooMany {
		tooMany[i] = "t" + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}
	cases := map[string]*Customer{
		"external_id too long": {ExternalID: &longID},
		"bad locale":           {Locale: "english please"},
		"bad email":            {BillingEmails: []string{"not-an-email"}},
		"named email":          {BillingEmails: []string{"Jane <j@x.com>"}},
		"tag too long":         {Tags: []string{strings.Repeat("t", MaxTagLength+1)}},
		"too many tags":        {Tags: tooMany},
		"bad shipping country": {ShippingAddress: &BillingAddress{Line1: "1 St", Country: "USA"}},
	}
	for name, c := range cases {
		if err := c.NormalizeProfile(); !isFieldErr(err) {
			t.Errorf("%s: want a field error, got %v", name, err)
		}
	}
}

func TestNormalizeProfile_EmptyShippingAddressClears(t *testing.T) {
	c := &Customer{ShippingAddress: &BillingAddress{}}
	if err := c.NormalizeProfile(); err != nil {
		t.Fatal(err)
	}
	if c.ShippingAddress != nil {
		t.Fatal("an all-empty shipping address must clear to nil")
	}
}
