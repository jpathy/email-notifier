package main

import (
	"strings"
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

func TestHeaderMods(t *testing.T) {
	data := `Return-Path: <test@example.com>
Received: (test program); Wed, 23 May 2042 13:37:69 +0020
DKIM-Signature: Wrong-Sig
From: test@example.com
To: contact@example.com
Subject: A test message
Date: Bad date1
DKIM-Signature: Wrong-Sig
X-Repeat: repeat-value1
Date: Wed, 23 May 2042 13:37:69 +0000
X-Repeat: repeat-value2
Message-ID: <0000000@localhost/>
X-Repeat: repeat-value3
Mime-Version: 1.0
Date: Bad date2
Content-Type: text/plain

Here is my message.
`
	trs := []HeaderMod{
		mailReplaceHeader("X-Repeat", "repeat-replaced2", 2),
		mailRemoveHeader("DKIM-Signature", 0),
		mailReplaceHeader("X-Repeat", "repeat-replaced2-2", 2),
		mailInsertHeader("X-Repeat", "repeat-inserted1", 11),
		mailReplaceHeader("X-Repeat", "repeat-replaced3", 3),
		mailRemoveHeader("Date", 1),
		mailInsertHeader("X-TEST-Insert", "insert-value1", 11),
		mailRemoveHeader("Date", -1),
		mailAddHeader("X-Test-Append", "append-value1"),
		mailAddHeader("X-Test-Append2", "append-value2"),
		mailInsertHeader("X-Test-Insert2", "insert-value2", -2),
		mailDeliverTo("realaddress@example.com"),
	}
	expectedData := `Return-Path: <>
Delivered-To: realaddress@example.com
Received: (test program); Wed, 23 May 2042 13:37:69 +0020
From: test@example.com
To: contact@example.com
Subject: A test message
X-Repeat: repeat-value1
Date: Wed, 23 May 2042 13:37:69 +0000
X-Repeat: repeat-replaced2-2
Message-ID: <0000000@localhost/>
X-Repeat: repeat-replaced3
X-Repeat: repeat-inserted1
X-Test-Insert: insert-value1
Mime-Version: 1.0
Content-Type: text/plain
X-Test-Insert2: insert-value2
X-Test-Append: append-value1
X-Test-Append2: append-value2

Here is my message.
`

	var mc *MailContent
	var err error
	if err = initRun(3); err != nil {
		t.Fatal("initRun failed:", err)
	}
	if mc, err = NewMailContent(strings.NewReader(data)); err != nil {
		t.Fatalf("Failed to read message, error: %s", err)
	}

	for _, tr := range trs {
		if err = tr.Transform(mc); err != nil {
			t.Fatalf("Failed to apply HeaderMod: %+v, error: %s", tr, err)
		}
	}

	var out strings.Builder
	if err = mc.writeTo(&out); err != nil {
		t.Errorf("Failed to write message, error: %s", err)
	}
	if o := out.String(); strings.Replace(expectedData, "\n", "\r\n", mc.hdr.Len()+1) != o {
		t.Log(o)
		t.Error("Test failure: expected data mismatch")
	}
}

func TestMessageBody1(t *testing.T) {
	data := `Return-Path: <>
Received: (test program); Wed, 23 May 2042 13:37:69 +0020
From: test@example.com
To: contact@example.com
Subject: A test message
Date: Wed, 23 May 2042 13:37:69 +0000
Message-ID: <0000000@localhost/>
Mime-Version: 1.0
Content-Type: text/plain
Content-Transfer-Encoding: base64

VGhpcyBpcyBhIHRlc3QgdGV4dCBtZXNzYWdlCg==
`

	var mc *MailContent
	var err error
	if err = initRun(3); err != nil {
		t.Fatal("initRun failed:", err)
	}
	if mc, err = NewMailContent(strings.NewReader(data)); err != nil {
		t.Fatalf("Failed to read message, error: %s", err)
	}

	var out strings.Builder
	if err = mc.writeTo(&out); err != nil {
		t.Errorf("Failed to write message, error: %s", err)
	}
	if o := out.String(); strings.Replace(data, "\n", "\r\n", mc.hdr.Len()+1) != o {
		t.Log(o)
		t.Error("Test failure: expected data mismatch")
	}
}
