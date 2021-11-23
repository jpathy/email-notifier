resource "aws_sqs_queue" "sns_sub_DLQ" {
  name                       = "${var.stack}-DLQ"
  visibility_timeout_seconds = 180 # 3mins
  receive_wait_time_seconds  = 20
}

data "aws_iam_policy_document" "dlq_policy_data" {
  statement {
    sid = "rootFullAccess"
    principals {
      type        = "AWS"
      identifiers = ["arn:aws:iam::${data.aws_caller_identity.current.account_id}:root"]
    }
    actions   = ["sqs:*"]
    resources = [aws_sqs_queue.sns_sub_DLQ.arn]
  }
  statement {
    sid = "subDlqPerms"
    principals {
      type        = "Service"
      identifiers = ["sns.amazonaws.com"]
    }
    actions   = ["sqs:SendMessage"]
    resources = [aws_sqs_queue.sns_sub_DLQ.arn]
    condition {
      test     = "ArnLike"
      values   = [aws_sns_topic.ses_sns_topic.arn]
      variable = "aws:SourceArn"
    }
  }
}

resource "aws_sqs_queue_policy" "sub_dlq_perms" {
  queue_url = aws_sqs_queue.sns_sub_DLQ.url
  policy    = data.aws_iam_policy_document.dlq_policy_data.json
}
