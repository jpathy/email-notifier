//go:build sqlite_json1 && sqlite_foreign_keys
// +build sqlite_json1,sqlite_foreign_keys

package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"math"
	"math/rand"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/aws/aws-sdk-go/aws/arn"
	"github.com/aws/aws-sdk-go/aws/awsutil"
	"github.com/aws/aws-sdk-go/aws/endpoints"
	"github.com/aws/aws-sdk-go/service/s3"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/google/uuid"
	"github.com/hashicorp/go-retryablehttp"
	lru "github.com/hashicorp/golang-lru"
	"github.com/mattn/go-sqlite3"
	"go.uber.org/atomic"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"golang.org/x/sync/semaphore"

	"github.com/jpathy/email-notifier/sesrcvr/ctrlrpc"
)

// Sqlite statements used by the program.
const (
	memTempStoreSQL = `PRAGMA temp_store = 2`
	recTriggersSQL  = `PRAGMA recursive_triggers = true`

	createMsgLogTSQL = `BEGIN IMMEDIATE TRANSACTION;
CREATE TABLE IF NOT EXISTS msglog(
id TEXT UNIQUE NOT NULL, message TEXT, tomb INTEGER DEFAULT 0,
topicArn TEXT GENERATED ALWAYS AS (iif(json_valid(message), json_extract(message, '$.receipt.action.topicArn'), NULL)),
bucketName TEXT GENERATED ALWAYS AS (iif(json_valid(message), json_extract(message, '$.receipt.action.bucketName'), NULL)),
objectKey TEXT GENERATED ALWAYS AS (iif(json_valid(message), json_extract(message, '$.receipt.action.objectKey'), NULL))
);
CREATE INDEX IF NOT EXISTS msgTopicArn ON msglog(topicArn);
END TRANSACTION`
	appendLogSQL        = "INSERT OR IGNORE INTO msglog(id, message) VALUES(?, ?) RETURNING rowid"
	queryLogSQL         = "SELECT id, message, tomb FROM msglog WHERE rowid = ?"
	retireLogSQL        = "UPDATE msglog SET tomb = 1 WHERE rowid = ?"
	pendingLogsSQL      = "SELECT rowid FROM msglog WHERE tomb = 0"
	garbageCountSQL     = "SELECT count(*) FROM msglog WHERE tomb = 1"
	clearGarbageLogsSQL = "DELETE FROM msglog WHERE rowid IN (SELECT rowid FROM temp.gcready)"

	createTempGCTSQL    = "CREATE TEMP TABLE gcready(rowid INTEGER PRIMARY KEY, topicArn TEXT, bucketName TEXT, objectKey TEXT, msgid TEXT)"
	deleteTempGCTSQL    = "DROP TABLE IF EXISTS temp.gcready"
	queryGarbageLogsSQL = "INSERT INTO temp.gcready(rowid, topicArn, bucketName, objectKey, msgid) SELECT rowid, topicArn, bucketName, objectKey, id FROM msglog WHERE tomb = 1 LIMIT ? RETURNING topicArn, bucketName, objectKey, msgid"

	createErrLogTSQL = `BEGIN IMMEDIATE TRANSACTION;
CREATE TABLE IF NOT EXISTS errors(msglogid TEXT UNIQUE NOT NULL, error TEXT, FOREIGN KEY(msglogid) REFERENCES msglog(id) ON DELETE CASCADE);
CREATE TRIGGER IF NOT EXISTS rmErrorOnSuccess
AFTER UPDATE OF tomb ON msglog WHEN new.tomb = 1
BEGIN DELETE FROM errors WHERE msglogid = old.id; END;
END TRANSACTION`
	recordExecErrorSQL = `INSERT OR REPLACE INTO errors(msglogid, error) VALUES(?, ?)`
	queryErrorsSQL     = `SELECT msglog.id, errors.error FROM errors, msglog WHERE msglog.topicArn IS nullif(?, '') AND errors.msglogid = msglog.id`

	createConfigTSQL = `BEGIN IMMEDIATE TRANSACTION;
CREATE TABLE IF NOT EXISTS config(key TEXT PRIMARY KEY, value TEXT) WITHOUT ROWID;
CREATE TRIGGER IF NOT EXISTS configRoUrlDel
BEFORE DELETE ON CONFIG WHEN old.key = 'externUrl' AND nullif(trim(old.value), '') IS NOT NULL AND EXISTS (SELECT topicArn FROM subscriptions)
BEGIN SELECT raise(ABORT, "can't change externUrl while subscriptions exist, this would require resubscribing with the updated url for all subscriptions"); END;
CREATE TRIGGER IF NOT EXISTS configRoUrlUpd
BEFORE UPDATE OF value ON CONFIG WHEN old.key = 'externUrl' AND nullif(trim(old.value), '') IS NOT NULL AND EXISTS (SELECT topicArn FROM subscriptions)
BEGIN SELECT raise(ABORT, "can't change externUrl while subscriptions exist, this would require resubscribing with the updated url for all subscriptions"); END;
END TRANSACTION`
	setConfigSQL   = "INSERT INTO config(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value WHERE value != excluded.value RETURNING value"
	queryConfigSQL = "SELECT trim(value) FROM config where key = ?"

	createSubTSQL = "CREATE TABLE IF NOT EXISTS subscriptions(topicArn TEXT PRIMARY KEY, auth TEXT DEFAULT (uuid_random()), awsCred TEXT, deliveryTemplate TEXT, subArn TEXT DEFAULT NULL) WITHOUT ROWID"
	addSubSQL     = `INSERT INTO subscriptions(topicArn, awsCred, deliveryTemplate) VALUES(?1, ?2, ?3) ON CONFLICT(topicArn) DO UPDATE SET
awsCred = coalesce(nullif(excluded.awsCred, ''), awsCred), deliveryTemplate = coalesce(nullif(excluded.deliveryTemplate, ''), deliveryTemplate) RETURNING auth, awsCred, deliveryTemplate, nullif(trim(subArn), '')`
	getSubsSQL              = "SELECT topicArn, awsCred, deliveryTemplate, nullif(trim(subArn), '') FROM subscriptions"
	querySubAuthSQL         = "SELECT auth FROM subscriptions WHERE topicArn = ?"
	confirmSubSQL           = "UPDATE subscriptions SET subArn = ? WHERE topicArn = ?"
	removeSubSQL            = "DELETE FROM subscriptions WHERE topicArn = ?"
	removeUnConfirmedSubSQL = "DELETE FROM subscriptions WHERE topicArn = ? AND (subArn IS NULL OR trim(subArn) = '')"
)

// Globals & consts used by the server.
const (
	noColorEnv = "NOCOLORLOG"
	noTSEnv    = "NOTIMESTAMPLOG"
)

var (
	sLog *zap.SugaredLogger

	snsKeyToFieldIdx = make(map[string]int)
	signingCertCache *lru.Cache

	retryHttp *http.Client

	ErrInstanceDirty = fmt.Errorf("SNSEndpoint is already running")
)

// Wrapper logger type for retryablehttp client

type lvlLog struct {
	*zap.SugaredLogger
}

func (l lvlLog) Error(msg string, keysAndValues ...interface{}) {
	l.SugaredLogger.Errorw(msg, keysAndValues...)
}
func (l lvlLog) Info(msg string, keysAndValues ...interface{}) {
	l.SugaredLogger.Infow(msg, keysAndValues...)
}
func (l lvlLog) Debug(msg string, keysAndValues ...interface{}) {
	l.SugaredLogger.Debugw(msg, keysAndValues...)
}
func (l lvlLog) Warn(msg string, keysAndValues ...interface{}) {
	l.SugaredLogger.Warnw(msg, keysAndValues...)
}

// Called by run command action.
func initRun(verbosity int) (err error) {
	// Setup logger.
	logLevel := zapcore.ErrorLevel - zapcore.Level(verbosity)
	if logLevel < zapcore.DebugLevel {
		logLevel = zap.DebugLevel
	}
	logConf := zap.NewDevelopmentConfig()
	logConf.Level.SetLevel(logLevel)

	isDefined := func(key string) bool {
		if s := os.Getenv(key); s != "" {
			var n int
			if n, err = strconv.Atoi(s); err == nil && n == 1 {
				return true
			}
		}
		return false
	}

	if !isDefined(noColorEnv) {
		logConf.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	}
	if isDefined(noTSEnv) {
		logConf.EncoderConfig.TimeKey = ""
	}
	var l *zap.Logger
	if l, err = logConf.Build(); err != nil {
		return fmt.Errorf("zap logger failure: %w", err)
	} else {
		zap.RedirectStdLog(l)
		zap.ReplaceGlobals(l)
		sLog = zap.S()
	}
	sLog.Info("Log level set to ", logLevel)

	if signingCertCache, err = lru.New(64); err != nil {
		return
	}

	snsType := reflect.TypeOf(snsMessage{})
	for i := 0; i < snsType.NumField(); i++ {
		f := snsType.Field(i)
		jsonKey := strings.Split(f.Tag.Get("json"), ",")[0]
		if jsonKey == "" || jsonKey == "-" {
			return fmt.Errorf("bug: %s.%s must have a valid json tag with key name", snsType.Name(), f.Name)
		} else if k := f.Type.Kind(); !(k == reflect.String || (k == reflect.Ptr && f.Type.Elem().Kind() == reflect.String)) {
			return fmt.Errorf("bug: invalid type for %s.%s: %s, must be of (pointer to)type string", snsType.Name(), f.Name, f.Type.String())
		}
		snsKeyToFieldIdx[jsonKey] = i
	}

	rc := retryablehttp.NewClient()
	rc.Logger = lvlLog{sLog}
	rc.RetryWaitMin, rc.RetryWaitMax, rc.RetryMax = httpOutgoingDelayMin, httpOutgoingDelayMax, httpOutgoingMaxRetry
	retryHttp = rc.StandardClient()

	return nil
}

