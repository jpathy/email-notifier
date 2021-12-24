//go:build sqlite_json1 && sqlite_foreign_keys
// +build sqlite_json1,sqlite_foreign_keys

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"

	lambdaInvoke "github.com/aws/aws-lambda-go/lambda/messages"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/arn"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/awsutil"
	awsclient "github.com/aws/aws-sdk-go/aws/client"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/endpoints"
	awsreq "github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/kms"
	"github.com/aws/aws-sdk-go/service/lambda"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/s3/s3crypto"
	"github.com/aws/aws-sdk-go/service/s3/s3iface"
	"github.com/aws/aws-sdk-go/service/sns"

	"github.com/jpathy/email-notifier/aws/lambdaconf"
	subevts "github.com/jpathy/email-notifier/aws/submgr/events"
)

// s3ObjReader

type objReader struct {
	c      *s3ObjClient
	ctx    context.Context
	bucket string
	key    string

	body io.ReadCloser
}

func NewObjReader(ctx context.Context, s3Client *s3ObjClient, bucket, key string) *objReader {
	return &objReader{
		c:      s3Client,
		ctx:    ctx,
		bucket: bucket,
		key:    key,
	}
}

func (r *objReader) Read(p []byte) (n int, err error) {
Retry:
	if r.body == nil {
		if r.body, err = r.request(); err != nil {
			return
		}
	}
	n, err = r.body.Read(p)
	if err != nil {
		var errNet net.Error
		if errors.As(err, &errNet) && (errNet.Temporary() || errNet.Timeout()) {
			r.body = nil
			goto Retry
		}
		return
	}

	return
}

func (r *objReader) Close() (err error) {
	if r.body != nil {
		err = r.body.Close()
		r.body = nil
	}
	return
}

func (r *objReader) request() (io.ReadCloser, error) {
	var out *s3.GetObjectOutput
	var err error
	req := &s3.GetObjectInput{
		Bucket: &r.bucket,
		Key:    &r.key,
	}
	if out, err = r.c.GetObjectWithContext(r.ctx, req); err != nil {
		return nil, err
	}

	return out.Body, nil
}

// AWS Client per subscription
type subAwsClient struct {
	SubMgr   subManager
	S3Client *s3ObjClient
}

func makeAWSClient(topicArn, awsId, awsSecret string) (awsClient subAwsClient, err error) {
	var awsRegion string
	{
		var _arn arn.ARN
		if _arn, err = arn.Parse(topicArn); err != nil {
			err = fmt.Errorf("couldn't parse topicARN, error: %w", err)
			return
		}
		awsRegion = _arn.Region
	}

	var sess *session.Session
	if sess, err = session.NewSession(&aws.Config{
		Region:      &awsRegion,
		Credentials: credentials.NewStaticCredentials(awsId, awsSecret, ""),
		MaxRetries:  aws.Int(httpOutgoingMaxRetry),
	}); err != nil {
		return
	}

	cr := s3crypto.NewCryptoRegistry()
	if err = s3crypto.RegisterKMSWrapWithAnyCMK(cr, kms.New(sess)); err != nil {
		return
	}
	if err = s3crypto.RegisterAESGCMContentCipher(cr); err != nil {
		return
	}
	cr.AddWrap(unencryptedWrapAlg, func(env s3crypto.Envelope) (s3crypto.CipherDataDecrypter, error) {
		return sesUnencryptedDecrypter{}, nil
	})
	cr.AddCEK(unencryptedCEKAlg, func(cd s3crypto.CipherData) (s3crypto.ContentCipher, error) {
		return sesUnencryptedContent{}, nil
	})

	var s3Api s3iface.S3API
	var decrypter *s3crypto.DecryptionClientV2
	if decrypter, err = s3crypto.NewDecryptionClientV2(sess, cr, func(o *s3crypto.DecryptionClientOptions) {
		o.LoadStrategy = defaultLoadStrategy{}
		s3Api = o.S3Client
	}); err != nil {
		return
	}

	awsClient = subAwsClient{
		SubMgr:   subManager{configProvider: sess},
		S3Client: &s3ObjClient{decrypter, s3Api},
	}
	return
}

type subManager struct {
	configProvider awsclient.ConfigProvider
	funcArn        string
}

