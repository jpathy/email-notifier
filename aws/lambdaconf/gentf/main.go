package main

import (
	"fmt"
	"os"
	"path/filepath"

	conf "github.com/jpathy/email-notifier/aws/lambdaconf"
)

const (
	genDir     = "gen_consts"
	outputName = "outputs.tf"
)

func main() {
	if len(os.Args) == 1 {
		fmt.Fprintln(os.Stderr, "required argument(dirpath for tf module) is missing")
		return
	}

	dir := filepath.Join(os.Args[1], genDir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "failed to create module dir: %s, %s\n", dir, err)
		return
	}

	fp := filepath.Join(dir, outputName)
	if err := os.WriteFile(
		fp,
		[]byte(fmt.Sprintf(`# GENERATE FILE. DO NOT EDIT.
output "dynamodb_table_hashkey_attrname" {
  value = %q
}

output "dynamodb_table_rangekey_attrname" {
  value = %q
}

output "dynamodb_idx_rangekey_attrname" {
  value = %q
}

output "sns_topic_tag_key_dlq_url" {
  value = %q
}

output "sns_topic_tag_key_submgr_arn" {
	value = %q
}

output "sns_topic_tag_key_interim_lambda_arn" {
  value = %q
}

output "sns_topic_tag_key_dynamodb_table" {
  value = %q
}

output "cloudwatch_json_sns_topic_field" {
  value = %q
}
`,
			conf.DynamoDBHashKeyAttr, conf.DynamoDBRangeKeyAttr, conf.DynamoDBIdxRangeKeyAttr,
			conf.SnsTagKeyDLQUrl, conf.SnsTagKeySubMgrLambda, conf.SnsTagKeyInterimLambdaSub, conf.SnsTagKeyDynamoDbTableName,
			conf.CloudwatchEventJsonSNSTopicField)),
		0644); err != nil {
		fmt.Fprintf(os.Stderr, "failed to generate file: %s, %s\n", fp, err)
	}
}