// Makes a retryable http GET request, returns the response Body bytes on success.
func getResponse(ctx context.Context, urlStr string) (r []byte, err error) {
	var req *http.Request
	var resp *http.Response
	if req, err = http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil); err != nil {
		return
	}

	jitter := func() time.Duration {
		return retryablehttp.LinearJitterBackoff(httpOutgoingDelayMin, httpOutgoingDelayMax, 0, resp)
	}
	jitterTm := time.NewTimer(0)
	if !jitterTm.Stop() {
		<-jitterTm.C
	}
	defer jitterTm.Stop()
Loop:
	for {
		if resp, err = retryHttp.Do(req); err != nil {
			return
		} else if resp.StatusCode != http.StatusOK {
			err = fmt.Errorf("unexpected response status: %s, from: %q", resp.Status, resp.Request.URL)
			resp.Body.Close()
			return
		}

		r, err = ioutil.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			jitterTm.Reset(jitter()) // set a delay timer
			select {
			case <-ctx.Done():
				err = ctx.Err()
			case <-jitterTm.C: // retry if timer expired
				continue Loop
			}
		}
		return
	}
}

type SESNotification struct {
	Type    string `json:"notificationType"`
	Receipt struct {
		Action struct {
			Type     string `json:"type"`
			TopicARN string `json:"topicArn"`

			BucketName *string `json:"bucketName"`
			ObjectKey  *string `json:"objectKey"`
		} `json:"action"`

		DKIMVerdict  SESMailVerdict `json:"dkimVerdict"`
		DMARCVerdict SESMailVerdict `json:"dmarcVerdict"`
		SPFVerdict   SESMailVerdict `json:"spfVerdict"`
		SpamVerdict  SESMailVerdict `json:"spamVerdict"`
		VirusVerdict SESMailVerdict `json:"virusVerdict"`

		Recipients []string `json:"recipients"`
		Timestamp  string   `json:"timestamp"`
	} `json:"receipt"`
	Mail struct {
		Timestamp   string   `json:"timestamp"`
		MessageId   string   `json:"messageId"`
		Source      string   `json:"source"`
		Destination []string `json:"destination"`

		CommonHeaders struct {
			ReturnPath string   `json:"returnPath"`
			From       []string `json:"from"`
			To         []string `json:"to"`
			Subject    string   `json:"subject"`

			MessageId string `json:"messageId"`
			Date      string `json:"date"`
		} `json:"commonHeaders"`
		Headers []SESMailHeader `json:"headers"`
	} `json:"mail"`
}

type SESMailHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type SESMailVerdict struct {
	Status string `json:"status"`
}

const (
	subConfirmType   = "SubscriptionConfirmation"
	notificationType = "Notification"
	unsubConfirmType = "UnsubscribeConfirmation"
)

type snsMessage struct {
	Type             string `json:"Type"`
	MessageID        string `json:"MessageId"`
	Token            string `json:"Token"`
	TopicARN         string `json:"TopicArn"`
	Timestamp        string `json:"Timestamp"`
	Message          string `json:"Message"`
	SignatureVersion string `json:"SignatureVersion"`
	Signature        string `json:"Signature"`
	SigningCertURL   string `json:"SigningCertURL"`

	Subject      *string `json:"Subject"`
	SubscribeURL *string `json:"SubscribeURL"`
}

type snsValidationError struct {
	error   string
	message *snsMessage
}

func (err *snsValidationError) Error() string {
	return fmt.Sprintf("%s, message: %s", err.error, awsutil.Prettify(err.message))
}

func (m *snsMessage) UnmarshalJSON(data []byte) error {
	type snsMsg snsMessage
	if err := json.Unmarshal(data, (*snsMsg)(m)); err != nil {
		return err
	}

	var errStr string
	switch m.Type {
	case subConfirmType:
		fallthrough
	case unsubConfirmType:
		if m.SubscribeURL == nil {
			errStr = fmt.Sprintf(`Missing "SubscribeURL" key in %s message`, m.Type)
		}
	case notificationType:
		if m.SubscribeURL != nil {
			errStr = fmt.Sprintf(`Unexpected "SubscribeURL" key in %s message`, m.Type)
		}
	default:
		errStr = fmt.Sprintf("%s is not a valid SNS message type", m.Type)
	}

	if len(errStr) != 0 {
		tmp := new(snsMessage)
		*tmp = *m
		*m = snsMessage{}
		return &snsValidationError{
			error:   errStr,
			message: tmp,
		}
	}

	return nil
}

const snsSignatureV1 = "1"

func (m *snsMessage) checkSignature(ctx context.Context) (err error) {
	if m.SignatureVersion != snsSignatureV1 {
		err = fmt.Errorf("signature version: %s is not supported", m.SignatureVersion)
		return
	}

	var mSig []byte
	if mSig, err = base64.StdEncoding.DecodeString(m.Signature); err != nil {
		return
	}

	var ordMsgKeys []string
	if m.Type == notificationType {
		ordMsgKeys = []string{"Message", "MessageId", "Subject", "Timestamp", "TopicArn", "Type"}
	} else {
		ordMsgKeys = []string{"Message", "MessageId", "SubscribeURL", "Timestamp", "Token", "TopicArn", "Type"}
	}

	var data bytes.Buffer
	rv := reflect.Indirect(reflect.ValueOf(m))
	for i := 0; i < len(ordMsgKeys); i++ {
		k := ordMsgKeys[i]
		v := rv.Field(snsKeyToFieldIdx[k])
		if v.Kind() != reflect.Ptr || !v.IsNil() {
			data.WriteString(k + "\n")
			data.WriteString(reflect.Indirect(v).String() + "\n")
		}
	}

	var x509Cert *x509.Certificate
	var hit bool
	if x509Cert, hit, err = fetchCertificate(ctx, m.SigningCertURL, false); err != nil {
		return
	}

	// If verification failed and cert was retrieved from cache, refresh the cert from the url and recheck.
	if err = x509Cert.CheckSignature(x509.SHA1WithRSA, data.Bytes(), mSig); err != nil && hit {
		sLog.Warnf("signature check failure for cached cert: %s, Issuer: %v, re-downloading cert", hex.EncodeToString(x509Cert.SerialNumber.Bytes()), x509Cert.Issuer)
		if x509Cert, _, err = fetchCertificate(ctx, m.SigningCertURL, true); err != nil {
			return
		}
		err = x509Cert.CheckSignature(x509.SHA1WithRSA, data.Bytes(), mSig)
	}

	return
}

// Helpers for signature validation

// Reference: https://github.com/aws/aws-sdk-java/blob/c75d9d19d15762cd9749526574bc3e5b93c16012/aws-java-sdk-sns/src/main/java/com/amazonaws/services/sns/message/SnsMessageManager.java#L119
func resolveCommonName(hostName string) string {
	region := strings.Split(hostName, ".")[1]

	switch region {
	case endpoints.CnNorth1RegionID:
		return "sns-cn-north-1.amazonaws.com.cn"
	case endpoints.CnNorthwest1RegionID:
		return "sns-cn-northwest-1.amazonaws.com.cn"
	case endpoints.UsGovWest1RegionID:
	case endpoints.UsGovEast1RegionID:
		return "sns-us-gov-west-1.amazonaws.com"
	case endpoints.ApEast1RegionID:
	case endpoints.MeSouth1RegionID:
	case endpoints.EuSouth1RegionID:
	case endpoints.AfSouth1RegionID:
		return "sns-signing." + region + ".amazonaws.com"
	default:
		return "sns.amazonaws.com"
	}
	return ""
}

func aiaVerify(ctx context.Context, commonName string, certs []*x509.Certificate) (x509Cert *x509.Certificate, err error) {
	if len(certs) == 0 {
		return nil, fmt.Errorf("couldn't retrieve pem certificate")
	}

	x509Cert = certs[0]

	iCertPool := x509.NewCertPool()
	for _, c := range certs[1:] {
		iCertPool.AddCert(c)
	}

	cert := certs[len(certs)-1]
	_, err = x509Cert.Verify(x509.VerifyOptions{DNSName: commonName, Intermediates: iCertPool})
Loop:
	for err != nil && len(cert.IssuingCertificateURL[0]) != 0 {
		var rb []byte
		if rb, err = getResponse(ctx, cert.IssuingCertificateURL[0]); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				sLog.Warnw("AIA cert fetch timeout", "url", cert.IssuingCertificateURL[0])
			}
			break Loop
		}
		if cert, err = x509.ParseCertificate(rb); err != nil {
			break
		}
		iCertPool.AddCert(cert)
		_, err = x509Cert.Verify(x509.VerifyOptions{DNSName: commonName, Intermediates: iCertPool})
	}

	return
}

