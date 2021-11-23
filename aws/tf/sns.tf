resource "aws_sns_topic" "ses_sns_topic" {
  name = "${var.stack}-ses-s3"
  tags = {
    (module.gen.sns_topic_tag_key_dlq_url)            = aws_sqs_queue.sns_sub_DLQ.url
    (module.gen.sns_topic_tag_key_submgr_arn)         = aws_lambda_function.submgr.arn
    (module.gen.sns_topic_tag_key_interim_lambda_arn) = aws_lambda_function.interim_sub.arn
    (module.gen.sns_topic_tag_key_dynamodb_table)     = aws_dynamodb_table.notifier_db.id
  }
}

data "external" "check_http_sub" {
  program = ["bash", "${path.module}/check_sub.sh"]
  query = {
    partiql_stmt = format(
      "SELECT * FROM \"%s\" WHERE %s='%s' AND %s='%s'",
      aws_dynamodb_table.notifier_db.id,
      module.gen.dynamodb_table_hashkey_attrname,
      aws_lambda_function.submgr.arn,
      module.gen.dynamodb_table_rangekey_attrname,
      aws_sns_topic.ses_sns_topic.arn
    )
    region = var.aws_region
  }
}

resource "aws_sns_topic_subscription" "sns_interim_sub" {
  // Don't create if we have a https subscription created(and hence interim sub removed) by submgr.
  count = tonumber(data.external.check_http_sub.result.count) > 0 ? 0 : 1

  topic_arn = aws_sns_topic.ses_sns_topic.arn
  protocol  = "lambda"
  endpoint  = aws_lambda_function.interim_sub.arn
  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.sns_sub_DLQ.arn
  })
}
