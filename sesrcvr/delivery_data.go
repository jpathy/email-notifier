package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/mail"
	"net/textproto"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/emersion/go-message"
	msgproto "github.com/emersion/go-message/textproto"
)

type MessageMods interface {
	Transform(m *MailContent) error
}

func mailMessageMods(m ...MessageMods) []MessageMods {
	return m
}

type headerField struct {
	key   string
	value string
	raw   []byte
}

type MailContent struct {
	lhdrs  []headerField
	entity message.Entity
}

func NewMailContent(r io.Reader) (*MailContent, error) {
	e, err := message.Read(r)
	if e == nil {
		return nil, err
	}
	return &MailContent{
		entity: *e,
	}, err
}

// TODO: ``Content-Type''/``Content-Transfer-Encoding'' changes should affect body, currently these changes are not allowed.
func (m *MailContent) writeTo(w io.Writer) (err error) {
	for i := len(m.lhdrs) - 1; i >= 0; i-- {
		if v := m.lhdrs[i]; v.raw == nil {
			m.entity.Header.Add(v.key, v.value)
		} else {
			m.entity.Header.AddRaw(v.raw)
		}
	}

	// Clear m.lhdrs
	m.lhdrs = nil

	// If the message uses MIME, it has to include MIME-Version
	if !m.entity.Header.Header.Has("Mime-Version") {
		m.entity.Header.Header.Set("MIME-Version", "1.0")
	}

	// Set empty return-path address, since we don't handle bounces/DSN.
	m.entity.Header.Header.Set("Return-Path", "<>")

	if err = msgproto.WriteHeader(w, m.entity.Header.Header); err != nil {
		sLog.Errorw("Failed to write message header", "error", err)
		return err
	}

	_, err = io.Copy(w, m.entity.Body)
	return
}

type DeliveryData struct {
	ctx        context.Context
	mailReader io.ReadSeeker
	SESNotification
}

func (data *DeliveryData) MailPipeTo(mods []MessageMods, cmd string, args ...string) (out string, err error) {
	procCtx, cancel := context.WithCancel(data.ctx)
	defer cancel()

	msgId := data.SESNotification.Mail.MessageId
	c := exec.CommandContext(procCtx, cmd, args...)
	var w io.WriteCloser
	if w, err = c.StdinPipe(); err != nil {
		return
	}

	go func() {
		var gerr error
		defer func() {
			if gerr == nil {
				w.Close()
			} else {
				sLog.Errorw("Failed to pipe mail to external command, cancelling process", "messageId", msgId, "error", gerr, "command", c.String())
				cancel()
			}
			data.mailReader.Seek(0, io.SeekStart)
		}()

		data.mailReader.Seek(0, io.SeekStart)
		mc, gerr := NewMailContent(data.mailReader)
		if gerr != nil && !message.IsUnknownCharset(gerr) && !message.IsUnknownEncoding(gerr) {
			sLog.Errorw("Failed to parse mail, skipping header transforms", "messageId", msgId, "error", gerr)
			data.mailReader.Seek(0, io.SeekStart)
			_, gerr = io.Copy(w, data.mailReader)
			return
		}

		if gerr != nil {
			sLog.Warnf("Unknown character set/encoding for messageId: %s, error: %s", msgId, gerr)
			gerr = nil
		}

		origReturn := mc.entity.Header.Get("Return-Path")
		mc.entity.Header.Del("Return-Path")
		mc.entity.Header.AddRaw([]byte(
			fmt.Sprintf(
				"Received: (sesrcvr %d invoked by uid %d);\n %s -0000\r\n",
				os.Getpid(), os.Geteuid(),
				time.Now().UTC().Format("Mon, 02 Jan 2006 15:04:05")), //RFC 5322 date-time
		))
		if rcpts := data.SESNotification.Receipt.Recipients; len(rcpts) == 1 {
			mods = append(mods, mailInsertHeader("X-Original-To", rcpts[0], 0))
		}
		if len(origReturn) != 0 {
			mods = append(mods, mailInsertHeader("X-Original-Return-Path", origReturn, 0))
		}
		for _, e := range mods {
			if e == nil {
				continue
			}
			if gerr = e.Transform(mc); gerr != nil {
				return
			}
		}
		gerr = mc.writeTo(w)
	}()

	var ob []byte
	if ob, err = c.Output(); err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			err = fmt.Errorf("ExitError stdout: %s, error: %w", string(e.Stderr), e)
		}
		return
	}

	out = string(ob)
	return
}