var snsHostRegex = regexp.MustCompile(`^sns\.[a-zA-Z0-9\-]{3,}\.amazonaws\.com(\.cn)?$`)

func fetchCertificate(ctx context.Context, urlString string, force bool) (x509Cert *x509.Certificate, cacheHit bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, certFetchTimeout)
	defer cancel()

	var certUrl *url.URL
	certUrl, err = url.Parse(urlString)
	if err != nil {
		return
	}
	certEndpoint := certUrl.Hostname()
	if certUrl.Scheme != "https" || !snsHostRegex.MatchString(certEndpoint) {
		err = fmt.Errorf("forbidden SigningURL: %q", urlString)
		return
	}

	if force {
		signingCertCache.Remove(urlString)
	}

	if cert, ok := signingCertCache.Get(urlString); ok {
		x509Cert = cert.(*x509.Certificate)
		cacheHit = true
		return
	}
	// uncached -> fetch and verify the signing cert.
	certCN := resolveCommonName(certEndpoint)
	var bs []byte
	if bs, err = getResponse(ctx, urlString); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			sLog.Warnw("SNS: signing cert download timeout", "certUrl", urlString)
		}
		return
	}

	certs := []*x509.Certificate{}
	var pb *pem.Block
	for len(bs) != 0 {
		if pb, bs = pem.Decode(bs); pb == nil {
			err = fmt.Errorf("pem certificate decode failed, download url: %q", urlString)
			break
		}
		var cert *x509.Certificate
		if cert, err = x509.ParseCertificate(pb.Bytes); err != nil {
			err = fmt.Errorf("x509 parse failure at pem chain index: %d with error: %w", len(certs), err)
			break
		}
		certs = append(certs, cert)
	}
	if err != nil {
		return
	}

	// TODO: OCSP status? wait for std to do it.
	if x509Cert, err = aiaVerify(ctx, certCN, certs); err != nil {
		if err != context.DeadlineExceeded && err != context.Canceled {
			err = fmt.Errorf("certchain validation(with AIA url) error: %w", err)
		}
		return
	}

	signingCertCache.Add(urlString, x509Cert)
	return
}

// subscription/auth state locking/management helpers

type subscription struct {
	subAwsClient
	DeliveryInstr   *template.Template
	SubscriptionArn *string
}

func makeDeliveryInstr(name string, text string) (t *template.Template, err error) {
	tmplFuncs := template.FuncMap{
		"parseAddress": mail.ParseAddress,
	}
	t = template.New(name)
	t.Funcs(tmplFuncs)
	t, err = t.Parse(text)
	return
}

type lockedSub struct {
	ref    int32
	sem    *semaphore.Weighted
	cached *subscription
}

func newLockedSub(sub *subscription) *lockedSub {
	return &lockedSub{
		ref:    0,
		sem:    semaphore.NewWeighted(1),
		cached: sub,
	}
}

type subscriptions struct {
	mu sync.Mutex
	m  map[string]*lockedSub
}

func initSubs(ctx context.Context, db *sql.DB) (*subscriptions, error) {
	var err error
	var rows *sql.Rows

	subs := new(subscriptions)

	if subs.m == nil {
		subs.m = make(map[string]*lockedSub)
	}

	if rows, err = db.QueryContext(ctx, getSubsSQL); err != nil {
		return nil, fmt.Errorf("failed to query subscriptions: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var topicArn, awsId, awsSecret string
		var ss [2]sql.NullString
		var sub subscription
		if err = rows.Scan(&topicArn, &ss[0], &ss[1], &sub.SubscriptionArn); err != nil {
			return nil, fmt.Errorf("failed to load subscription data: %w", err)
		}
		if awsId, awsSecret, err = ctrlrpc.DecodeAWSCred(strings.TrimSpace(ss[0].String)); err != nil {
			return nil, fmt.Errorf("failed to parse credential for: %s, error: %w", topicArn, err)
		} else if sub.subAwsClient, err = makeAWSClient(strings.TrimSpace(topicArn), awsId, awsSecret); err != nil {
			return nil, fmt.Errorf("failed to create aws client for: %s, error: %w", topicArn, err)
		}
		if deliveryTemplate := strings.TrimSpace(ss[1].String); len(deliveryTemplate) != 0 {
			if sub.DeliveryInstr, err = makeDeliveryInstr(topicArn, deliveryTemplate); err != nil {
				return nil, fmt.Errorf("failed to create delivery instruction for %s, error: %w", topicArn, err)
			}
		} else {
			return nil, fmt.Errorf("subscription entry with empty deliveryTemplate for topic: %s, fix to continue", topicArn)
		}
		// valid subscription
		subs.m[topicArn] = newLockedSub(&sub)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to load subscription data: %w", err)
	}

	return subs, nil
}

func (subs *subscriptions) withTopic(ctx context.Context, topicArn string, fn func(ctx context.Context, subCached **subscription) error) error {
	var exists bool
	var ls *lockedSub

	subs.mu.Lock()
	if ls, exists = subs.m[topicArn]; !exists {
		ls = newLockedSub(nil)
		subs.m[topicArn] = ls
	} else {
		ls.ref++
	}
	subs.mu.Unlock()

	if err := ls.sem.Acquire(ctx, 1); err != nil {
		return err
	}

	defer func() {
		subs.mu.Lock()
		if ls.ref == 0 && ls.cached == nil { // last accessor deletes it if cleared
			delete(subs.m, topicArn)
		}
		subs.mu.Unlock()
		ls.sem.Release(1)
	}()

	return fn(ctx, &ls.cached)
}

func getAuth(ctx context.Context, db *sql.DB, topicArn string) (auth uuid.UUID, err error) {
	if err = db.QueryRowContext(ctx, querySubAuthSQL, topicArn).Scan(&auth); err != nil {
		err = fmt.Errorf("failed to get subscriptions for: %s, error: %w", topicArn, err)
	}

	return
}

func redactSubARN(arnStr string) string {
	var _arn arn.ARN
	var err error
	if _arn, err = arn.Parse(arnStr); err != nil {
		return ""
	} else if _arn.Service != endpoints.SnsServiceID {
		return ""
	}

	s := strings.Split(_arn.Resource, ":")
	if len(s) != 2 {
		return ""
	}
	if _, err = uuid.Parse(s[1]); err != nil {
		return ""
	}

	us := []rune(s[1])
	for i := 4; i < len(us)-4; i++ {
		if us[i] != '-' {
			us[i] = 'x'
		}
	}
	_arn.Resource = strings.Join([]string{s[0], string(us)}, ":")
	return _arn.String()
}

const (
	snsMessageLimit      = 256 * 1024 // 256KB
	snsMsgTypeHeader     = "x-amz-sns-message-type"
	snsMsgIdHeader       = "x-amz-sns-message-id"
	snsMsgTopicArnHeader = "x-amz-sns-topic-arn"
	snsMsgSubArnHeader   = "x-amz-sns-subscription-arn"
)

func (ep *SNSEndpoint) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		io.WriteString(w, "Only HTTP POST is supported: AWS SNS Sub")
		return
	}

	reqCtx, cancel := context.WithTimeout(r.Context(), processingTimeout)
	defer cancel()

	msgType := r.Header.Get(snsMsgTypeHeader)
	msgId := r.Header.Get(snsMsgIdHeader)
	msgTopicArn := r.Header.Get(snsMsgTopicArnHeader)

	var err error
	var auth uuid.UUID
	if auth, err = getAuth(reqCtx, ep.db, msgTopicArn); err != nil {
		sLog.Infow("Failed to get auth", "topicArn", msgTopicArn, "messageId", msgId, "error", err)
	}

	if subtle.ConstantTimeCompare([]byte(path.Base(r.URL.Path)), []byte(auth.String())) == 0 {
		w.WriteHeader(http.StatusForbidden)
		sLog.Debugw("Auth failure", "messageId", msgId, "topicArn", msgTopicArn, "urlPath", r.URL.Path)
		return
	}

	var msg snsMessage
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, snsMessageLimit))
	if err = dec.Decode(&msg); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		sLog.Warnw("Failed to decode json", "messageId", msgId, "error", err)
		return
	}

	if msg.Type != msgType {
		w.WriteHeader(http.StatusBadRequest)
		sLog.Warnf("rejecting request: %s, header field %s: %s doesn't match given message type: %s", msgId, snsMsgTypeHeader, msgType, msg.Type)
		return
	}

	if err = msg.checkSignature(reqCtx); err != nil {
		if err == context.DeadlineExceeded || err == context.Canceled {
			w.WriteHeader(http.StatusGatewayTimeout)
			sLog.Warnw("Signature check timed out", "messageId", msgId)
		} else {
			w.WriteHeader(http.StatusForbidden)
			sLog.Errorf("Rejecting request, signature check failed", "messageId", msgId, "error", err)
		}
		return
	}

	subFn := func(ctx context.Context, sc **subscription, isSub bool) (_ error) {
		if isSub {
			if *sc == nil {
				w.WriteHeader(http.StatusServiceUnavailable)
				sLog.Errorf("Failed to get subscription for topicArn: %s, messageId: %s, sub removed while validating message signature", msg.TopicARN, msgId)
				return
			} else if (*sc).SubscriptionArn != nil {
				w.WriteHeader(http.StatusForbidden)
				io.WriteString(w, fmt.Sprintf("Topic: %s is already subscribed to this endpoint", msg.TopicARN))
				sLog.Warnf("Rejecting subscription request to topic: %s with existing subscription, messageId: %s", msg.TopicARN, msgId)
				return
			}
			var subArn *string
			if subArn, err = (*sc).SubMgr.ConfirmSubscription(ctx, msg.TopicARN, msg.Token); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				sLog.Errorf("Confirmation failed for subscription messageId: %s, error: %v", msgId, err)
			} else {
				if _, err = ep.db.ExecContext(ctx, confirmSubSQL, *subArn, msg.TopicARN); err != nil {
					w.WriteHeader(http.StatusInternalServerError)
					sLog.Errorf("Failed to set subscriptionArn for topicArn: %s, messageId: %s, error: %v", msg.TopicARN, msgId, err)
				} else {
					// update sub Arn cached if we db write succeeded.
					(*sc).SubscriptionArn = subArn
					w.WriteHeader(http.StatusOK)
				}
			}
		} else {
			// If unsubscribed we will delete it from our db and cache.
			if _, err = ep.db.ExecContext(ctx, removeSubSQL, msg.TopicARN); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				sLog.Warnf("Failed to remove subscription for topic: %s, messageId: %s, error: %v", msg.TopicARN, msg.MessageID, err)
			} else {
				// delete from cache if delete DB op succeeded.
				*sc = nil
				w.WriteHeader(http.StatusOK)
				sLog.Infof("Unsub confirmed messageId: %s, message: %s", msg.MessageID, msg.Message)
			}
		}
		return
	}

	var isSub bool
	switch msgType {
	case subConfirmType:
		isSub = true
		fallthrough
	case unsubConfirmType:
		if err = ep.cachedSubs.withTopic(reqCtx, msg.TopicARN, func(ctx context.Context, sc **subscription) error {
			return subFn(ctx, sc, isSub)
		}); err != nil { // this has to be from ctx failure before subFn got called since subFn write to w and returns no error.
			w.WriteHeader(http.StatusServiceUnavailable)
			sLog.Warnf("Busy: already processing %s message for topic: %s, rejecting messageId: %s", msgType, msg.TopicARN, msg.MessageID)
		}
	case notificationType:
		var rowid int64
		if err = ep.db.QueryRowContext(reqCtx, appendLogSQL, msg.MessageID, msg.Message).Scan(&rowid); err != nil {
			if err == sql.ErrNoRows { // Duplicate message -> success and return.
				w.WriteHeader(http.StatusOK)
			} else {
				w.WriteHeader(http.StatusInternalServerError)
				sLog.Errorf("Failed to log message for messageId: %s", msg.MessageID)
			}
			return
		}

		select {
		case ep.msgCh <- rowid:
			w.WriteHeader(http.StatusOK)
		case <-reqCtx.Done():
			w.WriteHeader(http.StatusServiceUnavailable)
		}

		// Set the subscription ARN for unconfirmed subscriptions, as we are receiving notifications(assuming it was confirmed manually).
		ep.cachedSubs.withTopic(reqCtx, msg.TopicARN, func(ctx context.Context, sc **subscription) error {
			if *sc != nil && (*sc).SubscriptionArn == nil {
				msgSubArn := r.Header.Get(snsMsgSubArnHeader)
				if len(msgSubArn) != 0 {
					if _, err = ep.db.ExecContext(ctx, confirmSubSQL, msgSubArn, msg.TopicARN); err == nil {
						// update sub Arn cached if we db write succeeded.
						(*sc).SubscriptionArn = &msgSubArn
					} else {
						sLog.Warnw("Failed to set subscription arn for notification message", "messageId", msg.MessageID, "topicArn", msg.TopicARN, "error", err)
					}
				}
			}
			return nil
		})
	default:
		w.WriteHeader(http.StatusBadRequest)
		sLog.Debugf("Unexpected message type: %s for messageId: %s", msgType, msg.MessageID)
	}
}

