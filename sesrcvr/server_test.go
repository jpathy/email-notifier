package main

import (
	"testing"
)

func TestMatchRecipients(t *testing.T) {
	tests := []struct {
		recipients []string
		patterns   []string
		result     bool
	}{
		{ // exact match.
			recipients: []string{"foo@example.com"},
			patterns:   []string{"bar@example.com", "foo@example.com"},
			result:     true,
		},
		{ // exact non-match.
			recipients: []string{"foo@example.com"},
			patterns:   []string{"bar@example.com"},
			result:     false,
		},
		{ // domain match.
			recipients: []string{"foo@example.com"},
			patterns:   []string{"bar@domain.com", "example.com"},
			result:     true,
		},
		{ // localpart match.
			recipients: []string{"baz@random.com"},
			patterns:   []string{"baz@"},
			result:     true,
		},
		{ // subdomain non-match.
			recipients: []string{"foo@example.com"},
			patterns:   []string{".example.com"},
			result:     false,
		},
		{ // subdomain match.
			recipients: []string{"foo@sub.example.com"},
			patterns:   []string{"bar@sub.example.com", ".example.com"},
			result:     true,
		},
		{ // diff subdomain non-match.
			recipients: []string{"foo@sub.example.com"},
			patterns:   []string{"diffsub.example.com"},
			result:     false,
		},
		{ // user match(with dquote)
			recipients: []string{"\"foo@bar+detail\"@example.com"},
			patterns:   []string{"foo@bar@example.com"},
			result:     true,
		},
		{ // user+detail match.
			recipients: []string{"foo+detail@example.com"},
			patterns:   []string{"foo+detail@example.com"},
			result:     true,
		},
		{ // user+detail non-match.
			recipients: []string{"foo@example.com"},
			patterns:   []string{"foo+detail@example.com"},
			result:     false,
		},
	}

	for _, tcase := range tests {
		data := DeliveryData{}
		data.Receipt.Recipients = tcase.recipients
		if r, err := data.MatchRecipients(tcase.patterns...); err != nil {
			t.Fatalf("Failed case:\n\tRecipients: %v\n\tPatterns:%v\n\tExpected: %v, Got: %v\n", tcase.recipients, tcase.patterns, tcase.result, err)
		} else if r != tcase.result {
			t.Errorf("Failed case:\n\tRecipients: %v\n\tPatterns:%v\n\tExpected: %v, Got: %v\n", tcase.recipients, tcase.patterns, tcase.result, r)
		}
	}
}
