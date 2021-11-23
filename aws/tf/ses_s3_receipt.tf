locals {
  email_dirname = "emails"
  dmarc_dirname = "dmarc-reports"
}

resource "aws_s3_bucket" "ses_bucket" {
  bucket = "${var.stack}-store-${data.aws_caller_identity.current.account_id}"
  acl    = "private"
  versioning {
    enabled = true
  }
  lifecycle_rule {
    id      = "cleanup-deleted-older-than-6month"
    enabled = true

    abort_incomplete_multipart_upload_days = 1
    expiration {
      expired_object_delete_marker = true
    }
    noncurrent_version_expiration {
      days = 180
    }
  }
}

resource "aws_s3_bucket_public_access_block" "ses_bucket_block_public" {
  bucket = aws_s3_bucket.ses_bucket.id

  block_public_acls       = true
  block_public_policy     = true
  restrict_public_buckets = true
  ignore_public_acls      = true
}

resource "aws_s3_bucket_object" "s3_emails_dir" {
  for_each = toset(keys(var.ses_domain_addresses))

  bucket = aws_s3_bucket.ses_bucket.id
  key    = "${local.email_dirname}/${each.key}/"
}

resource "aws_s3_bucket_object" "s3_dmarc_reports_dir" {
  bucket = aws_s3_bucket.ses_bucket.id
  key    = "${local.dmarc_dirname}/"
}

data "aws_iam_policy_document" "ses_s3_policy_data" {
  statement {
    sid = "AllowSESPuts"
    principals {
      type        = "Service"
      identifiers = ["ses.amazonaws.com"]
    }
    actions = ["s3:PutObject"]
    resources = [
      "${aws_s3_bucket.ses_bucket.arn}/${local.email_dirname}/*",
      "${aws_s3_bucket.ses_bucket.arn}/${local.dmarc_dirname}/*"
    ]
    condition {
      test     = "StringEquals"
      variable = "aws:Referer"
      values   = [data.aws_caller_identity.current.account_id]
    }
  }
}

resource "aws_s3_bucket_policy" "ses_allow_s3_put" {
  bucket = aws_s3_bucket.ses_bucket.id
  policy = data.aws_iam_policy_document.ses_s3_policy_data.json
}

resource "aws_ses_receipt_rule_set" "ses_receipt_ruleset" {
  rule_set_name = "${var.stack}-receipt-rules"
}

data "aws_kms_alias" "ses" {
  name = "alias/aws/ses"
}

resource "aws_ses_receipt_rule" "dmarc_report_address" {
  name          = "s3-sns-dmarc"
  rule_set_name = aws_ses_receipt_rule_set.ses_receipt_ruleset.id

  enabled      = true
  scan_enabled = true
  tls_policy   = "Optional"
  recipients   = toset(var.dmarc_addresses)
  s3_action {
    position          = 1
    bucket_name       = aws_s3_bucket.ses_bucket.id
    object_key_prefix = "${local.dmarc_dirname}/"
    topic_arn         = aws_sns_topic.ses_sns_topic.arn
  }
  depends_on = [
    aws_s3_bucket_policy.ses_allow_s3_put
  ]
}

resource "aws_ses_receipt_rule" "allowed_addresses" {
  for_each = var.ses_domain_addresses

  name          = "s3-sns-${each.key}"
  rule_set_name = aws_ses_receipt_rule_set.ses_receipt_ruleset.id
  after         = aws_ses_receipt_rule.dmarc_report_address.id

  enabled      = true
  scan_enabled = true
  tls_policy   = "Optional"
  recipients   = each.value
  s3_action {
    position          = 1
    bucket_name       = aws_s3_bucket.ses_bucket.id
    object_key_prefix = "${local.email_dirname}/${each.key}/"
    kms_key_arn       = data.aws_kms_alias.ses.arn
    topic_arn         = aws_sns_topic.ses_sns_topic.arn
  }
  depends_on = [
    aws_s3_bucket_policy.ses_allow_s3_put
  ]
}

resource "aws_ses_active_receipt_rule_set" "default_receipt" {
  rule_set_name = aws_ses_receipt_rule_set.ses_receipt_ruleset.id
}