type epConfig struct {
	ExtUrl  string
	Address string

	GCScanInterval     time.Duration
	GCCollectThreshold uint32
}

type epDaemonConfig struct {
	runtimeDir string
	cacheDir   string
	stateDir   string
}

type gcState struct {
	collectThreshold *atomic.Uint32
	scanPeriod       *atomic.Duration
	tombCount        *atomic.Int64
}

const (
	EpStateInit uint32 = iota
	EpStateStarting
	EpStateConfigReady
	EpStateRunning
	EpStateStopped
)

type SNSEndpoint struct {
	epState    *atomic.Uint32
	confInitCh chan struct{}

	db         *sql.DB
	cachedSubs *subscriptions

	msgCh    chan int64
	procLock *semaphore.Weighted

	gcTriggerCh chan bool
	gcState

	ctrlSocketPath string
	downloadDir    string
}

func NewSNSEndpoint(conf epDaemonConfig) (ep *SNSEndpoint, err error) {
	ctrlSocketPath := filepath.Join(conf.runtimeDir, ctrlSockFileName)

	downloadDir := filepath.Join(conf.cacheDir, downloadDirName)
	var fi os.FileInfo
	if fi, err = os.Lstat(downloadDir); err != nil {
		if err = os.MkdirAll(downloadDir, 0700); err != nil {
			return
		}
	} else if !fi.IsDir() {
		return nil, fmt.Errorf("mailCacheDir: %q must be a directory", downloadDir)
	}
	if downloadDir, err = filepath.Abs(downloadDir); err != nil {
		return nil, fmt.Errorf("mailCacheDir error: %w", err)
	}

	if fi, err = os.Lstat(conf.stateDir); err != nil {
		if err = os.MkdirAll(conf.stateDir, 0700); err != nil {
			return nil, fmt.Errorf("failed to create config dir: %q, error: %w", conf.stateDir, err)
		}
	} else if fi.Mode()&os.ModeSymlink == 0 && !fi.IsDir() {
		return nil, fmt.Errorf("config path: %q must be a directory", conf.stateDir)
	}
	dbPath := filepath.Join(conf.stateDir, dbFileName)
	if dbPath, err = filepath.Abs(dbPath); err != nil {
		return nil, fmt.Errorf("db path error: %w", err)
	}

	if fi, err = os.Lstat(ctrlSocketPath); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("control socket path error: %w", err)
		}
		err = nil
	} else {
		if fi.Mode()&os.ModeSocket != os.ModeSocket {
			return nil, fmt.Errorf("existing file %q is not of unix socket type", ctrlSocketPath)
		} else {
			return nil, fmt.Errorf("another endpoint is already running, busy: %q", ctrlSocketPath)
		}
	}

	dbParams := url.Values{}
	dbParams.Set("cache", "shared")
	dbParams.Set("mode", "rwc")
	dbParams.Set("_journal", "WAL")
	dbParams.Set("_txlock", "immediate")
	dbParams.Set("_timeout", sqlite3BusyTimeout)
	dbParams.Set("_cache_size", sqliteCacheSize)

	sql.Register("sqlite3_custom", &sqlite3.SQLiteDriver{
		ConnectHook: func(sc *sqlite3.SQLiteConn) error {
			if e := sc.RegisterFunc("uuid_random", func() string {
				u, _ := uuid.NewRandom()
				return u.String()
			}, false); e != nil {
				return e
			}
			return nil
		},
	})
	var db *sql.DB
	if db, err = sql.Open("sqlite3_custom", fmt.Sprintf("%s?%s", dbPath, dbParams.Encode())); err != nil {
		return nil, err
	}

	if _, err = db.Exec(memTempStoreSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("couldn't set temp_store to memory: %w", err)
	}
	if _, err = db.Exec(recTriggersSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("couldn't enable recursive triggers: %w", err)
	}

	if _, err = db.Exec(createConfigTSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("couldn't create config table: %w", err)
	}

	if _, err = db.Exec(createSubTSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("couldn't create subscriptions table: %w", err)
	}

	if _, err = db.Exec(createMsgLogTSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("couldn't create msglog table: %w", err)
	}

	if _, err = db.Exec(createErrLogTSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("couldn't create errorlog table: %w", err)
	}

	ep = &SNSEndpoint{
		epState:    atomic.NewUint32(EpStateInit),
		confInitCh: make(chan struct{}, 1),

		db:       db,
		msgCh:    make(chan int64, msgQSize),
		procLock: semaphore.NewWeighted(maxMessageProcessors),

		gcTriggerCh: make(chan bool),

		gcState: gcState{
			collectThreshold: atomic.NewUint32(0),
			scanPeriod:       atomic.NewDuration(0),
			tombCount:        atomic.NewInt64(0),
		},

		ctrlSocketPath: ctrlSocketPath,
		downloadDir:    downloadDir,
	}
	// trigger confInit.
	ep.confInitCh <- struct{}{}

	return
}

func (ep *SNSEndpoint) rpcServer() *EPRPCServer {
	return &EPRPCServer{
		_cfgLock: semaphore.NewWeighted(1),
		ep:       ep,
	}
}