// Allowed Patterns:
// domain.tld => matches all users for the specific domain(domain could be any nested sub domain).
// .domain.tld => matches all users on all nested subdomains for domain.
// user@domain.tld => matches the exact user for domain.tld.
// user@ => matches user for any domain.
// user can contain a detail(user+service) to match the exact detail, if not present it matches all details.
func (data *DeliveryData) MatchRecipients(patterns ...string) (bool, error) {
	// TODO:
	//  Q: are recipients' domains punycode-encoded? investigate when i have one.
	for _, r := range data.SESNotification.Receipt.Recipients {
		addr, err := mail.ParseAddress(r)
		var rcptLocalPart, rcptDomain string
		if err != nil {
			sLog.Errorw("Failed to parse recipient address", "recipient", r)
			continue
		} else if n := strings.LastIndexAny(addr.Address, "@"); n == -1 {
			sLog.Debugw("Invalid email address", "recipient", addr.Address)
			continue
		} else {
			rcptDomain = addr.Address[n+1:]
			rcptLocalPart = addr.Address[:n]
		}
		rcptUser := rcptLocalPart
		if n := strings.IndexAny(rcptLocalPart, "+"); n != -1 {
			rcptUser = rcptLocalPart[:n]
		}

		for _, pat := range patterns {
			if idx := strings.LastIndexAny(pat, "@"); idx != -1 { // pat = local@ | local@domain
				if idx == 0 {
					return false, fmt.Errorf("pattern: %s is invalid, localpart can't be empty", pat)
				}
				localPart := pat[:idx]
				domain := pat[idx+1:]
				if domain != "" && domain != rcptDomain { // different domain part
					continue
				}
				var b bool
				if strings.ContainsAny(localPart, "+") { // local = user+detail => exact match the localpart
					b = rcptLocalPart == localPart
				} else { // match the user of rcpt
					b = rcptUser == localPart
				}
				if b {
					return true, nil
				}
			} else if pat[0] == '.' { // pat = .domain => only subdomains allowed(pat is a suffix)
				if strings.HasSuffix(rcptDomain, pat) {
					return true, nil
				}
			} else if rcptDomain == pat { // pat = domain => exact match of domainpart
				return true, nil
			}
		}
	}

	return false, nil
}

const (
	invalidHeader int = iota
	removeHeader
	changeHeader
	addHeader
	insertHeader
)

type HeaderMod struct {
	op    int
	index int
	key   string
	value string
}

