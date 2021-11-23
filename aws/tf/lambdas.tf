data "aws_iam_policy_document" "lambda_policy_data" {
  statement {
    sid     = "cloudwatchLogs"
    actions = ["logs:CreateLogStream", "logs:PutLogEvents"]
    resources = [
      "${aws_cloudwatch_log_group.submgr_loggroup.arn}:*",
      "${aws_cloudwatch_log_group.interim_sub_loggroup.arn}:*",
      "${aws_cloudwatch_log_group.retrier_loggroup.arn}:*"
    ]
  }
  statement {
    sid = "dlqGetAttrSendReceiveDel"
    actions = [
      "sqs:GetQueueAttributes",
      "sqs:SendMessage",
      "sqs:ReceiveMessage",
      "sqs:DeleteMessage*",
      "sqs:ChangeMessageVisibility"
    ]
    resources = [aws_sqs_queue.sns_sub_DLQ.arn]
  }
  statement {
    sid     = "dynamodbFullAccess"
    actions = ["dynamodb:*"]
    resources = [
      aws_dynamodb_table.notifier_db.arn,
      "${aws_dynamodb_table.notifier_db.arn}/index/${local.idx_name}"
    ]
  }
  statement {
    sid = "snsSubCreateList"
    actions = [
      "sns:Subscribe",
      "sns:ConfirmSubscription",
      "sns:ListSubscriptionsByTopic",
      "sns:ListTagsForResource"
    ]
    resources = [aws_sns_topic.ses_sns_topic.arn]
  }
  statement {
    sid = "snsSubDelete"
    actions = [
      "sns:Unsubscribe",
    ]
    resources = ["*"]
  }
}

resource "aws_iam_policy" "lambda_policy" {
  name_prefix = "${var.stack}-lambda-"
  path        = "/Internal/lambda-roles/"
  description = "role policy attached to role assumed by lambdas in ${var.stack} stack"
  policy      = data.aws_iam_policy_document.lambda_policy_data.json
}

data "aws_iam_policy_document" "lambda_assume_role_policy_data" {
  statement {
    actions = ["sts:AssumeRole"]

    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "lambda_role" {
  name_prefix        = "${var.stack}-lambda-"
  path               = "/Internal/lambda-roles/"
  description        = "role for all lambdas used by ${var.stack} stack"
  assume_role_policy = data.aws_iam_policy_document.lambda_assume_role_policy_data.json
}

resource "aws_iam_role_policy_attachment" "lambda_role_attach" {
  role       = aws_iam_role.lambda_role.name
  policy_arn = aws_iam_policy.lambda_policy.arn
}

data "archive_file" "lambda_zip" {
  type = "zip"
  source {
    filename = "bootstrap"
    content  = <<EOF
#!/usr/bin/env bash
exit 1
EOF
  }
  output_path = "./bootstrap.zip"
}

resource "aws_lambda_function" "delivery_retrier" {
  function_name = "${var.stack}-delivery-retrier"
  role          = aws_iam_role.lambda_role.arn
  architectures = ["arm64"]
  description   = "Delivery retrier cron job that periodically moves messages from DLQ to DB and delivers them to sns http(s) subscription, if present"
  memory_size   = 128
  publish       = false
  handler       = "bootstrap"
  filename      = data.archive_file.lambda_zip.output_path
  runtime       = "provided.al2"
  timeout       = 120
  lifecycle {
    ignore_changes = [environment]
  }
}

resource "aws_cloudwatch_log_group" "retrier_loggroup" {
  name              = "/aws/lambda/${aws_lambda_function.delivery_retrier.function_name}"
  retention_in_days = 5
}

resource "aws_cloudwatch_event_rule" "retrier_cron" {
  name                = "${var.stack}-delivery-retrier-cron"
  description         = "Cron rule to trigger delivery-retrier lambda every 5 mins"
  schedule_expression = "rate(5 minutes)"
}

resource "aws_cloudwatch_event_target" "retrier_cron_target" {
  rule = aws_cloudwatch_event_rule.retrier_cron.id
  arn  = aws_lambda_function.delivery_retrier.arn
  retry_policy {
    maximum_retry_attempts       = 0
    maximum_event_age_in_seconds = 60
  }
  input = jsonencode({
    (module.gen.cloudwatch_json_sns_topic_field) = aws_sns_topic.ses_sns_topic.arn
  })
}

resource "aws_lambda_permission" "cron_retrier_perm" {
  statement_id  = "TriggerFromEventBridgeRule"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.delivery_retrier.function_name
  principal     = "events.amazonaws.com"
  source_arn    = aws_cloudwatch_event_rule.retrier_cron.arn
}

resource "aws_lambda_function" "submgr" {
  function_name = "${var.stack}-submgr"
  role          = aws_iam_role.lambda_role.arn
  architectures = ["arm64"]
  description   = "subscription manager for https subscriptions to the sns topic that receives ses notification"
  memory_size   = 128
  publish       = false
  handler       = "bootstrap"
  filename      = data.archive_file.lambda_zip.output_path
  runtime       = "provided.al2"
  timeout       = 60
}

resource "aws_cloudwatch_log_group" "submgr_loggroup" {
  name              = "/aws/lambda/${aws_lambda_function.submgr.function_name}"
  retention_in_days = 14
}

resource "aws_lambda_function" "interim_sub" {
  function_name = "${var.stack}-interim-sub"
  role          = aws_iam_role.lambda_role.arn
  architectures = ["arm64"]
  description   = "temporary lambda that saves notifications in dynamodb until a https subscription is created."
  memory_size   = 128
  publish       = false
  handler       = "bootstrap"
  filename      = data.archive_file.lambda_zip.output_path
  runtime       = "provided.al2"
  timeout       = 60
}

resource "aws_cloudwatch_log_group" "interim_sub_loggroup" {
  name              = "/aws/lambda/${aws_lambda_function.interim_sub.function_name}"
  retention_in_days = 14
}

resource "aws_lambda_function_event_invoke_config" "interim_sub_failure_dest" {
  function_name = aws_lambda_function.interim_sub.function_name
  destination_config {
    on_failure {
      destination = aws_sqs_queue.sns_sub_DLQ.arn
    }
  }
  depends_on = [
    aws_iam_role_policy_attachment.lambda_role_attach
  ]
}

resource "aws_lambda_permission" "sns_interim_perm" {
  statement_id  = "TriggerFromSNS"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.interim_sub.function_name
  principal     = "sns.amazonaws.com"
  source_arn    = aws_sns_topic.ses_sns_topic.arn
}