func (ep *SNSEndpoint) Run(ctx context.Context, doVacuum bool) error {
	defer func() {
		ep.db.Close()
		ep.epState.Store(EpStateStopped)
		_ = SvcNotifyStatus("sesrcvr stopped")
	}()

	if ep.epState.Load() != EpStateInit {
		return ErrInstanceDirty
	}

	// Starting
	ep.epState.Store(EpStateStarting)
	_ = SvcNotifyStatus("Starting")

	if doVacuum {
		if _, err := ep.db.ExecContext(ctx, "VACUUM"); err != nil {
			return fmt.Errorf("failed to vacuum the db: %w", err)
		}
		sLog.Debug("Vacuum on db: success")
	}

	var wg sync.WaitGroup

	// Run the controller.
	wg.Add(1)
	const maxClientReqSize = 64 * 1024 // 64K
	ctrlServer := grpc.NewServer(
		grpc.KeepaliveParams(keepalive.ServerParameters{MaxConnectionIdle: 2 * time.Minute}), // aggressively close idle conns.
		grpc.MaxRecvMsgSize(maxClientReqSize),
		grpc.NumStreamWorkers(uint32(runtime.NumCPU())),
	)
	ctrlServer.RegisterService(&ctrlrpc.ControlEndpoint_ServiceDesc, ep.rpcServer())
	defer ctrlServer.Stop()

	go func() {
		defer wg.Done()

		os.Remove(ep.ctrlSocketPath)
		ln, e := net.Listen("unix", ep.ctrlSocketPath)
		if e != nil {
			sLog.Error("Control socket Listen failed: ", e)
			return
		}
		if e = ctrlServer.Serve(ln); e != nil {
			sLog.Errorf("Control server error: %v, Can no longer set any configuration, restart needed.", e)
		}
	}()

	var err error
	var conf *epConfig
	var httpListener net.Listener

	// Create a child context for sub-goroutines.
	chldCtx, chldCancel := context.WithCancel(ctx)
	defer chldCancel()

	// Signal ready to svc manager.
	if err = SvcReadyNotify(&wg, chldCtx); err != nil {
		return err
	}

confCheckLoop:
	for {
		select {
		case <-ep.confInitCh:
			if conf, err = validateDBConf(ctx, ep.db, true); err == nil {
				httpListener, err = net.Listen("tcp", conf.Address)
			}
			if err == nil {
				break confCheckLoop
			} else {
				sLog.Debug("Configuration error: ", err)
				_ = SvcNotifyStatus("Waiting for config")
			}
		case <-ctx.Done():
			break confCheckLoop
		}
	}
	// if we got cancelled or accept for unixlistener failed while reading conf, return.
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		if httpListener != nil { // if tcp listen succeeded we close it.
			httpListener.Close()
		}
		return err
	}

	sLog.Info("Configuration OK. Starting server..")
	if ep.cachedSubs, err = initSubs(ctx, ep.db); err != nil {
		return err
	}

	// Initialize cached config state from DB.
	var garbageCount int64
	if ep.db.QueryRowContext(ctx, garbageCountSQL).Scan(&garbageCount); err != nil {
		return err
	}
	ep.tombCount.CAS(0, garbageCount)
	ep.collectThreshold.CAS(0, conf.GCCollectThreshold)
	ep.scanPeriod.CAS(0, conf.GCScanInterval)

	// Config Ready
	ep.epState.Store(EpStateConfigReady)
	_ = SvcNotifyStatus("Config OK")

	// now we replay failed/cancelled actions.
	if err = ep.replayLog(ctx); err != nil {
		return err
	}

	// Run the message processor in a child context.
	procErrCh := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()

		procErrCh <- ep.runMsgProc(chldCtx)
	}()

	// Register this instance as the default handler.
	http.Handle("/", ep)
	// Run the http server.
	httpServer := &http.Server{
		ReadTimeout:  requestReadTimeout,
		WriteTimeout: requestWriteTimeout,
	}
	srvErrCh := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()

		srvErrCh <- httpServer.Serve(httpListener)
	}()

	// run gcProc in the child context.
	wg.Add(1)
	go func() {
		defer wg.Done()

		ep.runGCProc(chldCtx)
	}()

	// Running.
	ep.epState.Store(EpStateRunning)
	_ = SvcNotifyStatus("Running")

	stopServer := func() {
		c, cancel := context.WithTimeout(ctx, shutdownTimeout)
		defer cancel()

		_ = SvcStopNotify()
		// Stop CtrlServer.
		ctrlServer.Stop()

		if err = httpServer.Shutdown(c); err != nil {
			if !errors.Is(err, context.Canceled) {
				err = fmt.Errorf("error shutting down http server: %w", err)
			} else {
				err = nil
			}
		}
	}

	select {
	case err = <-procErrCh:
		if err != nil && !errors.Is(err, context.Canceled) {
			sLog.Error("message processor failed, error: ", err)
		}
		stopServer()
	case <-ctx.Done():
		stopServer()
	case err = <-srvErrCh:
		_ = SvcStopNotify()
		// Stop CtrlServer.
		ctrlServer.Stop()
		// cancel child context on any error.
		chldCancel()

		if err != http.ErrServerClosed {
			err = fmt.Errorf("http server closed with error: %w", err)
		} else {
			err = nil
		}
	}

	// wait for goroutines to finish.
	wg.Wait()

	sLog.Info("Subscription server quit")
	return err
}

func (ep *SNSEndpoint) replayLog(ctx context.Context) (err error) {
	procErrCh := make(chan error)
	go func() {
		procErrCh <- ep.runMsgProc(ctx)
	}()

	cancelProc := func() error {
		close(ep.msgCh)
		e := <-procErrCh
		// Replace with a new channel AFTER runMsgProc() is done, so ep.msgCh is always valid.
		ep.msgCh = make(chan int64, msgQSize)
		return e
	}

	var rows *sql.Rows
	if rows, err = ep.db.QueryContext(ctx, pendingLogsSQL); err != nil {
		cancelProc()
		return
	}
	defer rows.Close()

	sLog.Debug("Replaying undelivered messages..")
	for rows.Next() {
		var rowid int64
		if err = rows.Scan(&rowid); err != nil {
			cancelProc()
			return
		}
		ep.msgCh <- rowid
	}
	err = cancelProc()

	sLog.Debug("Replay complete")
	if e := rows.Err(); e != nil {
		return e
	}

	return
}

func (ep *SNSEndpoint) pauseMsgProc(ctx context.Context) (resume func(), err error) {
	if err = ep.procLock.Acquire(ctx, maxMessageProcessors); err != nil {
		return
	}

	return func() { ep.procLock.Release(maxMessageProcessors) }, nil
}

const (
	sesNotificationType = "Received"
	sesS3ActionType     = "S3"
)

func (ep *SNSEndpoint) runMsgProc(ctx context.Context) error {
Loop:
	for {
		select {
		case <-ctx.Done():
			sLog.Debug("msgProc() cancelled")
			break Loop
		case rowid, ok := <-ep.msgCh:
			if !ok {
				sLog.Debug("Message rowid chan closed")
				break Loop
			}

			if err := ep.procLock.Acquire(ctx, 1); err != nil {
				sLog.Debugw("subscription ctx Done", "rowid", rowid)
				break Loop
			}
			go func() {
				var err error
				var msgid string

				defer func() {
					if err != nil && len(msgid) != 0 {
						if _, err = ep.db.ExecContext(ctx, recordExecErrorSQL, msgid, err.Error()); err != nil {
							sLog.Warnw("Failed to record execution error", "msgid", msgid, "error", err)
						}
					}

					ep.procLock.Release(1)
				}()

				var message string
				var tomb bool
				if err = ep.db.QueryRowContext(ctx, queryLogSQL, rowid).Scan(&msgid, &message, &tomb); err != nil {
					sLog.Errorf("Failed to get the message from log for rowid: %s, error: %v", rowid, err)
					return
				}

				// already processed message, shouldn't happen?
				if tomb {
					sLog.Warnw("Received rowid with a delivered message", "rowid", rowid, "messageId", msgid)
					return
				}

				var sesMsg SESNotification
				if err = json.Unmarshal([]byte(message), &sesMsg); err != nil {
					err = fmt.Errorf("failed to parse SES notification from msglog: %w", err)
					sLog.Errorw("Unmarshal error", "messageId", msgid, "Error", err)
					return
				}
				if sesMsg.Type != sesNotificationType || sesMsg.Receipt.Action.Type != sesS3ActionType {
					err = fmt.Errorf("invalid notification message type, not a ses notification of S3 action")
					sLog.Errorw("invalid msg/action type", "messageId", msgid, "sesMsgType", sesMsg.Type, "actionType", sesMsg.Receipt.Action.Type)
					return
				}

				topicArn := sesMsg.Receipt.Action.TopicARN
				b := sesMsg.Receipt.Action.BucketName
				o := sesMsg.Receipt.Action.ObjectKey
				if b == nil || o == nil {
					err = fmt.Errorf("missing bucketName/objectKey for message")
					sLog.Errorw("Unexpected message", "messageId", msgid, "error", err)
					return
				}

				var s3Client *s3ObjClient
				var deliveryInstr *template.Template
				if err = ep.cachedSubs.withTopic(ctx, topicArn, func(ctx context.Context, sc **subscription) (err error) {
					if *sc != nil {
						s3Client = (*sc).S3Client
						deliveryInstr = (*sc).DeliveryInstr
					} else {
						err = fmt.Errorf("can't get subscription for topic: %s, unsubscribed already, stale messageId: %s", topicArn, msgid)
					}
					return
				}); err != nil {
					sLog.Error("Failure: ", err)
					return
				}
				if s3Client == nil || deliveryInstr == nil {
					err = fmt.Errorf("missing s3client/delivery template")
					sLog.Error("Implementation bug: shouldn't have invalid client/delivery template", "topicArn", topicArn)
					return
				}

				lpath := filepath.Join(ep.downloadDir, *b, *o)
				ldir := filepath.Dir(lpath)
				if err = os.MkdirAll(ldir, 0700); err != nil {
					sLog.Errorf("Failed to create download directory: %s for message: %s, error: %v", ldir, msgid, err)
					return
				}

				var f *os.File
				if f, err = os.OpenFile(lpath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600); err != nil {
					sLog.Errorf("Failed to create email cache file: %s for message: %s, error: %v", lpath, msgid, err)
					return
				}
				defer func() {
					// remove the file if possible after closing.
					if f.Close() == nil {
						os.Remove(f.Name())
					}
				}()

				s3obj := NewObjReader(ctx, s3Client, *b, *o)
				defer s3obj.Close()

				if _, err = io.Copy(f, s3obj); err != nil {
					sLog.Errorf("Failed to download file: %s for messageId: %s, error: %v", lpath, msgid, err)
					return
				}

				var output strings.Builder
				dData := DeliveryData{ctx: ctx, SESNotification: sesMsg, MailContent: f}

				if err = deliveryInstr.Execute(&output, &dData); err != nil {
					sLog.Errorf("Failed to deliver email for topic: %s, messageId: %s, error: %v", topicArn, msgid, err)
					return
				}
				sLog.Debugf("Successfully delivered mail for messageId: %s, output: %q", msgid, output.String())

				if _, err = ep.db.Exec(retireLogSQL, rowid); err != nil {
					sLog.Warnf("Failed to retire delivered messageId: %s, error: %v", msgid, err)
					return
				}
				if ep.tombCount.Inc() >= int64(ep.collectThreshold.Load()) { // try trigger gc if threshold crossed
					select {
					case ep.gcTriggerCh <- true:
					default:
					}
				}
			}()
		}
	}

	if err := ep.procLock.Acquire(context.Background(), maxMessageProcessors); err != nil {
		return fmt.Errorf("waiting for message processors failed: %w", err)
	}
	ep.procLock.Release(maxMessageProcessors)

	return nil
}