// Note: ``Content-Type'' / ``Content-Transfer-Encoding'' header changes are ignored.
func (h HeaderMod) Transform(mc *MailContent) (err error) {
	if h.key == textproto.CanonicalMIMEHeaderKey("Content-Type") || h.key == textproto.CanonicalMIMEHeaderKey("Content-Transfer-Encoding") {
		return fmt.Errorf("content type/encoding modifications are not allowed")
	}

	var curHdr headerField
	if h.op != removeHeader {
		if strings.ContainsAny(h.value, "\r\n") {
			sLog.Debugf("Assuming formatted header value for key: %q", h.key)
			raw := []byte(h.key + ": " + h.value)
			if !strings.HasSuffix(h.value, "\r\n") {
				raw = append(raw, []byte{'\r', '\n'}...)
			}
			curHdr = headerField{key: h.key, raw: raw}
		} else {
			curHdr = headerField{key: h.key, value: h.value}
		}
	}
switchL:
	switch h.op {
	case removeHeader:
		if h.index == 0 { // Delete all fields with key == h.key
			i := 0
			for _, e := range mc.lhdrs {
				if e.key != h.key {
					mc.lhdrs[i] = e
					i++
				}
			}
			mc.lhdrs = mc.lhdrs[:i]
			mc.entity.Header.Del(h.key)
			break
		}

		kfs := mc.entity.Header.FieldsByKey(h.key)
		var ps []int
		for i, e := range mc.lhdrs {
			if e.key == h.key {
				ps = append(ps, i)
			}
		}
		tlen := len(ps) + kfs.Len()
		var ri int
		if h.index > 0 {
			ri = h.index
		} else {
			ri = tlen + h.index + 1
		}
		if ri < 1 || ri > tlen {
			sLog.Errorw("RemoveHeader index out of bound", "key", h.key, "index", h.index)
			break
		}
		if n := len(ps); ri <= n {
			idx := ps[ri-1]
			mc.lhdrs = append(mc.lhdrs[0:idx], mc.lhdrs[idx+1:]...)
		} else {
			for i := n + 1; kfs.Next(); i++ {
				if ri == i {
					kfs.Del()
					break
				}
			}
		}
	case changeHeader:
		if h.index <= 0 {
			return fmt.Errorf("invalid index: %d for replace header with key: %q, index must be positive integer", h.index, h.key)
		}
		cur, ci := 0, h.index-1
		for i, v := range mc.lhdrs {
			if h.key == v.key {
				if cur == ci {
					mc.lhdrs[i] = curHdr
					break switchL
				}
				cur++
			}
		}

		if mc.entity.Header.Has(h.key) {
			for fs := mc.entity.Header.Fields(); fs.Next(); {
				k := fs.Key()
				if k == h.key {
					if cur == ci {
						fs.Del()
						mc.lhdrs = append(mc.lhdrs, curHdr)
						break
					}
					cur++
				}
				var b []byte
				if b, err = fs.Raw(); err != nil {
					return
				}
				mc.lhdrs = append(mc.lhdrs, headerField{key: k, raw: b})
				fs.Del()
			}
			break
		}
		sLog.Infow("ChangeHeader: missing key, doing AddHeader instead", "index", ci, "key", h.key)
		fallthrough
	case addHeader:
		// append ~ insert at totallen() position(0-based) at this order.
		h.index = len(mc.lhdrs) + mc.entity.Header.Len()
		fallthrough
	case insertHeader:
		nb := len(mc.lhdrs) + mc.entity.Header.Len()
		if h.index < 0 {
			h.index += nb
		}
		if h.index < 0 || h.index > nb {
			h.index = nb
		}
		if h.index >= len(mc.lhdrs) {
			for i, fs := len(mc.lhdrs), mc.entity.Header.Fields(); i < h.index && fs.Next(); i++ {
				var b []byte
				if b, err = fs.Raw(); err != nil {
					return
				}
				mc.lhdrs = append(mc.lhdrs, headerField{key: fs.Key(), raw: b})
				fs.Del()
			}
			mc.lhdrs = append(mc.lhdrs, curHdr)
			break
		}
		mc.lhdrs = append(mc.lhdrs[:h.index+1], mc.lhdrs[h.index:]...)
		mc.lhdrs[h.index] = curHdr
	default:
		return fmt.Errorf("invalid header operation")
	}

	return
}

// mailAddHeader appends header ``key'' with ``value'' at the end of header list.
func mailAddHeader(key string, value string) HeaderMod {
	return HeaderMod{
		op:    addHeader,
		key:   textproto.CanonicalMIMEHeaderKey(key),
		value: value,
	}
}

// Insert ``Index''th (key, value) header field(0-based indexing).
// [0] = "Received" header added by us(after removing the return-path).
func mailInsertHeader(key string, value string, idx int) HeaderMod {
	return HeaderMod{
		op:    insertHeader,
		key:   textproto.CanonicalMIMEHeaderKey(key),
		value: value,
		index: idx,
	}
}

