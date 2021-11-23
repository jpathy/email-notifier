data "aws_iam_policy_document" "sesrcvr_user_policy_data" {
  statement {
    sid       = "kmsPerms"
    actions   = ["kms:Decrypt", "kms:DescribeKey"]
    resources = ["*"]
    condition {
      test     = "ForAnyValue:StringEquals"
      variable = "kms:ResourceAliases"
      values   = ["alias/aws/ses"]
    }
  }
  statement {
    sid = "s3Perms"
    actions = [
      "s3:GetBucketVersioning",
      "s3:GetObject",
      "s3:GetObjectVersion",
      "s3:DeleteObjectVersion",
      "s3:DeleteObject"
    ]
    resources = [
      aws_s3_bucket.ses_bucket.arn,
      "${aws_s3_bucket.ses_bucket.arn}/*"
    ]
  }
  statement {
    sid       = "snsPerms"
    actions   = ["sns:ListTagsForResource"]
    resources = [aws_sns_topic.ses_sns_topic.arn]
  }
  statement {
    sid       = "lambdaPerms"
    actions   = ["lambda:InvokeFunction"]
    resources = [aws_lambda_function.submgr.arn]
  }
}

resource "aws_iam_user" "sesrcvr_user" {
  name = "sesrcvr-user"
  path = "/External/${var.stack}/"
  tags = {
    access = "s3+kms+sns+lambda"
  }
}

resource "aws_iam_user_policy" "sesrcvr_user_policy" {
  name   = "sesrcvr-user-policy"
  user   = aws_iam_user.sesrcvr_user.name
  policy = data.aws_iam_policy_document.sesrcvr_user_policy_data.json
}