func (ep *SNSEndpoint) shouldCollect() bool {
	return len(ep.msgCh) == 0 // no pending requests ~ idle, can do a collection.
}

func (ep *SNSEndpoint) runGCProc(ctx context.Context) {
	jitterCalc := func(t time.Duration, pct float64, cap time.Duration) time.Duration {
		if pct <= 0 || pct > 1.0 {
			return t
		}
		rand := rand.New(rand.NewSource(int64(time.Now().Nanosecond())))
		j := int64(math.Round(float64(t) * pct))
		if cap > 0 && j > int64(cap) {
			j = int64(cap)
		}
		return time.Duration(int64(t) + 2*rand.Int63n(j) - j)
	}

	scanTimer := time.NewTimer(0) // delay between full gc
	if !scanTimer.Stop() {
		<-scanTimer.C
	}
	defer scanTimer.Stop()

	resetScanTimer := func() {
		t := ep.scanPeriod.Load()
		scanTimer.Reset(jitterCalc(t, 0.25, 30*time.Minute)) // ±jitter capped to 0.5hr.
	}

	incTm := time.NewTimer(0) // incremental timer for collection
	if !incTm.Stop() {
		<-incTm.C
	}
	defer incTm.Stop()

	bucketDelReady, _ := lru.New(64)
Loop:
	for {
		resetScanTimer()
		select {
		case <-ctx.Done():
			sLog.Debug("gcProc() cancelled")
			break Loop
		case doGc := <-ep.gcTriggerCh:
			if !scanTimer.Stop() {
				<-scanTimer.C
			}
			if !doGc {
				sLog.Debug("gcScanPeriod change received")
				continue Loop
			}
		case <-scanTimer.C:
		}

		var err error
		var dbConn *sql.Conn
		if dbConn, err = ep.db.Conn(ctx); err != nil {
			sLog.Errorw("failed to acquire DB Connection", "error", err)
			continue
		}

		cont := true
		for cont {
			func(doCollect bool) {
				defer func() {
					if doCollect {
						if _, err = dbConn.ExecContext(ctx, deleteTempGCTSQL); err != nil {
							sLog.Errorw("Failed to drop temp gc table", "error", err)
						}
					}
					if cont {
						incTm.Reset(jitterCalc(incrementalGCDelay, 0.4, 0))
						select {
						case <-incTm.C:
						case <-ctx.Done():
							cont = false
						}
					}
				}()

				if !doCollect {
					return
				}

				type delGroup struct {
					s3Client *s3ObjClient
					objs     map[string][]*s3.ObjectIdentifier // bucketName -> objs
				}
				toDelete := make(map[string]*delGroup) // topicArn -> delGroup

				var rows *sql.Rows
				var n int64
				if _, err = dbConn.ExecContext(ctx, createTempGCTSQL); err != nil {
					sLog.Errorw("Failed to create temp table for gc", "error", err)
					return
				}
				if rows, err = dbConn.QueryContext(ctx, queryGarbageLogsSQL, incrementalGCAmount); err != nil {
					sLog.Errorw("Failed to scan garbage from msglog table into temp table", "error", err)
					return
				}
				for rows.Next() {
					var topicArn, bucketName, objectKey, msgid string
					if err = rows.Scan(&topicArn, &bucketName, &objectKey, &msgid); err != nil {
						break
					}
					n++ // count the no. of rows
					if len(topicArn) == 0 || len(bucketName) == 0 || len(objectKey) == 0 {
						sLog.Debugw("skipping s3 delete for message with empty topicArn/bucketName/objectKey", "messageId", msgid)
						continue
					}

					var g *delGroup
					var ok bool
					if g, ok = toDelete[topicArn]; !ok {
						objs := make(map[string][]*s3.ObjectIdentifier)
						g = &delGroup{objs: objs}
						toDelete[topicArn] = g
					}
					g.objs[bucketName] = append(g.objs[bucketName], &s3.ObjectIdentifier{Key: &objectKey})
				}
				if n < incrementalGCAmount { // no need to continue if we got less garbage than requested, this gc cycle is complete.
					cont = false
				}
				if err = rows.Err(); err != nil {
					sLog.Errorw("Failed to enumerate the selected rows ready for gc", "error", rows.Err())
					rows.Close()
					return
				}
				rows.Close()
				if n == 0 {
					return
				}

				ep.cachedSubs.mu.Lock()
				for topic := range toDelete {
					s := ep.cachedSubs.m[topic].cached
					if s != nil {
						toDelete[topic].s3Client = s.S3Client
					}
				}
				ep.cachedSubs.mu.Unlock()

				for _, g := range toDelete {
					for b, os := range g.objs {
						var canDel bool
						if ready, ok := bucketDelReady.Get(b); !ok {
							var verOut *s3.GetBucketVersioningOutput
							if verOut, err = g.s3Client.GetBucketVersioningWithContext(ctx, &s3.GetBucketVersioningInput{Bucket: &b}); err != nil {
								if ctx.Err() == nil {
									sLog.Warnw("Failed to check bucket versioning status, not deleting objects", "bucketName", b, "error", err)
								} else { // context done -> return
									sLog.Debug("Context cancelled for GetBucketVersioning")
									return
								}
							} else {
								canDel = (*verOut.Status == s3.BucketVersioningStatusEnabled) && (verOut.MFADelete == nil || *verOut.MFADelete == s3.MFADeleteDisabled)
								bucketDelReady.Add(b, canDel)
							}
						} else {
							canDel = ready.(bool)
						}
						if canDel && g.s3Client != nil {
							var res *s3.DeleteObjectsOutput
							if res, err = g.s3Client.DeleteObjectsWithContext(ctx, &s3.DeleteObjectsInput{
								Bucket: &b,
								Delete: &s3.Delete{
									Objects: os,
								},
							}); err != nil {
								if ctx.Err() == nil {
									sLog.Warnw("Failed to delete objects", "bucketName", b, "errors", res.Errors)
								} else {
									sLog.Debug("Context cancelled for DeleteObjects")
									return
								}
							}
						}
					}
				}
				if _, err = dbConn.ExecContext(ctx, clearGarbageLogsSQL); err != nil {
					sLog.Errorw("Failed to clear collect ready selected rows from msglog", "error", err)
					return
				}
				ep.tombCount.Sub(n) // decrease tombcount by the amount collected
			}(ep.shouldCollect())
		}

		dbConn.Close()
	}
}

type DeliveryData struct {
	ctx context.Context
	SESNotification
	MailContent io.ReadSeeker
}