// Remove ``idx''th value for header name ``Key''(1-based indexing)(0 => remove all, negative => from the end).
func mailRemoveHeader(key string, idx int) HeaderMod {
	return HeaderMod{
		op:    removeHeader,
		key:   textproto.CanonicalMIMEHeaderKey(key),
		index: idx,
	}
}

// Replace ``idx''th value with ``value'' for key ``key'', idx = positive integer(1-based indexing), negative/0 idx is ignored.
func mailReplaceHeader(key string, value string, idx int) HeaderMod {
	return HeaderMod{op: changeHeader, key: textproto.CanonicalMIMEHeaderKey(key), value: value, index: idx}
}

func mailDeliverTo(address string) HeaderMod {
	return mailInsertHeader("Delivered-To", address, 0)
}

type RspamdMetricSymbol struct {
	Name        string   `json:"name"`
	Score       float64  `json:"score"`
	MetricScore float64  `json:"metric_score"`
	Description string   `json:"description"`
	Options     []string `json:"options"`
}

type RspamdMetricGroup struct {
	Score       float64 `json:"score"`
	Description string  `json:"description"`
}

const (
	SPAM_HEADER = "X-Spam"

	METRIC_ACTION_ADD_HEADER = "add header"
	METRIC_ACTION_REJECT     = "reject"
)

type RspamdScanResult struct {
	Error error `json:"-"`

	IsSkipped     bool    `json:"is_skipped"`
	Score         float64 `json:"score"`
	RequiredScore float64 `json:"required_score"`
	Action        string  `json:"action"`

	ScanTimeSecs float64 `json:"time_real"`

	Symbols map[string]RspamdMetricSymbol `json:"symbols"`
	Groups  map[string]RspamdMetricGroup  `json:"groups"`

	Milter json.RawMessage `json:"milter"`
}

func (r *RspamdScanResult) Transform(mc *MailContent) error {
	if r.Error != nil { // invalid scan result doesn't do anything.
		sLog.Warnw("Skipping transform for invalid RspamdScanResult", "error", r.Error)
		return nil
	}

	var m struct {
		RmHdr   map[string]interface{} `json:"remove_headers"`
		AddHdr  map[string]interface{} `json:"add_headers"`
		SpamHdr interface{}            `json:"spam_header"`
	}
	dec := json.NewDecoder(bytes.NewReader(r.Milter))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		sLog.Warnw("Failed to decode milter headers from scan result", "error", err)
		return nil
	}

	// First apply remove operation.
	for k, v := range m.RmHdr {
		processHdr := func(n json.Number) error {
			if idx, err := n.Int64(); err != nil {
				h := mailRemoveHeader(k, int(idx))
				return h.Transform(mc)
			}
			return nil
		}
		switch vt := v.(type) {
		case json.Number:
			if err := processHdr(vt); err != nil {
				return err
			}
		case []interface{}:
			for _, e := range vt {
				if n, ok := e.(json.Number); ok {
					if err := processHdr(n); err != nil {
						return err
					}
				}
			}
		}
	}

	// Apply all add/insert operation.
	for k, v := range m.AddHdr {
		processHdr := func(e interface{}) error {
			var val string
			var idx int64
			var hasVal, hasIdx bool
			if m, ok := e.(map[string]interface{}); ok {
				if mv, ok := m["value"]; ok {
					val, hasVal = mv.(string)
					if n, ok := m["order"].(json.Number); ok {
						var nerr error
						idx, nerr = n.Int64()
						hasIdx = nerr == nil
					}
				}
			} else {
				val, hasVal = e.(string)
			}
			if hasVal {
				var h HeaderMod
				if hasIdx {
					h = mailInsertHeader(k, val, int(idx))
				} else {
					h = mailAddHeader(k, val)
				}
				return h.Transform(mc)
			}
			return nil
		}
		switch vt := v.(type) {
		case []interface{}:
			for _, e := range vt {
				if err := processHdr(e); err != nil {
					return err
				}
			}
		case interface{}:
			if err := processHdr(vt); err != nil {
				return err
			}
		}
	}

	// Consider it as spam, we never discard any messages.
	if r.Action == METRIC_ACTION_ADD_HEADER || r.Action == METRIC_ACTION_REJECT {
		val := "Yes"
		if s, ok := m.SpamHdr.(string); ok { // TODO: handle json object as well.
			val = s
		}
		for _, h := range []HeaderMod{mailRemoveHeader(SPAM_HEADER, 0), mailAddHeader(SPAM_HEADER, val)} {
			if err := h.Transform(mc); err != nil {
				return err
			}
		}
	}

	return nil
}

