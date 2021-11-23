package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awsutil"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/dynamodb"

	myAws "github.com/jpathy/email-notifier/aws"
	conf "github.com/jpathy/email-notifier/aws/lambdaconf"
)

var sess *session.Session

func init() {
	var err error
	if sess, err = session.NewSession(&aws.Config{
		MaxRetries: aws.Int(10),
	}); err != nil {
		log.Panicln("Fatal: couldn't create aws session", err)
	}
}

func snsMsgHandler(_ctx context.Context, e struct {
	Records []struct {
		EventVersion string                 `json:"EventVersion"`
		SNS          map[string]interface{} `json:"Sns"`
	} `json:"Records"`
}) (err error) {
	deadline, _ := _ctx.Deadline()
	cctx, cancel := context.WithDeadline(_ctx, deadline.Add(-100*time.Millisecond))
	defer func() {
		cancel()
		if err != nil {
			log.Println("Function Error:", err)
		}
	}()

	// This should loop atmost once.
	for _, r := range e.Records {
		if r.EventVersion != "1.0" {
			log.Printf("Warn: EventVersion = %s, expected 1.0\n", r.EventVersion)
		}
		item := myAws.ConvertSNSToDbItem(r.SNS)
		if item == nil {
			log.Println("Warn: Failed to parse SNS message:", awsutil.Prettify(r.SNS))
			continue
		}

		var config *conf.Config
		if config, err = conf.GetConfig(sess, r.SNS["TopicArn"].(string)); err != nil {
			err = fmt.Errorf("configuration error: %w", err)
			return
		}
		if _, err = dynamodb.New(sess).PutItemWithContext(cctx, &dynamodb.PutItemInput{
			TableName: &config.DynamoDbTableName,
			Item:      item,
		}); err != nil {
			return
		}
	}

	err = nil
	return
}

func main() {
	lambda.Start(snsMsgHandler)
}
