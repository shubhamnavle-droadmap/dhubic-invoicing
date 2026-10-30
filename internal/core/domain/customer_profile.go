package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
)

// Limits for the caller-owned customer profile fields.
const (
	MaxExternalIDLength  = 64
	MaxLegalNameLength   = 200
	MaxPaymentTermsLen   = 40
	MaxWebsiteLength     = 2048
	MaxNotesLength       = 5000
	MaxTags              = 20
	MaxTagLength         = 40
	MaxBillingEmails     = 10
	MaxMetadataKeys      = 50
	MaxMetadataKeyLength = 40
	MaxMetadataValueLen  = 500
	MaxMetadataBytes     = 8 * 1024
)

// ErrCustomerExternalIDTaken is returned when another customer in the same
// workspace already uses the external_id.
var ErrCustomerExternalIDTaken = errors.New("external_id is already used by another customer")

// CustomerFieldError reports a caller-fixable problem with a customer field.
type CustomerFieldError struct{ Message string }

func (e *CustomerFieldError) Error() string { return e.Message }

func fieldErr(format string, args ...any) error {
	return &CustomerFieldError{Message: fmt.Sprintf(format, args...)}
}

var localePattern = regexp.MustCompile(`^[A-Za-z]{2,3}([-_][A-Za-z0-9]{2,8})*$`)

// NormalizeProfile trims and validates the caller-owned profile fields in
// place. It does not touch metadata (see ValidateMetadata / MergeMetadata).
func (c *Customer) NormalizeProfile() error {
	if c.ExternalID != nil {
		v := strings.TrimSpace(*c.ExternalID)
		if v == "" {
			c.ExternalID = nil
		} else {
			if len(v) > MaxExternalIDLength {
				return fieldErr("external_id must be at most %d characters", MaxExternalIDLength)
			}
			c.ExternalID = &v
		}
	}

	c.LegalName = strings.TrimSpace(c.LegalName)
	if len(c.LegalName) > MaxLegalNameLength {
		return fieldErr("legal_name must be at most %d characters", MaxLegalNameLength)
	}

	c.Locale = strings.TrimSpace(c.Locale)
	if c.Locale != "" && (len(c.Locale) > 35 || !localePattern.MatchString(c.Locale)) {
		return fieldErr("locale must be a language code such as en or en-GB")
	}

	c.PaymentTerms = strings.TrimSpace(c.PaymentTerms)
	if len(c.PaymentTerms) > MaxPaymentTermsLen {
		return fieldErr("payment_terms must be at most %d characters", MaxPaymentTermsLen)
	}

	c.Website = strings.TrimSpace(c.Website)
	if len(c.Website) > MaxWebsiteLength {
		return fieldErr("website must be at most %d characters", MaxWebsiteLength)
	}

	if len(c.Notes) > MaxNotesLength {
		return fieldErr("notes must be at most %d characters", MaxNotesLength)
	}

	tags, err := normalizeTags(c.Tags)
	if err != nil {
		return err
	}
	c.Tags = tags

	emails, err := normalizeEmails(c.BillingEmails)
	if err != nil {
		return err
	}
	c.BillingEmails = emails

	if c.ShippingAddress != nil {
		if *c.ShippingAddress == (BillingAddress{}) {
			c.ShippingAddress = nil
		} else if n := len(c.ShippingAddress.Country); n != 0 && n != 2 {
			return fieldErr("shipping_address.country must be a 2-letter country code")
		}
	}
	return nil
}

func normalizeTags(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, t := range in {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if len(t) > MaxTagLength {
			return nil, fieldErr("each tag must be at most %d characters", MaxTagLength)
		}
		if seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	if len(out) > MaxTags {
		return nil, fieldErr("at most %d tags are allowed", MaxTags)
	}
	return out, nil
}

func normalizeEmails(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, e := range in {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		addr, err := mail.ParseAddress(e)
		if err != nil || addr.Address != e {
			return nil, fieldErr("billing_emails contains an invalid email address: %q", e)
		}
		key := strings.ToLower(e)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, e)
	}
	if len(out) > MaxBillingEmails {
		return nil, fieldErr("at most %d billing_emails are allowed", MaxBillingEmails)
	}
	return out, nil
}

// checkMetadataValue accepts the scalar types a JSON body can carry. Nested
// objects and arrays are rejected so metadata stays flat and queryable.
func checkMetadataValue(key string, v any) error {
	switch val := v.(type) {
	case string:
		if len(val) > MaxMetadataValueLen {
			return fieldErr("metadata[%q]: string values must be at most %d characters", key, MaxMetadataValueLen)
		}
	case bool, float64, float32, int, int32, int64, json.Number:
	default:
		return fieldErr("metadata[%q]: value must be a string, number or boolean", key)
	}
	return nil
}

func checkMetadataKey(key string) error {
	if strings.TrimSpace(key) == "" {
		return fieldErr("metadata keys must not be empty")
	}
	if len(key) > MaxMetadataKeyLength {
		return fieldErr("metadata key %q is longer than %d characters", key, MaxMetadataKeyLength)
	}
	return nil
}

// ValidateMetadata checks a complete metadata object (used on create).
// A null value is not meaningful here and is dropped by the caller.
func ValidateMetadata(m map[string]any) error {
	_, err := MergeMetadata(nil, m)
	return err
}

// MergeMetadata applies patch onto existing and returns the new object.
// A nil value in patch removes that key. existing is never modified.
func MergeMetadata(existing, patch map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(existing)+len(patch))
	for k, v := range existing {
		out[k] = v
	}
	for k, v := range patch {
		if err := checkMetadataKey(k); err != nil {
			return nil, err
		}
		if v == nil {
			delete(out, k)
			continue
		}
		if err := checkMetadataValue(k, v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	if len(out) > MaxMetadataKeys {
		return nil, fieldErr("metadata can hold at most %d keys", MaxMetadataKeys)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, fieldErr("metadata is not valid JSON")
	}
	if len(raw) > MaxMetadataBytes {
		return nil, fieldErr("metadata must be at most %d bytes", MaxMetadataBytes)
	}
	return out, nil
}
