package lambdaconf

import (
	"fmt"
	"net/url"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/arn"
	"github.com/aws/aws-sdk-go/aws/endpoints"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/sns"
	"github.com/aws/aws-sdk-go/service/sqs"
)

const (
	DynamoDBHashKeyAttr     = "Arn"
	DynamoDBRangeKeyAttr    = "UniqId"
	DynamoDBIdxRangeKeyAttr = "Timestamp"
	DynamoDBEndpointAttr    = "Endpoint"
	DynamoDBSubArnAttr      = "SubArn"

	CloudwatchEventJsonSNSTopicField = "TopicArn"

	SnsTagKeySubMgrLambda      = "SubMgrLambda"
	SnsTagKeyInterimLambdaSub  = "InterimLambdaArn"
	SnsTagKeyDLQUrl            = "DLQUrl"
	SnsTagKeyDynamoDbTableName = "DynamoDbTable"
)

type Config struct {
	DlqArn            string
	DlqUrl            string
	InterimLambdaArn  string
	SubMgrLambdaArn   string
	DynamoDbTableName string
}

func GetConfig(sess *session.Session, topicArn string) (*Config, error) {
	var c Config
	if tagsOut, err := sns.New(sess).ListTagsForResource(&sns.ListTagsForResourceInput{
		ResourceArn: aws.String(topicArn),
	}); err != nil {
		return nil, fmt.Errorf("couldn't retrieve tags for topicArn: %s, %w", topicArn, err)
	} else {
		for _, t := range tagsOut.Tags {
			if t.Key == nil || t.Value == nil {
				continue
			}
			switch *t.Key {
			case SnsTagKeySubMgrLambda:
				c.SubMgrLambdaArn = *t.Value
			case SnsTagKeyInterimLambdaSub:
				c.InterimLambdaArn = *t.Value
			case SnsTagKeyDLQUrl:
				c.DlqUrl = *t.Value
			case SnsTagKeyDynamoDbTableName:
				c.DynamoDbTableName = *t.Value
			}
		}
	}

	checkServiceArn := func(arnStr string, svc string) (arn.ARN, error) {
		if _arn, err := arn.Parse(arnStr); err != nil {
			return arn.ARN{}, err
		} else if _arn.Region != *sess.Config.Region {
			return arn.ARN{}, fmt.Errorf("lambda must be executed in same region as resource: %s", arnStr)
		} else if _arn.Service != svc {
			return arn.ARN{}, fmt.Errorf("%s is not a valid %s ARN", arnStr, svc)
		} else {
			return _arn, nil
		}
	}

	if _, err := url.Parse(c.DlqUrl); err != nil {
		return nil, err
	} else if queueAttr, err := sqs.New(sess).GetQueueAttributes(&sqs.GetQueueAttributesInput{
		QueueUrl:       &c.DlqUrl,
		AttributeNames: aws.StringSlice([]string{sqs.QueueAttributeNameQueueArn}),
	}); err != nil {
		return nil, err
	} else {
		c.DlqArn = *queueAttr.Attributes[sqs.QueueAttributeNameQueueArn]
	}
	if _, err := checkServiceArn(c.InterimLambdaArn, endpoints.LambdaServiceID); err != nil {
		return nil, err
	}
	if _, err := checkServiceArn(c.SubMgrLambdaArn, endpoints.LambdaServiceID); err != nil {
		return nil, err
	}
	if len(c.DynamoDbTableName) == 0 {
		return nil, fmt.Errorf("dynamodb table name is empty")
	}

	return &c, nil
}