func (sub *subManager) call(ctx context.Context, input subevts.SNSSubRequest) (resp subevts.SNSSubResponse, err error) {
	if len(sub.funcArn) == 0 {
		var topicArn arn.ARN
		if topicArn, err = input.GetTopicARN(); err != nil {
			return
		}
		var tagsOut *sns.ListTagsForResourceOutput
		if tagsOut, err = sns.New(sub.configProvider).ListTagsForResource(&sns.ListTagsForResourceInput{
			ResourceArn: aws.String(topicArn.String()),
		}); err != nil {
			return
		}
		for _, t := range tagsOut.Tags {
			if t.Key == nil || t.Value == nil || *t.Key != lambdaconf.SnsTagKeySubMgrLambda {
				continue
			}
			funcArn := strings.TrimSpace(*t.Value)
			var _arn arn.ARN
			if _arn, err = arn.Parse(funcArn); err != nil {
				err = fmt.Errorf("couldn't parse sns topic tag value for %q, error: %w", *t.Key, err)
				return
			} else if _arn.Service != endpoints.LambdaServiceID {
				err = fmt.Errorf("topic tag %q's value is not a valid lambda ARN", *t.Key)
				return
			} else if _arn.Region != topicArn.Region {
				err = fmt.Errorf("submanager lambda must belong to the same region as topicArn")
				return
			}
			sub.funcArn = funcArn
			break
		}
	}

	var bs []byte
	if bs, err = json.Marshal(input); err != nil {
		return
	}

	var lRes *lambda.InvokeOutput
	if lRes, err = lambda.New(sub.configProvider).InvokeWithContext(ctx, &lambda.InvokeInput{
		FunctionName: &sub.funcArn,
		Payload:      bs,
	}); err != nil {
		return
	}
	if lRes.FunctionError != nil {
		var respErr lambdaInvoke.InvokeResponse_Error
		var errStr string
		if json.Unmarshal(lRes.Payload, &respErr) == nil {
			errStr = awsutil.Prettify(respErr)
		} else {
			errStr = string(lRes.Payload)
		}
		err = fmt.Errorf("subscription lambda error, %s: %s", *lRes.FunctionError, errStr)
		return
	}
	if err = json.Unmarshal(lRes.Payload, &resp); err != nil {
		err = fmt.Errorf("failed to parse function result as json object, error: %w", err)
		return
	}

	return resp, nil
}

func (sub *subManager) Subscribe(ctx context.Context, topicArn string, epUrl string) error {
	if _, err := sub.call(ctx, subevts.SubscribeInput(topicArn, epUrl)); err != nil {
		return err
	} else {
		return nil
	}
}

func (sub *subManager) Unsubscribe(ctx context.Context, subArn string) error {
	if _, err := sub.call(ctx, subevts.UnsubscribeInput(subArn)); err != nil {
		return err
	}
	return nil
}

func (sub *subManager) ConfirmSubscription(ctx context.Context, topicArn, token string) (*string, error) {
	if resp, err := sub.call(ctx, subevts.ConfirmSubscriptionInput(token, topicArn)); err != nil {
		return nil, err
	} else {
		return resp.SubscriptionArn, nil
	}
}

type s3ObjClient struct {
	decrypter *s3crypto.DecryptionClientV2
	apiClient s3iface.S3API
}

func (obj *s3ObjClient) GetObjectWithContext(ctx aws.Context, input *s3.GetObjectInput, options ...awsreq.Option) (*s3.GetObjectOutput, error) {
	return obj.decrypter.GetObjectWithContext(ctx, input, options...)
}

func (obj *s3ObjClient) DeleteObjectsWithContext(ctx aws.Context, input *s3.DeleteObjectsInput, options ...awsreq.Option) (*s3.DeleteObjectsOutput, error) {
	return obj.apiClient.DeleteObjectsWithContext(ctx, input, options...)
}

func (obj *s3ObjClient) GetBucketVersioningWithContext(ctx aws.Context, input *s3.GetBucketVersioningInput, options ...awsreq.Option) (*s3.GetBucketVersioningOutput, error) {
	return obj.apiClient.GetBucketVersioningWithContext(ctx, input, options...)
}

// s3crypto: Add support for unencrypted reader

const (
	metaHeader  = "x-amz-meta"
	keyV1Header = "x-amz-key"
	keyV2Header = keyV1Header + "-v2"

	unencryptedWrapAlg = "unenc"
	unencryptedCEKAlg  = "UnEnc/NoPadding"
)

type defaultLoadStrategy struct{}

func (defaultLoadStrategy) Load(req *awsreq.Request) (s3crypto.Envelope, error) {
	if value := req.HTTPResponse.Header.Get(strings.Join([]string{metaHeader, keyV2Header}, "-")); value != "" {
		strat := s3crypto.HeaderV2LoadStrategy{}
		return strat.Load(req)
	} else if value = req.HTTPResponse.Header.Get(strings.Join([]string{metaHeader, keyV1Header}, "-")); value != "" {
		return s3crypto.Envelope{}, awserr.New("V1NotSupportedError", "The AWS SDK for Go does not support version 1", nil)
	} else {
		return s3crypto.Envelope{WrapAlg: "unenc", CEKAlg: unencryptedCEKAlg}, nil
	}
}

type sesUnencryptedDecrypter struct{}

func (sesUnencryptedDecrypter) DecryptKey(_ []byte) ([]byte, error) {
	return nil, nil
}

type sesUnencryptedContent struct{}

func (sesUnencryptedContent) EncryptContents(r io.Reader) (io.Reader, error) {
	return r, nil
}

func (sesUnencryptedContent) DecryptContents(r io.ReadCloser) (io.ReadCloser, error) {
	return r, nil
}

func (sesUnencryptedContent) GetCipherData() s3crypto.CipherData {
	return s3crypto.CipherData{}
}
