package events

import (
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go/aws/arn"
	"github.com/aws/aws-sdk-go/aws/endpoints"
)

const (
	SNSSubscribeApiCall   = "Subscribe"
	SNSUnsubscribeApiCall = "Unsubscribe"
	SNSConfirmSubApiCall  = "ConfirmSubscription"
)

type SNSSubRequest struct {
	ApiCall         string  `json:"ApiCall"`
	Token           *string `json:"Token,omitempty"`
	TopicArn        *string `json:"TopicArn,omitempty"`
	HttpEndpoint    *string `json:"HttpEndpoint,omitempty"`
	SubscriptionArn *string `json:"SubscriptionArn,omitempty"`
}

func (req *SNSSubRequest) GetTopicARN() (arn.ARN, error) {
	switch t := req.ApiCall; t {
	case SNSSubscribeApiCall:
		fallthrough
	case SNSConfirmSubApiCall:
		if req.TopicArn == nil {
			return arn.ARN{}, fmt.Errorf("missing required TopicARN for %s request", t)
		}
		if _arn, err := arn.Parse(*req.TopicArn); err != nil {
			return arn.ARN{}, err
		} else if _arn.Service != endpoints.SnsServiceID {
			return arn.ARN{}, fmt.Errorf("%q is not a valid SNS topicARN", *req.TopicArn)
		} else {
			return _arn, nil
		}
	case SNSUnsubscribeApiCall:
		if req.SubscriptionArn == nil {
			return arn.ARN{}, fmt.Errorf("missing required subscriptionArn for %q request", t)
		}
		if _arn, err := arn.Parse(*req.SubscriptionArn); err != nil {
			return arn.ARN{}, err
		} else if s := strings.Split(_arn.Resource, ":"); _arn.Service != endpoints.SnsServiceID || len(s) != 2 {
			return arn.ARN{}, fmt.Errorf("%q is not a valid SNS subscription ARN", *req.SubscriptionArn)
		} else {
			_arn.Resource = s[0]
			return _arn, nil
		}
	default:
		return arn.ARN{}, fmt.Errorf("%q is not a valid request type", t)
	}
}

type SNSSubResponse struct {
	SubscriptionArn *string `json:"SubscriptionArn,omitempty"`
}

func SubscribeInput(TopicArn, endpoint string) SNSSubRequest {
	return SNSSubRequest{
		ApiCall:      SNSSubscribeApiCall,
		TopicArn:     &TopicArn,
		HttpEndpoint: &endpoint,
	}
}

func UnsubscribeInput(SubscriptionArn string) SNSSubRequest {
	return SNSSubRequest{
		ApiCall:         SNSUnsubscribeApiCall,
		SubscriptionArn: &SubscriptionArn,
	}
}

func ConfirmSubscriptionInput(Token, TopicArn string) SNSSubRequest {
	return SNSSubRequest{
		ApiCall:  SNSConfirmSubApiCall,
		TopicArn: &TopicArn,
		Token:    &Token,
	}
}