func (data *DeliveryData) MailPipeTo(cmd string, args ...string) (out string, err error) {
	c := exec.CommandContext(data.ctx, cmd, args...)
	var w io.WriteCloser
	if w, err = c.StdinPipe(); err != nil {
		return
	}

	ch := make(chan struct{})
	go func() {
		defer w.Close()
		data.MailContent.Seek(0, io.SeekStart)
		io.Copy(w, data.MailContent)
		data.MailContent.Seek(0, io.SeekStart)
		close(ch)
	}()

	var ob []byte
	if ob, err = c.Output(); err != nil {
		return
	}

	<-ch
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

// Configuration validation

func validateUserConf(conf map[string]string, fallback bool) (*epConfig, error) {
	readConf := func(key string) (v string, found bool, err error) {
		v, found = conf[key]
		return
	}
	return validateConf(readConf, false, fallback)
}

func validateDBConf(ctx context.Context, db interface {
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
}, fallback bool) (*epConfig, error) {
	readConf := func(key string) (v string, found bool, err error) {
		var ns sql.NullString
		if err = db.QueryRowContext(ctx, queryConfigSQL, key).Scan(&ns); err != nil {
			if errors.Is(err, sql.ErrNoRows) { // 'missing key' is not an error.
				err = nil
				sLog.Debugf("Missing key: %q from config table", key)
			} else {
				err = fmt.Errorf("read config key: %q, error: %w", key, err)
			}
			return
		}
		// null string is semantically same as empty for our usecase.
		found = true
		v = ns.String
		return
	}
	return validateConf(readConf, true, fallback)
}

func validateConf(readConf func(key string) (val string, found bool, err error), checkAll bool, fallback bool) (conf *epConfig, err error) {
	var c epConfig
	var found bool

	var extUrl string
	if extUrl, found, err = readConf(ctrlrpc.UrlConfKey); err != nil {
		return
	}
	if found {
		var u *url.URL
		if u, err = url.Parse(extUrl); err != nil {
			err = fmt.Errorf("failed to parse config %q, %w", ctrlrpc.UrlConfKey, err)
			return
		} else if u.Scheme != "http" && u.Scheme != "https" {
			err = fmt.Errorf("config %q doesn't have scheme http[s]", ctrlrpc.UrlConfKey)
			return
		} else if extUrl[len(extUrl)-1] != '/' {
			err = fmt.Errorf("config %q must be a rooted url(end with '/')", ctrlrpc.UrlConfKey)
			return
		}
		c.ExtUrl = extUrl
	} else if checkAll {
		err = fmt.Errorf("missing required config key: %q", ctrlrpc.UrlConfKey)
		return
	}

	var addr string
	if addr, found, err = readConf(ctrlrpc.AddressConfKey); err != nil {
		return
	}
	if found {
		if len(addr) == 0 {
			err = fmt.Errorf("config %q can't be empty", ctrlrpc.AddressConfKey)
			return
		} else if c := addr[0]; c != ':' && c != '[' && !('0' <= c && c <= '9') {
			err = fmt.Errorf("config %q can't contain a hostname", ctrlrpc.AddressConfKey)
			return
		}
		if _, err = net.ResolveTCPAddr("tcp", addr); err != nil {
			err = fmt.Errorf("validation failure of %q, error: %w", ctrlrpc.AddressConfKey, err)
			return
		}
		c.Address = addr
	} else if checkAll {
		err = fmt.Errorf("missing required config key: %q", ctrlrpc.AddressConfKey)
		return
	}

	readUint32Conf := func(key string, defVal uint32) (v uint32, err error) {
		var confStr string
		if confStr, found, err = readConf(key); err != nil {
			return
		}
		if found {
			var n uint64
			if n, err = strconv.ParseUint(confStr, 10, 32); err != nil {
				err = fmt.Errorf("failed to parse config %q, %w", key, err)
			} else if int(n) < 0 || n > uint64(int(^uint(0)>>1)) {
				err = fmt.Errorf("value %q for config %q is out of range", confStr, key)
			} else {
				v = uint32(n)
			}
		} else if checkAll {
			err = fmt.Errorf("missing config key: %q", key)
		}
		if err != nil {
			if !fallback {
				return
			}
			sLog.Infof("Using default %q: %d, reason: %v", key, defVal, err)
			v = defVal
			err = nil
		}
		return
	}

	const minGCScanPeriod = 5 * time.Minute
	var gcPeriodStr string
	if gcPeriodStr, found, err = readConf(ctrlrpc.GcScanPeriodKey); err != nil {
		return
	}
	if found {
		var t time.Duration
		if t, err = time.ParseDuration(gcPeriodStr); err != nil {
			err = fmt.Errorf("failed to parse config %q, %w", ctrlrpc.GcScanPeriodKey, err)
		} else if t < minGCScanPeriod {
			err = fmt.Errorf("value %q for config %q is out of range(should atleast be %s)", gcPeriodStr, ctrlrpc.GcScanPeriodKey, minGCScanPeriod.String())
		} else {
			c.GCScanInterval = t
		}
	} else if checkAll {
		err = fmt.Errorf("missing config key: %q", ctrlrpc.GcScanPeriodKey)
	}
	if err != nil {
		if !fallback {
			return
		}
		sLog.Infof("Using default %q: %s, reason: %v", ctrlrpc.GcScanPeriodKey, defaultGcPeriod.String(), err)
		c.GCScanInterval = defaultGcPeriod
		err = nil
	}

	if c.GCCollectThreshold, err = readUint32Conf(ctrlrpc.GcCollectThresholdKey, defaultGcThreshold); err != nil {
		return
	}

	conf = &c
	return
}

func fromContextErrCode(err error, code codes.Code) error {
	switch err {
	case nil:
		return nil
	case context.DeadlineExceeded:
		return status.Error(codes.DeadlineExceeded, err.Error())
	case context.Canceled:
		return status.Error(codes.Canceled, err.Error())
	default:
		if sqlite3Err, ok := err.(sqlite3.Error); ok && sqlite3Err.Code == sqlite3.ErrBusy {
			return status.Error(codes.Unavailable, err.Error())
		}
		return status.Error(code, err.Error())
	}
}

type EPRPCServer struct {
	ctrlrpc.UnimplementedControlEndpointServer
	_cfgLock *semaphore.Weighted
	ep       *SNSEndpoint
}

func (srv *EPRPCServer) GetConfig(ctx context.Context, _ *emptypb.Empty) (*ctrlrpc.Config, error) {
	conf, err := validateDBConf(ctx, srv.ep.db, true)
	if err != nil {
		return nil, fromContextErrCode(err, codes.FailedPrecondition)
	}
	return &ctrlrpc.Config{
		Keyvals: map[string]string{
			ctrlrpc.UrlConfKey:            conf.ExtUrl,
			ctrlrpc.AddressConfKey:        conf.Address,
			ctrlrpc.GcScanPeriodKey:       conf.GCScanInterval.String(),
			ctrlrpc.GcCollectThresholdKey: strconv.FormatUint(uint64(conf.GCCollectThreshold), 10),
		},
	}, nil
}

func (srv *EPRPCServer) SetConfig(ctx context.Context, in *ctrlrpc.Config) (*ctrlrpc.SetConfigResult, error) {
	srv._cfgLock.Acquire(ctx, 1)
	defer srv._cfgLock.Release(1)

	var err error
	var uConf *epConfig
	var cfg map[string]string
	if cfg = in.GetKeyvals(); cfg == nil {
		return nil, status.Error(codes.InvalidArgument, "missing configuration params")
	}
	if uConf, err = validateUserConf(cfg, false); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	var ks []string
	var tx *sql.Tx
	if tx, err = srv.ep.db.BeginTx(ctx, nil); err != nil {
		return nil, fromContextErrCode(err, codes.Unavailable)
	}
	func() {
		defer func() {
			if err != nil {
				tx.Rollback()
			}
		}()

		for _, k := range []string{ctrlrpc.UrlConfKey, ctrlrpc.AddressConfKey, ctrlrpc.GcScanPeriodKey, ctrlrpc.GcCollectThresholdKey} {
			if v, ok := cfg[k]; ok {
				var _ignore string
				// ErrNoRows => no update, not an error condition and has no changes.
				if e := tx.QueryRowContext(ctx, setConfigSQL, k, v).Scan(&_ignore); e != nil && e != sql.ErrNoRows {
					err = fromContextErrCode(e, codes.Internal)
					return
				} else if e == nil {
					ks = append(ks, k)
				}
			}
		}
		err = fromContextErrCode(tx.Commit(), codes.Unavailable)
	}()
	if err != nil {
		return nil, err
	}

	// Update cache and server state as needed.
	if uConf.GCScanInterval != 0 {
		if srv.ep.scanPeriod.Swap(uConf.GCScanInterval) != uConf.GCScanInterval { // changed scanPeriod
			// signal to restart the timer.
			select {
			case srv.ep.gcTriggerCh <- false:
			default:
			}
		}
	}
	if uConf.GCCollectThreshold != 0 {
		srv.ep.collectThreshold.Store(uConf.GCCollectThreshold)
	}

	var needRestart bool
	// Trigger confInit if it is waiting for good config.
	if srv.ep.epState.Load() < EpStateRunning {
		select {
		case srv.ep.confInitCh <- struct{}{}:
		default:
		}
	} else {
		for _, v := range ks {
			if v == ctrlrpc.AddressConfKey {
				needRestart = true
			}
		}
	}

	return &ctrlrpc.SetConfigResult{Changes: ks, RestartRequired: needRestart}, nil
}

func (srv *EPRPCServer) ListSub(_ context.Context, in *wrapperspb.StringValue) (*ctrlrpc.Subscriptions, error) {
	if srv.ep.epState.Load() != EpStateRunning {
		return nil, status.Error(codes.Aborted, "server not ready, requires valid configuration first")
	}

	topicArn := in.GetValue()

	srv.ep.cachedSubs.mu.Lock()
	defer srv.ep.cachedSubs.mu.Unlock()

	var err error

	if len(topicArn) != 0 {
		if _, err = arn.Parse(topicArn); err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		if lSub, ok := srv.ep.cachedSubs.m[topicArn]; ok && lSub.cached != nil {
			var sub *ctrlrpc.Subscription
			if lSub.cached.SubscriptionArn != nil {
				sub = &ctrlrpc.Subscription{
					SubArn: *lSub.cached.SubscriptionArn,
				}
			}
			return &ctrlrpc.Subscriptions{
				Values: map[string]*ctrlrpc.Subscription{
					topicArn: sub,
				},
			}, nil
		} else {
			return nil, status.Errorf(codes.NotFound, "no subscription exists for topic: %s", topicArn)
		}
	} else {
		m := make(map[string]*ctrlrpc.Subscription)
		for topicArn, lSub := range srv.ep.cachedSubs.m {
			if lSub.cached != nil {
				var sub *ctrlrpc.Subscription
				if lSub.cached.SubscriptionArn != nil {
					sub = &ctrlrpc.Subscription{
						SubArn: *lSub.cached.SubscriptionArn,
					}
				}
				m[topicArn] = sub
			}
		}
		return &ctrlrpc.Subscriptions{Values: m}, nil
	}
}

func (srv *EPRPCServer) EditSub(ctx context.Context, in *ctrlrpc.EditSubRequest) (*ctrlrpc.Result, error) {
	if srv.ep.epState.Load() != EpStateRunning {
		return nil, status.Error(codes.Aborted, "server not ready, requires valid configuration first")
	}

	var out string
	topicArn := in.GetTopicArn()
	if _arn, err := arn.Parse(topicArn); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	} else if _arn.Service != endpoints.SnsServiceID {
		return nil, status.Errorf(codes.InvalidArgument, "%q is not a valid SNS topicARN", topicArn)
	}
	inCred, inDTmpl := in.GetAwsCred(), in.GetDeliveryTemplate()
	if len(strings.TrimSpace(inCred)) == 0 {
		inCred = ""
	} else {
		out += "AWS Credentials,"
	}
	if len(strings.TrimSpace(inDTmpl)) == 0 {
		inDTmpl = ""
	} else {
		out += "DeliveryTemplate,"
	}
	if len(inCred) == 0 && len(inDTmpl) == 0 {
		return nil, status.Error(codes.InvalidArgument, "missing/empty subscription params")
	}

	resume, err := srv.ep.pauseMsgProc(ctx)
	if err != nil {
		return nil, status.Error(codes.DeadlineExceeded, "timed out while waiting for running requests to finish, try again")
	}
	defer resume()

	var doCreate bool
	if err = srv.ep.cachedSubs.withTopic(ctx, topicArn, func(ctx context.Context, sc **subscription) (err error) {
		var uniqPath string
		var tx *sql.Tx
		if tx, err = srv.ep.db.BeginTx(ctx, nil); err != nil {
			return fromContextErrCode(err, codes.Unavailable)
		}
		if err = func() (err error) { // DB update inside a transaction.
			defer func() {
				if err != nil {
					tx.Rollback()
				}
			}()

			var auth uuid.NullUUID
			var ss [3]sql.NullString
			if err = tx.QueryRowContext(ctx, addSubSQL, topicArn, inCred, inDTmpl).Scan(&auth, &ss[0], &ss[1], &ss[2]); err != nil {
				return fromContextErrCode(err, codes.Internal)
			}

			var sub subscription
			if !auth.Valid {
				return status.Errorf(codes.FailedPrecondition, "invalid uuid while parsing auth for: %s", topicArn)
			}
			uniqPath = auth.UUID.String()

			awsId, awsSecret, err := ctrlrpc.DecodeAWSCred(strings.TrimSpace(ss[0].String))
			if err != nil {
				if len(inCred) != 0 { // if sent by rpc
					return status.Errorf(codes.InvalidArgument, "bad aws credentials: %s", err)
				}
				return status.Errorf(codes.FailedPrecondition, "bad aws credentials: %s, recreate the subscription to fix", err)
			} else if sub.subAwsClient, err = makeAWSClient(topicArn, awsId, awsSecret); err != nil {
				return status.Error(codes.FailedPrecondition, err.Error())
			}
			c, s := codes.FailedPrecondition, ", recreate the subscription to fix"
			if len(inDTmpl) != 0 { // if sent by rpc
				c, s = codes.InvalidArgument, ""
			}
			if deliveryTemplate := strings.TrimSpace(ss[1].String); len(deliveryTemplate) == 0 {
				return status.Errorf(c, "empty delivery instruction%s", s)
			} else if sub.DeliveryInstr, err = makeDeliveryInstr(topicArn, deliveryTemplate); err != nil {
				return status.Errorf(c, "%s%s", err, s)
			}
			if ss[2].Valid {
				sub.SubscriptionArn = &ss[2].String
			}

			if err = tx.Commit(); err != nil {
				return fromContextErrCode(err, codes.Unavailable)
			}
			doCreate = *sc == nil || (*sc).SubscriptionArn == nil
			// update the cache.
			*sc = &sub
			return nil
		}(); err != nil {
			return err
		}

		if doCreate && len(inCred) != 0 { // Only if we have creds in the request.
			var conf *epConfig
			if conf, err = validateDBConf(ctx, srv.ep.db, true); err == nil {
				err = (*sc).SubMgr.Subscribe(ctx, topicArn, conf.ExtUrl+uniqPath)
			}
			if err != nil {
				return fromContextErrCode(err, codes.FailedPrecondition)
			}
			out = "Created subscription is pending confirmation(will be confirmed shortly)"
		} else {
			out = fmt.Sprintf("Updated: %s｡", strings.TrimRight(out, ","))
		}
		return nil
	}); err != nil {
		return nil, err
	}

	return &ctrlrpc.Result{Message: out}, nil
}

