package aws

import (
	"encoding/json"
	"fmt"
	"log"

	"github.com/aws/aws-sdk-go/service/dynamodb"
	conf "github.com/jpathy/email-notifier/aws/lambdaconf"
)

func ConvertSNSToDbItem(sns map[string]interface{}) map[string]*dynamodb.AttributeValue {
	if sns == nil {
		return nil
	}
	item := make(map[string]*dynamodb.AttributeValue)
	if s, _ := sns["Type"].(string); s != "Notification" {
		log.Printf("Error: ignoring SNS event of type: %s, only recording notifications\n", s)
		return nil
	}
	getKey := func(key string) (string, bool) {
		if vArn, ok := sns[key]; !ok {
			log.Printf("Error: sns event record missing %s\n", key)
			return "", ok
		} else if s, ok := vArn.(string); !ok {
			log.Printf("Error: %s doesn't have a string value\n", key)
			return "", ok
		} else {
			return s, true
		}
	}
	for k, v := range sns {
		switch k {
		case "UnsubscribeUrl": // lambda sns message with different casing
		case "UnsubscribeURL": // Not written to db
		case "TopicArn":
			arn, ok := getKey("TopicArn")
			if !ok {
				return nil
			}
			item[conf.DynamoDBHashKeyAttr] = &dynamodb.AttributeValue{S: &arn}
		case "MessageId":
			uniqId, ok := getKey("MessageId")
			if !ok {
				return nil
			}
			item[conf.DynamoDBRangeKeyAttr] = &dynamodb.AttributeValue{S: &uniqId}
		case "Timestamp":
			ts, ok := getKey("Timestamp")
			if !ok {
				return nil
			}
			item[conf.DynamoDBIdxRangeKeyAttr] = &dynamodb.AttributeValue{S: &ts}
		default:
			if k == "SigningCertUrl" {
				k = "SigningCertURL"
			}
			if s, ok := v.(string); ok {
				item[k] = &dynamodb.AttributeValue{S: &s}
			}
		}
	}
	return item
}

func ConvertDbItemToJSON(item map[string]*dynamodb.AttributeValue) ([]byte, error) {
	if len(item) == 0 {
		return nil, fmt.Errorf("item is missing")
	} else if v := item["Type"]; v == nil || v.S == nil || *v.S != "Notification" {
		return nil, fmt.Errorf("item is not a SNS notification message")
	}
	m := make(map[string]string)
	for k, v := range item {
		if v == nil || v.S == nil {
			continue
		}
		switch k {
		case conf.DynamoDBHashKeyAttr:
			k = "TopicArn"
		case conf.DynamoDBRangeKeyAttr:
			k = "MessageId"
		case conf.DynamoDBIdxRangeKeyAttr:
			k = "Timestamp"
		}
		m[k] = *v.S
	}
	return json.Marshal(&m)
}
