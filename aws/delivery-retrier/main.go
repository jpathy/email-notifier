package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awsutil"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/dynamodb"
	"github.com/aws/aws-sdk-go/service/dynamodb/expression"
	"github.com/aws/aws-sdk-go/service/sqs"

	myAws "github.com/jpathy/email-notifier/aws"
	conf "github.com/jpathy/email-notifier/aws/lambdaconf"
)

var (
	sess     *session.Session
	logDebug bool
)

func debugfn(format string, v ...interface{}) {
	if logDebug {
		log.Printf("DEBUG: %s\n", fmt.Sprintf(format, v...))
	}
}

func init() {
	var err error
	if os.Getenv("DEBUGLOG") == "1" {
		logDebug = true
	}
	if sess, err = session.NewSession(&aws.Config{
		MaxRetries: aws.Int(10),
	}); err != nil {
		log.Panicln("Fatal: couldn't create aws session", err)
	}
}

func cronEventHandler(_ctx context.Context, e map[string]string) (err error) {
	deadline, _ := _ctx.Deadline()
	cctx, cancel := context.WithDeadline(_ctx, deadline.Add(-100*time.Millisecond))
	defer func() {
		cancel()
		if err != nil {
			log.Printf("Function Error: %s\n", err)
		}
	}()

	var topicArn string
	if topicArn = e[conf.CloudwatchEventJsonSNSTopicField]; len(topicArn) == 0 {
		err = fmt.Errorf("failed to get %q from cron rule event", conf.CloudwatchEventJsonSNSTopicField)
		return
	}
	topicArn = strings.TrimSpace(topicArn)

	var config *conf.Config
	if config, err = conf.GetConfig(sess, topicArn); err != nil {
		err = fmt.Errorf("configuration error: %w", err)
		return
	}
	dynamoClient := dynamodb.New(sess)
	sqsClient := sqs.New(sess)

	var epUrl, subArn, dynamoDbIdxName string

	getSubCh := make(chan struct{})
	// do a parallel request to retrieve db index name & current subscription url,subArn.
	go func() {
		defer func() {
			getSubCh <- struct{}{}
		}()

		if tabDesc, e := dynamoClient.DescribeTableWithContext(cctx, &dynamodb.DescribeTableInput{
			TableName: &config.DynamoDbTableName,
		}); e != nil {
			return
		} else {
		idxLoop:
			for _, idxDesc := range tabDesc.Table.LocalSecondaryIndexes {
				for _, keysc := range idxDesc.KeySchema {
					if *keysc.KeyType == dynamodb.KeyTypeRange && *keysc.AttributeName == conf.DynamoDBIdxRangeKeyAttr {
						dynamoDbIdxName = *idxDesc.IndexName
						break idxLoop
					}
				}
			}
		}

		if itemRes, e := dynamoClient.GetItemWithContext(cctx, &dynamodb.GetItemInput{
			ConsistentRead: aws.Bool(true),
			TableName:      &config.DynamoDbTableName,
			Key: map[string]*dynamodb.AttributeValue{
				conf.DynamoDBHashKeyAttr:  {S: &config.SubMgrLambdaArn},
				conf.DynamoDBRangeKeyAttr: {S: &topicArn},
			},
		}); e != nil {
			log.Printf("Error: failed to retrieve subscriber for topic: %s from table: %s, %s\n", topicArn, config.DynamoDbTableName, err)
		} else if len(itemRes.Item) != 0 {
			if s := itemRes.Item[conf.DynamoDBSubArnAttr]; s != nil && s.S != nil {
				subArn = *s.S
			}
			if s := itemRes.Item[conf.DynamoDBEndpointAttr]; s != nil && s.S != nil {
				epUrl = *s.S
			}
			debugfn("Found subscription, Endpoint: %s, SubArn: %s", epUrl, subArn)
		}
	}()

	// Long-poll the DLQ for messages, process(store into dynamoDB) and delete processed messages from the queue.
	var msgs *sqs.ReceiveMessageOutput
	if msgs, err = sqsClient.ReceiveMessageWithContext(cctx, &sqs.ReceiveMessageInput{
		MaxNumberOfMessages: aws.Int64(10),
		QueueUrl:            &config.DlqUrl,
		WaitTimeSeconds:     aws.Int64(11), // Max 20secs.
	}); err != nil {
		return
	}
	var toDeleteIdx []int
	for i, m := range msgs.Messages {
		if m == nil || m.Body == nil {
			continue
		}
		debugfn("Retrieved message from SQS, id: %s, MD5Body: %s", *m.MessageId, *m.MD5OfBody)

		// This is the failed event that is sent to DLQ, if interim-lambda failed to process it.
		var lambdaFailureEv struct {
			Version        string `json:"version"`
			RequestPayload struct {
				Records []struct {
					EventVersion string                 `json:"EventVersion"`
					SNS          map[string]interface{} `json:"Sns"`
				} `json:"Records"`
			} `json:"requestPayload"`
			ResponsePayload struct {
				ErrorMessage string `json:"errorMessage"`
			} `json:"responsePayload"`
		}
		var item map[string]*dynamodb.AttributeValue
		if err = json.Unmarshal([]byte(*m.Body), &lambdaFailureEv); err == nil && len(lambdaFailureEv.Version) != 0 {
			// Records array should contain atmost 1 event.
			if n := len(lambdaFailureEv.RequestPayload.Records); n == 1 {
				debugfn("SQS message contains failed lambda event(version=%s) with error: %s", lambdaFailureEv.Version, lambdaFailureEv.ResponsePayload.ErrorMessage)
				item = myAws.ConvertSNSToDbItem(lambdaFailureEv.RequestPayload.Records[0].SNS)
			} else if n > 1 {
				log.Println("Warn: violating assumption that sns event received by lambda has atmost 1 record")
			}
		} else { // A single sns message sent to DLQ because of failed delivery
			var snsObj map[string]interface{}
			if err = json.Unmarshal([]byte(*m.Body), &snsObj); err != nil {
				log.Printf("Error: failed to parse message: %s from DLQ, %s\n", *m.MessageId, err)
			} else {
				debugfn("SQS message contains SNS message")
				item = myAws.ConvertSNSToDbItem(snsObj)
			}
		}
		if item != nil {
			if _, err = dynamoClient.PutItemWithContext(cctx, &dynamodb.PutItemInput{
				TableName: &config.DynamoDbTableName,
				Item:      item,
			}); err != nil {
				log.Printf("Error: failed to PutItem for sqs message: %s to the dynamodb table, %s\n", *m.MessageId, err)
			} else { // Ready to be deleted from queue
				toDeleteIdx = append(toDeleteIdx, i)
			}
		} else { // Change its visibility so we don't keep receiving same invalid messages
			visTimeout := int64(21600) // 6hrs
			log.Printf("Warn: Couldn't convert message: %s to a dynamoDB item, setting message visibility to %dsecs\n", *m.MessageId, visTimeout)
			if _, err = sqsClient.ChangeMessageVisibilityWithContext(cctx, &sqs.ChangeMessageVisibilityInput{
				QueueUrl:          &config.DlqUrl,
				ReceiptHandle:     m.ReceiptHandle,
				VisibilityTimeout: aws.Int64(visTimeout),
			}); err != nil {
				log.Println("Warn: failed to change sqs message visibility,", err)
			}
		}
	}
	var sqsDelMsgs []*sqs.DeleteMessageBatchRequestEntry
	for _, idx := range toDeleteIdx {
		debugfn("Deleting SQS message id: %s", *msgs.Messages[idx].MessageId)
		sqsDelMsgs = append(sqsDelMsgs, &sqs.DeleteMessageBatchRequestEntry{
			Id:            msgs.Messages[idx].MessageId,
			ReceiptHandle: msgs.Messages[idx].ReceiptHandle,
		})
	}
	if len(sqsDelMsgs) > 0 {
		if _, err = sqsClient.DeleteMessageBatchWithContext(cctx, &sqs.DeleteMessageBatchInput{
			QueueUrl: &config.DlqUrl,
			Entries:  sqsDelMsgs,
		}); err != nil {
			log.Println("Error: sqs delete failed for written db entries", err)
		}
	}

	// Wait for goroutine.
	<-getSubCh

	if len(dynamoDbIdxName) == 0 {
		err = fmt.Errorf("failed to find local secondary index with range key: %q for table: %q", conf.DynamoDBIdxRangeKeyAttr, config.DynamoDbTableName)
		return
	}
	if len(epUrl) == 0 || len(subArn) == 0 { // No valid subscription -> done
		debugfn("No http(s) subscription for: %q found in the table: %q", topicArn, config.DynamoDbTableName)
		err = nil
		return
	}

	// Query items for topicArn using the index(sorted (ascending)order on Timestamp).
	var expr expression.Expression
	if expr, err = expression.NewBuilder().WithKeyCondition(
		expression.KeyEqual(expression.Key(conf.DynamoDBHashKeyAttr), expression.Value(topicArn)),
	).Build(); err != nil {
		return
	}

	// We do 10 requests at a time with a delay between batches.
	// 3 * 20(http post timeout) + Poll-time above + ε < lambda deadline(2mins)
	const batchSize = 10
	for i := 0; i < 3; i++ { // Limit * 3 items read from table per run.
		var queryRes *dynamodb.QueryOutput
		if queryRes, err = dynamoClient.QueryWithContext(cctx, &dynamodb.QueryInput{
			TableName: &config.DynamoDbTableName,
			IndexName: &dynamoDbIdxName,
			Select:    aws.String(dynamodb.SelectAllAttributes),
			Limit:     aws.Int64(batchSize),

			KeyConditionExpression:    expr.KeyCondition(),
			ExpressionAttributeNames:  expr.Names(),
			ExpressionAttributeValues: expr.Values(),
		}); err != nil {
			return
		}

		var success int64
		var wg sync.WaitGroup
		for _, q := range queryRes.Items {
			q := q // bad lang-design mitigation
			if len(q) == 0 {
				continue
			}

			// Do a POST(with json item and headers) to the endpoint url just like sns would do.
			wg.Add(1)
			go func() {
				var lerr error
				var msgId string
				var delOk bool
				defer func() {
					if len(msgId) != 0 && delOk {
						if _, lerr = dynamoClient.DeleteItemWithContext(cctx, &dynamodb.DeleteItemInput{
							TableName: &config.DynamoDbTableName,
							Key: map[string]*dynamodb.AttributeValue{
								conf.DynamoDBHashKeyAttr:  {S: &topicArn},
								conf.DynamoDBRangeKeyAttr: {S: &msgId},
							},
						}); lerr != nil {
							log.Println("Error:", lerr)
						} else {
							log.Printf("Info: sns message %s: %s deleted from table\n", conf.DynamoDBRangeKeyAttr, msgId)
						}
					}
					wg.Done()
				}()

				// range key has the MessageId.
				if p := q[conf.DynamoDBRangeKeyAttr]; p != nil && p.S != nil {
					msgId = *p.S
				}
				if len(msgId) == 0 { // Bad messageId => Done.
					log.Printf("Error: MessageId is empty for item: %s\n", awsutil.Prettify(q))
					return
				}

				var bs []byte
				if bs, lerr = myAws.ConvertDbItemToJSON(q); lerr != nil {
					// Bad item => delete
					delOk = true
					debugfn("Bad item: %s,\n error: %s", awsutil.Prettify(q), lerr)
					return
				}

				// Max 20 seconds(15-sec is typical SNS deadline).
				httpCtx, cancel := context.WithTimeout(cctx, 20*time.Second)
				defer cancel()

				req, lerr := http.NewRequestWithContext(httpCtx, http.MethodPost, epUrl, bytes.NewReader(bs))
				if lerr != nil {
					return
				}
				req.Header.Set("x-amz-sns-message-type", "Notification")
				req.Header.Set("x-amz-sns-message-id", msgId)
				req.Header.Set("x-amz-sns-topic-arn", topicArn)
				req.Header.Set("x-amz-sns-subscription-arn", subArn)
				resp, lerr := http.DefaultClient.Do(req)
				if lerr != nil {
					return
				}
				defer resp.Body.Close()

				if resp.StatusCode >= 500 { // 5xx error => retry the next time lambda runs.
					debugfn("POST request to %q failed, response status: %s, message %s will be retried on next run", resp.Request.URL.String(), resp.Status, msgId)
					return
				} else if resp.StatusCode == http.StatusUnauthorized && len(resp.Header.Get("WWW-Authenticate")) != 0 {
					// Assumption: No basic/digest auth.
					log.Printf("Warn: Basic/digest auth is not supported: %q, messages will be lost\n", epUrl)
					return
				}
				// 2xx/4xx => considered successful delivery.
				delOk = true
				atomic.AddInt64(&success, 1) // success += 1
				log.Printf("Info: sns message: %s delivered to: %q, with response status: %s\n", msgId, epUrl, resp.Status)
			}()
		}
		wg.Wait()

		debugfn("Delivery successful for %d/%d messages", success, len(queryRes.Items))
		if success < int64(len(queryRes.Items)) {
			debugfn("Assuming subscription endpoint is busy, terminating this run")
			break
		}
		if len(queryRes.Items) < batchSize { // Received less than Limit, no need to continue next batch.
			break
		}
		select {
		case <-cctx.Done():
		case <-time.After(time.Second):
		}
	}

	err = nil
	return
}

func main() {
	lambda.Start(cronEventHandler)
}