func rspamdHttpClient(addr string) (*http.Client, error) {
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	var dc func(context.Context, string, string) (net.Conn, error)
	if addr[0] == '/' {
		dc = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", addr)
		}
	} else if _, _, err := net.SplitHostPort(addr); err == nil {
		dc = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "tcp", addr)
		}
	} else {
		return nil, err
	}
	return &http.Client{
		Transport: &http.Transport{
			DialContext:           dc,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
	}, nil
}

// ScanWithRspamd scans the email message with rspamd, returns error only for invalid arguments, scan errors are returned in RspamdScanResult.Error.
// ``address'' is interpreted as unix socket path if it starts with a '/'(has to absoluted path) or tcp socket given as ``host:port".
// `kvs'' are extra http headers that can be passed to rspamd: https://rspamd.com/doc/architecture/protocol.html#http-headers.
// The following headers are passed in request by default(can be overridden):
//   Flags: milter,groups
//   From:  data.SESNotification.Mail.Source
//   Rcpt:  data.SESNotification.Receipt.Recipients
//   Subject: data.SESNotification.Mail.CommonHeaders.Subject
//   Queue-Id: data.SESNotification.Mail.MessageId
func (data *DeliveryData) ScanWithRspamd(address string, kvs ...string) (*RspamdScanResult, error) {
	hdrs := make(http.Header)
	for i := 0; i < len(kvs); i += 2 {
		if i == len(kvs)-1 {
			return nil, fmt.Errorf("missing header value for key: %q", kvs[i])
		}
		hdrs.Add(kvs[i], kvs[i+1])
	}

	host := address
	if address[0] == '/' {
		host = "localhost"
	}
	httpClient, err := rspamdHttpClient(address)
	if err != nil || httpClient == nil {
		return nil, err
	}

	data.mailReader.Seek(0, io.SeekStart)
	// data.mailReader mustn't get closed, hence the use of NopCloser
	req, err := http.NewRequestWithContext(data.ctx, http.MethodPost, fmt.Sprintf("http://%s/checkv2", host), io.NopCloser(data.mailReader))
	if err != nil {
		return nil, err
	}

	if len(hdrs.Values("From")) == 0 {
		hdrs.Set("From", data.SESNotification.Mail.Source)
	}
	if len(hdrs.Values("Subject")) == 0 {
		hdrs.Set("Subject", data.SESNotification.Mail.CommonHeaders.Subject)
	}
	if len(hdrs.Values("Rcpt")) == 0 {
		for _, r := range data.SESNotification.Receipt.Recipients {
			hdrs.Add("Rcpt", r)
		}
	}
	if len(hdrs.Values("Queue-Id")) == 0 {
		hdrs.Set("Queue-Id", data.SESNotification.Mail.MessageId)
	}
	if len(hdrs.Values("Flags")) == 0 {
		hdrs.Set("Flags", "milter,groups")
	}

	// Set the headers.
	req.Header = hdrs
	resp, err := httpClient.Do(req)
	if err != nil {
		return &RspamdScanResult{Error: err}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return &RspamdScanResult{Error: fmt.Errorf("rspamd scan failed, response status: %s", resp.Status)}, nil
	}

	dec := json.NewDecoder(resp.Body)
	var scanRes RspamdScanResult
	if err = dec.Decode(&scanRes); err != nil {
		scanRes.Error = err
		return &scanRes, nil
	}

	scanRes.Error = nil
	return &scanRes, nil
}