func (srv *EPRPCServer) RemoveSub(ctx context.Context, in *wrapperspb.StringValue) (*ctrlrpc.Result, error) {
	if srv.ep.epState.Load() != EpStateRunning {
		return nil, status.Error(codes.Aborted, "server not ready, requires valid configuration first")
	}

	topicArn := in.GetValue()
	var force bool
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if len(md.Get("force")) != 0 {
			force = true
		}
	}

	if err := srv.ep.cachedSubs.withTopic(ctx, topicArn, func(_ context.Context, sc **subscription) (err error) {
		if *sc == nil {
			return status.Errorf(codes.NotFound, "subscription for topic %q doesn't exist", topicArn)
		}

		removeQuery := removeUnConfirmedSubSQL
		if force && (*sc).SubscriptionArn != nil {
			if err = (*sc).SubMgr.Unsubscribe(ctx, *(*sc).SubscriptionArn); err != nil {
				return fromContextErrCode(err, codes.FailedPrecondition)
			}
			removeQuery = removeSubSQL
		}
		var r sql.Result
		if r, err = srv.ep.db.ExecContext(ctx, removeQuery, topicArn); err != nil {
			sLog.Warnf("Failed to remove subscription for topic: %s, error: %v", topicArn, err)
			return fromContextErrCode(err, codes.Internal)
		} else if n, _ := r.RowsAffected(); n == 0 {
			if (*sc).SubscriptionArn == nil {
				sLog.Warnw("Subscription cache de-sync: removeUnConfirmedSubSQL didn't update DB but subscriptionArn is empty, probably out-of-band db write", "topicArn", topicArn)
			}
			if !force {
				return status.Errorf(codes.FailedPrecondition, "can't remove confirmed subscription for topic: %q", topicArn)
			}
		}

		// delete subscription.
		*sc = nil
		return nil
	}); err != nil {
		return nil, err
	}

	return &ctrlrpc.Result{Message: "Success."}, nil
}

func (srv *EPRPCServer) GetErrors(in *wrapperspb.StringValue, errstream ctrlrpc.ControlEndpoint_GetErrorsServer) error {
	topicArn := in.GetValue()

	rows, err := srv.ep.db.QueryContext(errstream.Context(), queryErrorsSQL, topicArn)
	if err != nil {
		sLog.Warnf("Failed to query errors table for topic: %s, error: %v", topicArn, err)
		return fromContextErrCode(err, codes.Internal)
	}
	defer rows.Close()

	for rows.Next() {
		var id, errStr string
		if err = rows.Scan(&id, &errStr); err != nil {
			break
		}
		if err = errstream.Send(&ctrlrpc.DeliveryError{Id: id, ErrorString: errStr}); err != nil {
			return err
		}
	}
	if err = rows.Err(); err != nil {
		return fromContextErrCode(err, codes.Internal)
	}

	return nil
}

func (srv *EPRPCServer) GC(context.Context, *emptypb.Empty) (*ctrlrpc.Result, error) {
	if srv.ep.epState.Load() != EpStateRunning {
		return nil, status.Error(codes.Aborted, "server not ready, requires valid configuration first")
	}

	s := "OK."
	select {
	case srv.ep.gcTriggerCh <- true:
		return &ctrlrpc.Result{Message: s}, nil
	default:
		return nil, status.Error(codes.AlreadyExists, "GC is already in progress.")
	}
}
