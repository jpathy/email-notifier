package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-lambda-go/lambdacontext"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/arn"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/dynamodb"
	"github.com/aws/aws-sdk-go/service/dynamodb/expression"
	"github.com/aws/aws-sdk-go/service/sns"

	conf "github.com/jpathy/email-notifier/aws/lambdaconf"
	subevts "github.com/jpathy/email-notifier/aws/submgr/events"
)

var deliveryPolicy = `{
  "healthyRetryPolicy": {
    "minDelayTarget":     20,
    "maxDelayTarget":     300,
    "numRetries":         19,
    "numNoDelayRetries":  0,
    "numMinDelayRetries": 3,
    "numMaxDelayRetries": 10,
    "backoffFunction":    "exponential"
  },
  "throttlePolicy": {
    "maxReceivesPerSecond": 10
  }
}`

var sess *session.Session

func init() {
	var err error
	if sess, err = session.NewSession(&aws.Config{
		MaxRetries: aws.Int(10),
	}); err != nil {
		log.Panicln("Fatal: couldn't create aws session", err)
	}
}

func snsSubHandler(_ctx context.Context, e subevts.SNSSubRequest) (resp subevts.SNSSubResponse, err error) {
	defer func() {
		if err != nil {
			log.Println("Function Error:", err)
		}
	}()

	lc, ok := lambdacontext.FromContext(_ctx)
	if !ok {
		err = fmt.Errorf("lambda context missing")
		return
	}
	var topicArn string
	var config *conf.Config
	{
		var _arn arn.ARN
		if _arn, err = e.GetTopicARN(); err != nil {
			return
		}
		topicArn = _arn.String()
	}
	if config, err = conf.GetConfig(sess, topicArn); err != nil {
		err = fmt.Errorf("configuration error: %w", err)
		return
	}
	snsClient := sns.New(sess)
	dynamoClient := dynamodb.New(sess)

	deadline, _ := _ctx.Deadline()
	cctx, cancel := context.WithDeadline(_ctx, deadline.Add(-100*time.Millisecond))
	defer cancel()

	switch e.ApiCall {
	case subevts.SNSSubscribeApiCall:
		if e.HttpEndpoint == nil {
			err = fmt.Errorf("missing required Endpoint for %s request", e.ApiCall)
			return
		}
		var _url *url.URL
		if _url, err = url.Parse(*e.HttpEndpoint); err != nil {
			return
		} else if _url.Scheme != "http" && _url.Scheme != "https" {
			err = fmt.Errorf("%q is not a http(s) endpoint", *e.HttpEndpoint)
			return
		}

		// Make sure atmost 1 sub in dynamodb table using conditional put.
		var expr expression.Expression
		if expr, err = expression.NewBuilder().WithCondition(
			expression.Name(conf.DynamoDBEndpointAttr).AttributeNotExists().Or(expression.Name(conf.DynamoDBEndpointAttr).Equal(expression.Value(e.HttpEndpoint))),
		).Build(); err != nil {
			return
		}
		var putRes *dynamodb.PutItemOutput
		if putRes, err = dynamoClient.PutItemWithContext(cctx, &dynamodb.PutItemInput{
			ConditionExpression:       expr.Condition(),
			ExpressionAttributeNames:  expr.Names(),
			ExpressionAttributeValues: expr.Values(),

			TableName: &config.DynamoDbTableName,
			Item: map[string]*dynamodb.AttributeValue{
				conf.DynamoDBHashKeyAttr:  {S: &lc.InvokedFunctionArn},
				conf.DynamoDBRangeKeyAttr: {S: &topicArn},
				conf.DynamoDBEndpointAttr: {S: e.HttpEndpoint},
			},
			ReturnValues: aws.String(dynamodb.ReturnValueAllOld),
		}); err != nil {
			return
		}

		// Create the subscription if subscription isn't confirmed already.
		if len(putRes.Attributes) == 0 || putRes.Attributes[conf.DynamoDBSubArnAttr] == nil || putRes.Attributes[conf.DynamoDBSubArnAttr].S == nil {
			var res *sns.SubscribeOutput
			var dpolicy bytes.Buffer
			if err = json.Compact(&dpolicy, []byte(deliveryPolicy)); err != nil {
				err = fmt.Errorf("invalid constant DeliveryPolicy: %w", err)
				return
			}
			if res, err = snsClient.SubscribeWithContext(cctx, &sns.SubscribeInput{
				TopicArn: &topicArn,
				Protocol: &_url.Scheme,
				Endpoint: e.HttpEndpoint,
				Attributes: aws.StringMap(map[string]string{
					"DeliveryPolicy": dpolicy.String(),
					"RedrivePolicy":  fmt.Sprintf(`{"deadLetterTargetArn":%q}`, config.DlqArn),
				}),
			}); err != nil {
				return
			}

			resp.SubscriptionArn = res.SubscriptionArn
		} else {
			resp.SubscriptionArn = putRes.Attributes[conf.DynamoDBSubArnAttr].S
		}
		return

	case subevts.SNSUnsubscribeApiCall:
		if _, err = snsClient.UnsubscribeWithContext(cctx, &sns.UnsubscribeInput{
			SubscriptionArn: e.SubscriptionArn,
		}); err != nil {
			return
		}

		// subscribe interim lambda to sns topic if doesn't already exist
		// Ideally `subscribe` would be idempotent(we wouldn't need ListSubscriptions), unfortunately it requires the attributes to match exactly or fails with:
		//   "InvalidParameter: Invalid parameter: Attributes Reason: Subscription already exists with different attributes"
		var hasInterim bool
		if err = snsClient.ListSubscriptionsByTopicPagesWithContext(cctx, &sns.ListSubscriptionsByTopicInput{
			TopicArn: &topicArn,
		}, func(ls *sns.ListSubscriptionsByTopicOutput, _ bool) bool {
			for _, s := range ls.Subscriptions {
				if *s.Protocol == "lambda" && *s.Endpoint == config.InterimLambdaArn {
					hasInterim = true
					return false
				}
			}
			return true
		}); err != nil {
			err = fmt.Errorf("failed to query interim lambda subscription, %w", err)
		}
		if !hasInterim {
			if _, err = snsClient.SubscribeWithContext(cctx, &sns.SubscribeInput{
				TopicArn: &topicArn,
				Protocol: aws.String("lambda"),
				Endpoint: &config.InterimLambdaArn,
				Attributes: aws.StringMap(map[string]string{
					"RedrivePolicy": fmt.Sprintf(`{"deadLetterTargetArn":%q}`, config.DlqArn),
				}),
			}); err != nil {
				err = fmt.Errorf("failed to setup interim lambda subscription, %w", err)
				return
			}
		}

		// Remove topicarn from table(idempotent)
		if _, err = dynamoClient.DeleteItemWithContext(cctx, &dynamodb.DeleteItemInput{
			TableName: &config.DynamoDbTableName,
			Key: map[string]*dynamodb.AttributeValue{
				conf.DynamoDBHashKeyAttr:  {S: &lc.InvokedFunctionArn},
				conf.DynamoDBRangeKeyAttr: {S: &topicArn},
			},
		}); err != nil {
			err = fmt.Errorf("failed to remove subscription, %w", err)
		}
		return

	case subevts.SNSConfirmSubApiCall:
		if e.Token == nil {
			err = fmt.Errorf("missing required token for %q request", e.ApiCall)
			return
		}

		var res *sns.ConfirmSubscriptionOutput
		if res, err = snsClient.ConfirmSubscriptionWithContext(cctx, &sns.ConfirmSubscriptionInput{
			AuthenticateOnUnsubscribe: aws.String("true"),
			Token:                     e.Token,
			TopicArn:                  &topicArn,
		}); err != nil {
			return
		}

		// Remove the sns trigger for interimlambda.
		var unsubErr error
		if err = snsClient.ListSubscriptionsByTopicPagesWithContext(cctx, &sns.ListSubscriptionsByTopicInput{
			TopicArn: &topicArn,
		}, func(ls *sns.ListSubscriptionsByTopicOutput, _ bool) bool {
			for _, s := range ls.Subscriptions {
				if *s.Protocol == "lambda" && *s.Endpoint == config.InterimLambdaArn { // found the sub
					_, unsubErr = snsClient.UnsubscribeWithContext(cctx, &sns.UnsubscribeInput{
						SubscriptionArn: s.SubscriptionArn,
					})
					return false
				}
			}
			return true
		}); err != nil {
			return
		}
		if unsubErr != nil {
			err = unsubErr
			log.Println("Error: removing lambda from sns topic failed,", e)
			return
		}

		// set the subArn field for our topic in dynamodb table.
		var expr expression.Expression
		if expr, err = expression.NewBuilder().WithUpdate(
			expression.Set(expression.Name(conf.DynamoDBSubArnAttr), expression.Value(*res.SubscriptionArn)),
		).Build(); err != nil {
			return
		}
		if _, err = dynamoClient.UpdateItemWithContext(cctx, &dynamodb.UpdateItemInput{
			TableName: &config.DynamoDbTableName,
			Key: map[string]*dynamodb.AttributeValue{
				conf.DynamoDBHashKeyAttr:  {S: &lc.InvokedFunctionArn},
				conf.DynamoDBRangeKeyAttr: {S: &topicArn},
			},
			UpdateExpression:          expr.Update(),
			ExpressionAttributeNames:  expr.Names(),
			ExpressionAttributeValues: expr.Values(),
		}); err != nil {
			return
		}

		resp.SubscriptionArn = res.SubscriptionArn
		return
	default:
		err = fmt.Errorf("%q is not a valid request type", e.ApiCall)
		return
	}
}

func main() {
	lambda.Start(snsSubHandler)
}
