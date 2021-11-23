# GENERATE FILE. DO NOT EDIT.
output "dynamodb_table_hashkey_attrname" {
  value = "Arn"
}

output "dynamodb_table_rangekey_attrname" {
  value = "UniqId"
}

output "dynamodb_idx_rangekey_attrname" {
  value = "Timestamp"
}

output "sns_topic_tag_key_dlq_url" {
  value = "DLQUrl"
}

output "sns_topic_tag_key_submgr_arn" {
	value = "SubMgrLambda"
}

output "sns_topic_tag_key_interim_lambda_arn" {
  value = "InterimLambdaArn"
}

output "sns_topic_tag_key_dynamodb_table" {
  value = "DynamoDbTable"
}

output "cloudwatch_json_sns_topic_field" {
  value = "TopicArn"
}
